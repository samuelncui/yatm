package scan

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

func TestOriginalEditsCopiesAndRenamesKeepIndependentFiles(t *testing.T) {
	exe, source := setupAnalyze(t)
	ctx := context.Background()
	writeAnalyzeFile(t, source, "original", "content one")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	rows, err := exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	owner := rows[0].FileID
	file, err := exe.Lib().GetFile(ctx, owner)
	require.NoError(t, err)
	file.Note = "keep my note"
	require.NoError(t, exe.Lib().SaveFile(ctx, file))
	writeAnalyzeFile(t, source, "original", "content two")
	writeAnalyzeFile(t, source, "copy", "content two")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	rows, err = exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, owner, rows[1].FileID)
	require.NotEqual(t, owner, rows[0].FileID)
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, "original"), filepath.Join(source.RootPath, "renamed")))
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	rows, err = exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Equal(t, owner, rows[1].FileID)
	kept, err := exe.Lib().GetFile(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, file.Note, kept.Note)
	versions, _, err := exe.Lib().ListFileVersions(ctx, owner, 0, 10)
	require.NoError(t, err)
	require.Empty(t, versions, "Analyze never manufactures archive history")
}

func TestMetadataOnlyOriginalCanRelocateWithoutAHash(t *testing.T) {
	exe, source := setupAnalyze(t)
	ctx := context.Background()
	writeAnalyzeFile(t, source, "first", "never hash during this sync")
	run := func() {
		reply, err := (&service{exe: exe}).Create(ctx, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, LocationId: source.ID, SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY}})
		require.NoError(t, err)
		require.Eventually(t, func() bool { return !exe.IsRunning(reply.Job.Id) }, 15*time.Second, 10*time.Millisecond)
		runner, err := exe.GetJobRunner(ctx, reply.Job.Id)
		require.NoError(t, err)
		require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runner.Phase())
	}
	run()
	rows, err := exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Nil(t, rows[0].Signature)
	owner := rows[0].FileID
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, "first"), filepath.Join(source.RootPath, "moved")))
	run()
	rows, err = exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Equal(t, owner, rows[0].FileID)
	require.Nil(t, rows[0].Signature)
}

func TestNativeRelocationPreservesOpaqueObservedSignature(t *testing.T) {
	// Model content observed by another supported signature producer, keeping native evidence.
	ctx := context.Background()
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "first", "unchanged content")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	source, err := exe.Lib().GetOnlineSource(ctx, source.ID)
	require.NoError(t, err)
	rows, err := exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	keys, err := exe.Lib().TrackingCandidatesPage(ctx, source, 0, 10)
	require.NoError(t, err)
	rows[0].Signature, rows[0].TrackingKeys = []byte{0, 255, 42}, keys
	_, err = exe.Lib().PublishOnline(ctx, source.ID, source.Revision, 9,
		func(_ context.Context, yield func(*library.OnlinePosition) error) error { return yield(rows[0]) })
	require.NoError(t, err)

	// The path and producer-generated signature miss, so native matching retains both File and opaque content.
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, "first"), filepath.Join(source.RootPath, "moved")))
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	current, err := exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, current, 1)
	require.Equal(t, rows[0].FileID, current[0].FileID)
	require.Equal(t, rows[0].Signature, current[0].Signature)
}

func TestHashFactsCorrectMetadataOnlyChangeClassification(t *testing.T) {
	// A valid new cache may disprove an unchanged metadata tuple; it must appear in the Diff.
	before, after := sha256.Sum256([]byte("before")), sha256.Sum256([]byte("after!"))
	item := &Item{Size: 6, Mode: 0644, MtimeNS: 1, Change: entity.ScanChange_SCAN_CHANGE_UNCHANGED,
		Before: &entity.OnlinePosition{Size: 6, Mode: 0644, MtimeNs: 1, Sha256: before[:], Signature: []byte("opaque")}}
	applyHash(item, after[:])
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_CHANGED, item.Change)
	require.NotEqual(t, item.Before.Signature, item.Signature)

	// Equal observed content keeps its known opaque representation and unchanged classification.
	applyHash(item, before[:])
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_UNCHANGED, item.Change)
	require.Equal(t, item.Before.Signature, item.Signature)
}

func TestAnalyzeCopyProvenancePreventsSignatureInheritance(t *testing.T) {
	exe, source := setupAnalyze(t)
	ctx := context.Background()
	writeAnalyzeFile(t, source, "original", "same content")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	original, err := exe.Lib().GetFileLocationAtPath(ctx, source.ID, "original")
	require.NoError(t, err)
	source, err = exe.Lib().GetOnlineSource(ctx, source.ID)
	require.NoError(t, err)
	writeAnalyzeFile(t, source, "copy", "same content")
	info, err := os.Lstat(filepath.Join(source.RootPath, "copy"))
	require.NoError(t, err)
	require.NoError(t, exe.Lib().PublishFileOperation(ctx, &library.FileOperationResult{OperationID: uuid.NewString(), ItemID: 1,
		LocationID: source.ID, BindingToken: source.BindingToken, Kind: entity.FileOperationKind_COPY, SourcePath: "original", TargetPath: "copy",
		OutputIdentity: executor.LocationFacts(info).Identity}))
	require.NoError(t, os.Remove(filepath.Join(source.RootPath, "original")))
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	copy, err := exe.Lib().GetFileLocationAtPath(ctx, source.ID, "copy")
	require.NoError(t, err)
	require.NotEqual(t, original.FileID, copy.FileID)
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, "copy"), filepath.Join(source.RootPath, "renamed")))
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	renamed, err := exe.Lib().GetFileLocationAtPath(ctx, source.ID, "renamed")
	require.NoError(t, err)
	require.Equal(t, copy.FileID, renamed.FileID)

	// Explicit cleanup can remove the old organizer, but a retained receipt must not block later admission.
	require.NoError(t, os.Mkdir(filepath.Join(source.RootPath, "excluded"), 0755))
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, "renamed"), filepath.Join(source.RootPath, "excluded", "held")))
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	require.NoError(t, exe.Lib().Trim(ctx, false, true))
	_, err = exe.Lib().GetFile(ctx, copy.FileID)
	require.ErrorIs(t, err, library.ErrFileNotFound)
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, "excluded", "held"), filepath.Join(source.RootPath, "returned")))
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	returned, err := exe.Lib().GetFileLocationAtPath(ctx, source.ID, "returned")
	require.NoError(t, err)
	require.NotEqual(t, copy.FileID, returned.FileID)
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, "returned"), filepath.Join(source.RootPath, "last-name")))
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	last, err := exe.Lib().GetFileLocationAtPath(ctx, source.ID, "last-name")
	require.NoError(t, err)
	require.Equal(t, returned.FileID, last.FileID)
}

func TestAnalyzeBasicPathReplacementClearsPreviousContent(t *testing.T) {
	// A new inode with the same size/mode/mtime retains path organization, not the old content assessment.
	exe, source := setupAnalyze(t)
	ctx := context.Background()
	writeAnalyzeFile(t, source, "original", "before")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, source.ID, true).Phase())
	old, err := exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, old[0].TrackingKeys)
	filename := filepath.Join(source.RootPath, "original")
	info, err := os.Stat(filename)
	require.NoError(t, err)
	replacement := filepath.Join(source.RootPath, "replacement")
	require.NoError(t, os.WriteFile(replacement, []byte("after!"), info.Mode()))
	require.NoError(t, os.Chtimes(replacement, info.ModTime(), info.ModTime()))
	require.NoError(t, os.Rename(replacement, filename))
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, LocationId: source.ID, SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	current, err := exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Equal(t, old[0].FileID, current[0].FileID)
	require.Empty(t, current[0].Signature)
	require.Empty(t, current[0].Hash)
}
