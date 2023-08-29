package executor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"

	"github.com/sirupsen/logrus"
)

func (e *Executor) logPath(jobID int64) (string, string) {
	return path.Join(e.paths.Work, "job-logs"), fmt.Sprintf("%d.log", jobID)
}

func (e *Executor) newLogWriter(jobID int64) (*os.File, error) {
	dir, filename := e.logPath(jobID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("make job log dir fail, path= '%s', err= %w", dir, err)
	}

	file, err := os.OpenFile(path.Join(dir, filename), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("create file fail, path= '%s', err= %w", path.Join(dir, filename), err)
	}

	return file, nil
}

func (e *Executor) NewLogReader(jobID int64) (*os.File, error) {
	dir, filename := e.logPath(jobID)
	file, err := os.OpenFile(path.Join(dir, filename), os.O_RDONLY, 0644)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("create file")
	}

	return file, nil
}

func runCmdWithReturn(logger *logrus.Logger, cmd *exec.Cmd) ([]byte, error) {
	out, err := os.CreateTemp("", "*.out")
	if err != nil {
		return nil, fmt.Errorf("create cmd out fail, %w", err)
	}
	out.Chmod(fs.ModePerm)
	out.Close()
	defer os.Remove(out.Name())

	cmd.Env = append(cmd.Env, fmt.Sprintf("OUT=%s", out.Name()))
	if err := runCmd(logger, cmd); err != nil {
		return nil, err
	}

	buf, err := os.ReadFile(out.Name())
	if err != nil {
		return nil, fmt.Errorf("read cmd out fail, %w", err)
	}

	return buf, nil
}

func runCmd(logger *logrus.Logger, cmd *exec.Cmd) error {
	writer := logger.WriterLevel(logrus.InfoLevel)
	cmd.Stdout = writer
	cmd.Stderr = writer

	return cmd.Run()
}

func (e *Executor) reportPath(barcode string) (string, string) {
	return path.Join(e.paths.Work, "write-reports"), fmt.Sprintf("%s.log", barcode)
}

func (e *Executor) newReportWriter(barcode string) (*os.File, error) {
	dir, filename := e.reportPath(barcode)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("make job log dir fail, path= '%s', err= %w", dir, err)
	}

	file, err := os.OpenFile(path.Join(dir, filename), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("create file fail, path= '%s', err= %w", path.Join(dir, filename), err)
	}

	return file, nil
}
