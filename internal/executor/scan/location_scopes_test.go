package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func runAnalysis(t *testing.T, exe *executor.Executor, spec *entity.ScanJobSpec) (*runner, *entity.GetScanJobProgressResponse) {
	// Exercise managed asynchronous creation and retain the typed per-scope result.
	t.Helper()
	ctx := context.Background()
	created, err := Create(ctx, exe, &entity.CreateScanJobRequest{Spec: spec})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(created.Job.Id) }, 15*time.Second, 10*time.Millisecond)
	value, err := exe.GetJobRunner(ctx, created.Job.Id)
	require.NoError(t, err)
	result, err := (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: created.Job.Id})
	require.NoError(t, err)
	return value.(*runner), result
}

func TestAnalyzeBasicSelectedScopesDoNotReadUnselectedContent(t *testing.T) {
	// Explicit ranges collect only ordinary files below the selection, without content hashes.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "selected/a", "alpha")
	writeAnalyzeFile(t, source, "selected/b", "beta")
	writeAnalyzeFile(t, source, "outside/c", "gamma")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS,
		Selections: scanLocationSelections(source.ID, "selected", "selected/a"), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
	})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	job, err := exe.GetJob(context.Background(), r.job.ID)
	require.NoError(t, err)
	require.Equal(t, source.ID, job.LocationID)
	require.Equal(t, source.Name, job.TargetName)

	// Unknown content remains unknown; basic collection does not invent saved versions or hash evidence.
	rows, err := exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Empty(t, row.Hash)
		require.Empty(t, row.Signature)
		require.NotZero(t, row.FileID)
	}
}

func TestAnalyzeFailedRangeRequiresNewJobForAllSelections(t *testing.T) {
	// A missing range prevents publishing any of this Location's selected observations.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "available/file", "saved metadata")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID, "available", "missing")})
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, r.Phase())
	failed, err := exe.GetJob(context.Background(), r.job.ID)
	require.NoError(t, err)
	require.Contains(t, failed.Error, "missing")

	rows, err := exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Empty(t, rows)

	// A failed preparation is terminal, so a new Job observes both ranges after the failed input
	// becomes available.
	writeAnalyzeFile(t, source, "missing/file", "now available")
	fresh, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID, "available", "missing")})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, fresh.Phase())
	rows, err = exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestAnalyzeExplicitFileObeysIgnore(t *testing.T) {
	// Ignore scopes this Location's content, so an explicitly named excluded file is not published.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "excluded/file", "explicitly selected")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID, "excluded/file")})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	rows, err := exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestAnalyzeHashProgressAccumulatesAcrossScopes(t *testing.T) {
	// Independent content ranges share one progress accumulator, even before completion reconstruction.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "one/a", "first")
	writeAnalyzeFile(t, source, "two/b", "second")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID, "one", "two"), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	progress := r.progress.ToEntity()
	require.EqualValues(t, 2, progress.CopiedFileCount)
	require.EqualValues(t, len("firstsecond"), progress.CopiedBytes)
}

func TestAnalyzeSelectionsPublishMultipleLocationsIndependently(t *testing.T) {
	// Per-Location matching may reuse one staging projection, but durable results and publication stay source-qualified.
	exe, first := setupAnalyze(t)
	secondRoot := filepath.Join(first.RootPath, "second-source")
	require.NoError(t, os.Mkdir(secondRoot, 0755))
	second := &library.Location{Name: "Secondary", RootPath: secondRoot, ExecutorID: "local"}
	require.NoError(t, exe.Lib().CreateLocation(context.Background(), second))
	writeAnalyzeFile(t, first, "shared/name.txt", "first")
	writeAnalyzeFile(t, second, "shared/name.txt", "second")
	selection := func(source *library.Location) *entity.FileSelection {
		return &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{
			LocationId: source.ID, Path: "shared",
		}}}
	}
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{
		Selections:      []*entity.FileSelection{selection(first), selection(second)},
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS,
	})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	job, err := exe.GetJob(context.Background(), r.job.ID)
	require.NoError(t, err)
	require.Zero(t, job.LocationID)
	require.Empty(t, job.TargetName)

	for _, source := range []*library.Location{first, second} {
		rows, err := exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, "shared/name.txt", rows[0].Path)
		require.NotEmpty(t, rows[0].Signature)
		page, err := exe.ListJob(context.Background(), &entity.JobFilter{LocationId: proto.Int64(source.ID)})
		require.NoError(t, err)
		require.Len(t, page.Jobs, 1)
		require.Equal(t, job.ID, page.Jobs[0].ID)
	}
	var entries []*Entry
	require.NoError(t, r.db.Order("location_id, path").Find(&entries).Error)
	require.Len(t, entries, 2)
	require.NotEqual(t, entries[0].LocationID, entries[1].LocationID)
	require.NotEqual(t, entries[0].After.GetFileId(), entries[1].After.GetFileId())

	// A Library root can contribute another Location without creating a misleading primary target.
	firstRows, err := exe.Lib().LocationOriginalsPage(context.Background(), first.ID, "", 10)
	require.NoError(t, err)
	mixed, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: []*entity.FileSelection{
		{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: second.ID, Path: "shared"}}},
		{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: firstRows[0].FileID}}, Scope: entity.FileScope_FILE_SCOPE_ALL},
	}, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY})
	mixedJob, err := exe.GetJob(context.Background(), mixed.job.ID)
	require.NoError(t, err)
	require.Zero(t, mixedJob.LocationID)
	for _, source := range []*library.Location{first, second} {
		page, err := exe.ListJob(context.Background(), &entity.JobFilter{LocationId: proto.Int64(source.ID)})
		require.NoError(t, err)
		require.Len(t, page.Jobs, 2)
		require.Equal(t, mixedJob.ID, page.Jobs[0].ID)
	}
}

func TestAnalyzeKnownOnlyAndCachedContentCountAsProcessed(t *testing.T) {
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "known-only", "metadata")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
		Selections: scanLocationSelections(source.ID), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY})
	r.phase = entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT
	startContentAttempt(t, r, &Scope{LocationID: source.ID})
	progress, err := r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.True(t, progress.TotalKnown)
	require.Equal(t, progress.TotalFileCount, progress.CopiedFileCount)
	require.Equal(t, progress.TotalBytes, progress.CopiedBytes)

	// A prior content read populates the disposable cache; a new fill-missing attempt reuses it.
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	cached, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
		Selections: scanLocationSelections(source.ID), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING})
	cached.phase = entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT
	startContentAttempt(t, cached, &Scope{LocationID: source.ID})
	progress, err = cached.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, progress.TotalFileCount, progress.CopiedFileCount)
	require.Equal(t, progress.TotalBytes, progress.CopiedBytes)
}

func TestScanProgressAPIReportsDurableStageWindows(t *testing.T) {
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "one", "first")
	writeAnalyzeFile(t, source, "two", "second")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
		Selections: scanLocationSelections(source.ID), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY})
	service := &service{exe: exe}

	// Enumeration has discovered work but cannot claim a stage denominator yet: the card reports a
	// growing found count, and the frozen business total is not a percentage.
	r.setPhase(entity.JobPhase_JOB_PHASE_INDEXING)
	result, err := service.GetProgress(context.Background(), &entity.GetScanJobProgressRequest{Id: r.job.ID})
	require.NoError(t, err)
	require.EqualValues(t, 2, result.Progress.CopiedFileCount)
	require.Nil(t, result.Progress.Stage.Total)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE, result.Progress.Stage.EstimateState)

	// Once frozen, the running attempt reads the durable manifest into a reconstructable window.
	r.setPhase(entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT)
	startContentAttempt(t, r, &Scope{LocationID: source.ID})
	result, err = service.GetProgress(context.Background(), &entity.GetScanJobProgressRequest{Id: r.job.ID})
	require.NoError(t, err)
	require.True(t, result.Progress.TotalKnown)
	require.EqualValues(t, 2, result.Progress.TotalFileCount)
	require.Equal(t, result.Progress.TotalFileCount, result.Progress.CopiedFileCount)

	var first Entry
	require.NoError(t, r.db.Order("id").First(&first).Error)
	require.NoError(t, r.db.Model(&Entry{}).Where("1 = 1").Update("compared", false).Error)
	require.NoError(t, r.db.Model(&Entry{}).Where("id = ?", first.ID).Update("compared", true).Error)
	r.setPhase(entity.JobPhase_JOB_PHASE_COMPARING_CONTENT)
	result, err = service.GetProgress(context.Background(), &entity.GetScanJobProgressRequest{Id: r.job.ID})
	require.NoError(t, err)
	require.True(t, result.Progress.TotalKnown)
	require.EqualValues(t, 1, result.Progress.CopiedFileCount)
	require.EqualValues(t, 2, result.Progress.TotalFileCount)
	r.setPhase(entity.JobPhase_JOB_PHASE_COMPLETED)
}

func TestScanPreparationFailurePublishesNoLocation(t *testing.T) {
	// Every selected Location must finish preparation before the first Library observation is published.
	exe, first := setupAnalyze(t)
	ctx := context.Background()
	second := &library.Location{Name: "Second", RootPath: filepath.Join(first.RootPath, "second-root"), ExecutorID: "local"}
	third := &library.Location{Name: "Third", RootPath: filepath.Join(first.RootPath, "third-root"), ExecutorID: "local"}
	require.NoError(t, os.Mkdir(second.RootPath, 0755))
	require.NoError(t, os.Mkdir(third.RootPath, 0755))
	require.NoError(t, exe.Lib().CreateLocation(ctx, second))
	require.NoError(t, exe.Lib().CreateLocation(ctx, third))
	writeAnalyzeFile(t, first, "selected/a", "first")
	writeAnalyzeFile(t, third, "selected/c", "third")
	selectPath := func(source *library.Location) *entity.FileSelection {
		return &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: source.ID, Path: "selected"}}}
	}
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: []*entity.FileSelection{selectPath(first), selectPath(second), selectPath(third)}, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, r.Phase())
	firstRows, err := exe.Lib().LocationOriginalsPage(ctx, first.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, firstRows)
	for _, source := range []*library.Location{second, third} {
		rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
		require.NoError(t, err)
		require.Empty(t, rows)
	}

	// A new Job scans every selected Location after the missing range becomes available.
	writeAnalyzeFile(t, first, "selected/b", "added on rerun")
	writeAnalyzeFile(t, second, "selected/b", "second")
	fresh, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: []*entity.FileSelection{selectPath(first), selectPath(second), selectPath(third)}, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, fresh.Phase())
	firstRows, err = exe.Lib().LocationOriginalsPage(ctx, first.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, firstRows, 2)
}

func TestScanSchemaContainsOnlyConfigAndManifest(t *testing.T) {
	r := newProgressRunner(t)
	var tables []string
	require.NoError(t, r.db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name").Scan(&tables).Error)
	require.Equal(t, []string{"config", "entries"}, tables)
}
