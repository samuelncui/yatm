package scan

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAnalyzeJobCheckpointFailureKeepsPublishedLibrary(t *testing.T) {
	// Inject only a post-publication Job checkpoint error, never a Library transaction failure.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "file", "content")
	ctx := context.Background()
	var r *runner
	job, err := exe.CreatePreparedJob(ctx, entity.JobKind_JOB_KIND_SCAN, 1, func(db *gorm.DB) error {
		if err := db.AutoMigrate(frozenLimits(&Config{}), &Item{}); err != nil {
			return err
		}
		return db.Create(frozenLimits(&Config{ID: 1, Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID)}})).Error
	}, func(value executor.Runner) error {
		r = value.(*runner)
		return r.db.Callback().Create().Before("gorm:create").Register("test:checkpoint", func(tx *gorm.DB) {
			if rows, ok := tx.Statement.Dest.([]*Entry); ok {
				for _, row := range rows {
					if row.Published {
						tx.AddError(errors.New("injected Job checkpoint failure"))
						return
					}
				}
			}
		})
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, r.Phase())
	published, err := exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, job.ID, published.LastSyncJobID)
	rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// A failed preparation is terminal, so a new Job scans the selection again, including files
	// removed after publication.
	require.NoError(t, os.Remove(filepath.Join(source.RootPath, "file")))
	require.NoError(t, r.db.Callback().Create().Remove("test:checkpoint"))
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
func (analyzePreviewer) Open([]byte, string) (io.ReadCloser, error)       { return nil, os.ErrNotExist }

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
	// Block a new manifest insert until the public cancellation path cancels its statement context.
	exe, source := setupAnalyze(t)
	ctx := context.Background()
	writeAnalyzeFile(t, source, "old", "old content")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
	writeAnalyzeFile(t, source, "new", "new content")
	entered := make(chan struct{})
	var r *runner
	job, err := exe.CreatePreparedJob(ctx, entity.JobKind_JOB_KIND_SCAN, 1, func(db *gorm.DB) error {
		if err := db.AutoMigrate(frozenLimits(&Config{}), &Item{}); err != nil {
			return err
		}
		return db.Create(frozenLimits(&Config{ID: 1, Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID)}})).Error
	}, func(value executor.Runner) error {
		r = value.(*runner)
		return r.db.Callback().Create().Before("gorm:create").Register("test:cancel", func(tx *gorm.DB) {
			if tx.Statement.Table != "entries" {
				return
			}
			close(entered)
			<-tx.Statement.Context.Done()
			tx.AddError(tx.Statement.Context.Err())
		})
	})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not reach manifest insertion")
	}
	require.NoError(t, exe.Cancel(job.ID))
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, r.Phase())
	rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "old", rows[0].Path)
	var observations, entries int64
	require.NoError(t, r.db.Model(&Item{}).Count(&observations).Error)
	require.NoError(t, r.db.Model(&Entry{}).Count(&entries).Error)
	require.Zero(t, observations)
	require.Zero(t, entries)
	// A cancelled preparation is terminal, so a new Job includes the new file instead of retaining
	// the canceled partial manifest.
	require.NoError(t, r.db.Callback().Create().Remove("test:cancel"))
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
