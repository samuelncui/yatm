package legacy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/executor/archive"
	legacypb "github.com/samuelncui/yatm/migrate/legacy/pb"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func preserveTestBackup(t *testing.T, db *gorm.DB, work string) Backup {
	t.Helper()
	// Freeze the test database and copy the same complete evidence used by the installer.
	root := filepath.Join(t.TempDir(), "legacy.backup")
	require.NoError(t, os.MkdirAll(root, 0o700))
	var databases []struct{ File string }
	require.NoError(t, db.Raw("PRAGMA database_list").Scan(&databases).Error)
	backup := Backup{Root: root, CatalogPath: filepath.Join(root, "catalog.db"), WorkRoot: filepath.Join(root, "work")}
	backup.IndexRoot = filepath.Join(backup.WorkRoot, legacyLTFSIndexDirectory)
	require.NoError(t, copyEvidence(databases[0].File, backup.CatalogPath))
	if exists(databases[0].File + "-wal") {
		require.NoError(t, copyEvidence(databases[0].File+"-wal", backup.CatalogPath+"-wal"))
	}
	require.NoError(t, filepath.WalkDir(work, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(work, path)
		if err != nil {
			return err
		}
		target := filepath.Join(backup.WorkRoot, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyEvidence(path, target)
	}))
	return backup
}

func backupHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := make(map[string]string)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hashes[path] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	}))
	return hashes
}

func TestValidateDetectsManifestDamageAndCleanupPreservesBackup(t *testing.T) {
	// Include enough files to cross a page boundary and retain a real historical log.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	state := &legacypb.JobArchiveState{}
	for i := 0; i < 300; i++ {
		state.Sources = append(state.Sources, &legacypb.SourceState{
			Source: &legacypb.Source{Base: root, Path: []string{fmt.Sprintf("file-%03d.txt", i)}}, Size: int64(i + 1),
		})
	}
	require.NoError(t, db.Create(&legacyJob{ID: 7, CreateTime: time.Unix(1, 0),
		State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: state}}}).Error)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "job-logs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "job-logs", "7.log"), []byte("historical log\n"), 0o600))
	backup := preserveTestBackup(t, db, root)
	hashes := backupHashes(t, backup.Root)
	_, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.NoError(t, Commit(ctx, db, root))
	report, err := Validate(ctx, db, root, backup)
	require.NoError(t, err)
	require.Equal(t, 300, report.ArchiveItems)

	// Equal item counts do not hide corruption in a later manifest row.
	jobDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "7", "state.db"))
	require.NoError(t, err)
	require.NoError(t, jobDB.Model(&archive.Item{}).Where("id = ?", 290).Update("size", 999).Error)
	_, err = Validate(ctx, db, root, backup)
	require.ErrorContains(t, err, "item 290 field size differs")
	require.Error(t, Cleanup(ctx, db, root, backup))
	require.True(t, db.Migrator().HasTable("jobs_legacy"))
	require.NoError(t, jobDB.Model(&archive.Item{}).Where("id = ?", 290).Update("size", 290).Error)
	closeDB(jobDB)

	// Failure halfway through table cleanup rolls the entire metadata transaction back.
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:cleanup-failure", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "DROP TABLE") && strings.Contains(tx.Statement.SQL.String(), "files_legacy") {
			tx.AddError(fmt.Errorf("injected table cleanup failure"))
		}
	}))
	require.ErrorContains(t, Cleanup(ctx, db, root, backup), "injected table cleanup failure")
	for _, table := range stagingTableSwaps {
		require.True(t, db.Migrator().HasTable(table.backup))
	}
	require.NoError(t, db.Callback().Raw().Remove("test:cleanup-failure"))

	// An interrupted file-cleanup stage can be retried after its table transaction committed.
	require.NoError(t, os.WriteFile(filepath.Join(root, "job-logs", "new-user.log"), []byte("keep"), 0o600))
	require.ErrorContains(t, Cleanup(ctx, db, root, backup), "has no backup")
	require.False(t, db.Migrator().HasTable("jobs_legacy"))
	require.FileExists(t, filepath.Join(root, "job-logs", "7.log"))
	require.NoError(t, os.Rename(filepath.Join(root, "job-logs", "new-user.log"), filepath.Join(root, "kept-user.log")))
	require.NoError(t, Cleanup(ctx, db, root, backup))
	require.NoError(t, Cleanup(ctx, db, root, backup))
	require.NoDirExists(t, filepath.Join(root, "job-logs"))
	require.FileExists(t, filepath.Join(root, "kept-user.log"))
	require.Equal(t, hashes, backupHashes(t, backup.Root))

	// Repair still reads the complete backup after every active legacy table has gone.
	require.NoError(t, RepairJob(ctx, db, root, 7, backup))
	_, err = Validate(ctx, db, root, backup)
	require.NoError(t, err)
	require.Equal(t, hashes, backupHashes(t, backup.Root))
}

func TestBackupSnapshotIncludesWALWithoutWritingEvidence(t *testing.T) {
	// Preserve an uncheckpointed catalog exactly as a stopped-service snapshot might contain it.
	db := newLegacyTestDB(t)
	require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
	require.NoError(t, db.Create(&legacyJob{ID: 42, CreateTime: time.Unix(1, 0),
		State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{}}}}).Error)
	backup := preserveTestBackup(t, db, t.TempDir())
	hashes := backupHashes(t, backup.Root)

	// Snapshot reconstruction can write temporary SQLite state, but never source WAL or sidecars.
	snapshot, cleanup, err := backup.open()
	require.NoError(t, err)
	var job legacyJob
	require.NoError(t, snapshot.First(&job, 42).Error)
	cleanup()
	require.Equal(t, hashes, backupHashes(t, backup.Root))
}

func TestValidateDetectsChangedArchivedLog(t *testing.T) {
	// Freeze one log and one empty historical Job.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	require.NoError(t, db.Create(&legacyJob{ID: 1, CreateTime: time.Unix(1, 0),
		State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{}}}}).Error)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "job-logs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "job-logs", "1.log"), []byte("original\n"), 0o600))
	backup := preserveTestBackup(t, db, root)
	_, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.NoError(t, Commit(ctx, db, root))

	// Corrupting the copied log must block destructive cleanup even when the Job is empty.
	require.NoError(t, os.WriteFile(filepath.Join(root, "jobs", "1", "job.log"), []byte("changed\n"), 0o600))
	_, err = Validate(ctx, db, root, backup)
	require.ErrorContains(t, err, "Job 1 log differs")
	require.Error(t, Cleanup(ctx, db, root, backup))
	require.True(t, db.Migrator().HasTable("jobs_legacy"))
}
