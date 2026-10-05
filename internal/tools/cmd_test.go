package tools

import (
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type commandLogHook chan string

func (commandLogHook) Levels() []logrus.Level           { return logrus.AllLevels }
func (h commandLogHook) Fire(entry *logrus.Entry) error { h <- entry.Message; return nil }

func TestRunCmdClosesLogPipeOnSuccessAndFailure(t *testing.T) {
	for _, script := range []string{"printf final", "printf final; exit 1"} {
		t.Run(script, func(t *testing.T) {
			// An unterminated final line is delivered only once the writer closes.
			logger := logrus.New()
			logger.SetOutput(io.Discard)
			hook := make(commandLogHook, 1)
			logger.AddHook(hook)
			err := RunCmd(logger, exec.Command("sh", "-c", script))
			if script == "printf final" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			select {
			case message := <-hook:
				require.Equal(t, "final", message)
			case <-time.After(time.Second):
				t.Fatal("command output pipe did not close")
			}
		})
	}
}
