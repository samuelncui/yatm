package executor

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/samber/lo"
	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/tools"
)

func (a *jobArchiveExecutor) makeTape(ctx context.Context, device, barcode, name string) (rerr error) {
	barcode = strings.ToUpper(barcode)

	state := a.getState()
	if state == nil {
		return fmt.Errorf("cannot found archive state, abort")
	}

	if !a.exe.OccupyDevice(device) {
		return fmt.Errorf("device is using, device= %s", device)
	}
	defer a.exe.ReleaseDevice(device)

	defer a.makeTapeFinished(tools.WithoutTimeout(ctx))
	encryption, keyPath, keyRecycle, err := a.exe.newKey()
	if err != nil {
		return err
	}
	defer keyRecycle()

	if err := runCmd(a.logger, a.exe.makeEncryptCmd(ctx, device, keyPath, barcode, name)); err != nil {
		return fmt.Errorf("run encrypt script fail, %w", err)
	}

	mkfsCmd := exec.CommandContext(ctx, a.exe.scripts.Mkfs)
	mkfsCmd.Env = append(mkfsCmd.Env, fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("TAPE_BARCODE=%s", barcode), fmt.Sprintf("TAPE_NAME=%s", name))
	if err := runCmd(a.logger, mkfsCmd); err != nil {
		return fmt.Errorf("run mkfs script fail, %w", err)
	}

	mountPoint, err := os.MkdirTemp("", "*.ltfs")
	if err != nil {
		return fmt.Errorf("create temp mountpoint, %w", err)
	}

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

	wildcardJobOpts := make([]acp.WildcardJobOption, 0, 6)
	wildcardJobOpts = append(wildcardJobOpts, acp.Target(mountPoint))
	for _, source := range state.Sources {
		if source.Status == entity.CopyStatus_SUBMITED {
			continue
		}
		wildcardJobOpts = append(wildcardJobOpts, acp.AccurateSource(source.Source.Base, source.Source.Path))
	}

	opts := make([]acp.Option, 0, 4)
	opts = append(opts, acp.WildcardJob(wildcardJobOpts...))
	opts = append(opts, acp.WithHash(true))
	opts = append(opts, acp.SetToDevice(acp.LinearDevice(true)))
	opts = append(opts, acp.WithLogger(a.logger))

	reportHander, reportGetter := acp.NewReportGetter()
	opts = append(opts, acp.WithEventHandler(reportHander))

	a.progress = newProgress()
	defer func() { a.progress = nil }()

	var dropToReadonly bool
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
			case acp.JobStatusPending, acp.JobStatusPreparing:
				targetStatus = entity.CopyStatus_PENDING
			case acp.JobStatusCopying:
				targetStatus = entity.CopyStatus_RUNNING
			case acp.JobStatusFinished:
				targetStatus = entity.CopyStatus_FAILED
				if len(job.SuccessTargets) > 0 {
					a.logger.WithContext(ctx).Infof("file '%s' copy success, size= %d", src.RealPath(), job.Size)
					targetStatus = entity.CopyStatus_STAGED
					break // break from switch
				}

				for dst, err := range job.FailTargets {
					if err == nil {
						continue
					}
					if errors.Is(err, acp.ErrTargetNoSpace) {
						continue
					}

					a.logger.WithContext(ctx).WithError(err).Errorf("file '%s' copy fail, dst= '%s'", src.RealPath(), dst)
					if errors.Is(err, acp.ErrTargetDropToReadonly) {
						dropToReadonly = true
					}
				}
			default:
				return
			}

			a.updateJob(ctx, func(_ *Job, state *entity.JobArchiveState) error {
				idx := sort.Search(len(state.Sources), func(idx int) bool {
					return src.Compare(state.Sources[idx].Source) <= 0
				})
				if idx < 0 || idx >= len(state.Sources) || src.Compare(state.Sources[idx].Source) != 0 {
					return fmt.Errorf(
						"cannot found target file, real_path= %s found_index= %d tape_file_path= %v", src.RealPath(), idx,
						lo.Map(state.Sources, func(source *entity.SourceState, _ int) string { return source.Source.RealPath() }),
					)
				}

				founded := state.Sources[idx]
				if founded == nil || !src.Equal(founded.Source) {
					return fmt.Errorf(
						"founded file not match, real_path= %s found_path= %s tape_file_path= %v", src.RealPath(), founded.Source.RealPath(),
						lo.Map(state.Sources, func(source *entity.SourceState, _ int) string { return source.Source.RealPath() }),
					)
				}

				founded.Status = targetStatus
				return nil
			})
		}
	}))

	defer func() {
		ctx := tools.WithoutTimeout(ctx)

		// if tape drop to readonly, ltfs cannot write index to partition a.
		// rollback sources for next try.
		if dropToReadonly {
			a.logger.WithContext(ctx).Errorf("tape filesystem had droped to readonly, rollback, barcode= '%s'", barcode)
			a.rollbackSources(ctx)
			return
		}

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

func (a *jobArchiveExecutor) markSourcesAsSubmited(ctx context.Context, jobs []*acp.Job) error {
	return a.updateJob(ctx, func(_ *Job, state *entity.JobArchiveState) error {
		searchableSource := state.Sources[:]
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

			target.Status = entity.CopyStatus_SUBMITED
		}

		return nil
	})
}

func (a *jobArchiveExecutor) rollbackSources(ctx context.Context) error {
	return a.updateJob(ctx, func(_ *Job, state *entity.JobArchiveState) error {
		for _, source := range state.Sources {
			if source.Status == entity.CopyStatus_SUBMITED {
				continue
			}
			source.Status = entity.CopyStatus_PENDING
		}

		return nil
	})
}

func (a *jobArchiveExecutor) getTodoSources() int {
	state := a.getState()

	var todo int
	for _, s := range state.Sources {
		if s.Status == entity.CopyStatus_SUBMITED {
			continue
		}
		todo++
	}

	return todo
}

func (a *jobArchiveExecutor) makeTapeFinished(ctx context.Context) {
	if a.getTodoSources() > 0 {
		a.dispatch(ctx, &entity.JobArchiveDispatchParam{Param: &entity.JobArchiveDispatchParam_WaitForTape{WaitForTape: &entity.JobArchiveWaitForTapeParam{}}})
	} else {
		a.dispatch(ctx, &entity.JobArchiveDispatchParam{Param: &entity.JobArchiveDispatchParam_Finished{Finished: &entity.JobArchiveFinishedParam{}}})
	}
}
