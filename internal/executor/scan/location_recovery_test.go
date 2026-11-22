package scan

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/preview"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAnalyzeJobCheckpointFailureKeepsPublishedLibrary(t *testing.T) {
	// Inject only a post-publication Job checkpoint error, never a Library transaction failure.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "file", "content")
	ctx := context.Background()
	job, err := exe.CreateJob(ctx, entity.JobKind_JOB_KIND_SCAN, 1, func(db *gorm.DB) error {
		if err := prepareSchema(db); err != nil {
			return err
		}
		// The bundle's own trigger rejects only the published Entry checkpoint.
		for _, event := range []string{"INSERT", "UPDATE"} {
			if err := db.Exec("CREATE TRIGGER test_checkpoint_" + event + " BEFORE " + event + " ON entries WHEN NEW.published BEGIN SELECT RAISE(ABORT, 'injected Job checkpoint failure'); END").Error; err != nil {
				return err
			}
		}
		return db.Create(frozenLimits(&Config{ID: 1, Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID)}})).Error
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, 10*time.Millisecond)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	r := value.(*runner)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, r.Phase())
	failed, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, failed.Status)
	require.Contains(t, failed.Error, "injected Job checkpoint failure")
	published, err := exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, job.ID, published.LastSyncJobID)
	rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// A failed preparation is terminal, so a new Job scans the selection again, including files
	// removed after publication.
	require.NoError(t, os.Remove(filepath.Join(source.RootPath, "file")))
	rerun, rerunProgress := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID)})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, rerun.Phase())
	_ = rerunProgress
	rows, err = exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.EqualValues(t, 1, rerunProgress.RemovedCount)
}

func TestAnalyzeImportedLocationIsImmediatelyUsable(t *testing.T) {
	// A backup import restores the Location as authored; scanning it needs no local confirmation.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "file", "content")
	ctx := context.Background()
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
	var backup bytes.Buffer
	require.NoError(t, exe.Lib().Export(ctx, &backup, []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_LOCATION}))
	require.NoError(t, exe.Lib().Import(ctx, &backup, false))
	imported, err := exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	require.Zero(t, imported.LastJobID)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
}

type analyzePreviewer struct{ fail bool }

func (analyzePreviewer) Supports(filename string, _ *entity.PreviewJobSettings) bool {
	return strings.HasSuffix(filename, ".jpg")
}
func (p analyzePreviewer) Generate(context.Context, string, []byte, int64, int64, bool, *entity.PreviewJobSettings) ([]byte, error) {
	// The dependency deliberately fails independently of the already committed parent Analyze.
	if p.fail {
		return nil, errors.New("injected Preview execution failure")
	}
	return []byte{1}, nil
}
func (analyzePreviewer) Manifest([]byte) (*entity.PreviewManifest, error) { return nil, os.ErrNotExist }
func (analyzePreviewer) Exists([]byte) (bool, error)                      { return false, nil }
func (analyzePreviewer) Open([]byte, string) (*preview.OpenedAsset, error) {
	return nil, os.ErrNotExist
}

func TestAnalyzeOptionalPreviewUsesOnlyPublishedDeduplicatedManifest(t *testing.T) {
	// Generate one Preview per content identity, excluding unsupported and excluded source paths.
	for _, test := range []struct {
		name string
		fail bool
	}{{name: "success"}, {name: "preview-failure", fail: true}} {
		t.Run(test.name, func(t *testing.T) {
			exe, source := setupAnalyzeWithPreview(t, analyzePreviewer{fail: test.fail})
			writeAnalyzeFile(t, source, "a.jpg", "photo")
			writeAnalyzeFile(t, source, "b.jpg", "photo")
			writeAnalyzeFile(t, source, "excluded/hidden.jpg", "excluded photo")
			writeAnalyzeFile(t, source, "notes.txt", "unsupported")
			ctx := context.Background()
			created, err := (&service{exe: exe}).Create(ctx, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID), PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY}})
			require.NoError(t, err)
			require.Eventually(t, func() bool { return !exe.IsRunning(created.Job.Id) }, 5*time.Second, 10*time.Millisecond)
			parent, err := exe.GetJob(ctx, created.Job.Id)
			require.NoError(t, err)
			require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, parent.Status)
			progress, err := (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: created.Job.Id})
			require.NoError(t, err)
			if test.fail {
				require.EqualValues(t, 2, progress.PreviewsFailedCount)
			} else {
				require.EqualValues(t, 2, progress.PreviewsReadyCount)
			}
			require.EqualValues(t, 1, progress.PreviewsSkippedCount)
			rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
			require.NoError(t, err)
			require.Len(t, rows, 3)

		})
	}
}

func TestAnalyzeCancellationRetainsOldIndexAndReleasesSource(t *testing.T) {
	// Block the manifest's Library enrichment until the public cancellation path cancels its statement context.
	exe, source, catalog := setupAnalyzeCatalog(t, nil)
	ctx := context.Background()
	writeAnalyzeFile(t, source, "old", "old content")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
	writeAnalyzeFile(t, source, "new", "new content")
	entered := make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	require.NoError(t, catalog.Callback().Query().Before("gorm:query").Register("test:cancel", func(tx *gorm.DB) {
		// Only the running attempt carries a cancellable statement context.
		if tx.Statement.Table != "file_locations" || tx.Statement.Context.Done() == nil || !armed.CompareAndSwap(true, false) {
			return
		}
		close(entered)
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}))
	created, err := Create(ctx, exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID)}})
	require.NoError(t, err)
	job := created.Job
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not reach manifest insertion")
	}
	require.NoError(t, exe.Cancel(job.Id))
	require.Eventually(t, func() bool { return !exe.IsRunning(job.Id) }, 5*time.Second, 10*time.Millisecond)
	value, err := exe.GetJobRunner(ctx, job.Id)
	require.NoError(t, err)
	r := value.(*runner)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, r.Phase())
	failed, err := exe.GetJob(ctx, job.Id)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, failed.Status)
	rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "old", rows[0].Path)
	var entries int64
	require.NoError(t, r.db.Model(&Entry{}).Count(&entries).Error)
	require.Zero(t, entries)
	// A cancelled preparation is terminal, so a new Job includes the new file instead of retaining
	// the canceled partial manifest.
	rerun, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID)})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, rerun.Phase())
	rows, err = exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestAnalyzePermissionFailureIsNotRemoval(t *testing.T) {
	// A partially unreadable tree cannot publish the accessible subset as a complete index.
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary directory permission restrictions")
	}
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "private/file", "retained")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
	directory := filepath.Join(source.RootPath, "private")
	require.NoError(t, os.Chmod(directory, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(directory, 0755)) })
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, runAnalyze(t, exe, source.ID, false).Phase())
	rows, err := exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestAnalyzeUnreadableFilesAreSkippedAndKeepTheirOriginals(t *testing.T) {
	// A file the Scan cannot read is recorded and skipped while every other file is published.
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary file permission restrictions")
	}
	exe, source := setupAnalyze(t)
	ctx := context.Background()
	writeAnalyzeFile(t, source, "kept", "kept")
	writeAnalyzeFile(t, source, "changed", "old")
	writeAnalyzeFile(t, source, "moved-from", "moved")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
	originals := func() map[string]*library.FileLocation {
		rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
		require.NoError(t, err)
		byPath := make(map[string]*library.FileLocation, len(rows))
		for _, row := range rows {
			byPath[row.Path] = row
		}
		return byPath
	}
	before := originals()

	// Change, relocate and add one file each and make all three unreadable; add one readable file.
	writeAnalyzeFile(t, source, "changed", "new content")
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, "moved-from"), filepath.Join(source.RootPath, "moved-to")))
	writeAnalyzeFile(t, source, "added", "added")
	writeAnalyzeFile(t, source, "fresh", "fresh")
	unreadable := []string{"added", "changed", "moved-to"}
	for _, name := range unreadable {
		filename := filepath.Join(source.RootPath, name)
		require.NoError(t, os.Chmod(filename, 0))
		t.Cleanup(func() { require.NoError(t, os.Chmod(filename, 0644)) })
	}
	r := runAnalyze(t, exe, source.ID, false)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())

	// Each unreadable file reports its reason, and the counters include it.
	progress, err := (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: r.job.ID})
	require.NoError(t, err)
	require.EqualValues(t, len(unreadable), progress.UnreadableCount)
	var rows []*Entry
	require.NoError(t, r.db.Where("finding = ?", entity.ScanFinding_SCAN_FINDING_UNREADABLE).Order("path").Find(&rows).Error)
	require.Len(t, rows, len(unreadable))
	for i, row := range rows {
		require.Equal(t, unreadable[i], row.Path)
		require.Contains(t, row.Detail, "permission denied")
		require.False(t, row.Published)
	}
	// Tracking evidence still relates the relocated path to its File.
	require.Equal(t, before["moved-from"].FileID, rows[2].FileID)

	// Unreadable files keep their previous originals, including the old path of the relocated File.
	after := originals()
	require.ElementsMatch(t, []string{"changed", "fresh", "kept", "moved-from"}, slices.Collect(maps.Keys(after)))
	for _, name := range []string{"changed", "kept", "moved-from"} {
		require.Equal(t, before[name], after[name], name)
	}

	// A forced read skips the signature cache, so the content read itself reports the failures.
	forced := runAnalyze(t, exe, source.ID, true)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, forced.Phase())
	progress, err = (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: forced.job.ID})
	require.NoError(t, err)
	require.EqualValues(t, len(unreadable), progress.UnreadableCount)
	require.Equal(t, after, originals())
}

func TestAnalyzeBundlesSurviveStartupWithEmptyAndFailedManifests(t *testing.T) {
	// Completed empty Scans and failed unavailable sources are both complete durable bundles.
	exe, source := setupAnalyze(t)
	ctx := context.Background()
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
	require.NoError(t, os.Rename(source.RootPath, source.RootPath+"-offline"))
	r := runAnalyze(t, exe, source.ID, false)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, r.Phase())
	require.NoError(t, exe.ReconcileStorage(ctx))
	job, err := exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobKind_JOB_KIND_SCAN, job.Kind)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, job.Status)
}
