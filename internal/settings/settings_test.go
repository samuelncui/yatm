package settings

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func testModule(t *testing.T) *Module {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "settings.db"))
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	module := New(db, PreviewDefinition{
		Default: func() (*entity.PreviewSettings, error) {
			return &entity.PreviewSettings{Concurrency: 2}, nil
		},
		Validate: func(value *entity.PreviewSettings) error { return nil },
	})
	require.NoError(t, module.AutoMigrate())
	return module
}

func TestSettingsSavePreservesNanosecondTimes(t *testing.T) {
	// Two updates in the same millisecond must retain their distinct source clock values.
	module := testModule(t)
	ctx := context.Background()
	stamp := time.Unix(1791000000, 123456781)
	module.db.Config.NowFunc = func() time.Time { return stamp }
	_, err := module.Library.Save(ctx, &entity.LibrarySettings{ConfirmRemove: true})
	require.NoError(t, err)
	var first row
	require.NoError(t, module.db.First(&first, "key = ?", "library").Error)
	require.Equal(t, stamp.UnixNano(), first.CreatedAtNS)
	require.Equal(t, stamp.UnixNano(), first.UpdatedAtNS)

	// The upsert updates only its modification instant and leaves creation unchanged.
	stamp = stamp.Add(time.Nanosecond)
	_, err = module.Library.Save(ctx, &entity.LibrarySettings{ConfirmRemove: false})
	require.NoError(t, err)
	var second row
	require.NoError(t, module.db.First(&second, "key = ?", "library").Error)
	require.Equal(t, first.CreatedAtNS, second.CreatedAtNS)
	require.Equal(t, stamp.UnixNano(), second.UpdatedAtNS)
	var storage string
	require.NoError(t, module.db.Raw("SELECT typeof(updated_at_ns) FROM settings WHERE key = ?", "library").Scan(&storage).Error)
	require.Equal(t, "integer", storage)
}

func TestGroupsShareStorageWithoutSharingBehavior(t *testing.T) {
	// Missing groups publish independent defaults without creating stored rows.
	ctx := context.Background()
	module := testModule(t)
	library, err := module.Library.Read(ctx)
	require.NoError(t, err)
	require.False(t, library.Present)
	require.True(t, library.Value.IncludeUnbackedFiles)
	preview, err := module.Preview.Read(ctx)
	require.NoError(t, err)
	require.False(t, preview.Present)
	require.EqualValues(t, 2, preview.Value.Concurrency)
	var count int64
	require.NoError(t, module.db.Model(&row{}).Count(&count).Error)
	require.Zero(t, count)

	// Each save replaces only its typed group and returns an isolated value.
	saved, err := module.Library.Save(ctx, &entity.LibrarySettings{ConfirmRemove: true})
	require.NoError(t, err)
	saved.ConfirmRemove = false
	_, err = module.Preview.Save(ctx, &entity.PreviewSettings{Concurrency: 4})
	require.NoError(t, err)
	library, err = module.Library.Read(ctx)
	require.NoError(t, err)
	require.True(t, library.Present)
	require.True(t, library.Value.ConfirmRemove)
	preview, err = module.Preview.Read(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 4, preview.Value.Concurrency)

	// A later complete replacement is last-write-wins for this local operator surface.
	_, err = module.Library.Save(ctx, &entity.LibrarySettings{IncludeUnbackedFiles: true})
	require.NoError(t, err)
	library, err = module.Library.Read(ctx)
	require.NoError(t, err)
	require.True(t, library.Value.IncludeUnbackedFiles)
	require.False(t, library.Value.ConfirmRemove)
}

func TestStoredSettingsMustDecodeAndValidate(t *testing.T) {
	for name, document := range map[string]string{
		"malformed":     "invalid",
		"unknown field": `{"execution":{"readBatch":1},"draftRevision":2}`,
		"invalid value": `{"execution":{"readBatch":1,"readBufferMax":1,"writeBufferMax":1,"writeBatchSize":1,"flushIntervalMs":99}}`,
	} {
		t.Run(name, func(t *testing.T) {
			// Persist one valid group before replacing its bytes with an unusable document.
			ctx := context.Background()
			module := testModule(t)
			_, err := module.Job.Save(ctx, &entity.JobSettings{Execution: DefaultJobExecution()})
			require.NoError(t, err)
			require.NoError(t, module.db.Model(&row{}).Where("key = ?", "jobs").Update("value", []byte(document)).Error)

			// Corruption is explicit and never falls back to defaults.
			_, err = module.Job.Current(ctx)
			require.ErrorIs(t, err, ErrStored)
		})
	}
}

func TestInvalidSettingsDoNotReplaceStoredValue(t *testing.T) {
	// Save a valid baseline and then reject an unusable complete replacement.
	ctx := context.Background()
	module := testModule(t)
	valid := &entity.JobSettings{Execution: DefaultJobExecution()}
	_, err := module.Job.Save(ctx, valid)
	require.NoError(t, err)
	_, err = module.Job.Save(ctx, &entity.JobSettings{Execution: &entity.JobExecutionSettings{ReadBatch: 1}})
	require.ErrorIs(t, err, ErrInvalid)

	// The previously saved group remains authoritative after validation fails.
	stored, err := module.Job.Current(ctx)
	require.NoError(t, err)
	require.True(t, protoEqualJob(stored, valid))
}

func protoEqualJob(left, right *entity.JobSettings) bool {
	return left.GetExecution().GetReadBatch() == right.GetExecution().GetReadBatch() &&
		left.GetExecution().GetReadBufferMax() == right.GetExecution().GetReadBufferMax() &&
		left.GetExecution().GetWriteBufferMax() == right.GetExecution().GetWriteBufferMax() &&
		left.GetExecution().GetWriteBatchSize() == right.GetExecution().GetWriteBatchSize() &&
		left.GetExecution().GetFlushIntervalMs() == right.GetExecution().GetFlushIntervalMs()
}
