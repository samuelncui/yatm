package restore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
		return err == nil && stored.Status == entity.JobStatus_COMPLETED && !exe.IsRunning(job.ID)
	}, 5*time.Second, time.Millisecond)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	require.Equal(t, entity.CopyStatus_COMPLETED, copy.Status)
	after, err := os.Stat(target)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "equal bytes should not be replaced")
	require.EqualValues(t, copy.Mode, after.Mode().Perm())
	require.Equal(t, copy.MtimeNS, after.ModTime().UnixNano())
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.NotNil(t, original)
	location, err := lib.GetOnlineSource(ctx, original.LocationID)
	require.NoError(t, err)
	require.True(t, original.CurrentBinding(location))
	require.Equal(t, entity.OnlineBinding_CONFIRMED, location.Binding)
	require.Zero(t, location.LastSyncAt)
	position, err := lib.GetOnlinePosition(ctx, file.ID)
	require.NoError(t, err)
	opened, err := exe.OpenOnlinePosition(ctx, position, &entity.ExpectedFile{FileId: file.ID, Signature: copy.Signature,
		Sha256: copy.Hash, Size: copy.Size, Mode: original.Mode, MtimeNs: original.MtimeNS})
	require.NoError(t, err)
	require.NoError(t, opened.Close())
	summary, err := runner.resultSummary(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, summary.VerifiedFiles)
	require.Zero(t, summary.UnlinkedFiles)

	// Reindexing preserves the same durable target instead of allocating another file.
	config := new(Config)
	require.NoError(t, runner.db.First(config, 1).Error)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var outputs []Output
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
	copyer, err := acp.New(ctx, acp.AccurateJob(target, nil), acp.WithSignatureCache(true), acp.ForceRehash(true))
	require.NoError(t, err)
	require.NoError(t, copyer.WaitErr())
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
	require.Equal(t, "Unforged/CONFLICT/"+library.RestoredName("name.txt", copy.FileVersionID), copy.TargetPath)
	actual, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, []byte("other"), actual)
	config := new(Config)
	require.NoError(t, runner.db.First(config, 1).Error)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var outputs []Output
	require.NoError(t, runner.db.Find(&outputs).Error)
	require.Len(t, outputs, 1)
	require.Equal(t, copy.TargetPath, outputs[0].Path)

	// A later conflict at the reserved path stops the attempt rather than suffixing indefinitely.
	require.NoError(t, os.WriteFile(runner.restoreTarget(copy.TargetPath), []byte("taken"), 0644))
	require.ErrorContains(t, runner.applySpec(ctx, config.Spec), "reserved Restore output changed")
	require.NoFileExists(t, filepath.Join(filepath.Dir(target), "name (3).txt"))
}

func TestRestoreDestinationConfigurationCannotRedirectFrozenJob(t *testing.T) {
	// Freeze a confirmed Location without requiring a complete directory scan.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	frozen, err := exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{LocationId: 1, Path: "restored"})
	require.NoError(t, err)
	location, err := lib.GetOnlineSource(ctx, frozen.LocationId)
	require.NoError(t, err)
	require.NotEmpty(t, location.BindingToken)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, location.Binding)
	_, err = exe.RestoreOutputPath(ctx, frozen, "file.txt")
	require.NoError(t, err)

	// Changing the registration root invalidates the Job rather than following the new binding.
	newRoot := filepath.Join(location.RootPath, "new-root")
	require.NoError(t, os.Mkdir(newRoot, 0755))
	location.RootPath = newRoot
	_, err = lib.UpdateOnlineSource(ctx, location, false)
	require.NoError(t, err)
	_, err = exe.RestoreOutputPath(ctx, frozen, "file.txt")
	require.ErrorIs(t, err, library.ErrOnlineConflict)
	require.NoFileExists(t, filepath.Join(newRoot, "restored", "file.txt"))

	// Caller-supplied roots cannot bypass registration or imported-path confirmation.
	_, err = exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{RootPath: t.TempDir(), ExecutorId: "local"})
	require.Error(t, err)
}

func TestRestoreMediaAttemptExcludesCatalogImport(t *testing.T) {
	// Build a real Volume restore whose manifest has already captured a destination binding.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	root := filepath.Join(exe.Paths().Volumes[0], "fixture")
	require.NoError(t, os.Mkdir(root, 0755))
	volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{SerialNumber: "restore-import", Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	content := []byte("saved")
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), content, 0644))
	_, file := createMediaFile(t, lib, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "fixture",
		Profile: volume.Marker.Profile.Pack(), CreateTime: volume.Marker.CreatedAt,
	}, "", "file.txt", content, nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var backup bytes.Buffer
	require.NoError(t, lib.Export(ctx, &backup, []entity.LibraryEntityType{entity.LibraryEntityType_FILE}))

	// Attempt real catalog replacement exactly when the active Media runner fetches its copy page.
	var once sync.Once
	imported := make(chan error, 1)
	require.NoError(t, runner.db.Callback().Query().Before("gorm:query").Register("test:import-during-restore", func(tx *gorm.DB) {
		if tx.Statement.Table != "copies" {
			return
		}
		once.Do(func() { imported <- lib.Import(ctx, bytes.NewReader(backup.Bytes())) })
	}))
	_, err = (&service{exe: exe}).RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: job.ID, Target: (&entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
	select {
	case importErr := <-imported:
		require.ErrorIs(t, importErr, library.ErrOnlineBusy)
	case <-time.After(time.Second):
		t.Fatal("Restore did not reach its copy page")
	}
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
	location, err := lib.GetOnlineSource(ctx, 1)
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
	source := &copySource{runner: legacy, mediaID: media.ID, session: &testReadSession{root: "/media"}}
	request, err := source.Next(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(frozenRoot, "Unforged/LEGACY/file.txt")}, request.Targets)
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
			require.Len(t, copies, 2)
			require.Equal(t, filepath.ToSlash(fixture.firstPath), copies[0].TargetPath)

			// Completing one output cannot make the other reservation refer to those bytes.
			firstTarget := runner.restoreTarget(copies[0].TargetPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(firstTarget), 0755))
			require.NoError(t, os.WriteFile(firstTarget, []byte("first"), 0644))
			_, desiredErr := os.Stat(filepath.Join(exe.Paths().Target, fixture.secondPath))
			if os.IsNotExist(desiredErr) {
				require.Equal(t, filepath.ToSlash(fixture.secondPath), copies[1].TargetPath, "case-sensitive destinations retain distinct original spellings")
			} else {
				require.NoError(t, desiredErr)
			}
			_, exists, err := runner.outputMatches(ctx, copies[1].TargetPath, copies[1].Hash, copies[1].Size)
			require.NoError(t, err)
			if exists {
				firstOutput, err := os.Stat(firstTarget)
				require.NoError(t, err)
				secondOutput, err := os.Stat(runner.restoreTarget(copies[1].TargetPath))
				require.NoError(t, err)
				require.False(t, os.SameFile(firstOutput, secondOutput), "the second reservation aliases the first on this filesystem")
			}

			// Retry retains both actual chosen paths after the first item has become satisfied.
			config := new(Config)
			require.NoError(t, runner.db.First(config, 1).Error)
			require.NoError(t, runner.applySpec(ctx, config.Spec))
			var retried []Copy
			require.NoError(t, runner.db.Order("item_id").Find(&retried).Error)
			require.Len(t, retried, 2)
			require.Equal(t, copies[0].TargetPath, retried[0].TargetPath)
			require.Equal(t, copies[1].TargetPath, retried[1].TargetPath)
		})
	}
}
