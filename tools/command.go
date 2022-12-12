package tools

import (
	"context"
	"io"
	"os/exec"
)

func RunCommand(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (<-chan error, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ch := make(chan error, 1)
	go func() {
		ch <- cmd.Wait()
	}()

	return ch, nil
}
