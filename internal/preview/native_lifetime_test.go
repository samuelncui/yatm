package preview

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/internal/previewprotocol"
	"github.com/stretchr/testify/require"
)

func TestRunNativeCancellationClosesInheritedOutput(t *testing.T) {
	// A configured helper wrapper can exit while its child still owns the output pipe.
	root := t.TempDir()
	pidPath := filepath.Join(root, "child.pid")
	t.Setenv("YATM_NATIVE_CHILD_PID", pidPath)
	helper := filepath.Join(root, "helper")
	require.NoError(t, os.WriteFile(helper, []byte(`#!/bin/sh
sleep 30 &
printf '%s' "$!" > "$YATM_NATIVE_CHILD_PID"
printf '{"protocol":1,"type":"progress","phase":"opening"}\n'
wait
`), 0700))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var runErr error
	var finishedAt time.Time
	t.Cleanup(func() {
		// Stop the stand-in descendant even when the regression leaves the reader blocked.
		cancel()
		data, err := os.ReadFile(pidPath)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			require.NoError(t, err)
			child, err := os.FindProcess(pid)
			require.NoError(t, err)
			_ = child.Kill()
			_ = child.Release()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("helper did not finish after fixture cleanup")
		}
	})

	// Start the shutdown deadline after the child inherited stdout, not during process startup.
	ready := make(chan struct{})
	ctx = WithProgress(ctx, func(previewprotocol.Event) { close(ready) })
	go func() {
		_, runErr = runNative(ctx, helper, nil)
		finishedAt = time.Now()
		close(done)
	}()
	select {
	case <-ready:
	case <-done:
		t.Fatalf("helper finished before emitting progress: %v", runErr)
	case <-time.After(15 * time.Second):
		t.Fatal("helper did not emit progress before the startup deadline")
	}

	// Retain the shutdown bound even if the test goroutine is scheduled after completion.
	canceledAt := time.Now()
	cancel()
	select {
	case <-done:
		require.ErrorIs(t, runErr, context.Canceled)
		require.Less(t, finishedAt.Sub(canceledAt), 5*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled helper is still waiting for inherited stdout to close")
	}
}
