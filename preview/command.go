package preview

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

const commandOutputLimit = 64 << 10

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = b.Buffer.Write(data[:remaining])
	}
	return len(data), nil
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	// Bound command output while preserving the complete process exit result.
	stdout := &limitedBuffer{limit: commandOutputLimit}
	stderr := &limitedBuffer{limit: commandOutputLimit}
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("run command failed, command=%q stderr=%q, %w", name, stderr.String(), err)
	}
	return stdout.String(), nil
}
