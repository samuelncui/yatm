package archive

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type stoppedTarTransport struct {
	commands     atomic.Int32
	closes       atomic.Int32
	finalWarning bool
	budgetStop   bool
}

func (t *stoppedTarTransport) Execute(_ context.Context, command tape.Command) (tape.Response, error) {
	t.commands.Add(1)
	if t.finalWarning && command.CDB[0] == 0x0a {
		sense := make([]byte, 18)
		sense[0], sense[2], sense[7], sense[13] = 0xf0, 0x40, 10, 2
		binary.BigEndian.PutUint32(sense[3:7], uint32(len(command.Data)))
		return tape.Response{Completed: true, Status: 2, Residual: int32(len(command.Data)), Sense: sense}, nil
	}
	if t.budgetStop && command.CDB[0] == 0x0a {
		if len(command.Data) != 1024 {
			return tape.Response{}, errors.New("refused member emitted bytes beyond the normal tar trailer")
		}
		for _, value := range command.Data {
			if value != 0 {
				return tape.Response{}, errors.New("refused member emitted a header or payload")
			}
		}
		return tape.Response{Completed: true}, nil
	}
	if (t.finalWarning || t.budgetStop) && command.CDB[0] == 0x10 {
		return tape.Response{Completed: true}, nil
	}
	return tape.Response{}, errors.New("stopped tar Session issued another command")
}

func (t *stoppedTarTransport) Close() error {
	t.closes.Add(1)
	return nil
}

func TestSuccessfulEmptyTarFormatRegistersMedia(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		finalWarning, budgetStop bool
	}{
		{name: "prepared EW"},
		{name: "final buffered WRITE EW", finalWarning: true},
		{name: "cached capacity before header", budgetStop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Freeze a real original and retain its pending work across a target-full Media attempt.
			ctx := context.Background()
			exe := setupTestExecutor(t, executor.Scripts{})
			require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, "file.txt"), []byte("saved"), 0644))
			job := createArchiveJob(t, exe, &archiveTestSource{Base: exe.Paths().Source, Path: []string{"file.txt"}})
			waitIndexed(t, exe, job.ID)
			value, err := exe.GetJobRunner(ctx, job.ID)
			require.NoError(t, err)
			runner := value.(*jobArchiveRunner)
			before := runner.progressSnapshot()
			descriptor := &mediapkg.Descriptor{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "EMP001", CreateTime: time.Unix(1, 0),
				Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 262144}}).Pack()}

			// Exercise prepared/settled EW and a cached refusal through the same ordinary publication boundary.
			directory, err := exe.EnsureTapeWorkPath(ctx, job.ID, descriptor.Identity)
			require.NoError(t, err)
			transport := &stoppedTarTransport{finalWarning: tc.finalWarning, budgetStop: tc.budgetStop}
			mode := tape.RecordMode{MaximumBytes: 262144, WarningCounts: true}
			var stop error
			complete := func(context.Context) error { return nil }
			if !tc.finalWarning && !tc.budgetStop {
				stop = errors.Join(mediapkg.ErrTargetNoSpace, tape.ErrEarlyWarning)
			} else {
				complete = nil // The real record provider synchronizes exactly one terminating filemark.
			}
			var capacity *mediapkg.TarCapacityOptions
			if tc.budgetStop {
				now := time.Now().Add(-time.Millisecond)
				capacity = &mediapkg.TarCapacityOptions{Initial: &tape.CapacitySample{
					IssuedAt: now, CompletedAt: now, RemainingBytes: proto.Uint64(0), Quality: tape.CapacityEstimate,
				}, Cost: func(encoded, _ uint64, _ uint32) (uint64, error) { return encoded, nil }}
			}
			session, err := mediapkg.NewTarWriteSession(tape.Records{Transport: transport, Mode: mode, Timeout: time.Second}, mediapkg.TarSessionOptions{
				Media: descriptor, Mode: mode, TapeFile: 1, Stop: stop,
				Complete: complete, Capacity: capacity,
				Cleanup: func(context.Context) error { return transport.Close() },
			})
			require.NoError(t, err)
			target := (&entity.ArchiveTapeTarget{Device: "/dev/nst0", Barcode: descriptor.Identity,
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT, Format: proto.String(library.TapeFormatTarPAXV1)}).Pack()

			// Admit the ordinary Archive lifecycle; real ACP, Session finalization and Library publish together.
			outcome := make(chan error, 1)
			require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(attempt context.Context, _ executor.Runner) error {
				if err := runner.transition(archiveStatePreparingMedia); err != nil {
					outcome <- err
					return err
				}
				err := runner.archivePreparedMedia(attempt, target, session)
				err = errors.Join(err, runner.transition(archiveStateWaitingForMedia))
				outcome <- err
				return err
			}))
			var attemptErr error
			select {
			case attemptErr = <-outcome:
			case <-time.After(5 * time.Second):
				t.Fatal("empty tar FORMAT did not settle")
			}
			require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
			require.ErrorIs(t, attemptErr, mediapkg.ErrTargetNoSpace)
			if tc.budgetStop {
				require.NotErrorIs(t, attemptErr, tape.ErrEarlyWarning)
			} else {
				require.ErrorIs(t, attemptErr, tape.ErrEarlyWarning)
			}

			// The initialized Media and its searchable Job property survive without any Position or copied item.
			media, err := exe.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, descriptor.Identity)
			require.NoError(t, err)
			require.Positive(t, media.ID)
			positions, err := exe.Lib().ListMediaFilePositions(ctx, media.ID, "", 100)
			require.NoError(t, err)
			require.Empty(t, positions)
			jobs, err := exe.ListJob(ctx, &entity.JobFilter{MediaId: proto.Int64(media.ID)})
			require.NoError(t, err)
			require.Len(t, jobs.Jobs, 1)
			require.Equal(t, job.ID, jobs.Jobs[0].ID)
			var item Item
			require.NoError(t, runner.db.First(&item).Error)
			require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, item.Status)
			require.Empty(t, item.MediaPath)
			require.Nil(t, item.Result)

			// The zero-file report and READY checkpoint retain the stop cause without advancing content progress.
			data, err := os.ReadFile(filepath.Join(directory, "yatm-report.json"))
			require.NoError(t, err)
			var report mediaArchiveReport
			require.NoError(t, json.Unmarshal(data, &report))
			require.Equal(t, media.ID, report.MediaID)
			require.Zero(t, report.FileCount)
			require.Zero(t, report.Bytes)
			require.Contains(t, report.TerminationError, mediapkg.ErrTargetNoSpace.Error())
			state, err := exe.GetJob(ctx, job.ID)
			require.NoError(t, err)
			require.Equal(t, entity.JobStatus_JOB_STATUS_READY, state.Status)
			require.Equal(t, entity.JobStatus_JOB_STATUS_READY, state.Checkpoint)
			require.Contains(t, state.Error, mediapkg.ErrTargetNoSpace.Error())
			if tc.budgetStop {
				require.Contains(t, report.TerminationError, "capacity admission stopped")
				require.Contains(t, state.Error, "capacity admission stopped")
			} else {
				require.Contains(t, report.TerminationError, tape.ErrEarlyWarning.Error())
				require.Contains(t, state.Error, tape.ErrEarlyWarning.Error())
			}
			after := runner.progressSnapshot()
			require.Equal(t, before.CopiedBytes, after.CopiedBytes)
			require.Equal(t, before.CopiedFileCount, after.CopiedFileCount)
			require.Equal(t, before.TotalBytes, after.TotalBytes)
			require.Equal(t, before.TotalFileCount, after.TotalFileCount)
			require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, after.GetStage().GetPhase())
			if tc.finalWarning || tc.budgetStop {
				require.EqualValues(t, 2, transport.commands.Load(), "one final WRITE and one completing filemark")
				require.NotContains(t, report.TerminationError, "missing staged files")
			} else {
				require.Zero(t, transport.commands.Load())
			}
			require.EqualValues(t, 1, transport.closes.Load())

		})
	}
}
