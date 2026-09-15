package scan

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/stretchr/testify/require"
)

func runAnalysis(t *testing.T, exe *executor.Executor, spec *entity.ScanJobSpec) (*runner, *entity.GetScanJobProgressReply) {
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
	r, result := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS,
		LocationId: source.ID, SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY, Paths: []string{"selected", "selected/a"},
	})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	require.Len(t, result.Scopes, 1)
	require.NotZero(t, result.Scopes[0].PublishedAtMs)

	// Unknown content remains unknown; basic collection does not invent saved versions or hash evidence.
	rows, err := exe.Lib().OnlineFilesPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Empty(t, row.Hash)
		require.Empty(t, row.Signature)
		require.NotZero(t, row.FileID)
	}
}

func TestAnalyzeFailedScopeRetainsSuccessfulResultsAndRetries(t *testing.T) {
	// One missing range cannot discard another range that was fully read and validated.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "available/file", "saved metadata")
	r, result := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, LocationId: source.ID, Paths: []string{"available", "missing"}})
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, r.Phase())
	require.Len(t, result.Scopes, 2)
	require.NotZero(t, result.Scopes[0].PublishedAtMs)
	require.NotEmpty(t, result.Scopes[1].Error)
	rows, err := exe.Lib().OnlineFilesPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	fileID := rows[0].FileID

	// A complete retry converges on the committed identity instead of creating duplicate Files.
	writeAnalyzeFile(t, source, "missing/file", "now available")
	require.NoError(t, exe.RetryIndex(context.Background(), r.job.ID))
	require.Eventually(t, func() bool { return !exe.IsRunning(r.job.ID) }, 15*time.Second, 10*time.Millisecond)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	rows, err = exe.Lib().OnlineFilesPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, fileID, rows[0].FileID)
}

func TestAnalyzeExplicitFileMayBypassIgnore(t *testing.T) {
	// Directory selection obeys Ignore, whereas an explicit ordinary file remains a deliberate selection.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "excluded/file", "explicitly selected")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, LocationId: source.ID, Paths: []string{"excluded/file"}})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	rows, err := exe.Lib().OnlineFilesPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].Signature)
}

func TestAnalyzeHashProgressAccumulatesAcrossScopes(t *testing.T) {
	// Independent content ranges share one progress accumulator, even before completion reconstruction.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "one/a", "first")
	writeAnalyzeFile(t, source, "two/b", "second")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, LocationId: source.ID, Paths: []string{"one", "two"}, SignaturePolicy: entity.ScanSignaturePolicy_FORCE_READ})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	progress := r.progress.ToEntity()
	require.EqualValues(t, 2, progress.CopiedFiles)
	require.EqualValues(t, len("firstsecond"), progress.CopiedBytes)
}
