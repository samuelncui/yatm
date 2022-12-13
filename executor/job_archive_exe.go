package executor

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abc950309/acp"
	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/library"
	"github.com/abc950309/tapewriter/tools"
	mapset "github.com/deckarep/golang-set/v2"
	"github.com/sirupsen/logrus"
)

var (
	runningArchives sync.Map
)

func (e *Executor) getArchiveExecutor(ctx context.Context, job *Job) *jobArchiveExecutor {
	if running, has := runningArchives.Load(job.ID); has {
		return running.(*jobArchiveExecutor)
	}
	return nil
}

func (e *Executor) newArchiveExecutor(ctx context.Context, job *Job) (*jobArchiveExecutor, error) {
	if exe := e.getArchiveExecutor(ctx, job); exe != nil {
		return exe, nil
	}

	logFile, err := e.newLogWriter(job.ID)
	if err != nil {
		return nil, fmt.Errorf("get log writer fail, %w", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, logFile))

	exe := &jobArchiveExecutor{
		exe: e,
		job: job,

		state: job.State.GetArchive(),

		logFile: logFile,
		logger:  logger,
	}

	runningArchives.Store(job.ID, exe)
	return exe, nil
}

type jobArchiveExecutor struct {
	exe *Executor
	job *Job

	stateLock sync.Mutex
	state     *entity.JobStateArchive

	progress *progress
	logFile  *os.File
	logger   *logrus.Logger
}

func (a *jobArchiveExecutor) submit(ctx context.Context, param *entity.JobArchiveNextParam) {
	if err := a.handle(ctx, param); err != nil {
		a.logger.WithContext(ctx).Infof("handler param fail, err= %w", err)
	}
}

func (a *jobArchiveExecutor) handle(ctx context.Context, param *entity.JobArchiveNextParam) error {
	if p := param.GetCopying(); p != nil {
		if err := a.switchStep(ctx, entity.JobArchiveStep_Copying, entity.JobStatus_Processing, mapset.NewThreadUnsafeSet(entity.JobArchiveStep_WaitForTape)); err != nil {
			return err
		}

		tools.Working()
		go tools.WrapWithLogger(ctx, a.logger, func() {
			defer tools.Done()
			if err := a.makeTape(tools.ShutdownContext, p.Device, p.Barcode, p.Name); err != nil {
				a.logger.WithContext(ctx).WithError(err).Errorf("make type has error, barcode= '%s' name= '%s'", p.Barcode, p.Name)
			}
		})

		return nil
	}

	if p := param.GetWaitForTape(); p != nil {
		return a.switchStep(ctx, entity.JobArchiveStep_WaitForTape, entity.JobStatus_Processing, mapset.NewThreadUnsafeSet(entity.JobArchiveStep_Pending, entity.JobArchiveStep_Copying))
	}

	if p := param.GetFinished(); p != nil {
		if err := a.switchStep(ctx, entity.JobArchiveStep_Finished, entity.JobStatus_Completed, mapset.NewThreadUnsafeSet(entity.JobArchiveStep_Copying)); err != nil {
			return err
		}

		a.logFile.Close()
		runningArchives.Delete(a.job.ID)
		return nil
	}

	return nil
}

func (a *jobArchiveExecutor) makeTape(ctx context.Context, device, barcode, name string) (rerr error) {
	if !a.exe.occupyDevice(device) {
		return fmt.Errorf("device is using, device= %s", device)
	}
	defer a.exe.releaseDevice(device)
	defer a.makeTapeFinished(tools.WithoutTimeout(ctx))

	encryption, keyPath, keyRecycle, err := a.exe.newKey()
	if err != nil {
		return err
	}
	defer keyRecycle()

	if err := runCmd(a.logger, a.exe.makeEncryptCmd(ctx, device, keyPath, barcode, name)); err != nil {
		return fmt.Errorf("run encrypt script fail, %w", err)
	}

	mkfsCmd := exec.CommandContext(ctx, a.exe.mkfsScript)
	mkfsCmd.Env = append(mkfsCmd.Env, fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("TAPE_BARCODE=%s", barcode), fmt.Sprintf("TAPE_NAME=%s", name))
	if err := runCmd(a.logger, mkfsCmd); err != nil {
		return fmt.Errorf("run mkfs script fail, %w", err)
	}

	mountPoint, err := os.MkdirTemp("", "*.ltfs")
	if err != nil {
		return fmt.Errorf("create temp mountpoint, %w", err)
	}

	mountCmd := exec.CommandContext(ctx, a.exe.mountScript)
	mountCmd.Env = append(mountCmd.Env, fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("MOUNT_POINT=%s", mountPoint))
	if err := runCmd(a.logger, mountCmd); err != nil {
		return fmt.Errorf("run mount script fail, %w", err)
	}
	defer func() {
		umountCmd := exec.CommandContext(tools.WithoutTimeout(ctx), a.exe.umountScript)
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
	for _, source := range a.state.Sources {
		if source.Status == entity.CopyStatus_Submited {
			continue
		}
		opts = append(opts, acp.AccurateSource(source.Source.Base, source.Source.Path))
	}

	opts = append(opts, acp.Target(mountPoint))
	opts = append(opts, acp.WithHash(true))
	opts = append(opts, acp.SetToDevice(acp.LinearDevice(true)))
	opts = append(opts, acp.WithLogger(a.logger))

	reportHander, reportGetter := acp.NewReportGetter()
	opts = append(opts, acp.WithEventHandler(reportHander))

	a.progress = newProgress()
	defer func() { a.progress = nil }()

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
				targetStatus = entity.CopyStatus_Pending
			case "preparing":
				targetStatus = entity.CopyStatus_Running
			case "finished":
				a.logger.WithContext(ctx).Infof("file '%s' copy finished, size= %d", src.RealPath(), job.Size)
				targetStatus = entity.CopyStatus_Staged

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

			idx := sort.Search(len(a.state.Sources), func(idx int) bool {
				return src.Compare(a.state.Sources[idx].Source) <= 0
			})

			target := a.state.Sources[idx]
			if target == nil || !src.Equal(target.Source) {
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
		ctx := tools.WithoutTimeout(ctx)

		report := reportGetter()
		sort.Slice(report.Jobs, func(i, j int) bool {
			return entity.NewSourceFromACPJob(report.Jobs[i]).Compare(entity.NewSourceFromACPJob(report.Jobs[j])) < 0
		})

		reportFile, err := a.exe.newReportWriter(barcode)
		if err != nil {
			a.logger.WithContext(ctx).WithError(err).Warnf("open report file fail, barcode= '%s'", barcode)
		} else {
			defer reportFile.Close()
			tools.WrapWithLogger(ctx, a.logger, func() {
				reportFile.Write([]byte(report.ToJSONString(false)))
			})
		}

		filteredJobs := make([]*acp.Job, 0, len(report.Jobs))
		files := make([]*library.TapeFile, 0, len(report.Jobs))
		for _, job := range report.Jobs {
			if len(job.SuccessTargets) == 0 {
				continue
			}
			if !job.Mode.IsRegular() {
				continue
			}

			hash, err := hex.DecodeString(job.SHA256)
			if err != nil {
				a.logger.WithContext(ctx).WithError(err).Warnf("decode sha256 fail, path= '%s'", entity.NewSourceFromACPJob(job).RealPath())
				continue
			}

			files = append(files, &library.TapeFile{
				Path:      path.Join(job.Path...),
				Size:      job.Size,
				Mode:      job.Mode,
				ModTime:   job.ModTime,
				WriteTime: job.WriteTime,
				Hash:      hash,
			})
			filteredJobs = append(filteredJobs, job)
		}

		tape, err := a.exe.lib.CreateTape(ctx, &library.Tape{
			Barcode:    barcode,
			Name:       name,
			Encryption: encryption,
			CreateTime: time.Now(),
		}, files)
		if err != nil {
			rerr = tools.AppendError(rerr, fmt.Errorf("create tape fail, barcode= '%s' name= '%s', %w", barcode, name, err))
			return
		}
		a.logger.Infof("create tape success, tape_id= %d", tape.ID)

		if err := a.exe.lib.TrimFiles(ctx); err != nil {
			a.logger.WithError(err).Warnf("trim library files fail")
		}

		if err := a.markSourcesAsSubmited(ctx, filteredJobs); err != nil {
			rerr = tools.AppendError(rerr, fmt.Errorf("mark source as submited fail, %w", err))
			return
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

func (a *jobArchiveExecutor) switchStep(ctx context.Context, target entity.JobArchiveStep, status entity.JobStatus, expect mapset.Set[entity.JobArchiveStep]) error {
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

func (a *jobArchiveExecutor) markSourcesAsSubmited(ctx context.Context, jobs []*acp.Job) error {
	a.stateLock.Lock()
	defer a.stateLock.Unlock()

	searchableSource := a.state.Sources[:]
	for _, job := range jobs {
		src := entity.NewSourceFromACPJob(job)
		for idx, testSrc := range searchableSource {
			if src.Compare(testSrc.Source) <= 0 {
				searchableSource = searchableSource[idx:]
				break
			}
		}

		target := searchableSource[0]
		if target == nil || !src.Equal(target.Source) {
			continue
		}

		target.Status = entity.CopyStatus_Submited
	}

	if _, err := a.exe.SaveJob(ctx, a.job); err != nil {
		return fmt.Errorf("mark sources as submited, save job, %w", err)
	}
	return nil
}

func (a *jobArchiveExecutor) getTodoSources() int {
	a.stateLock.Lock()
	defer a.stateLock.Unlock()

	var todo int
	for _, s := range a.state.Sources {
		if s.Status == entity.CopyStatus_Submited {
			continue
		}
		todo++
	}

	return todo
}

func (a *jobArchiveExecutor) makeTapeFinished(ctx context.Context) {
	if a.getTodoSources() > 0 {
		a.submit(ctx, &entity.JobArchiveNextParam{Param: &entity.JobArchiveNextParam_WaitForTape{WaitForTape: &entity.JobArchiveWaitForTapeParam{}}})
	} else {
		a.submit(ctx, &entity.JobArchiveNextParam{Param: &entity.JobArchiveNextParam_Finished{Finished: &entity.JobArchiveFinishedParam{}}})
	}
}
