package restore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
)

func TestRestoreExistingEqualContentCompletesWithoutCopyAndRestoresMetadata(t *testing.T) {
	// An ordinary existing target has equal bytes but different metadata from the saved version.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("EXISTING"), ".", "equal.txt", []byte("saved"), nil)
	target := filepath.Join(exe.Paths().Target, "Unforged", "EXISTING", "equal.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, []byte("saved"), 0600))
	require.NoError(t, os.Chtimes(target, time.Unix(40, 0), time.Unix(40, 0)))
	before, err := os.Stat(target)
	require.NoError(t, err)

	// Indexing verifies actual existing bytes and finishes without asking for unavailable Tape media.
	job := createRestoreJob(t, exe, file.ID)
	require.Eventually(t, func() bool {
		stored, err := exe.GetJob(ctx, job.ID)
		return err == nil && stored.Status == entity.JobStatus_JOB_STATUS_COMPLETED && !exe.IsRunning(job.ID)
	}, 5*time.Second, time.Millisecond)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	candidate := loadTestCopyCandidate(t, runner, &copy)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_COMPLETED, copy.Status)
	after, err := os.Stat(target)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "equal bytes should not be replaced")
	require.EqualValues(t, candidate.Mode, after.Mode().Perm())
	require.Equal(t, candidate.MtimeNS, after.ModTime().UnixNano())
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.NotNil(t, original)
	resolved, locationID, err := exe.ResolveOriginal(ctx, &entity.ExpectedFile{FileId: file.ID})
	require.NoError(t, err)
	canonicalTarget, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)
	require.Equal(t, canonicalTarget, resolved)
	require.Equal(t, original.LocationID, locationID)
	summary, err := runner.resultSummary(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, summary.VerifiedFiles)
	require.Zero(t, summary.UnlinkedFiles)

	// Reindexing preserves the same durable target instead of allocating another file.
	config := new(Config)
	require.NoError(t, runner.db.First(config, 1).Error)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var outputs []File
	require.NoError(t, runner.db.Find(&outputs).Error)
	require.Len(t, outputs, 1)
	require.Equal(t, "Unforged/EXISTING/equal.txt", outputs[0].Path)
	require.NoFileExists(t, filepath.Join(filepath.Dir(target), "equal (2).txt"))
}

func TestRestoreConflictingBytesIgnoreStaleHashCacheAndReserveSuffix(t *testing.T) {
	// Seed a cache for saved bytes, then replace them without changing cache-visible metadata.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("CONFLICT"), ".", "name.txt", []byte("saved"), nil)
	target := filepath.Join(exe.Paths().Target, "Unforged", "CONFLICT", "name.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, []byte("saved"), 0644))
	before, err := os.Stat(target)
	require.NoError(t, err)
	// Seed a cache holding the same size and mtime but different content bytes.
	saved, err := executor.VerifyObservedContent(ctx, target, before)
	require.NoError(t, err)
	require.Equal(t, before.Size(), saved.SizeBytes)
	require.NoError(t, os.WriteFile(target, []byte("other"), 0644))
	require.NoError(t, os.Chtimes(target, before.ModTime(), before.ModTime()))

	// Restore verifies bytes rather than trusting the stale cache, preserving the conflicting file.
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	candidate := loadTestCopyCandidate(t, runner, &copy)
	require.Equal(t, "Unforged/CONFLICT/"+library.RestoredName("name.txt", candidate.FileVersionID), candidate.TargetPath)
	actual, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, []byte("other"), actual)
	config := new(Config)
	require.NoError(t, runner.db.First(config, 1).Error)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var outputs []File
	require.NoError(t, runner.db.Find(&outputs).Error)
	require.Len(t, outputs, 1)
	require.Equal(t, candidate.TargetPath, outputs[0].Path)

	// A later conflict at the reserved path stops the attempt rather than suffixing indefinitely.
	require.NoError(t, os.WriteFile(runner.restoreTarget(candidate.TargetPath), []byte("taken"), 0644))
	require.ErrorContains(t, runner.applySpec(ctx, config.Spec), "reserved Restore output changed")
	require.NoFileExists(t, filepath.Join(filepath.Dir(target), "name (3).txt"))
}

func TestRestoreDestinationConfigurationCannotRedirectFrozenJob(t *testing.T) {
	// Freeze a Location without requiring a complete directory scan.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	frozen, err := exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{LocationId: 1, Path: "restored"})
	require.NoError(t, err)
	location, err := lib.GetLocation(ctx, frozen.LocationId)
	require.NoError(t, err)
	_, err = exe.RestoreOutputPath(ctx, frozen, "file.txt")
	require.NoError(t, err)

	// Changing the registration root invalidates the Job rather than redirecting its frozen root.
	newRoot := filepath.Join(location.RootPath, "new-root")
	require.NoError(t, os.Mkdir(newRoot, 0755))
	location.RootPath = newRoot
	_, err = lib.UpdateLocation(ctx, location)
	require.NoError(t, err)
	_, err = exe.RestoreOutputPath(ctx, frozen, "file.txt")
	require.ErrorIs(t, err, library.ErrLocationConflict)
	require.NoFileExists(t, filepath.Join(newRoot, "restored", "file.txt"))

	// Caller-supplied roots cannot bypass registration or imported-path confirmation.
	_, err = exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{RootPath: t.TempDir(), ExecutorId: "local"})
	require.Error(t, err)
}

func TestLegacyRestoreFrozenRootAllowsMissingAuthorizedOutputDirectory(t *testing.T) {
	// A legacy job freezes its configured output root, which need not exist before the first restore.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	media, file := createMediaFile(t, lib, newTapeMedia("LEGACY"), ".", "file.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	location, err := lib.GetLocation(ctx, 1)
	require.NoError(t, err)
	frozenRoot := filepath.Join(location.RootPath, "legacy-output")
	require.NoError(t, runner.db.Model(&Config{}).Where("id = ?", 1).Updates(map[string]any{
		"spec": &entity.RestoreJobSpec{}, "legacy_root": frozenRoot,
	}).Error)
	stored, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	reloaded, err := newRunner(ctx, exe, stored)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reloaded.Close()) })
	legacy := reloaded.(*jobRestoreRunner)
	require.Equal(t, frozenRoot, legacy.destination.RootPath)
	require.Zero(t, legacy.destination.LocationId)

	// The frozen authorized path supplies the output without creating directories during validation.
	source := newTestCopyItems(legacy, media.ID, mediapkg.Capabilities{Read: mediapkg.AccessRandom})
	items, err := source.nextPage(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, []string{filepath.Join(frozenRoot, "Unforged/LEGACY/file.txt")}, items[0].Targets())
	require.NoDirExists(t, frozenRoot)
}

func TestRestoreReservationsRespectCaseInsensitiveDestination(t *testing.T) {
	// Preserve distinct Linux names while reserving non-aliasing outputs on case/normalization-insensitive volumes.
	for _, fixture := range []struct {
		name            string
		firstPath       string
		secondPath      string
		existingParents bool
	}{
		{name: "file-case", firstPath: "File.txt", secondPath: "file.txt"},
		{name: "directory-case", firstPath: "Directory/file.txt", secondPath: "directory/file.txt"},
		{name: "existing-directory-case", firstPath: "Directory/file.txt", secondPath: "directory/file.txt", existingParents: true},
		{name: "unicode-normalization", firstPath: "\u00e9.txt", secondPath: "e\u0301.txt"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			// Keep logical spellings distinct and build only the optional existing target directories.
			ctx := context.Background()
			exe, lib := setupTestExecutor(t)
			_, first := createMediaFile(t, lib, newTapeMedia("FIRST"), ".", "first.txt", []byte("first"), nil)
			_, second := createMediaFile(t, lib, newTapeMedia("SECOND"), ".", "second.txt", []byte("other"), nil)
			for index, relative := range []string{fixture.firstPath, fixture.secondPath} {
				file := []*library.File{first, second}[index]
				file.Name, file.ParentID = filepath.Base(relative), library.Root.ID
				if directory := filepath.Dir(relative); directory != "." {
					parent, err := lib.MkdirAll(ctx, library.Root.ID, filepath.ToSlash(directory), 0755)
					require.NoError(t, err)
					file.ParentID = parent.ID
					if fixture.existingParents {
						require.NoError(t, os.MkdirAll(filepath.Join(exe.Paths().Target, directory), 0755))
					}
				}
				require.NoError(t, lib.SaveFile(ctx, file))
			}
			job := createRestoreJob(t, exe, first.ID, second.ID)
			waitIndexed(t, exe, job.ID)
			value, err := exe.GetJobRunner(ctx, job.ID)
			require.NoError(t, err)
			runner := value.(*jobRestoreRunner)
			var copies []Copy
			require.NoError(t, runner.db.Order("item_id").Find(&copies).Error)
			candidates := loadTestCopyCandidates(t, runner, copies)
			require.Len(t, candidates, 2)
			require.Equal(t, filepath.ToSlash(fixture.firstPath), candidates[0].TargetPath)

			// Completing one output cannot make the other reservation refer to those bytes.
			firstTarget := runner.restoreTarget(candidates[0].TargetPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(firstTarget), 0755))
			require.NoError(t, os.WriteFile(firstTarget, []byte("first"), 0644))
			_, desiredErr := os.Stat(filepath.Join(exe.Paths().Target, fixture.secondPath))
			if os.IsNotExist(desiredErr) {
				require.Equal(t, filepath.ToSlash(fixture.secondPath), candidates[1].TargetPath, "case-sensitive destinations retain distinct original spellings")
			} else {
				require.NoError(t, desiredErr)
			}
			_, exists, err := runner.outputMatches(ctx, candidates[1].TargetPath, candidates[1].Hash, candidates[1].Size)
			require.NoError(t, err)
			if exists {
				firstOutput, err := os.Stat(firstTarget)
				require.NoError(t, err)
				secondOutput, err := os.Stat(runner.restoreTarget(candidates[1].TargetPath))
				require.NoError(t, err)
				require.False(t, os.SameFile(firstOutput, secondOutput), "the second reservation aliases the first on this filesystem")
			}

			// Another Media attempt retains both actual chosen paths after the first item has become satisfied.
			config := new(Config)
			require.NoError(t, runner.db.First(config, 1).Error)
			require.NoError(t, runner.applySpec(ctx, config.Spec))
			var retried []Copy
			require.NoError(t, runner.db.Order("item_id").Find(&retried).Error)
			retriedCandidates := loadTestCopyCandidates(t, runner, retried)
			require.Len(t, retriedCandidates, 2)
			require.Equal(t, candidates[0].TargetPath, retriedCandidates[0].TargetPath)
			require.Equal(t, candidates[1].TargetPath, retriedCandidates[1].TargetPath)
		})
	}
}
