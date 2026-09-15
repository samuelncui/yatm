package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestReadTapeBarcodeValidatesScriptResult(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "six characters", value: "abc001", want: "ABC001"},
		{name: "empty", value: "", want: ""},
		{name: "media suffix not stripped", value: "ABC001L5", wantErr: true},
		{name: "invalid character", value: "ABC!01", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]string{"barcode": test.value})
			require.NoError(t, err)
			script := writeExecutorTestScript(t, "read-info", fmt.Sprintf(
				"printf '%%s\\n' %s > \"$OUT\"", strconv.Quote(string(data)),
			))
			exe := New(nil, nil, nil, Paths{}, Scripts{ReadInfo: script}, nil)

			barcode, err := exe.ReadTapeBarcode(context.Background(), "/dev/nst0")
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, barcode)
		})
	}
}

func TestReadInfoScriptWaitsForExplicitBarcode(t *testing.T) {
	tests := []struct {
		name       string
		deviceData string
		emptyReads int
		expire     bool
		want       string
		wantErr    bool
	}{
		{name: "six characters", deviceData: "ABC001", want: "ABC001"},
		{name: "media suffix", deviceData: "ABC001L5", want: "ABC001"},
		{name: "invalid length preserved", deviceData: "ABC0017", want: "ABC0017"},
		{name: "delayed MAM", deviceData: "ABC001L5", emptyReads: 2, want: "ABC001"},
		{name: "MAM unavailable", expire: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			require.NoError(t, os.Mkdir(bin, 0o755))
			loadMarker := filepath.Join(root, "loaded")
			probeCount := filepath.Join(root, "probe-count")
			dateCount := filepath.Join(root, "date-count")
			output := filepath.Join(root, "result.json")

			writeExecutorTestScriptAt(t, filepath.Join(bin, "mt"), `
: > "$READINFO_LOAD_MARKER"
`)
			writeExecutorTestScriptAt(t, filepath.Join(bin, "timeout"), `
shift
exec "$@"
`)
			writeExecutorTestScriptAt(t, filepath.Join(bin, "sleep"), "exit 0")
			writeExecutorTestScriptAt(t, filepath.Join(bin, "date"), `
if [ "${READINFO_EXPIRE:-false}" != "true" ]; then
    exec /bin/date "$@"
fi
count=0
if [ -f "$READINFO_DATE_COUNT" ]; then
    read -r count < "$READINFO_DATE_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$READINFO_DATE_COUNT"
case "$count" in
    1|2) printf '100\n' ;;
    *) printf '160\n' ;;
esac
`)
			writeExecutorTestScriptAt(t, filepath.Join(root, "yatm-lto-info"), `
test -f "$READINFO_LOAD_MARKER"
count=0
if [ -f "$READINFO_PROBE_COUNT" ]; then
    read -r count < "$READINFO_PROBE_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$READINFO_PROBE_COUNT"
printf 'NotBarcode : BAD999L5\n'
if [ "$count" -gt "$READINFO_EMPTY_READS" ]; then
    printf '  Barcode       : %s\n' "$READINFO_DEVICE_DATA"
fi
`)

			script, err := filepath.Abs(filepath.Join("..", "scripts", "readinfo"))
			require.NoError(t, err)
			cmd := exec.Command(script)
			cmd.Dir = root
			cmd.Env = []string{
				"PATH=" + bin + ":" + os.Getenv("PATH"),
				"DEVICE=/dev/nst0",
				"OUT=" + output,
				"READINFO_LOAD_MARKER=" + loadMarker,
				"READINFO_PROBE_COUNT=" + probeCount,
				"READINFO_DATE_COUNT=" + dateCount,
				"READINFO_DEVICE_DATA=" + test.deviceData,
				fmt.Sprintf("READINFO_EMPTY_READS=%d", test.emptyReads),
				"READINFO_EXPIRE=" + strconv.FormatBool(test.expire),
			}
			data, err := cmd.CombinedOutput()
			if test.wantErr {
				require.Error(t, err, string(data))
				require.NoFileExists(t, output)
				return
			}
			require.NoError(t, err, string(data))
			result, err := os.ReadFile(output)
			require.NoError(t, err)
			var info struct {
				Barcode string `json:"barcode"`
			}
			require.NoError(t, json.Unmarshal(result, &info))
			require.Equal(t, test.want, info.Barcode)
		})
	}
}

func TestTapeSessionsRejectUnverifiedIdentityBeforeMutation(t *testing.T) {
	tests := []struct {
		name          string
		deviceBarcode string
		writeTarget   *entity.ArchiveTapeTarget
		readTarget    *entity.ReadTapeTarget
		wantError     string
	}{
		{
			name: "format identity unavailable",
			writeTarget: &entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
			},
			wantError: "archive Tape identity is unavailable",
		},
		{
			name: "append identity unavailable",
			writeTarget: &entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			},
			wantError: "archive Tape identity is unavailable",
		},
		{
			name:       "restore identity unavailable",
			readTarget: &entity.ReadTapeTarget{Device: "/dev/nst0"},
			wantError:  "restore Tape identity is unavailable",
		},
		{
			name:          "append identity mismatch",
			deviceBarcode: "ABC002",
			writeTarget: &entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			},
			wantError: `archive Tape changed, requested="ABC001" device="ABC002"`,
		},
		{
			name:          "format identity mismatch",
			deviceBarcode: "ABC002",
			writeTarget: &entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
			},
			wantError: `archive Tape changed, requested="ABC001" device="ABC002"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Record every physical mutation behind the identity check.
			mutationMarker := filepath.Join(t.TempDir(), "mutation")
			readInfo := writeExecutorTestScript(t, "read-info", fmt.Sprintf(
				"printf '%%s\\n' %s > \"$OUT\"", strconv.Quote(fmt.Sprintf(`{"barcode":%q}`, test.deviceBarcode)),
			))
			mutation := writeExecutorTestScript(t, "mutate", fmt.Sprintf(": > %s", strconv.Quote(mutationMarker)))
			exe := New(nil, nil, []string{"/dev/nst0"}, Paths{}, Scripts{
				ReadInfo: readInfo, Encrypt: mutation, Mkfs: mutation, Mount: mutation,
			}, nil)
			require.True(t, exe.beginAttempt(1, func() {}))
			t.Cleanup(func() { exe.endAttempt(1) })
			backend := exe.NewMediaBackend(1, nil).(*mediaBackend)

			// Neither read nor write sessions may cross into encryption, format, or mount.
			var err error
			if test.writeTarget != nil {
				_, err = backend.newTapeWriteSession(context.Background(), nil, test.writeTarget)
			} else {
				_, err = backend.newTapeReadSession(context.Background(), nil, test.readTarget, nil)
			}
			require.ErrorContains(t, err, test.wantError)
			require.NoFileExists(t, mutationMarker)
		})
	}
}

func TestUnmountScriptWaitsForDeviceReleaseAndEject(t *testing.T) {
	// Simulate an LTFS process releasing its SG handle before the drive reports an open door.
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	fuserCount := filepath.Join(root, "fuser-count")
	mtCount := filepath.Join(root, "mt-count")
	unmounted := filepath.Join(root, "unmounted")
	writeExecutorTestScriptAt(t, filepath.Join(bin, "readlink"), `printf '%s\n' "$2"`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "sg_map"), `printf '/dev/sg0 /dev/nst0\n'`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "umount"), `: > "$UNMOUNTED"`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "sleep"), `exit 0`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "fuser"), `
count=0
if [ -f "$FUSER_COUNT" ]; then read -r count < "$FUSER_COUNT"; fi
count=$((count + 1))
printf '%s\n' "$count" > "$FUSER_COUNT"
test "$count" -le 2
`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "mt"), `
count=0
if [ -f "$MT_COUNT" ]; then read -r count < "$MT_COUNT"; fi
count=$((count + 1))
printf '%s\n' "$count" > "$MT_COUNT"
if [ "$count" -lt 2 ]; then printf 'ONLINE\n'; else printf 'DR_OPEN\n'; fi
`)

	// Successful completion proves both post-unmount boundaries were observed.
	output, err := runUnmountTestScript(t, root, bin, []string{
		"FUSER_COUNT=" + fuserCount, "MT_COUNT=" + mtCount, "UNMOUNTED=" + unmounted,
	})
	require.NoError(t, err, output)
	require.FileExists(t, unmounted)
	require.Equal(t, "3\n", readExecutorTestFile(t, fuserCount))
	require.Equal(t, "2\n", readExecutorTestFile(t, mtCount))
}

func TestUnmountScriptTimesOutWhileDeviceIsBusy(t *testing.T) {
	// Advance the shared deadline immediately while the SG device remains occupied.
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	dateCount := filepath.Join(root, "date-count")
	writeExecutorTestScriptAt(t, filepath.Join(bin, "readlink"), `printf '%s\n' "$2"`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "sg_map"), `printf '/dev/sg0 /dev/nst0\n'`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "umount"), `exit 0`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "sleep"), `exit 0`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "fuser"), `exit 0`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "mt"), `printf 'ONLINE\n'`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "date"), `
count=0
if [ -f "$DATE_COUNT" ]; then read -r count < "$DATE_COUNT"; fi
count=$((count + 1))
printf '%s\n' "$count" > "$DATE_COUNT"
if [ "$count" -eq 1 ]; then printf '100\n'; else printf '700\n'; fi
`)

	// Timeout is a failed normal unmount and must remain visible to the Backend.
	output, err := runUnmountTestScript(t, root, bin, []string{"DATE_COUNT=" + dateCount})
	require.Error(t, err)
	require.Contains(t, output, "Tape device is still in use after unmount")
}

func runUnmountTestScript(t *testing.T, root, bin string, environment []string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "scripts", "umount"))
	require.NoError(t, err)
	cmd := exec.Command(script)
	cmd.Env = append([]string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"DEVICE=/dev/nst0",
		"MOUNT_POINT=" + filepath.Join(root, "mount"),
		"TAPE_DIR=" + filepath.Join(root, "tape"),
	}, environment...)
	output, runErr := cmd.CombinedOutput()
	return string(output), runErr
}

func readExecutorTestFile(t *testing.T, filename string) string {
	t.Helper()
	data, err := os.ReadFile(filename)
	require.NoError(t, err)
	return string(data)
}

func writeExecutorTestScript(t *testing.T, name, body string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), name)
	writeExecutorTestScriptAt(t, filename, body)
	return filename
}

func writeExecutorTestScriptAt(t *testing.T, filename, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filename, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755))
}
