package restore

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/acp"
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
	originals, err := exe.NewOriginalResolver(ctx, []int64{file.ID})
	require.NoError(t, err)
	resolved, locationID, err := originals.Resolve(ctx, &entity.ExpectedFile{FileId: file.ID})
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

	// Validation creates nothing; the frozen authorized path receives the output once it is created.
	_, err = exe.RestoreOutputPath(ctx, legacy.destination, "Unforged/LEGACY/file.txt")
	require.NoError(t, err)
	require.NoDirExists(t, frozenRoot)
	source := newTestCopyItems(legacy, media.ID, mediapkg.Capabilities{Read: mediapkg.AccessRandom})
	items, err := source.nextPage(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, []string{filepath.Join(frozenRoot, "Unforged/LEGACY/file.txt")}, items[0].Targets())
	require.FileExists(t, filepath.Join(frozenRoot, "Unforged/LEGACY/file.txt"))
}

// claimTestOutputs resolves one random-access page, creating each item's output as an attempt does.
func claimTestOutputs(t *testing.T, runner *jobRestoreRunner, mediaID int64) (*copyItems, []acp.Item) {
	t.Helper()
	source := newTestCopyItems(runner, mediaID, mediapkg.Capabilities{Read: mediapkg.AccessRandom})
	items, err := source.nextPage(context.Background())
	if errors.Is(err, io.EOF) {
		return source, nil
	}
	require.NoError(t, err)
	return source, items
}

func TestRestoreOutputCreationResolvesConflictsAtWriteTime(t *testing.T) {
	// Seed a cache for saved bytes, then replace them without changing cache-visible metadata.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	media, file := createMediaFile(t, lib, newTapeMedia("CONFLICT"), ".", "name.txt", []byte("saved"), nil)
	target := filepath.Join(exe.Paths().Target, "Unforged", "CONFLICT", "name.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, []byte("saved"), 0644))
	before, err := os.Stat(target)
	require.NoError(t, err)
	saved, err := executor.VerifyObservedContent(ctx, target)
	require.NoError(t, err)
	require.Equal(t, before.Size(), saved.SizeBytes)
	require.NoError(t, os.WriteFile(target, []byte("other"), 0644))
	require.NoError(t, os.Chtimes(target, before.ModTime(), before.ModTime()))

	// Preparation verifies bytes rather than trusting the stale cache and leaves the logical path.
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	require.Equal(t, "Unforged/CONFLICT/name.txt", loadTestCopyCandidate(t, runner, &copy).TargetPath)

	// Creation reports the existing name and persists the conflict name it actually created.
	restored := "Unforged/CONFLICT/" + library.RestoredName("name.txt", copy.ItemID)
	_, items := claimTestOutputs(t, runner, media.ID)
	require.Len(t, items, 1)
	require.Equal(t, []string{runner.restoreTarget(restored)}, items[0].Targets())
	actual, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, []byte("other"), actual)
	var output File
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.Equal(t, restored, output.Path)
	require.Equal(t, media.ID, output.ReadMediaID)

	// Another preparation and attempt keep the claimed path and its empty output.
	config := new(Config)
	require.NoError(t, runner.db.First(config, 1).Error)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	_, items = claimTestOutputs(t, runner, media.ID)
	require.Len(t, items, 1)
	require.Equal(t, []string{runner.restoreTarget(restored)}, items[0].Targets())

	// Other content at the claimed path fails the item rather than suffixing indefinitely.
	require.NoError(t, os.WriteFile(runner.restoreTarget(restored), []byte("taken"), 0644))
	source, items := claimTestOutputs(t, runner, media.ID)
	require.Empty(t, items)
	require.ErrorContains(t, source.failure, "reserved Restore output changed")
	entries, err := os.ReadDir(filepath.Dir(target))
	require.NoError(t, err)
	require.Len(t, entries, 2, "no further conflict name is created")
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.Equal(t, restored, output.Path)
}

func TestRestoreOutputCreationReusesDirectoriesAndRejectsKindConflicts(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		existing func(t *testing.T, directory string)
		want     string
	}{
		{name: "existing directory", existing: func(t *testing.T, directory string) {
			require.NoError(t, os.MkdirAll(directory, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "unrelated.txt"), []byte("kept"), 0644))
		}, want: "file.txt"},
		{name: "directory at file name", existing: func(t *testing.T, directory string) {
			require.NoError(t, os.MkdirAll(filepath.Join(directory, "file.txt"), 0755))
		}, want: "restored"},
		{name: "file at directory name", existing: func(t *testing.T, directory string) {
			require.NoError(t, os.MkdirAll(filepath.Dir(directory), 0755))
			require.NoError(t, os.WriteFile(directory, []byte("kept"), 0644))
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			// Prepare first so the attempt, not preparation, meets the existing entries.
			ctx := context.Background()
			exe, lib := setupTestExecutor(t)
			media, file := createMediaFile(t, lib, newTapeMedia("KIND"), ".", "file.txt", []byte("saved"), nil)
			job := createRestoreJob(t, exe, file.ID)
			waitIndexed(t, exe, job.ID)
			value, err := exe.GetJobRunner(ctx, job.ID)
			require.NoError(t, err)
			runner := value.(*jobRestoreRunner)
			var copy Copy
			require.NoError(t, runner.db.First(&copy).Error)
			directory := filepath.Join(exe.Paths().Target, "Unforged", "KIND")
			fixture.existing(t, directory)

			// A file where an output directory belongs is never replaced and stops the attempt.
			if fixture.want == "" {
				source := newTestCopyItems(runner, media.ID, mediapkg.Capabilities{Read: mediapkg.AccessRandom})
				_, err := source.nextPage(ctx)
				require.ErrorContains(t, err, "not a directory")
				kept, err := os.ReadFile(directory)
				require.NoError(t, err)
				require.Equal(t, []byte("kept"), kept)
				return
			}

			// Existing directories are reused; a directory at the file name is a conflict.
			want := fixture.want
			if want == "restored" {
				want = library.RestoredName("file.txt", copy.ItemID)
			}
			_, items := claimTestOutputs(t, runner, media.ID)
			require.Len(t, items, 1)
			require.Equal(t, []string{runner.restoreTarget("Unforged/KIND/" + want)}, items[0].Targets())
			require.FileExists(t, filepath.Join(directory, want))
			if fixture.name == "existing directory" {
				require.FileExists(t, filepath.Join(directory, "unrelated.txt"))
			}
		})
	}
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

			// Preparation keeps both logical spellings; it never probes the destination.
			require.Equal(t, filepath.ToSlash(fixture.firstPath), candidates[0].TargetPath)
			require.Equal(t, filepath.ToSlash(fixture.secondPath), candidates[1].TargetPath)

			// Creating the first output lets the filesystem decide whether the second name is taken.
			_, items := claimTestOutputs(t, runner, candidates[0].MediaID)
			require.Len(t, items, 1)
			firstTarget := runner.restoreTarget(candidates[0].TargetPath)
			_, desiredErr := os.Lstat(runner.restoreTarget(candidates[1].TargetPath))
			_, items = claimTestOutputs(t, runner, candidates[1].MediaID)
			require.Len(t, items, 1)
			var outputs []File
			require.NoError(t, runner.db.Order("item_id").Find(&outputs).Error)
			require.Len(t, outputs, 2)
			require.Equal(t, candidates[0].TargetPath, outputs[0].Path)
			if os.IsNotExist(desiredErr) {
				require.Equal(t, candidates[1].TargetPath, outputs[1].Path, "case-sensitive destinations retain distinct original spellings")
			} else {
				// An equivalent existing name is reported as EEXIST and receives the conflict name.
				require.NoError(t, desiredErr)
				want := path.Join(path.Dir(candidates[1].TargetPath), library.RestoredName(path.Base(candidates[1].TargetPath), outputs[1].ItemID))
				require.Equal(t, want, outputs[1].Path)
			}
			firstOutput, err := os.Stat(firstTarget)
			require.NoError(t, err)
			secondOutput, err := os.Stat(runner.restoreTarget(outputs[1].Path))
			require.NoError(t, err)
			require.False(t, os.SameFile(firstOutput, secondOutput), "the second output aliases the first on this filesystem")

			// Another preparation and attempt retain both actual chosen paths.
			config := new(Config)
			require.NoError(t, runner.db.First(config, 1).Error)
			require.NoError(t, runner.applySpec(ctx, config.Spec))
			for index, output := range outputs {
				_, items := claimTestOutputs(t, runner, candidates[index].MediaID)
				require.Len(t, items, 1)
				require.Equal(t, []string{runner.restoreTarget(output.Path)}, items[0].Targets())
			}
		})
	}
}
