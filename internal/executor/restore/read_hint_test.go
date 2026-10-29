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
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRestoreTarHintUsesOnlyPendingFrozenCandidate(t *testing.T) {
	// A marker would reveal any attempt to invoke the LTFS identity adapter.
	ctx := context.Background()
	base, lib := setupTestExecutor(t)
	marker := filepath.Join(t.TempDir(), "LTFS-called")
	script := filepath.Join(t.TempDir(), "read-info")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0755))
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "executor.db"))
	require.NoError(t, err)
	exe := executor.New(db, lib, []string{"/dev/nst0"}, base.Paths(), executor.Scripts{ReadInfo: script}, nil)
	require.NoError(t, exe.AutoMigrate())
	_, file := createMediaFile(t, lib, newTapeMedia("TAR001"), "", "file.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var frozen Copy
	require.NoError(t, runner.db.First(&frozen).Error)
	profile := (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 262144}}).Pack()
	require.NoError(t, runner.db.Model(&Copy{}).Where("id = ?", frozen.ID).Update("media_profile", profile).Error)
	request := (&entity.ReadTapeTarget{Device: " /dev/nst0 ", MediaId: proto.Int64(frozen.MediaID), RefreshPositionMap: true}).Pack()
	request.ExpectedIdentity = "CLIENT"
	request.ExpectedMediaId = 99
	request.ExpectedProfile = (&entity.TapeMediaProfile{Format: "ltfs_v1"}).Pack()

	// The hint selects frozen facts and preserves the normalized device without calling readinfo.
	type selectionResult struct {
		target *entity.ReadMediaTarget
		err    error
	}
	result := make(chan selectionResult, 1)
	require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_RESTORE, func(attempt context.Context, _ executor.Runner) error {
		selected, err := runner.frozenReadTarget(attempt, request)
		result <- selectionResult{selected, err}
		return err
	}))
	var selected *entity.ReadMediaTarget
	select {
	case observed := <-result:
		require.NoError(t, observed.err)
		selected = observed.target
	case <-time.After(5 * time.Second):
		t.Fatal("Tape selection did not settle")
	}
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
	require.Equal(t, "/dev/nst0", selected.GetTape().Device)
	require.EqualValues(t, frozen.MediaID, selected.GetTape().GetMediaId())
	require.True(t, selected.GetTape().RefreshPositionMap)
	require.EqualValues(t, frozen.MediaID, selected.ExpectedMediaId)
	require.Equal(t, "TAR001", selected.ExpectedIdentity)
	require.True(t, proto.Equal(profile, selected.ExpectedProfile))
	require.NoFileExists(t, marker)

	// Unknown and already completed candidates never become a trusted read selection.
	request.GetTape().MediaId = proto.Int64(frozen.MediaID + 100)
	_, err = runner.frozenReadTarget(ctx, request)
	require.ErrorContains(t, err, "no frozen pending")
	require.NoError(t, runner.db.Model(&Copy{}).Where("id = ?", frozen.ID).Update("status", entity.CopyStatus_COPY_STATUS_COMPLETED).Error)
	request.GetTape().MediaId = proto.Int64(frozen.MediaID)
	_, err = runner.frozenReadTarget(ctx, request)
	require.ErrorContains(t, err, "no frozen pending")
	require.NoFileExists(t, marker)
}

func TestRestoreRefreshRejectsKnownLTFSBeforeDeviceAccess(t *testing.T) {
	// Only frozen tar selections authorize refresh; neither explicit nor all-LTFS selection needs an adapter probe.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "copies.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Copy{}))
	require.NoError(t, db.Create(&Copy{ID: 1, ItemID: 1, MediaID: 1, MediaIdentity: "LTFS01",
		Status: entity.CopyStatus_COPY_STATUS_PENDING, MediaProfile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV0}).Pack()}).Error)
	runner := &jobRestoreRunner{db: db}
	for _, hint := range []*int64{nil, proto.Int64(1)} {
		_, err := runner.frozenReadTarget(context.Background(), (&entity.ReadTapeTarget{
			Device: "/dev/nst0", MediaId: hint, RefreshPositionMap: true,
		}).Pack())
		require.ErrorContains(t, err, "refresh requires tar")
	}
	_, err = runner.selectFrozenTapeMedia(context.Background(), &entity.ReadTapeTarget{RefreshPositionMap: true}, "LTFS01")
	require.ErrorContains(t, err, "refresh requires tar")
}

func TestRestoreAutomaticSelectionUsesOnlyFrozenPendingFacts(t *testing.T) {
	// A large native candidate set and a settled tar copy keep the existing LTFS identity path.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "candidates.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Copy{}))
	runner := &jobRestoreRunner{db: db}
	rows := make([]Copy, 0, batchSize*2+1)
	for index := 1; index <= batchSize*2; index++ {
		rows = append(rows, Copy{ID: int64(index), ItemID: int64(index), MediaID: 1, MediaIdentity: "LTFS001",
			Status: entity.CopyStatus_COPY_STATUS_PENDING, MediaProfile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV0}).Pack()})
	}
	profile := &entity.TarMediaProfile{InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 262144}
	tarID := int64(batchSize*2 + 1)
	rows = append(rows, Copy{ID: tarID, ItemID: tarID, MediaID: 2, MediaIdentity: "TAR001", Status: entity.CopyStatus_COPY_STATUS_COMPLETED,
		MediaProfile: (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: profile}).Pack()})
	require.NoError(t, db.CreateInBatches(rows, batchSize).Error)
	raw, err := runner.hasPendingTarMedia(ctx)
	require.NoError(t, err)
	require.False(t, raw)
	require.NoError(t, db.Model(&Copy{}).Where("id = ?", tarID).Update("status", entity.CopyStatus_COPY_STATUS_PENDING).Error)
	raw, err = runner.hasPendingTarMedia(ctx)
	require.NoError(t, err)
	require.True(t, raw)

	// The finished profile stream releases its connection before identity lookup, and returns cloned expectations.
	request := &entity.ReadTapeTarget{Device: " /dev/nst0 "}
	selected, err := runner.selectFrozenTapeMedia(ctx, request, " tar001 ")
	require.NoError(t, err)
	require.Equal(t, "/dev/nst0", selected.GetTape().Device)
	require.EqualValues(t, 2, selected.GetTape().GetMediaId())
	require.Equal(t, "TAR001", selected.ExpectedIdentity)
	selected.ExpectedProfile.GetTape().Tar.RecordBytes = 512
	selected, err = runner.selectFrozenTapeMedia(ctx, request, "TAR001")
	require.NoError(t, err)
	require.EqualValues(t, 262144, selected.ExpectedProfile.GetTape().Tar.RecordBytes)
	require.Nil(t, request.MediaId)

	// The cartridge memory barcode selects either format.
	ltfs, err := runner.selectFrozenTapeMedia(ctx, request, "LTFS001")
	require.NoError(t, err)
	require.Equal(t, library.TapeFormatLTFSV0, ltfs.ExpectedProfile.GetTape().Format)
	_, err = runner.selectFrozenTapeMedia(ctx, request, "UNKNOWN")
	require.ErrorContains(t, err, "no frozen pending")
	require.NoError(t, db.Model(&Copy{}).Where("id = ?", tarID).Update("status", entity.CopyStatus_COPY_STATUS_COMPLETED).Error)
	_, err = runner.selectFrozenTapeMedia(ctx, request, "TAR001")
	require.ErrorContains(t, err, "no frozen pending")
}

func TestRestoreAutomaticSelectionRejectsAmbiguousFrozenMedia(t *testing.T) {
	// Reused barcodes cannot authorize choosing one of two frozen cartridge incarnations.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "candidates.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Copy{}))
	for index := int64(1); index <= 2; index++ {
		require.NoError(t, db.Create(&Copy{ID: index, ItemID: index, MediaID: index, MediaIdentity: "SAME001",
			Status: entity.CopyStatus_COPY_STATUS_PENDING, MediaProfile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV0}).Pack()}).Error)
	}
	runner := &jobRestoreRunner{db: db}
	_, err = runner.selectFrozenTapeMedia(context.Background(), &entity.ReadTapeTarget{Device: "/dev/nst0"}, "SAME001")
	require.ErrorContains(t, err, "ambiguous frozen pending")
}
