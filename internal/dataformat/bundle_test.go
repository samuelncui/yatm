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
		{name: "published", metadata: NewBundle(7, time.Unix(1, 0)), accepted: true},
		{name: "Draft one", metadata: Bundle{FormatVersion: 1, ID: 7}},
		{name: "Draft two", metadata: Bundle{FormatVersion: 2, ID: 7}},
		{name: "future", metadata: Bundle{Format: BundleFormat, FormatVersion: 2, ID: 7}},
		{name: "other family", metadata: Bundle{Format: "other", FormatVersion: 1, ID: 7}},
		{name: "wrong identity", metadata: NewBundle(8, time.Unix(1, 0))},
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

func TestCheckBundlesPagesAndSkipsTombstones(t *testing.T) {
	// A removed Job legitimately has no bundle; active checks continue beyond the first page.
	db, _ := openCatalog(t)
	require.NoError(t, db.Exec("CREATE TABLE jobs (id INTEGER PRIMARY KEY, deleted_at INTEGER NOT NULL DEFAULT 0)").Error)
	root := t.TempDir()
	for id := int64(1); id <= 102; id++ {
		require.NoError(t, db.Exec("INSERT INTO jobs (id) VALUES (?)", id).Error)
		writeBundle(t, root, id, NewBundle(id, time.Unix(1, 0)))
	}
	require.NoError(t, db.Exec("INSERT INTO jobs VALUES (103, 1000)").Error)
	require.NoError(t, CheckBundles(db, root))
	writeBundle(t, root, 102, Bundle{ID: 102, FormatVersion: 2})
	require.ErrorContains(t, CheckBundles(db, root), "id=102")
}

func TestCheckBundlesRetainsIncompleteEvidence(t *testing.T) {
	// An empty interrupted creation is owned by recovery, but missing bundles are never inferred empty.
	db, _ := openCatalog(t)
	require.NoError(t, db.Exec("CREATE TABLE jobs (id INTEGER PRIMARY KEY, deleted_at INTEGER NOT NULL DEFAULT 0)").Error)
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
