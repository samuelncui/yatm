package archive

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestArchiveSelectionDeduplicatesBeforeCaptureAcrossBatches(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	root, err := exe.LocationRoot(exe.Paths().Source)
	require.NoError(t, err)
	location := &library.Location{Name: "capture", RootPath: root, ExecutorID: "local"}
	require.NoError(t, exe.Lib().CreateLocation(ctx, location))
	var entries []*library.ObservedEntry
	hash := sha256.Sum256([]byte("content"))
	for i := 0; i < batchSize+1; i++ {
		name := fmt.Sprintf("file-%03d", i)
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("content"), 0644))
		info, err := os.Stat(filepath.Join(root, name))
		require.NoError(t, err)
		entries = append(entries, &library.ObservedEntry{Path: name, Mode: uint32(info.Mode()), Size: info.Size(), MtimeNS: info.ModTime().UnixNano(), Hash: hash[:], Signature: []byte("opaque")})
	}
	_, err = exe.Lib().PublishAnalyzed(ctx, location.ID, 1, func(_ context.Context, yield func(*library.ObservedEntry) error) error {
		for _, entry := range entries {
			if err := yield(entry); err != nil {
				return err
			}
		}
		return nil
	}, nil)
	require.NoError(t, err)
	job, err := exe.CreateJob(ctx, entity.JobKind_JOB_KIND_ARCHIVE, 0, func(db *gorm.DB) error {
		if err := ensureSchema(ctx, db); err != nil {
			return err
		}
		if err := db.Create(&Config{ID: 1, Spec: &entity.ArchiveJobSpec{}}).Error; err != nil {
			return err
		}
		return db.Create(&Item{TargetPath: "seed", Data: &entity.ArchiveManifestFile{SourcePath: filepath.Join(root, "file-000")}}).Error
	})
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	runner := newItemTestRunner(t)
	runner.exe, runner.job = exe, job

	// Once a batch is captured, remove its physical inputs. Any recapture of the later
	// overlapping logical selection fails at Lstat, even when it would deduplicate on write.
	captured := 0
	require.NoError(t, runner.db.Callback().Create().After("gorm:create").Register("remove-captured-inputs", func(tx *gorm.DB) {
		rows, ok := tx.Statement.Dest.(*[]*Item)
		if !ok || tx.Error != nil {
			return
		}
		require.LessOrEqual(t, len(*rows), batchSize)
		for _, row := range *rows {
			require.NoError(t, os.Remove(row.Data.SourcePath))
			captured++
		}
	}))
	selections := []*entity.FileSelection{
		{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: location.ID}}},
		{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{}}, Scope: entity.FileScope_FILE_SCOPE_ALL},
	}
	require.NoError(t, runner.applyLibrarySpec(ctx, &entity.ArchiveJobSpec{Selections: selections}))
	require.Equal(t, batchSize+1, captured)
	var rows []*Item
	require.NoError(t, runner.db.Order("target_path").Find(&rows).Error)
	require.Len(t, rows, batchSize+1)
	for i, row := range rows {
		require.Contains(t, row.TargetPath, fmt.Sprintf("file-%03d", i))
		require.Equal(t, []byte("opaque"), row.Data.Expected.Signature)
	}
}

func TestArchiveSelectionTargetOwnershipBeforeCapture(t *testing.T) {
	ctx := context.Background()
	runner := newItemTestRunner(t, &Item{TargetPath: "accepted", Data: &entity.ArchiveManifestFile{SourcePath: "/accepted", Expected: &entity.ExpectedFile{FileId: 1}}})
	ids, err := runner.uncapturedSelectionIDs(ctx, []int64{1, 2, 2}, map[int64]string{1: "accepted", 2: "new"})
	require.NoError(t, err)
	require.Equal(t, []int64{2}, ids)
	_, err = runner.uncapturedSelectionIDs(ctx, []int64{3}, map[int64]string{3: "accepted"})
	require.ErrorContains(t, err, "selection changed")
	_, err = runner.uncapturedSelectionIDs(ctx, []int64{2, 3}, map[int64]string{2: "new", 3: "new"})
	require.ErrorContains(t, err, "selection changed")
}
