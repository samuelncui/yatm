package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
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
	l := library.NewWithSettings(db, testSettings(t, db))
	require.NoError(t, l.AutoMigrate())
	sourceRoot := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(sourceRoot, 0755))
	exe := executor.New(
		db, l, nil, executor.Paths{Source: sourceRoot, Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: sourceRoot}}},
		executor.Scripts{}, previews,
	)
	require.NoError(t, exe.AutoMigrate())
	t.Cleanup(func() {
		for _, id := range exe.RunningJobIDs() {
			_ = exe.Cancel(id)
		}
		require.Eventually(t, func() bool { return len(exe.RunningJobIDs()) == 0 }, 10*time.Second, 10*time.Millisecond)
	})

	// Register through the Library model with the same canonical path produced by API admission.
	source := &library.Location{Name: "Daily", RootPath: sourceRoot, ExecutorID: "local", Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "/excluded\n/future"}}}
	require.NoError(t, l.CreateLocation(context.Background(), source))
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
		return entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ
	}
	return entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING
}

func runAnalyze(t *testing.T, exe *executor.Executor, sourceID int64, force bool) *runner {
	// Invoke the same asynchronous creation flow as the public RPC.
	t.Helper()
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(sourceID), SignaturePolicy: analyzeMode(force)}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(reply.Job.Id) }, 30*time.Second, 10*time.Millisecond)

	// Retain the runner for semantic manifest and phase assertions after the operation finishes.
	value, err := exe.GetJobRunner(context.Background(), reply.Job.Id)
	require.NoError(t, err)
	return value.(*runner)
}

func TestAnalyzePublishesBoundedCompleteIndex(t *testing.T) {
	// Cross several manifest pages and include dot files, duplicate content, zero bytes, and excluded subtrees.
	exe, source := setupAnalyze(t)
	const generatedFiles = batchSize*4 + 3
	for i := 0; i < generatedFiles; i++ {
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
	require.EqualValues(t, generatedFiles+3, progress.AddedCount)
	var rows []*library.FileLocation
	var after string
	for {
		page, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, after, 1000)
		require.NoError(t, err)
		rows = append(rows, page...)
		if len(page) < 1000 {
			break
		}
		after = page[len(page)-1].Path
	}
	require.Len(t, rows, generatedFiles+3)
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
	require.EqualValues(t, generatedFiles+3, progress.UnchangedCount)
	require.Zero(t, progress.AddedCount+progress.ChangedCount+progress.RemovedCount)
}

func TestAnalyzeUnavailableEmptyAndTypeReplacement(t *testing.T) {
	// Preserve the last successful index when its bound root disappears.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "old", "retained content")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, false).Phase())
	require.NoError(t, os.Rename(source.RootPath, source.RootPath+"-offline"))
	failed := runAnalyze(t, exe, source.ID, false)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, failed.Phase())
	rows, err := exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// A failed preparation is terminal, so a new Job performs the fresh attempt; a regular file
	// becoming a symlink legitimately leaves the index.
	require.NoError(t, os.Rename(source.RootPath+"-offline", source.RootPath))
	require.NoError(t, os.Remove(filepath.Join(source.RootPath, "old")))
	require.NoError(t, os.Symlink("missing", filepath.Join(source.RootPath, "old")))
	fresh := runAnalyze(t, exe, source.ID, false)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, fresh.Phase())
	rows, err = exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestAnalyzeForceRehashesMetadataStableContent(t *testing.T) {
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
	require.EqualValues(t, 1, result.ChangedCount)

	// Force-read publication records the content examined during the operation.
	rows, err := exe.Lib().LocationOriginalsPage(context.Background(), source.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 6, rows[0].Size)
}

func TestAnalyzeRejectsUnknownPreviewPolicy(t *testing.T) {
	// Unknown Preview policies fail before creating a Job.
	exe, source := setupAnalyze(t)
	_, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, Selections: scanLocationSelections(source.ID), PreviewPolicy: entity.PreviewPolicy(99)}})
	require.ErrorContains(t, err, "invalid Scan Preview policy")
}
