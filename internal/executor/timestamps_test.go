package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/plugin/soft_delete"
)

func TestJobNanosecondsRoundTrip(t *testing.T) {
	// Both endpoints must retain every bit through SQLite, the runtime snapshot and JSON.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "timestamps.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeGORMDB(db)) })
	require.NoError(t, db.AutoMigrate(ModelJob))
	for i, stamp := range []int64{math.MinInt64, math.MaxInt64} {
		row := &jobCatalogRow{ID: int64(i + 1), CreatedAtNS: stamp, UpdatedAtNS: stamp, DeletedAtNS: soft_delete.DeletedAt(math.MaxInt64)}
		require.NoError(t, db.Create(row).Error)
		var stored jobCatalogRow
		require.NoError(t, db.Unscoped().First(&stored, row.ID).Error)
		require.Equal(t, stamp, stored.CreatedAtNS)
		require.Equal(t, stamp, stored.UpdatedAtNS)
		require.EqualValues(t, math.MaxInt64, stored.DeletedAtNS)
		var kinds struct{ Created, Updated, Deleted string }
		require.NoError(t, db.Raw("SELECT typeof(created_at_ns) AS created, typeof(updated_at_ns) AS updated, typeof(deleted_at_ns) AS deleted FROM jobs WHERE id = ?", row.ID).Scan(&kinds).Error)
		require.Equal(t, "integer", kinds.Created)
		require.Equal(t, "integer", kinds.Updated)
		require.Equal(t, "integer", kinds.Deleted)
		for _, value := range []any{&stored, stored.snapshot()} {
			data, err := json.Marshal(value)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &fields))
			require.JSONEq(t, strconv.Quote(strconv.FormatInt(stamp, 10)), string(fields["created_at_ns"]))
			require.JSONEq(t, strconv.Quote(strconv.FormatInt(stamp, 10)), string(fields["updated_at_ns"]))
		}
		wire, err := protojson.Marshal(stored.snapshot().ToEntity())
		require.NoError(t, err)
		var decoded entity.Job
		require.NoError(t, protojson.Unmarshal(wire, &decoded))
		require.Equal(t, stamp, decoded.CreatedAtNs)
		require.EqualValues(t, math.MaxInt64, decoded.DeletedAtNs)
	}
	var schema string
	require.NoError(t, db.Raw("SELECT sql FROM sqlite_master WHERE name = 'jobs'").Scan(&schema).Error)
	require.Contains(t, schema, "`deleted_at_ns` bigint")
	require.NotContains(t, schema, "unsigned")
}

func TestAttemptNanosecondsKeepElapsedMilliseconds(t *testing.T) {
	// A retained attempt's precise instants must not inflate the existing duration unit.
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	db, closeDB, err := exe.openStateDB(job.ID)
	require.NoError(t, err)
	defer closeDB()
	start := int64(1780000000000123456)
	for _, elapsed := range []time.Duration{987 * time.Nanosecond, 7*time.Millisecond + 123*time.Nanosecond} {
		finish := start + int64(elapsed)
		require.NoError(t, db.Model(&JobRecord{}).Where("id = ?", singletonJobID).Updates(map[string]any{
			"latest_attempt_started_at_ns": start, "latest_attempt_finished_at_ns": finish,
		}).Error)
		require.Equal(t, elapsed.Milliseconds(), attemptProgress(t, exe, job.ID).GetElapsedMs())
		var row JobRecord
		require.NoError(t, db.First(&row, singletonJobID).Error)
		require.Equal(t, start, *row.LatestAttemptStartedAtNS)
		require.Equal(t, finish, *row.LatestAttemptFinishedAtNS)
		data, err := json.Marshal(&row)
		require.NoError(t, err)
		var restored JobRecord
		require.NoError(t, json.Unmarshal(data, &restored))
		require.Equal(t, row.LatestAttemptStartedAtNS, restored.LatestAttemptStartedAtNS)
		require.Equal(t, row.LatestAttemptFinishedAtNS, restored.LatestAttemptFinishedAtNS)
		require.Contains(t, string(data), `"latest_attempt_started_at_ns":"1780000000000123456"`)
	}
	p := NewProgress()
	p.startTime = time.Unix(0, start)
	require.Equal(t, start, p.ToEntity().StartedAtNs)
}

type timestampInfo struct{ stamp time.Time }

func (i timestampInfo) Name() string       { return "file" }
func (i timestampInfo) Size() int64        { return 7 }
func (i timestampInfo) Mode() os.FileMode  { return 0644 }
func (i timestampInfo) ModTime() time.Time { return i.stamp }
func (i timestampInfo) IsDir() bool        { return false }
func (i timestampInfo) Sys() any           { return nil }

type timestampEntry struct {
	info  os.FileInfo
	calls *int
}

func (e timestampEntry) Name() string               { return e.info.Name() }
func (e timestampEntry) Type() os.FileMode          { return e.info.Mode().Type() }
func (e timestampEntry) IsDir() bool                { return e.info.IsDir() }
func (e timestampEntry) Info() (os.FileInfo, error) { *e.calls++; return e.info, nil }

func TestLocationTimestampErrorsRetainRowsAndFailWorkflows(t *testing.T) {
	for _, year := range []int{1600, 2400} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			// Supply the actual stat boundary a filesystem can return, without Chtimes truncating the fixture.
			info := timestampInfo{stamp: time.Date(year, 1, 1, 0, 0, 0, 123, time.UTC)}
			calls := 0
			child := timestampEntry{info: info, calls: &calls}
			reader := &LocationDirectoryReader{Location: &library.Location{ID: 7}, Parent: &entity.LocationEntry{Path: "parent"}}
			directory := &LocationDirectory{LocationDirectoryReader: reader, entries: []os.DirEntry{child}}
			rows, infos, err := directory.Read(context.Background(), 0, 1)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.ErrorContains(t, rows[0].Error, "outside the signed Unix nanosecond range")
			require.Nil(t, rows[0].Entry)
			require.Empty(t, infos)
			require.Equal(t, 1, calls)
			calls = 0
			_, _, err = reader.Observe(context.Background(), []os.DirEntry{child})
			require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
			require.Equal(t, 1, calls)
			_, err = selectedFileObservation(reader.Location, &entity.LocationEntryRef{Path: "file"}, info)
			require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
			hash := &observedHash{filename: "file"}
			hash.complete(acp.Result{Size: 7, Mode: 0644, ModTime: info.stamp, SHA256: make([]byte, 32)})
			require.ErrorContains(t, hash.failure(), "outside the signed Unix nanosecond range")
			require.Nil(t, hash.expected())
		})
	}
	// Representable negative mtimes and zero sentinels keep their exact meaning.
	for _, stamp := range []int64{math.MinInt64, -123456789, 0, math.MaxInt64} {
		facts, err := InspectLocationFacts(timestampInfo{stamp: time.Unix(0, stamp)})
		require.NoError(t, err)
		require.Equal(t, stamp, facts.MtimeNs)
	}
	facts, err := InspectLocationFacts(timestampInfo{})
	require.NoError(t, err)
	require.Zero(t, facts.MtimeNs)
}
