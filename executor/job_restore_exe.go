package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abc950309/acp"
	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/tools"
	mapset "github.com/deckarep/golang-set/v2"
	jsoniter "github.com/json-iterator/go"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var (
	runningRestores sync.Map
)

func (e *Executor) getRestoreExecutor(ctx context.Context, job *Job) *jobRestoreExecutor {
	if running, has := runningRestores.Load(job.ID); has {
		return running.(*jobRestoreExecutor)
	}
	return nil
}

func (e *Executor) newRestoreExecutor(ctx context.Context, job *Job) (*jobRestoreExecutor, error) {
	if exe := e.getRestoreExecutor(ctx, job); exe != nil {
		return exe, nil
	}

	logFile, err := e.newLogWriter(job.ID)
	if err != nil {
		return nil, fmt.Errorf("get log writer fail, %w", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, logFile))

	exe := &jobRestoreExecutor{
		exe: e,
		job: job,

		state: job.State.GetRestore(),

		logFile: logFile,
		logger:  logger,
	}

	runningRestores.Store(job.ID, exe)
	return exe, nil
}

type jobRestoreExecutor struct {
	exe *Executor
	job *Job

	stateLock sync.Mutex
	state     *entity.JobRestoreState

	progress *progress
	logFile  *os.File
	logger   *logrus.Logger
}

func (a *jobRestoreExecutor) submit(ctx context.Context, param *entity.JobRestoreNextParam) {
	if err := a.handle(ctx, param); err != nil {
		a.logger.WithContext(ctx).Infof("handler param fail, err= %w", err)
	}
}

func (a *jobRestoreExecutor) handle(ctx context.Context, param *entity.JobRestoreNextParam) error {
	if p := param.GetCopying(); p != nil {
		if err := a.switchStep(
			ctx, entity.JobRestoreStep_COPYING, entity.JobStatus_PROCESSING,
			mapset.NewThreadUnsafeSet(entity.JobRestoreStep_WAIT_FOR_TAPE),
		); err != nil {
			return err
		}

		tools.Working()
		go tools.WrapWithLogger(ctx, a.logger, func() {
			defer tools.Done()
			if err := a.restoreTape(tools.ShutdownContext, p.Device); err != nil {
				a.logger.WithContext(ctx).WithError(err).Errorf("restore tape has error, device= '%s'", p.Device)
			}
		})

		return nil
	}

	if p := param.GetWaitForTape(); p != nil {
		return a.switchStep(
			ctx, entity.JobRestoreStep_WAIT_FOR_TAPE, entity.JobStatus_PROCESSING,
			mapset.NewThreadUnsafeSet(entity.JobRestoreStep_PENDING, entity.JobRestoreStep_COPYING),
		)
	}

	if p := param.GetFinished(); p != nil {
		if err := a.switchStep(
			ctx, entity.JobRestoreStep_FINISHED, entity.JobStatus_COMPLETED,
			mapset.NewThreadUnsafeSet(entity.JobRestoreStep_COPYING),
		); err != nil {
			return err
		}

		a.logFile.Close()
		runningRestores.Delete(a.job.ID)
		return nil
	}

	return nil
}

func (a *jobRestoreExecutor) restoreTape(ctx context.Context, device string) (rerr error) {
	if !a.exe.occupyDevice(device) {
		return fmt.Errorf("device is using, device= %s", device)
	}
	defer a.exe.releaseDevice(device)
	defer func() {
		if _, found := lo.Find(a.state.Tapes, func(item *entity.RestoreTape) bool {
			return item.Status != entity.CopyStatus_SUBMITED
		}); found {
			a.submit(tools.WithoutTimeout(ctx), &entity.JobRestoreNextParam{
				Param: &entity.JobRestoreNextParam_WaitForTape{WaitForTape: &entity.JobRestoreWaitForTapeParam{}},
			})
			return
		}

		a.submit(tools.WithoutTimeout(ctx), &entity.JobRestoreNextParam{
			Param: &entity.JobRestoreNextParam_Finished{Finished: &entity.JobRestoreFinishedParam{}},
		})
	}()

	readInfoCmd := exec.CommandContext(ctx, a.exe.scripts.ReadInfo)
	readInfoCmd.Env = append(readInfoCmd.Env, fmt.Sprintf("DEVICE=%s", device))
	infoBuf, err := runCmdWithReturn(a.logger, readInfoCmd)
	if err != nil {
		return fmt.Errorf("run read info script fail, %w", err)
	}

	barcode := jsoniter.Get(infoBuf, "barcode").ToString()
	if len(barcode) > 6 {
		barcode = barcode[:6]
	}

	restoreTape, found := lo.Find(a.state.Tapes, func(t *entity.RestoreTape) bool {
		return t.Barcode == barcode
	})
	if !found || restoreTape == nil {
		expects := lo.Map(a.state.Tapes, func(t *entity.RestoreTape, _ int) string { return t.Barcode })
		return fmt.Errorf("unexpected tape barcode in library, has= '%s' expect= %v", barcode, expects)
	}
	if restoreTape.Status != entity.CopyStatus_PENDING {
		return fmt.Errorf("unexpected restore tape state status, has= '%s' expect= '%s'", restoreTape.Status, entity.CopyStatus_PENDING)
	}

	tape, err := a.exe.lib.GetTape(ctx, restoreTape.TapeId)
	if err != nil {
		return fmt.Errorf("get tape info fail, barcode= '%s' id= %d, %w", restoreTape.Barcode, restoreTape.TapeId, err)
	}

	keyPath, keyRecycle, err := a.exe.restoreKey(tape.Encryption)
	if err != nil {
		return err
	}
	defer func() {
		time.Sleep(time.Second)
		keyRecycle()
	}()

	if err := runCmd(a.logger, a.exe.makeEncryptCmd(ctx, device, keyPath, barcode, tape.Name)); err != nil {
		return fmt.Errorf("run encrypt script fail, %w", err)
	}

	mountPoint, err := os.MkdirTemp("", "*.ltfs")
	if err != nil {
		return fmt.Errorf("create temp mountpoint, %w", err)
	}
	sourcePath := tools.Cache(func(p string) string { return path.Join(mountPoint, p) })

	mountCmd := exec.CommandContext(ctx, a.exe.scripts.Mount)
	mountCmd.Env = append(mountCmd.Env, fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("MOUNT_POINT=%s", mountPoint))
	if err := runCmd(a.logger, mountCmd); err != nil {
		return fmt.Errorf("run mount script fail, %w", err)
	}

	defer func() {
		umountCmd := exec.CommandContext(tools.WithoutTimeout(ctx), a.exe.scripts.Umount)
		umountCmd.Env = append(umountCmd.Env, fmt.Sprintf("MOUNT_POINT=%s", mountPoint))
		if err := runCmd(a.logger, umountCmd); err != nil {
			a.logger.WithContext(ctx).WithError(err).Errorf("run umount script fail, %s", mountPoint)
			return
		}
		if err := os.Remove(mountPoint); err != nil {
			a.logger.WithContext(ctx).WithError(err).Errorf("remove mount point fail, %s", mountPoint)
			return
		}
	}()

	opts := make([]acp.Option, 0, 4)
	for _, f := range restoreTape.Files {
		if f.Status == entity.CopyStatus_SUBMITED {
			continue
		}

		opts = append(opts, acp.AccurateJob(sourcePath(f.TapePath), []string{path.Join(a.exe.paths.Target, f.TargetPath)}))
	}

	opts = append(opts, acp.WithHash(true))
	opts = append(opts, acp.SetFromDevice(acp.LinearDevice(true)))
	opts = append(opts, acp.WithLogger(a.logger))

	a.progress = newProgress()
	defer func() { a.progress = nil }()

	convertPath := tools.Cache(func(p string) string { return strings.ReplaceAll(p, "/", "\x00") })
	opts = append(opts, acp.WithEventHandler(func(ev acp.Event) {
		switch e := ev.(type) {
		case *acp.EventUpdateCount:
			atomic.StoreInt64(&a.progress.totalBytes, e.Bytes)
			atomic.StoreInt64(&a.progress.totalFiles, e.Files)
			return
		case *acp.EventUpdateProgress:
			a.progress.setBytes(e.Bytes)
			atomic.StoreInt64(&a.progress.files, e.Files)
			return
		case *acp.EventReportError:
			a.logger.WithContext(ctx).Errorf("acp report error, src= '%s' dst= '%s' err= '%s'", e.Error.Src, e.Error.Dst, e.Error.Err)
			return
		case *acp.EventUpdateJob:
			job := e.Job
			src := entity.NewSourceFromACPJob(job)

			var targetStatus entity.CopyStatus
			switch job.Status {
			case "pending":
				targetStatus = entity.CopyStatus_PENDING
			case "preparing":
				targetStatus = entity.CopyStatus_RUNNING
			case "finished":
				a.logger.WithContext(ctx).Infof("file '%s' copy finished, size= %d", src.RealPath(), job.Size)

				targetStatus = entity.CopyStatus_SUBMITED
				if len(job.SuccessTargets) > 0 {
					targetStatus = entity.CopyStatus_FAILED
				}

				for dst, err := range job.FailTargets {
					if err == nil {
						continue
					}
					a.logger.WithContext(ctx).WithError(err).Errorf("file '%s' copy fail, dst= '%s'", src.RealPath(), dst)
				}
			default:
				return
			}

			a.stateLock.Lock()
			defer a.stateLock.Unlock()

			realPath := src.RealPath()
			idx := sort.Search(len(restoreTape.Files), func(idx int) bool {
				return convertPath(realPath) < convertPath(sourcePath(restoreTape.Files[idx].TapePath))
			})

			target := restoreTape.Files[idx]
			if target == nil || realPath != sourcePath(target.TapePath) {
				return
			}
			target.Status = targetStatus

			if _, err := a.exe.SaveJob(ctx, a.job); err != nil {
				logrus.WithContext(ctx).Infof("save job for update file fail, name= %s", job.Base+path.Join(job.Path...))
			}
			return
		}
	}))

	defer func() {
		restoreTape.Status = entity.CopyStatus_SUBMITED
		if _, err := a.exe.SaveJob(tools.WithoutTimeout(ctx), a.job); err != nil {
			logrus.WithContext(ctx).Infof("save job for submit tape fail, barcode= %s", restoreTape.Barcode)
		}
	}()

	copyer, err := acp.New(ctx, opts...)
	if err != nil {
		rerr = fmt.Errorf("start copy fail, %w", err)
		return
	}

	copyer.Wait()
	return
}

func (a *jobRestoreExecutor) switchStep(ctx context.Context, target entity.JobRestoreStep, status entity.JobStatus, expect mapset.Set[entity.JobRestoreStep]) error {
	a.stateLock.Lock()
	defer a.stateLock.Unlock()

	if !expect.Contains(a.state.Step) {
		return fmt.Errorf("unexpected current step, target= '%s' expect= '%s' has= '%s'", target, expect, a.state.Step)
	}

	a.state.Step = target
	a.job.Status = status
	if _, err := a.exe.SaveJob(ctx, a.job); err != nil {
		return fmt.Errorf("switch to step copying, save job fail, %w", err)
	}

	return nil
}
