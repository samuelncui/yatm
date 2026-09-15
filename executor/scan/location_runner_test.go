package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func setupAnalyze(t *testing.T) (*executor.Executor, *library.Location) {
	// Most analysis tests do not need a Preview module.
	t.Helper()
	return setupAnalyzeWithPreview(t, nil)
}

func setupAnalyzeWithPreview(t *testing.T, previews executor.Previewer) (*executor.Executor, *library.Location) {
	// Keep the real source directory separate from both catalog and Job resources.
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	l := library.New(db)
	require.NoError(t, l.AutoMigrate())
	sourceRoot := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(sourceRoot, 0755))
	exe := executor.New(db, l, nil, executor.Paths{Source: sourceRoot, Work: filepath.Join(root, "work")}, executor.Scripts{}, previews)
	require.NoError(t, exe.AutoMigrate())

	// Register through the Library model with the same canonical path produced by API admission.
	source := &library.Location{Name: "Daily", RootPath: sourceRoot, ExecutorID: "local", Exclusions: &entity.OnlineExclusions{Format: "gitignore", Text: "/excluded\n/future"}}
	require.NoError(t, l.CreateOnlineSource(context.Background(), source))
	return exe, source
}

func writeAnalyzeFile(t *testing.T, source *library.Location, name, content string) {
	t.Helper()
	filename := filepath.Join(source.RootPath, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0755))
	require.NoError(t, os.WriteFile(filename, []byte(content), 0644))
}

func analyzeMode(force bool) entity.ScanSignaturePolicy {
	if force {
		return entity.ScanSignaturePolicy_FORCE_READ
	}
	return entity.ScanSignaturePolicy_FILL_MISSING
}

func runAnalyze(t *testing.T, exe *executor.Executor, sourceID int64, force bool) *runner {
	// Invoke the same asynchronous creation flow as the public RPC.
	t.Helper()
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, LocationId: sourceID, SignaturePolicy: analyzeMode(force)}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(reply.Job.Id) }, 15*time.Second, 10*time.Millisecond)

	// Retain the runner for semantic manifest and phase assertions after the operation finishes.
	value, err := exe.GetJobRunner(context.Background(), reply.Job.Id)
	require.NoError(t, err)
	return value.(*runner)
}

func TestAnalyzePublishesBoundedCompleteIndex(t *testing.T) {
	// Cross a manifest page and include dot files, duplicate content, zero bytes, and excluded subtrees.
	exe, source := setupAnalyze(t)
	for i := 0; i < batchSize+3; i++ {
		writeAnalyzeFile(t, source, fmt.Sprintf("folder/%04d", i), fmt.Sprint(i))
	}
	writeAnalyzeFile(t, source, ".hidden/empty", "")
	writeAnalyzeFile(t, source, ".yatm.json", "ordinary source content")
	writeAnalyzeFile(t, source, "duplicate", "0")
	writeAnalyzeFile(t, source, "excluded/file", "never indexed")
	require.NoError(t, os.Mkdir(filepath.Join(source.RootPath, "empty-dir"), 0755))
	require.NoError(t, os.Symlink("folder/0000", filepath.Join(source.RootPath, "link")))
	r := runAnalyze(t, exe, source.ID, false)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())

	// Every physical path is independent even when current content matches.
	ctx := context.Background()
	progress, err := (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: r.job.ID})
	require.NoError(t, err)
	require.EqualValues(t, batchSize+6, progress.Added)
	require.NotEmpty(t, progress.Scopes)
	rows, err := exe.Lib().OnlineFilesPage(ctx, source.ID, "", 1000)
	require.NoError(t, err)
	require.Len(t, rows, batchSize+6)
	fileIDs := map[string]int64{}
	for _, row := range rows {
		fileIDs[row.Path] = row.FileID
	}
	require.NotEqual(t, fileIDs["duplicate"], fileIDs["folder/0000"])
	require.NotContains(t, fileIDs, "link")
	require.NotContains(t, fileIDs, "excluded/file")

	// A second successful manual analysis reuses all unchanged metadata facts.
	next := runAnalyze(t, exe, source.ID, false)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, next.Phase())
	progress, err = (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: next.job.ID})
	require.NoError(t, err)
	require.EqualValues(t, batchSize+6, progress.Unchanged)
	require.Zero(t, progress.Added+progress.Changed+progress.Removed)
}

func TestAnalyzeUnavailableEmptyAndTypeReplacement(t *testing.T) {
	// Preserve the last successful index when its bound root disappears.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "old", "retained content")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
	require.NoError(t, os.Rename(source.RootPath, source.RootPath+"-offline"))
	failed := runAnalyze(t, exe, source.ID, false)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, failed.Phase())
	rows, err := exe.Lib().OnlineFilesPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// Retry performs a fresh attempt; a regular file becoming a symlink legitimately leaves the index.
	require.NoError(t, os.Rename(source.RootPath+"-offline", source.RootPath))
	require.NoError(t, os.Remove(filepath.Join(source.RootPath, "old")))
	require.NoError(t, os.Symlink("missing", filepath.Join(source.RootPath, "old")))
	require.NoError(t, exe.RetryIndex(context.Background(), failed.job.ID))
	require.Eventually(t, func() bool { return !exe.IsRunning(failed.job.ID) }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, failed.Phase())
	rows, err = exe.Lib().OnlineFilesPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestAnalyzeForceRehashAndValidationDrift(t *testing.T) {
	// An unchanged metadata tuple may reuse cached facts; force rehash explicitly reads the bytes.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "file", "before")
	first := runAnalyze(t, exe, source.ID, false)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, first.Phase())
	info, err := os.Stat(filepath.Join(source.RootPath, "file"))
	require.NoError(t, err)
	writeAnalyzeFile(t, source, "file", "after!")
	require.NoError(t, os.Chtimes(filepath.Join(source.RootPath, "file"), info.ModTime(), info.ModTime()))
	rehashed := runAnalyze(t, exe, source.ID, true)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, rehashed.Phase())
	result, err := (&service{exe: exe}).GetProgress(context.Background(), &entity.GetScanJobProgressRequest{Id: rehashed.job.ID})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Changed)

	// A detectable change after hashing aborts prepublication validation, leaving the successful index untouched.
	writeAnalyzeFile(t, source, "file", "changed size again")
	require.Error(t, (&locationStage{runner: rehashed, source: source}).validateScope(context.Background(), source, ""))
	writeAnalyzeFile(t, source, "newly-created", "new")
	require.Error(t, (&locationStage{runner: rehashed, source: source}).validateScope(context.Background(), source, ""))
	rows, err := exe.Lib().OnlineFilesPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 6, rows[0].Size)
}

func TestAnalyzeCreateBusyAndImportedBinding(t *testing.T) {
	// Busy admission fails before allocating a second Job bundle or starting any source reads.
	exe, source := setupAnalyze(t)
	release, err := exe.Lib().UseOnlineSource(source.ID)
	require.NoError(t, err)
	_, err = (&service{exe: exe}).Create(context.Background(), &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, LocationId: source.ID}})
	require.Error(t, err)
	release()

	// Unknown Preview policies fail at Create instead of being silently ignored.
	_, err = (&service{exe: exe}).Create(context.Background(), &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, LocationId: source.ID, PreviewPolicy: entity.PreviewPolicy(99)}})
	require.ErrorContains(t, err, "invalid Scan Preview policy")
}
