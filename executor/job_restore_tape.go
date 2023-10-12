package executor

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/samber/lo"
	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/tools"
)

func (a *jobRestoreExecutor) restoreTape(ctx context.Context, device string) (rerr error) {
	if !a.exe.OccupyDevice(device) {
		return fmt.Errorf("device is using, device= %s", device)
	}
	defer a.exe.ReleaseDevice(device)

	defer func() {
		tapes := a.getState().Tapes
		if _, found := lo.Find(tapes, func(item *entity.RestoreTape) bool {
			return item.Status != entity.CopyStatus_SUBMITED
		}); found {
			a.dispatch(tools.WithoutTimeout(ctx), &entity.JobRestoreDispatchParam{
				Param: &entity.JobRestoreDispatchParam_WaitForTape{WaitForTape: &entity.JobRestoreWaitForTapeParam{}},
			})
			return
		}

		a.dispatch(tools.WithoutTimeout(ctx), &entity.JobRestoreDispatchParam{
			Param: &entity.JobRestoreDispatchParam_Finished{Finished: &entity.JobRestoreFinishedParam{}},
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
	barcode = strings.ToUpper(barcode)

	tapes := a.getState().Tapes
	tape, found := lo.Find(tapes, func(t *entity.RestoreTape) bool {
		return t.Barcode == barcode
	})
	if !found || tape == nil {
		expects := lo.Map(tapes, func(t *entity.RestoreTape, _ int) string { return t.Barcode })
		return fmt.Errorf("unexpected tape barcode in library, has= '%s' expect= %v", barcode, expects)
	}
	if tape.Status == entity.CopyStatus_SUBMITED {
		return fmt.Errorf("unexpected restore tape state status, tape is restored, status= '%s'", tape.Status)
	}

	libTape, err := a.exe.lib.GetTape(ctx, tape.TapeId)
	if err != nil {
		return fmt.Errorf("get tape info fail, barcode= '%s' id= %d, %w", tape.Barcode, tape.TapeId, err)
	}

	keyPath, keyRecycle, err := a.exe.restoreKey(libTape.Encryption)
	if err != nil {
		return err
	}
	defer func() {
		time.Sleep(time.Second)
		keyRecycle()
	}()

	if err := runCmd(a.logger, a.exe.makeEncryptCmd(ctx, device, keyPath, barcode, libTape.Name)); err != nil {
		return fmt.Errorf("run encrypt script fail, %w", err)
	}

	mountPoint, err := os.MkdirTemp("", "*.ltfs")
	if err != nil {
		return fmt.Errorf("create temp mountpoint, %w", err)
	}
	sourcePath := tools.ThreadUnsafeCache(func(p string) string { return path.Join(mountPoint, p) })

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

	opts := make([]acp.Option, 0, 16)
	for _, f := range tape.Files {
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

	convertPath := tools.ThreadUnsafeCache(func(p string) string { return strings.ReplaceAll(p, "/", "\x00") })
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

				targetStatus = entity.CopyStatus_STAGED
				if len(job.FailTargets) > 0 {
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

			realPath := src.RealPath()
			a.updateJob(ctx, func(_ *Job, state *entity.JobRestoreState) error {
				tape, has := lo.Find(state.Tapes, func(tape *entity.RestoreTape) bool { return tape.Barcode == barcode })
				if !has || tape == nil {
					return fmt.Errorf("cannot found tape, barcode= %s", barcode)
				}

				idx := sort.Search(len(tape.Files), func(idx int) bool {
					return convertPath(realPath) <= convertPath(sourcePath(tape.Files[idx].TapePath))
				})
				if idx < 0 || idx >= len(tape.Files) {
					return fmt.Errorf(
						"cannot found target file, real_path= %s found_index= %d tape_file_path= %v", realPath, idx,
						lo.Map(tape.Files, func(file *entity.RestoreFile, _ int) string { return sourcePath(file.TapePath) }),
					)
				}

				found := tape.Files[idx]
				if found == nil || realPath != sourcePath(found.TapePath) {
					return fmt.Errorf(
						"cannot match found file, real_path= %s found_index= %d found_file_path= %s",
						realPath, idx, sourcePath(found.TapePath),
					)
				}

				if targetStatus == entity.CopyStatus_STAGED {
					if targetHash := hex.EncodeToString(found.Hash); targetHash != job.SHA256 {
						targetStatus = entity.CopyStatus_FAILED

						a.logger.Warnf(
							"copy checksum do not match target file hash, real_path= %s target_hash= %s copy_hash= %s",
							realPath, targetHash, job.SHA256,
						)
					}
					if targetSize := found.Size; targetSize != job.Size {
						targetStatus = entity.CopyStatus_FAILED

						a.logger.Warnf(
							"copy size do not match target file hash, real_path= %s target_size= %d copy_size= %d",
							realPath, targetSize, job.Size,
						)
					}
				}

				found.Status = targetStatus
				return nil
			})
		}
	}))

	a.updateJob(ctx, func(_ *Job, state *entity.JobRestoreState) error {
		tape, has := lo.Find(state.Tapes, func(tape *entity.RestoreTape) bool { return tape.Barcode == barcode })
		if !has || tape == nil {
			return fmt.Errorf("cannot found tape, barcode= %s", barcode)
		}

		tape.Status = entity.CopyStatus_RUNNING
		return nil
	})

	defer func() {
		a.updateJob(ctx, func(job *Job, state *entity.JobRestoreState) error {
			tape, has := lo.Find(state.Tapes, func(tape *entity.RestoreTape) bool { return tape.Barcode == barcode })
			if !has || tape == nil {
				return fmt.Errorf("cannot found tape, barcode= %s", barcode)
			}

			tape.Status = entity.CopyStatus_SUBMITED
			for _, file := range tape.Files {
				if file.Status == entity.CopyStatus_STAGED {
					file.Status = entity.CopyStatus_SUBMITED
				}

				if file.Status != entity.CopyStatus_SUBMITED {
					tape.Status = entity.CopyStatus_FAILED
				}
			}

			return nil
		})
	}()

	copyer, err := acp.New(ctx, opts...)
	if err != nil {
		rerr = fmt.Errorf("start copy fail, %w", err)
		return
	}

	copyer.Wait()
	return
}
