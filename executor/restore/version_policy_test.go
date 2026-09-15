package restore

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func policyFileSelection(id int64) *entity.FileSelection {
	return &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}},
		Scope: entity.FileScope_FILE_SCOPE_ALL}
}

func archivePolicyVersion(t *testing.T, db *gorm.DB, lib *library.Library, media *library.Media, file *library.File,
	content string, archivedAt int64,
) (*library.Media, *library.FileVersion) {
	t.Helper()
	// Seed historical metadata without changing the system clock or touching a physical Tape.
	ctx := context.Background()
	if media.ID == 0 {
		stored, err := lib.CreateMedia(ctx, media)
		require.NoError(t, err)
		media = stored
	}
	hash := sha256.Sum256([]byte(content))
	version := &library.FileVersion{FileID: file.ID, Signature: []byte(content), Hash: hash[:],
		Size: int64(len(content)), Mode: 0644, FirstArchivedAt: &archivedAt, LastArchivedAt: &archivedAt}
	require.NoError(t, db.Create(version).Error)
	require.NoError(t, db.Create(&library.Position{MediaID: media.ID, Path: content + ".txt",
		Signature: version.Signature, Hash: version.Hash, Size: version.Size, Mode: version.Mode}).Error)
	return media, version
}

func waitPolicyIndexAttempt(t *testing.T, exe *executor.Executor, jobID int64) *jobRestoreRunner {
	t.Helper()
	value, err := exe.GetJobRunner(context.Background(), jobID)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(jobID) }, 5*time.Second, time.Millisecond)
	return value.(*jobRestoreRunner)
}

func TestRestoreTimePolicyFreezesVersionBeforeRetry(t *testing.T) {
	// Provide versions on either side of the requested time and mixed overlapping directory roots.
	ctx := context.Background()
	exe, lib, db := setupTestExecutorWithLibraryDB(t)
	directory := &library.File{Name: "reports", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, lib.SaveFile(ctx, directory))
	file := &library.File{Name: "report.txt", ParentID: directory.ID, Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, file))
	media, older := archivePolicyVersion(t, db, lib, newTapeMedia("HISTORY"), file, "older", 100)
	media, _ = archivePolicyVersion(t, db, lib, media, file, "newer", 300)
	cutoff := int64(200)
	spec := &entity.RestoreJobSpec{Selections: []*entity.FileSelection{policyFileSelection(directory.ID), policyFileSelection(file.ID)},
		Destination: &entity.RestoreDestination{LocationId: 1}, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff}}

	// Review and indexing use the same version policy and count overlapping roots once.
	estimate, err := exe.InspectSelections(ctx, &entity.InspectSelectionRequest{Restore: true,
		Selections: spec.Selections, VersionPolicy: spec.VersionPolicy})
	require.NoError(t, err)
	require.EqualValues(t, 1, estimate.Files)
	require.Len(t, estimate.ResolvedVersions, 1)
	require.Equal(t, older.ID, estimate.ResolvedVersions[0].Version.Id)
	job, err := Create(ctx, exe, 0, spec)
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	runner := waitPolicyIndexAttempt(t, exe, job.ID)
	var copies []Copy
	require.NoError(t, runner.db.Find(&copies).Error)
	require.Len(t, copies, 1)
	require.Equal(t, older.ID, copies[0].FileVersionID)
	require.Equal(t, "reports/report.txt", copies[0].TargetPath)

	// Later-discovered evidence changes a fresh estimate but never a manifest already frozen for this Job.
	_, middle := archivePolicyVersion(t, db, lib, media, file, "middle", 150)
	estimate, err = exe.InspectSelections(ctx, &entity.InspectSelectionRequest{Restore: true,
		Selections: spec.Selections, VersionPolicy: spec.VersionPolicy})
	require.NoError(t, err)
	require.Equal(t, middle.ID, estimate.ResolvedVersions[0].Version.Id)
	var config Config
	require.NoError(t, runner.db.First(&config, 1).Error)
	require.True(t, config.ManifestFrozen)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var retried []Copy
	require.NoError(t, runner.db.Find(&retried).Error)
	require.Equal(t, copies, retried)
}

func TestRestoreTimePolicySkipNeverAcceptsMissingCopiesOrEmptyManifest(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		cutoff   int64
		skip     bool
		explicit bool
		remove   bool
		ready    bool
	}{
		{name: "latest", cutoff: -1, ready: true},
		{name: "later only", cutoff: 50},
		{name: "skip all", cutoff: 50, skip: true},
		{name: "explicit later override", cutoff: 50, explicit: true, ready: true},
		{name: "missing matching copy", cutoff: 200, skip: true, remove: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Each isolated selection has saved content that can be removed from inventory independently.
			ctx := context.Background()
			exe, lib, db := setupTestExecutorWithLibraryDB(t)
			file := &library.File{Name: "report.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
			require.NoError(t, lib.SaveFile(ctx, file))
			media, version := archivePolicyVersion(t, db, lib, newTapeMedia("POLICY"), file, "saved", 100)
			if scenario.remove {
				require.NoError(t, lib.DeleteMedia(ctx, media.ID))
			}
			spec := &entity.RestoreJobSpec{Selections: []*entity.FileSelection{policyFileSelection(file.ID)},
				Destination: &entity.RestoreDestination{LocationId: 1}, SkipUnmatchedVersions: scenario.skip}
			if scenario.cutoff >= 0 {
				spec.VersionPolicy = &entity.RestoreVersionPolicy{BeforeAtMs: &scenario.cutoff}
			}
			if scenario.explicit {
				spec.FileVersionIds = []int64{version.ID}
			}

			// Only nonempty, matched versions with available physical candidates become executable.
			job, err := Create(ctx, exe, 0, spec)
			require.NoError(t, err)
			runner := waitPolicyIndexAttempt(t, exe, job.ID)
			var config Config
			require.NoError(t, runner.db.First(&config, 1).Error)
			var count int64
			require.NoError(t, runner.db.Model(&Copy{}).Count(&count).Error)
			if scenario.ready {
				require.True(t, config.ManifestFrozen)
				require.EqualValues(t, 1, count)
				require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, runner.Phase())
				return
			}
			require.False(t, config.ManifestFrozen)
			require.Zero(t, count, "failed manifests roll back every partial candidate")
			require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, runner.Phase())
		})
	}
}

func TestRestoreCreateRejectsVersionPolicyWithoutValidConsent(t *testing.T) {
	// Policy validation runs before destination probing or bundle creation.
	ctx := context.Background()
	exe, _ := setupTestExecutor(t)
	negative := int64(-1)
	for _, spec := range []*entity.RestoreJobSpec{
		{FileVersionIds: []int64{1}, SkipUnmatchedVersions: true},
		{FileVersionIds: []int64{1}, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &negative}},
	} {
		_, err := Create(ctx, exe, 0, spec)
		require.Error(t, err)
	}
}

func TestRestoreLocationSelectionUsesCatalogHistoryAndExplicitOverrides(t *testing.T) {
	// An offline original association still supplies its File history, not a live latest-only estimate.
	ctx := context.Background()
	exe, lib, db := setupTestExecutorWithLibraryDB(t)
	source := &library.Location{Name: "Offline original", ExecutorID: "local", RootPath: filepath.Join(exe.Paths().Source, "offline")}
	require.NoError(t, lib.CreateOnlineSource(ctx, source))
	source, err := lib.PublishOnline(ctx, source.ID, source.Revision, 1,
		func(_ context.Context, yield func(*library.OnlinePosition) error) error {
			return yield(&library.OnlinePosition{Path: "report.txt", Size: 5, Mode: 0644, MtimeNS: 1})
		})
	require.NoError(t, err)
	original, err := lib.GetFileLocationAtPath(ctx, source.ID, "report.txt")
	require.NoError(t, err)
	file, err := lib.GetFile(ctx, original.FileID)
	require.NoError(t, err)
	media, older := archivePolicyVersion(t, db, lib, newTapeMedia("OFFLINE"), file, "older", 100)
	_, latest := archivePolicyVersion(t, db, lib, media, file, "latest", 300)
	cutoff := int64(200)
	selections := []*entity.FileSelection{
		{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: source.ID, Path: "report.txt"}}},
		policyFileSelection(file.ID),
	}

	// The estimate and manifest resolve the same retained association without opening the missing source.
	estimate, err := exe.InspectSelections(ctx, &entity.InspectSelectionRequest{Restore: true,
		Selections: selections, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff}})
	require.NoError(t, err)
	require.NoDirExists(t, source.RootPath)
	require.EqualValues(t, 1, estimate.Files)
	require.Len(t, estimate.ResolvedVersions, 1)
	require.Equal(t, older.ID, estimate.ResolvedVersions[0].Version.Id)
	job, err := Create(ctx, exe, 0, &entity.RestoreJobSpec{Selections: selections,
		FileVersionIds: []int64{latest.ID}, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff},
		Destination: &entity.RestoreDestination{LocationId: 1}})
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	runner := waitPolicyIndexAttempt(t, exe, job.ID)
	var copies []Copy
	require.NoError(t, runner.db.Find(&copies).Error)
	require.Len(t, copies, 1)
	require.Equal(t, latest.ID, copies[0].FileVersionID)
}

func TestRestorePolicyPartialSkipKeepsMatchingFiles(t *testing.T) {
	// A mixed directory contains one eligible saved file and one file saved only after the cutoff.
	ctx := context.Background()
	exe, lib, db := setupTestExecutorWithLibraryDB(t)
	first := &library.File{Name: "earlier.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	second := &library.File{Name: "later.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, first))
	require.NoError(t, lib.SaveFile(ctx, second))
	media, version := archivePolicyVersion(t, db, lib, newTapeMedia("PARTIAL"), first, "earlier", 100)
	_, _ = archivePolicyVersion(t, db, lib, media, second, "later", 300)
	cutoff := int64(200)
	spec := &entity.RestoreJobSpec{Selections: []*entity.FileSelection{policyFileSelection(0)},
		VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff}, SkipUnmatchedVersions: true,
		Destination: &entity.RestoreDestination{LocationId: 1}}

	// Explicit consent omits only the unmatched file, preserving the eligible content and frozen counts.
	estimate, err := exe.InspectSelections(ctx, &entity.InspectSelectionRequest{Restore: true,
		Selections: spec.Selections, VersionPolicy: spec.VersionPolicy, SkipUnmatchedVersions: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, estimate.Files)
	require.EqualValues(t, 1, estimate.SkippedVersions)
	job, err := Create(ctx, exe, 0, spec)
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	runner := waitPolicyIndexAttempt(t, exe, job.ID)
	var copies []Copy
	require.NoError(t, runner.db.Find(&copies).Error)
	require.Len(t, copies, 1)
	require.Equal(t, version.ID, copies[0].FileVersionID)
}
