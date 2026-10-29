package executor

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func readyReadOrderJob(t *testing.T, exe *Executor, kind entity.JobKind) *Job {
	t.Helper()
	// A ready synthetic bundle isolates admission from the different workflows' manifest preparation.
	job := createTestCatalogJob(t, exe, kind)
	require.NoError(t, exe.createBundle(context.Background(), job, &JobRecord{
		ID: singletonJobID, Kind: kind, Status: entity.JobStatus_JOB_STATUS_READY, Checkpoint: entity.JobStatus_JOB_STATUS_READY,
	}, func(*gorm.DB) error { return nil }))
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	exe.runners[job.ID] = &testRunner{exe: exe, job: stored}
	return stored
}

func TestTapeReadOrderFreezesAtEveryJobAdmission(t *testing.T) {
	for _, kind := range []entity.JobKind{entity.JobKind_JOB_KIND_ARCHIVE, entity.JobKind_JOB_KIND_RESTORE, entity.JobKind_JOB_KIND_SCAN} {
		t.Run(kind.String(), func(t *testing.T) {
			// The admitted clone must survive a Settings edit while the attempt waits for its read preparation.
			ctx := context.Background()
			exe := setupTestExecutor(t)
			value := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
				Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO}},
			}}}
			_, err := exe.settings.Device.Save(ctx, value)
			require.NoError(t, err)
			job := readyReadOrderJob(t, exe, kind)
			observed := make(chan entity.TapeReadOrder, 2)
			release := make(chan struct{})
			require.NoError(t, exe.StartJob(ctx, job.ID, kind, func(runCtx context.Context, _ Runner) error {
				backend := &mediaBackend{executor: exe}
				order, err := backend.resolveTapeReadOrder(runCtx, "/dev/nst0")
				if err != nil {
					return err
				}
				observed <- order
				<-release
				order, err = backend.resolveTapeReadOrder(runCtx, "/dev/nst0")
				if err != nil {
					return err
				}
				observed <- order
				return errors.New("controlled Media failure")
			}))
			require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, <-observed)
			value.Devices[0].GetTape().ReadOrder = entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL
			_, err = exe.settings.Device.Save(ctx, value)
			require.NoError(t, err)
			close(release)
			require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, <-observed)
			waitJobReleased(t, exe, job.ID)

			// A fresh Job observes the edit without restarting the failed Scan or resetting any lifecycle.
			fresh := readyReadOrderJob(t, exe, kind)
			require.NoError(t, exe.StartJob(ctx, fresh.ID, kind, func(runCtx context.Context, _ Runner) error {
				order, err := (&mediaBackend{executor: exe}).resolveTapeReadOrder(runCtx, "/dev/nst0")
				if err == nil {
					observed <- order
				}
				return err
			}))
			require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, <-observed)
			waitJobReleased(t, exe, fresh.ID)
		})
	}
}

func TestTarAutoFallsBackAndRefreshRejectsBeforeControls(t *testing.T) {
	ctx := context.Background()
	exe, backend, fixture, jobID := tarFactoryFixture(t)
	write, err := backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
	require.NoError(t, err)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	stored, err := exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile})
	require.NoError(t, err)
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	_, err = exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
		Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO}},
	}}})
	require.NoError(t, err)
	var diagnostics bytes.Buffer
	backend.logger.SetOutput(&diagnostics)
	read, err := backend.NewReadSession(ctx, tarEncryptedReadTarget(stored))
	require.NoError(t, err)
	require.NoError(t, read.Finalize(ctx, nil))
	require.Contains(t, diagnostics.String(), "requested_read_order=TAPE_READ_ORDER_AUTO")
	require.Contains(t, diagnostics.String(), "effective_read_order=TAPE_READ_ORDER_LOGICAL")

	// The explicit unsupported action fails before settings, key load or physical positioning.
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	fixture.securityEvents = nil
	target := tarEncryptedReadTarget(stored)
	target.GetTape().RefreshPositionMap = true
	_, err = backend.NewReadSession(ctx, target)
	require.Error(t, err)
	require.Empty(t, fixture.securityEvents)
	require.Equal(t, fixture.opens, fixture.closes)

	// Automatic selection without a cartridge memory barcode fails before any candidate, key or motion.
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	fixture.barcode = ""
	_, err = exe.ResolveTapeReadMedia(ctx, jobID, target.GetTape(), func(context.Context, string) (*entity.ReadMediaTarget, error) {
		t.Fatal("a Tape without MAM identity cannot select a candidate")
		return nil, nil
	})
	require.ErrorContains(t, err, "Tape identity is unavailable")
	require.Empty(t, fixture.securityEvents)
}
