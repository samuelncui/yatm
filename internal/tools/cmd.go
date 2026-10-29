package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/samuelncui/yatm/internal/resource"
	"github.com/sirupsen/logrus"
)

func RunCmdWithReturn(logger *logrus.Logger, cmd *exec.Cmd, work string) (_ []byte, rerr error) {
	// The script's OUT adapter remains a file, owned through command completion and readback.
	temporary, err := resource.NewTemporaryDirectory(work, "script-output-")
	if err != nil {
		return nil, fmt.Errorf("create command output directory failed, %w", err)
	}
	defer func() { rerr = errors.Join(rerr, temporary.Close()) }()
	path := filepath.Join(temporary.Directory, "output")
	if err := os.WriteFile(path, nil, fs.ModePerm); err != nil {
		return nil, fmt.Errorf("create command output failed, %w", err)
	}
	if err := os.Chmod(path, fs.ModePerm); err != nil {
		return nil, fmt.Errorf("set command output permissions failed, %w", err)
	}

	// Preserve the configured Tape script's output-file contract and execution context.
	cmd.Env = append(cmd.Env, fmt.Sprintf("OUT=%s", path))
	if err := RunCmd(logger, cmd); err != nil {
		return nil, err
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read command output failed, %w", err)
	}
	return buf, nil
}

func RunCmd(logger *logrus.Logger, cmd *exec.Cmd) (rerr error) {
	// The command owns both output streams and their logging pipe until Wait completes.
	writer := logger.WriterLevel(logrus.InfoLevel)
	defer func() { rerr = errors.Join(rerr, writer.Close()) }()
	cmd.Stdout = writer
	cmd.Stderr = writer

	return cmd.Run()
}
