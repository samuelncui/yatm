package tools

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"

	"github.com/sirupsen/logrus"
)

func RunCmdWithReturn(logger *logrus.Logger, cmd *exec.Cmd) ([]byte, error) {
	out, err := os.CreateTemp("", "*.out")
	if err != nil {
		return nil, fmt.Errorf("create cmd out fail, %w", err)
	}
	out.Chmod(fs.ModePerm)
	out.Close()
	defer os.Remove(out.Name())

	cmd.Env = append(cmd.Env, fmt.Sprintf("OUT=%s", out.Name()))
	if err := RunCmd(logger, cmd); err != nil {
		return nil, err
	}

	buf, err := os.ReadFile(out.Name())
	if err != nil {
		return nil, fmt.Errorf("read cmd out fail, %w", err)
	}

	return buf, nil
}

func RunCmd(logger *logrus.Logger, cmd *exec.Cmd) error {
	writer := logger.WriterLevel(logrus.InfoLevel)
	cmd.Stdout = writer
	cmd.Stderr = writer

	return cmd.Run()
}
