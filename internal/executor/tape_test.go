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
		raw     string
		want    string
		wantErr bool
	}{
		{name: "six characters", value: "abc001", want: "ABC001"},
		{name: "empty", value: "", want: ""},
		{name: "historical media suffix", value: "ABC001L5", want: "ABC001"},
		{name: "historical lower case suffix", value: "abc001l5", want: "ABC001"},
		{name: "short barcode", value: "ABC01", wantErr: true},
		{name: "invalid character", value: "ABC!01", wantErr: true},
		{name: "missing field", raw: `{}`, wantErr: true},
		{name: "null response", raw: `null`, wantErr: true},
		{name: "null barcode", raw: `{"barcode":null}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Keep a successful empty identity distinct from incomplete probe output.
			data, err := json.Marshal(map[string]string{"barcode": test.value})
			require.NoError(t, err)
			if test.raw != "" {
				data = []byte(test.raw)
			}
			script := writeExecutorTestScript(t, "read-info", fmt.Sprintf(
				"printf '%%s\\n' %s > \"$OUT\"", strconv.Quote(string(data)),
			))
			exe := New(nil, nil, nil, Paths{}, Scripts{ReadInfo: script}, nil)

			// Normalize only the explicitly reported identity; malformed reports stay errors.
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

func TestHistoricalReadInfoScriptWorksUnchanged(t *testing.T) {
	// Execute the published adapter byte-for-byte; stand-ins replace only host/device commands.
	script, err := os.ReadFile("testdata/tape-scripts/v0.1.21/readinfo")
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "readinfo"), script, 0o700))
	for _, name := range []string{"mt", "sleep"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "yatm-lto-info"), []byte(
		"#!/bin/sh\ntest \"$1\" = -f && test \"$2\" = /dev/nst0 || exit 1\nprintf 'Barcode : ABC001L5\\n'\n",
	), 0o700))
	wrapper := writeExecutorTestScript(t, "legacy-probe", fmt.Sprintf(
		"cd %q\nexport PATH=%q:$PATH\nexec ./readinfo", root, root,
	))
	exe := New(nil, nil, nil, Paths{}, Scripts{ReadInfo: wrapper}, nil)
	barcode, err := exe.ReadTapeBarcode(context.Background(), "/dev/nst0")
	require.NoError(t, err)
	require.Equal(t, "ABC001", barcode)
}

func TestReadInfoScriptWaitsForExplicitBarcode(t *testing.T) {
	tests := []struct {
		name       string
		deviceData string
		emptyReads int
		expire     bool
		probeFails bool
		want       string
		wantErr    bool
	}{
		{name: "six characters", deviceData: "ABC001", want: "ABC001"},
		{name: "media suffix", deviceData: "ABC001L5", want: "ABC001"},
		{name: "invalid length preserved", deviceData: "ABC0017", want: "ABC0017"},
		{name: "delayed MAM", deviceData: "ABC001L5", emptyReads: 2, want: "ABC001"},
		{name: "unassigned barcode", want: ""},
		{name: "MAM unavailable", emptyReads: 1, expire: true, wantErr: true},
		{name: "failed probe output rejected", deviceData: "ABC001", probeFails: true, expire: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Probe stand-ins distinguish an explicit empty field from unavailable MAM and command failure.
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
if [ "$READINFO_PROBE_FAILS" = "true" ]; then
    exit 1
fi
`)

			// Execute the maintained adapter without waiting for its real readiness deadline.
			script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "readinfo"))
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
				"READINFO_PROBE_FAILS=" + strconv.FormatBool(test.probeFails),
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
			count, err := os.ReadFile(probeCount)
			require.NoError(t, err)
			require.Equal(t, fmt.Sprintf("%d\n", test.emptyReads+1), string(count))
		})
	}
}

func TestTapeSessionsRejectUnverifiedIdentityBeforeMutation(t *testing.T) {
	tests := []struct {
		name          string
		deviceBarcode string
		probeFails    bool
		probeOutput   string
		writeTarget   *entity.ArchiveTapeTarget
		readTarget    *entity.ReadTapeTarget
		wantError     string
	}{
		{
			name:       "format probe failure",
			probeFails: true,
			writeTarget: &entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
			},
			wantError: "read tape info failed",
		},
		{
			name:        "format missing barcode field",
			probeOutput: `{}`,
			writeTarget: &entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
			},
			wantError: "missing barcode field",
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
			output := test.probeOutput
			if output == "" {
				output = fmt.Sprintf(`{"barcode":%q}`, test.deviceBarcode)
			}
			readInfo := writeExecutorTestScript(t, "read-info", fmt.Sprintf(
				"printf '%%s\\n' %s > \"$OUT\"", strconv.Quote(output),
			))
			if test.probeFails {
				readInfo = writeExecutorTestScript(t, "failed-read-info", "exit 1")
			}
			mutation := writeExecutorTestScript(t, "mutate", fmt.Sprintf(": > %s", strconv.Quote(mutationMarker)))
			exe := New(nil, nil, []string{"/dev/nst0"}, Paths{}, Scripts{
				ReadInfo: readInfo, Encrypt: mutation, Mkfs: mutation, Mount: mutation,
			}, nil)
			require.NoError(t, exe.beginAttempt(1, func(error) {}))
			t.Cleanup(func() { exe.endAttempt(1) })
			backend := exe.NewMediaBackend(1, nil, nil).(*mediaBackend)

			// Neither read nor write sessions may cross into encryption, format, or mount.
			var err error
			if test.writeTarget != nil {
				_, err = backend.newTapeWriteSession(context.Background(), test.writeTarget)
			} else {
				_, err = backend.newTapeReadSession(context.Background(), test.readTarget, nil)
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
	fuserArguments := filepath.Join(root, "fuser-arguments")
	mtCount := filepath.Join(root, "mt-count")
	unmounted := filepath.Join(root, "unmounted")
	writeExecutorTestScriptAt(t, filepath.Join(bin, "umount"), `: > "$UNMOUNTED"`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "sleep"), `exit 0`)
	writeExecutorTestScriptAt(t, filepath.Join(bin, "fuser"), `
printf '%s\n' "$@" > "$FUSER_ARGUMENTS"
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
		"FUSER_COUNT=" + fuserCount, "FUSER_ARGUMENTS=" + fuserArguments,
		"MT_COUNT=" + mtCount, "UNMOUNTED=" + unmounted,
	})
	require.NoError(t, err, output)
	require.FileExists(t, unmounted)
	require.Equal(t, "3\n", readExecutorTestFile(t, fuserCount))
	require.Equal(t, "-s\n/dev/sg0\n", readExecutorTestFile(t, fuserArguments))
	require.Equal(t, "2\n", readExecutorTestFile(t, mtCount))
}

func TestUnmountScriptTimesOutWhileDeviceIsBusy(t *testing.T) {
	// Advance the shared deadline immediately while the SG device remains occupied.
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	dateCount := filepath.Join(root, "date-count")
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

	// Resolve the Tape through a fake kernel mapping without accessing host devices or sysfs.
	kernelDevice := filepath.Join(root, "kernel device")
	require.NoError(t, os.MkdirAll(filepath.Join(kernelDevice, "scsi_generic", "sg0"), 0o755))
	writeExecutorTestScriptAt(t, filepath.Join(bin, "readlink"), `
test "$#" -eq 3 && test "$1" = -f && test "$2" = --
case "$3" in
  /dev/nst0) printf '%s\n' "$3" ;;
  /sys/class/scsi_tape/st0/device) printf '%s\n' "$KERNEL_DEVICE" ;;
  *) exit 1 ;;
esac
`)

	// Exercise the shipped unmount adapter against the simulated release and eject commands.
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "umount"))
	require.NoError(t, err)
	cmd := exec.Command(script)
	cmd.Env = append([]string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"DEVICE=/dev/nst0",
		"KERNEL_DEVICE=" + kernelDevice,
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
