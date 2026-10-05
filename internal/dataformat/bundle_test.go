package dataformat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeBundle(t *testing.T, root string, id int64, metadata Bundle) string {
	t.Helper()
	dir := filepath.Join(root, "jobs", strconv.FormatInt(id, 10))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "job.json"), data, 0o644))
	return dir
}

func TestBundleFamilyAndRevision(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata Bundle
		accepted bool
	}{
		{name: "published", metadata: NewBundle(7, time.Unix(1, 0).UnixNano()), accepted: true},
		{name: "Draft one", metadata: Bundle{FormatVersion: 1, ID: 7}},
		{name: "Draft two", metadata: Bundle{FormatVersion: 2, ID: 7}},
		{name: "future", metadata: Bundle{Format: BundleFormat, FormatVersion: BundleRevision + 1, ID: 7}},
		{name: "other family", metadata: Bundle{Format: "other", FormatVersion: 1, ID: 7}},
		{name: "wrong identity", metadata: NewBundle(8, time.Unix(1, 0).UnixNano())},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A revision number without the new family must not collide with published revision one.
			dir := writeBundle(t, t.TempDir(), 7, test.metadata)
			filename := filepath.Join(dir, "job.json")
			before, err := os.ReadFile(filename)
			require.NoError(t, err)
			err = CheckBundle(dir, 7)
			if test.accepted {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "unsupported Job bundle format")
			}
			after, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestBundleTimestampShapeAndPrecision(t *testing.T) {
	for _, test := range []struct {
		name      string
		timestamp string
		accepted  bool
	}{
		{"nanoseconds", `"created_at_ns":"1791000000000000001"`, true},
		{"unknown", `"created_at_ns":"0"`, true},
		{"negative", `"created_at_ns":"-1"`, true},
		{"minimum", `"created_at_ns":"-9223372036854775808"`, true},
		{"maximum", `"created_at_ns":"9223372036854775807"`, true},
		{"overflow", `"created_at_ns":"9223372036854775808"`, false},
		{"number", `"created_at_ns":1791000000000000001`, false},
		{"null", `"created_at_ns":null`, false},
		{"quoted null", `"created_at_ns":"null"`, false},
		{"missing", `"id":7`, false},
		{"old timestamp", `"created_at":"2026-10-04T00:00:00Z"`, false},
		{"mixed layouts", `"created_at_ns":"1","created_at":"2026-10-04T00:00:00Z"`, false},
		{"trailing document", `"created_at_ns":"1"} {`, false},
		{"trailing garbage", `"created_at_ns":"1"} invalid {`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Admission reads the submitted timestamp shape before any Job state database is opened.
			dir := t.TempDir()
			data := []byte(`{"format":"yatm-job-bundle","format_version":1,"id":7,` + test.timestamp + `}`)
			filename := filepath.Join(dir, "job.json")
			require.NoError(t, os.WriteFile(filename, data, 0o644))
			err := CheckBundle(dir, 7)
			if !test.accepted {
				require.Error(t, err)
				after, readErr := os.ReadFile(filename)
				require.NoError(t, readErr)
				require.Equal(t, data, after)
				return
			}
			require.NoError(t, err)

			// String encoding round-trips all 64 bits, including negative instants and zero.
			var metadata Bundle
			require.NoError(t, json.Unmarshal(data, &metadata))
			encoded, err := json.Marshal(NewBundle(7, metadata.CreatedAtNS))
			require.NoError(t, err)
			require.JSONEq(t, string(data), string(encoded))
		})
	}
}

func TestCheckBundlesPagesAndSkipsTombstones(t *testing.T) {
	// A removed Job legitimately has no bundle; active checks continue beyond the first page.
	db, _ := openCatalog(t)
	require.NoError(t, db.Exec("CREATE TABLE jobs (id INTEGER PRIMARY KEY, deleted_at_ns INTEGER NOT NULL DEFAULT 0)").Error)
	root := t.TempDir()
	for id := int64(1); id <= 102; id++ {
		require.NoError(t, db.Exec("INSERT INTO jobs (id) VALUES (?)", id).Error)
		writeBundle(t, root, id, NewBundle(id, time.Unix(1, 0).UnixNano()))
	}
	require.NoError(t, db.Exec("INSERT INTO jobs VALUES (103, 1000)").Error)
	require.NoError(t, CheckBundles(db, root))
	writeBundle(t, root, 102, Bundle{ID: 102, FormatVersion: 2})
	require.ErrorContains(t, CheckBundles(db, root), "id=102")
}

func TestCheckBundlesRetainsIncompleteEvidence(t *testing.T) {
	// An empty interrupted creation is owned by recovery, but missing bundles are never inferred empty.
	db, _ := openCatalog(t)
	require.NoError(t, db.Exec("CREATE TABLE jobs (id INTEGER PRIMARY KEY, deleted_at_ns INTEGER NOT NULL DEFAULT 0)").Error)
	require.NoError(t, db.Exec("INSERT INTO jobs (id) VALUES (1)").Error)
	root := t.TempDir()
	require.ErrorIs(t, CheckBundles(db, root), ErrBundleMissing)
	dir := filepath.Join(root, "jobs", "1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, CheckBundles(db, root))

	// Evidence without a recognized metadata file must survive a refused startup.
	evidence := filepath.Join(dir, "state.db")
	require.NoError(t, os.WriteFile(evidence, []byte("retained"), 0o644))
	require.Error(t, CheckBundles(db, root))
	actual, err := os.ReadFile(evidence)
	require.NoError(t, err)
	require.Equal(t, []byte("retained"), actual)
}
