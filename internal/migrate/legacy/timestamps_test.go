package legacy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/library"
	legacypb "github.com/samuelncui/yatm/internal/migrate/legacy/pb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const legacyTimestampNS int64 = 1_700_000_000_123_456_789

func TestPreparePreservesNanosecondInstants(t *testing.T) {
	// Distinct source instants within one millisecond must survive the released legacy adapter.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	t.Cleanup(func() { closeDB(db) })
	seedLegacyTimestampRows(t, db)
	root := t.TempDir()
	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.NoError(t, Commit(ctx, db, root))
	require.NoError(t, dataformat.CheckBundles(db, root))

	// Current rows retain exact signed values and leave unknown archive operation times absent.
	var file stagedLibraryFile
	require.NoError(t, db.Table("files").First(&file, 1).Error)
	require.Equal(t, int64(-17), file.CreatedAtNS)
	require.Equal(t, int64(-17), file.UpdatedAtNS)
	media, err := library.New(db).GetMedia(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, legacyTimestampNS, media.CreatedAtNS)
	require.NotNil(t, media.DestroyedAtNS)
	require.Equal(t, legacyTimestampNS+1, *media.DestroyedAtNS)
	var position library.Position
	require.NoError(t, db.First(&position, 1).Error)
	require.Equal(t, int64(-17), position.MtimeNS)
	require.Equal(t, legacyTimestampNS+2, position.WrittenAtNS)
	require.Zero(t, position.CheckedAtNS)
	var version library.FileVersion
	require.NoError(t, db.First(&version, "file_id = ?", 1).Error)
	require.Equal(t, int64(-17), version.MtimeNS)
	require.Nil(t, version.FirstArchivedAtNS)
	require.Nil(t, version.LastArchivedAtNS)
	var job catalogJob
	require.NoError(t, db.Table("jobs").First(&job, 1).Error)
	require.Equal(t, legacyTimestampNS+3, job.CreatedAtNS)
	require.Equal(t, legacyTimestampNS+4, job.UpdatedAtNS)
	require.Zero(t, job.DeletedAtNS)

	// SQLite stores current instants as integers rather than legacy dates or decimal text.
	for _, table := range []struct {
		name    string
		columns []string
	}{
		{"files", []string{"created_at_ns", "updated_at_ns"}},
		{"media", []string{"created_at_ns", "destroyed_at_ns"}},
		{"positions", []string{"mtime_ns", "written_at_ns", "checked_at_ns"}},
		{"file_versions", []string{"mtime_ns"}},
		{"jobs", []string{"created_at_ns", "updated_at_ns", "deleted_at_ns"}},
	} {
		for _, column := range table.columns {
			var kind string
			require.NoError(t, db.Table(table.name).Select("typeof("+column+")").Where("id = ?", 1).Scan(&kind).Error)
			require.Equal(t, "integer", kind, "%s.%s", table.name, column)
		}
	}

	// Bundle and report JSON carry decimal strings; source backup timestamps remain legacy values.
	bundle, err := os.ReadFile(filepath.Join(root, "jobs", "1", "job.json"))
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(bundle, &fields))
	require.Equal(t, `"`+strconv.FormatInt(job.CreatedAtNS, 10)+`"`, string(fields["created_at_ns"]))
	require.NotContains(t, fields, "created_at")
	encoded, err := os.ReadFile(filepath.Join(root, reportFilename))
	require.NoError(t, err)
	fields = nil
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Equal(t, `"`+strconv.FormatInt(report.CreatedAtNS, 10)+`"`, string(fields["created_at_ns"]))
	require.NotContains(t, fields, "created_at")
	var source legacyJob
	require.NoError(t, db.Table("jobs_legacy").First(&source, 1).Error)
	require.True(t, time.Unix(0, job.CreatedAtNS).Equal(source.CreateTime))
	require.True(t, time.Unix(0, job.UpdatedAtNS).Equal(source.UpdateTime))
}

func TestPreparePreservesZeroTimeAndNullableDestruction(t *testing.T) {
	// Legacy zero times map to zero without triggering GORM's automatic timestamp population.
	db := newLegacyTestDB(t)
	t.Cleanup(func() { closeDB(db) })
	seedLegacyTimestampRows(t, db)
	require.NoError(t, db.Delete(&legacyJob{}, 1).Error)
	require.NoError(t, db.Model(&legacyLibraryFile{}).Where("id = ?", 1).Update("mod_time", time.Time{}).Error)
	require.NoError(t, db.Model(&legacyLibraryTape{}).Where("id = ?", 1).
		Updates(map[string]any{"create_time": time.Time{}, "destroy_time": nil}).Error)
	zero := time.Time{}
	require.NoError(t, db.Create(&legacyLibraryTape{ID: 2, Barcode: "ZERO02", DestroyTime: &zero}).Error)
	require.NoError(t, db.Model(&legacyLibraryPosition{}).Where("id = ?", 1).
		Updates(map[string]any{"mod_time": zero, "write_time": zero}).Error)
	report, err := Prepare(context.Background(), db, t.TempDir())
	require.NoError(t, err)
	require.True(t, report.Success)

	// Null destruction and an explicitly supplied zero time remain different current facts.
	var file stagedLibraryFile
	require.NoError(t, db.First(&file, 1).Error)
	require.Zero(t, file.CreatedAtNS)
	require.Zero(t, file.UpdatedAtNS)
	require.Zero(t, file.MtimeNS)
	var media []stagedLibraryMedia
	require.NoError(t, db.Order("id").Find(&media).Error)
	require.Len(t, media, 2)
	require.Zero(t, media[0].CreatedAtNS)
	require.Nil(t, media[0].DestroyedAtNS)
	require.Zero(t, media[1].CreatedAtNS)
	require.NotNil(t, media[1].DestroyedAtNS)
	require.Zero(t, *media[1].DestroyedAtNS)
	var position stagedLibraryPosition
	require.NoError(t, db.First(&position, 1).Error)
	require.Zero(t, position.MtimeNS)
	require.Zero(t, position.WrittenAtNS)
	require.Zero(t, position.CheckedAtNS)
}

func TestPrepareRejectsOutOfRangeLegacyInstants(t *testing.T) {
	// Every source timestamp boundary must reject dates that UnixNano would silently wrap.
	for _, field := range []struct{ table, column, message string }{
		{"files", "mod_time", "File 1 mtime"},
		{"tapes", "create_time", "Tape 1 creation time"},
		{"tapes", "destroy_time", "Tape 1 destruction time"},
		{"positions", "mod_time", "Position 1 mtime"},
		{"positions", "write_time", "Position 1 write time"},
		{"jobs", "create_time", "Job 1 creation time"},
		{"jobs", "update_time", "Job 1 update time"},
	} {
		for _, year := range []int{1600, 2400} {
			t.Run(fmt.Sprintf("%s/%s/%d", field.table, field.column, year), func(t *testing.T) {
				// Record the released source cell before attempting conversion into staging.
				db := newLegacyTestDB(t)
				t.Cleanup(func() { closeDB(db) })
				seedLegacyTimestampRows(t, db)
				stamp := time.Date(year, 1, 2, 3, 4, 5, 123456789, time.UTC)
				require.NoError(t, db.Table(field.table).Where("id = ?", 1).Update(field.column, stamp).Error)
				query := "SELECT CAST(" + field.column + " AS TEXT) FROM " + field.table + " WHERE id = 1"
				var before string
				require.NoError(t, db.Raw(query).Scan(&before).Error)
				root := t.TempDir()
				report, err := Prepare(context.Background(), db, root)
				require.ErrorContains(t, err, field.message)
				require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
				require.False(t, report.Success)

				// Failure leaves source data intact and never publishes a wrapped Job or current Catalog.
				var after string
				require.NoError(t, db.Raw(query).Scan(&after).Error)
				require.Equal(t, before, after)
				require.False(t, db.Migrator().HasTable(dataformat.CatalogTable))
				require.NoFileExists(t, filepath.Join(root, "jobs", "1", "job.json"))
			})
		}
	}
}

func seedLegacyTimestampRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Source fixtures deliberately retain the released time.Time fields and protobuf model.
	hash := sha256.Sum256([]byte("legacy timestamp content"))
	require.NoError(t, db.Create(&legacyLibraryFile{
		ID: 1, Name: "timestamp.txt", Mode: 0o644, ModTime: time.Unix(0, -17), Size: 24, Hash: hash[:],
	}).Error)
	destroyed := time.Unix(0, legacyTimestampNS+1)
	require.NoError(t, db.Create(&legacyLibraryTape{
		ID: 1, Barcode: "STAMP1", CreateTime: time.Unix(0, legacyTimestampNS), DestroyTime: &destroyed,
	}).Error)
	require.NoError(t, db.Create(&legacyLibraryPosition{
		ID: 1, FileID: 1, TapeID: 1, Path: "timestamp.txt", Mode: 0o644, ModTime: time.Unix(0, -17),
		WriteTime: time.Unix(0, legacyTimestampNS+2), Size: 24, Hash: hash[:],
	}).Error)
	require.NoError(t, db.Create(&legacyJob{
		ID: 1, Status: legacypb.JobStatus_COMPLETED,
		CreateTime: time.Unix(0, legacyTimestampNS+3), UpdateTime: time.Unix(0, legacyTimestampNS+4),
		State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{}}},
	}).Error)
}
