package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/abc950309/tapewriter/library"
	"github.com/sirupsen/logrus"
)

func (e *Executor) RestoreLoadTape(ctx context.Context, device string, tape *library.Tape) error {
	if !e.occupyDevice(device) {
		return fmt.Errorf("device is using, device= %s", device)
	}
	defer e.releaseDevice(device)

	keyPath, keyRecycle, err := e.restoreKey(tape.Encryption)
	if err != nil {
		return err
	}
	defer func() {
		time.Sleep(time.Second)
		keyRecycle()
	}()

	logger := logrus.StandardLogger()

	if err := runCmd(logger, e.makeEncryptCmd(ctx, device, keyPath, tape.Barcode, tape.Name)); err != nil {
		return fmt.Errorf("run encrypt script fail, %w", err)
	}

	mountPoint, err := os.MkdirTemp("", "*.ltfs")
	if err != nil {
		return fmt.Errorf("create temp mountpoint, %w", err)
	}

	mountCmd := exec.CommandContext(ctx, e.mountScript)
	mountCmd.Env = append(mountCmd.Env, fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("MOUNT_POINT=%s", mountPoint))
	if err := runCmd(logger, mountCmd); err != nil {
		return fmt.Errorf("run mount script fail, %w", err)
	}
	// defer func() {
	// 	umountCmd := exec.CommandContext(ctx, e.umountScript)
	// 	umountCmd.Env = append(umountCmd.Env, fmt.Sprintf("MOUNT_POINT=%s", mountPoint))
	// 	if err := runCmd(logger, umountCmd); err != nil {
	// 		logger.WithContext(ctx).WithError(err).Errorf("run umount script fail, %s", mountPoint)
	// 		return
	// 	}
	// 	if err := os.Remove(mountPoint); err != nil {
	// 		logger.WithContext(ctx).WithError(err).Errorf("remove mount point fail, %s", mountPoint)
	// 		return
	// 	}
	// }()

	return nil
}
