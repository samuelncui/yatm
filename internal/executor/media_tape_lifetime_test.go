package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTapeSessionConstructionReleasesOwnedResources(t *testing.T) {
	for _, test := range []struct {
		name, failure string
		read          bool
		unmountFails  bool
		unassigned    bool
		readOrder     bool
		format        string
	}{
		{name: "read encryption failure", failure: "encrypt", read: true},
		{name: "read mount failure", failure: "mount", read: true},
		{name: "read success", read: true},
		{name: "read AUTO preference", read: true, readOrder: true},
		{name: "read legacy AUTO preference", read: true, readOrder: true, format: library.TapeFormatLTFSV0},
		{name: "write encryption failure", failure: "encrypt"},
		{name: "write format failure", failure: "format"},
		{name: "write mounted index failure", failure: "index"},
		{name: "write mounted index and unmount failure", failure: "index", unmountFails: true},
		{name: "write success"},
		{name: "write assigns requested barcode", unassigned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Only local stand-in scripts touch these fixture paths; no device command runs.
			exe := setupTestExecutor(t)
			t.Cleanup(func() { require.NoError(t, resource.CloseDB(exe.db)) })
			require.NoError(t, exe.lib.AutoMigrate())
			fixture := newTarDeviceFixture()
			exe.tapeProvider = fixture
			exit := func(stage string) int {
				if test.failure == stage {
					return 1
				}
				return 0
			}
			exe.scripts.ReadInfo = writeExecutorTestScript(t, "identity", `printf '{"barcode":"ABC001"}' > "$OUT"`)
			if test.unassigned {
				exe.scripts.ReadInfo = writeExecutorTestScript(t, "unassigned", `printf '{"barcode":""}' > "$OUT"`)
			}
			exe.scripts.Encrypt = writeExecutorTestScript(t, "encrypt", fmt.Sprintf(
				"printf '%%s' \"$KEY_FILE\" > \"$TAPE_DIR/key-path\"\nexit %d", exit("encrypt")))
			exe.scripts.Mkfs = writeExecutorTestScript(t, "format", fmt.Sprintf(
				"printf '%%s\\n' \"$TAPE_BARCODE\" \"$TAPE_NAME\" > \"$TAPE_DIR/formatted\"\nexit %d", exit("format")))
			mount := "printf '%s' \"$MOUNT_POINT\" > \"$TAPE_DIR/mount-path\"\n"
			if test.failure == "index" {
				mount += "mkdir \"$TAPE_DIR/ABC001.schema\"\nprintf keep > \"$TAPE_DIR/ABC001.schema/keep\"\n"
			}
			exe.scripts.Mount = writeExecutorTestScript(t, "mount", mount+fmt.Sprintf("exit %d", exit("mount")))
			unmount := "printf released > \"$TAPE_DIR/released\"\n"
			if test.unmountFails {
				unmount += "exit 1\n"
			} else if test.failure == "" && !test.read {
				unmount += "printf index > \"$TAPE_DIR/ABC001.schema\"\n"
			}
			exe.scripts.Umount = writeExecutorTestScript(t, "unmount", unmount)
			require.NoError(t, exe.beginAttempt(1, func(error) {}))
			t.Cleanup(func() { exe.endAttempt(1) })
			tapeDir := filepath.Join(exe.jobWorkPath(1), "tapes", "ABC001")
			var diagnostics bytes.Buffer
			logger := logrus.New()
			logger.SetOutput(&diagnostics)
			backend := exe.newMediaBackend(1, logger, nil)
			ctx := context.Background()
			if test.readOrder {
				ctx = context.WithValue(ctx, deviceSettingsKey{}, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
					Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO}},
				}}})
			}
			var finalize func() error
			var createErr error

			// A successful constructor transfers key/mount ownership to Finalize.
			if test.read {
				// Native reads need no Device Settings access, even when an admitted snapshot is present.
				exe.settings = nil
				format := test.format
				if format == "" {
					format = library.TapeFormatLTFSV1
				}
				stored, err := exe.lib.CreateMedia(ctx, &library.Media{
					Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001",
					Profile: (&entity.TapeMediaProfile{Format: format}).Pack(),
				})
				require.NoError(t, err)
				target := (&entity.ReadTapeTarget{Device: "/dev/nst0"}).Pack()
				target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
				session, err := openRead(ctx, backend, target)
				createErr = err
				if err == nil {
					finalize = func() error { return session.Finalize(ctx, nil) }
				}
				if test.readOrder {
					require.Contains(t, diagnostics.String(), "requested_read_order=TAPE_READ_ORDER_AUTO")
					require.Contains(t, diagnostics.String(), "effective_read_order=TAPE_READ_ORDER_LOGICAL")
					require.Contains(t, diagnostics.String(), "read_order_reason=")
				}
			} else {
				session, err := openWrite(ctx, backend, (&entity.ArchiveTapeTarget{
					Device: "/dev/nst0", Barcode: "ABC001", Name: "Test Tape",
					Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT, Format: proto.String(library.TapeFormatLTFSV1),
				}).Pack())
				createErr = err
				if err == nil {
					finalize = func() error { _, err := session.Finalize(ctx, nil); return err }
				}
			}
			key, err := os.ReadFile(filepath.Join(tapeDir, "key-path"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(string(key))) })
			point, pointErr := os.ReadFile(filepath.Join(tapeDir, "mount-path"))
			mountPoint := strings.TrimSpace(string(point))
			if pointErr == nil {
				t.Cleanup(func() { require.NoError(t, os.RemoveAll(mountPoint)) })
			} else {
				require.ErrorIs(t, pointErr, os.ErrNotExist)
			}
			if test.failure == "" {
				require.NoError(t, createErr)
				require.FileExists(t, string(key))
				if !test.read {
					formatted, err := os.ReadFile(filepath.Join(tapeDir, "formatted"))
					require.NoError(t, err)
					require.Equal(t, "ABC001\nTest Tape\n", string(formatted))
				}
				require.NoError(t, finalize())
			} else {
				require.Error(t, createErr)
				if test.unmountFails {
					require.ErrorContains(t, createErr, "remove mounted LTFS index failed")
					require.ErrorContains(t, createErr, "unmount Tape failed")
				}
			}
			require.NoFileExists(t, string(key))

			// A failure before the mount script unloads natively; a mount failure and later ones use the unmount adapter.
			nativeUnloads := map[bool]int{true: 1}[test.failure == "encrypt" || test.failure == "format"]
			require.Equal(t, nativeUnloads, fixture.unloads)
			require.Zero(t, fixture.loads)
			require.Equal(t, fixture.opens, fixture.closes)

			// Session cleanup never releases the attempt lease or hides physical uncertainty.
			if pointErr == nil {
				require.FileExists(t, filepath.Join(tapeDir, "released"))
				if test.unmountFails {
					require.DirExists(t, mountPoint)
				} else {
					require.NoDirExists(t, mountPoint)
				}
			}
			require.Empty(t, exe.ListAvailableDevices())
			exe.endAttempt(1)
			if test.unmountFails {
				require.Empty(t, exe.ListAvailableDevices())
			} else {
				require.Equal(t, []string{"/dev/nst0"}, exe.ListAvailableDevices())
			}
		})
	}
}

func TestLTFSFailureBeforeMountUnloadsOnlyAMatchingSettledCartridge(t *testing.T) {
	for _, test := range []struct {
		name, barcode, encrypt string
		cancel, rawMissing     bool
		poison                 bool
		unloads                int
		available              bool
	}{
		{name: "different cartridge", barcode: "XYZ999", encrypt: "exit 0", available: true},
		{name: "cancelled script", barcode: "ABC001", encrypt: "exec sleep 5", cancel: true, unloads: 1, available: true},
		{name: "raw access unavailable", barcode: "ABC001", encrypt: "exit 1", rawMissing: true, available: true},
		{name: "uncertain observation", barcode: "ABC001", encrypt: "exit 0", poison: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The read factory checks identity with readinfo, then fails before the mount script runs.
			exe := setupTestExecutor(t)
			t.Cleanup(func() { require.NoError(t, resource.CloseDB(exe.db)) })
			require.NoError(t, exe.lib.AutoMigrate())
			fixture := newTarDeviceFixture()
			if !test.rawMissing {
				exe.tapeProvider = fixture
			}
			exe.scripts.ReadInfo = writeExecutorTestScript(t, "identity", fmt.Sprintf(`printf '{"barcode":"%s"}' > "$OUT"`, test.barcode))
			exe.scripts.Encrypt = writeExecutorTestScript(t, "encrypt", test.encrypt)
			exe.scripts.Mount = writeExecutorTestScript(t, "mount", "exit 1")
			exe.scripts.Umount = writeExecutorTestScript(t, "unmount", "exit 0")
			require.NoError(t, exe.beginAttempt(1, func(error) {}))
			backend := exe.newMediaBackend(1, logrus.New(), nil)
			ctx := context.Background()
			if test.poison {
				// AUTO observes the drive before mounting; its unknown outcome forbids the unload.
				ctx = context.WithValue(ctx, deviceSettingsKey{}, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
					Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO}},
				}}})
				fixture.poison = &tape.UncertainError{Cause: errors.New("fixture outcome unknown")}
			}
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
			}
			stored, err := exe.lib.CreateMedia(context.Background(), &library.Media{
				Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001",
				Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack(),
			})
			require.NoError(t, err)
			target := (&entity.ReadTapeTarget{Device: "/dev/nst0"}).Pack()
			target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
			_, err = openRead(ctx, backend, target)
			require.Error(t, err)
			require.Equal(t, test.unloads, fixture.unloads)
			if test.poison {
				require.Equal(t, 1, fixture.opens, "no descriptor opens after an unknown outcome")
			}
			require.Equal(t, fixture.opens, fixture.closes)
			exe.endAttempt(1)
			require.Equal(t, test.available, len(exe.ListAvailableDevices()) == 1)
		})
	}
}
