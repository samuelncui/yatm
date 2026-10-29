package executor

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestTapeEncryptScriptContract(t *testing.T) {
	// Execute the shipped adapter with an exclusive tool directory, never host device tools.
	bundled, err := filepath.Abs("../../scripts/encrypt")
	require.NoError(t, err)
	cases := []struct {
		name       string
		restore    bool
		failures   int
		loadExit   int
		wantTries  int
		wantSleeps []string
		wantExit   int
	}{
		{name: "new key succeeds after retries", failures: 2, wantTries: 3, wantSleeps: []string{"5", "5", "5", "3"}},
		{name: "restored key succeeds", restore: true, wantTries: 1, wantSleeps: []string{"5", "3"}},
		{name: "exhausted retries stop Session", restore: true, failures: 60, wantTries: 60, wantSleeps: repeatEncryptSleep("5", 60), wantExit: 1},
		{name: "load failure stops before stenc", loadExit: 23, wantExit: 23},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Keep the adapter's service working directory and relative helper contract intact.
			service := t.TempDir()
			t.Chdir(service)
			state := t.TempDir()
			toolDir := filepath.Join(state, "tools")
			tapeDir := filepath.Join(state, "Tape artifacts")
			require.NoError(t, os.Mkdir(toolDir, 0700))
			require.NoError(t, os.Mkdir(tapeDir, 0700))
			require.NoError(t, os.Symlink("/bin/bash", filepath.Join(toolDir, "bash")))
			writeEncryptContractScript(t, filepath.Join(toolDir, "mkdir"), `
[ "$#" -eq 2 ] && [ "$1" = -p ] && [ "$2" = "$TAPE_DIR" ] && [ -d "$2" ] || exit 91
`)
			writeEncryptContractScript(t, filepath.Join(toolDir, "tee"), `
[ "$#" -eq 2 ] && [ "$1" = -a ] && [ "$2" = "$TAPE_DIR/ltfs.log" ] || exit 92
while IFS= read -r line; do printf '%s\n' "$line" >> "$2"; done
`)
			writeEncryptContractScript(t, filepath.Join(toolDir, "mt"), fmt.Sprintf(`
printf '%%s\n' "$@" > "$CONTRACT_STATE/mt"
exit %d
`, test.loadExit))
			writeEncryptContractScript(t, filepath.Join(toolDir, "sleep"), `
printf '%s\n' "$@" >> "$CONTRACT_STATE/sleep"
`)
			writeEncryptContractScript(t, filepath.Join(toolDir, "stenc"), fmt.Sprintf(`
count=0
if [ -f "$CONTRACT_STATE/count" ]; then read -r count < "$CONTRACT_STATE/count"; fi
count=$((count + 1))
printf '%%s\n' "$count" > "$CONTRACT_STATE/count"
printf '%%s\n' "$@" > "$CONTRACT_STATE/stenc-$count"
[ "$count" -gt %d ]
`, test.failures))
			helper := `
printf '%s\n' "$DEVICE" "$KEY_FILE" "$TAPE_BARCODE" "$TAPE_NAME" "$TAPE_DIR" "$PWD" "$PATH" > "$CONTRACT_STATE/environment"
key=''
IFS= read -r key < "$KEY_FILE" || [ -n "$key" ]
printf '%s' "$key" > "$CONTRACT_STATE/key"
`
			require.NoError(t, os.WriteFile(filepath.Join(service, "encrypt-helper"), []byte(helper), 0600))
			// A test-only bootstrap supplies the exclusive PATH; the bundled adapter is unchanged.
			adapter := filepath.Join(state, "encrypt")
			writeEncryptContractScript(t, adapter, fmt.Sprintf(`
export PATH=%s
export CONTRACT_STATE=%s
. ./encrypt-helper
exec /bin/bash %s
`, encryptShellQuote(toolDir), encryptShellQuote(state), encryptShellQuote(bundled)))
			exe := &Executor{paths: Paths{Work: t.TempDir()}, scripts: Scripts{Encrypt: adapter}}
			logger := logrus.New()
			logger.SetOutput(io.Discard)
			backend := &mediaBackend{executor: exe, logger: logger}

			// New and restored keys share the actual private temporary-file lifecycle.
			var encoded, keyPath string
			var recycle func()
			if test.restore {
				encoded = "v1:" + strings.Repeat("ab", 32)
				keyPath, recycle, err = exe.RestoreKey(encoded)
			} else {
				encoded, keyPath, recycle, err = exe.NewKey()
			}
			require.NoError(t, err)
			t.Cleanup(recycle)
			require.True(t, strings.HasPrefix(encoded, "v1:"))
			decoded, err := hex.DecodeString(strings.TrimPrefix(encoded, "v1:"))
			require.NoError(t, err)
			require.Len(t, decoded, 32)
			keyInfo, err := os.Stat(keyPath)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0600), keyInfo.Mode().Perm())
			keyBytes, err := os.ReadFile(keyPath)
			require.NoError(t, err)
			require.Equal(t, strings.TrimPrefix(encoded, "v1:"), string(keyBytes))

			// Literal arguments include spaces and shell metacharacters to detect adapter splitting.
			device, barcode, name := "/dev/stub Tape [0]", "AB1234", "Tape name * [one]"
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exe.MakeEncryptCmd(ctx, device, keyPath, barcode, name)
			require.Equal(t, []string{adapter}, cmd.Args)
			require.Empty(t, cmd.Dir)
			err = backend.configureTape(ctx, device, keyPath, barcode, name, tapeDir)
			if test.wantExit == 0 {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "configure Tape encryption failed")
				var exit *exec.ExitError
				require.True(t, errors.As(err, &exit))
				require.Equal(t, test.wantExit, exit.ExitCode())
			}

			// Assert observed inputs, retries and command boundaries rather than mocked return values.
			cwd, err := os.Getwd()
			require.NoError(t, err)
			cwd, err = filepath.EvalSymlinks(cwd)
			require.NoError(t, err)
			require.Equal(t, []string{device, keyPath, barcode, name, tapeDir, cwd, toolDir}, encryptContractLines(t, filepath.Join(state, "environment")))
			require.Equal(t, []string{"-f", device, "load"}, encryptContractLines(t, filepath.Join(state, "mt")))
			require.Equal(t, test.wantSleeps, encryptContractLines(t, filepath.Join(state, "sleep")))
			for attempt := 1; attempt <= test.wantTries; attempt++ {
				require.Equal(t, []string{"-f", device, "-e", "on", "-k", keyPath, "-kd", barcode, "-a", "1", "--ckod"}, encryptContractLines(t, filepath.Join(state, fmt.Sprintf("stenc-%d", attempt))))
			}
			require.NoFileExists(t, filepath.Join(state, fmt.Sprintf("stenc-%d", test.wantTries+1)))
			observedKey, err := os.ReadFile(filepath.Join(state, "key"))
			require.NoError(t, err)
			require.Equal(t, keyBytes, observedKey)
			require.FileExists(t, filepath.Join(tapeDir, "ltfs.log"))
			require.FileExists(t, keyPath)
			recycle()
			require.NoDirExists(t, filepath.Dir(keyPath))
		})
	}
}

func writeEncryptContractScript(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0700))
}

func encryptShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func encryptContractLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func repeatEncryptSleep(value string, count int) []string {
	result := make([]string, count)
	for i := range result {
		result[i] = value
	}
	return result
}
