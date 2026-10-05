package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestTapeSessionConstructionReleasesOwnedResources(t *testing.T) {
	for _, test := range []struct {
		name, failure string
		read          bool
		unmountFails  bool
	}{
		{name: "read encryption failure", failure: "encrypt", read: true},
		{name: "read mount failure", failure: "mount", read: true},
		{name: "read success", read: true},
		{name: "write encryption failure", failure: "encrypt"},
		{name: "write format failure", failure: "format"},
		{name: "write mounted index failure", failure: "index"},
		{name: "write mounted index and unmount failure", failure: "index", unmountFails: true},
		{name: "write success"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Only local stand-in scripts touch these fixture paths; no device command runs.
			exe := setupTestExecutor(t)
			t.Cleanup(func() { require.NoError(t, closeGORMDB(exe.db)) })
			require.NoError(t, exe.lib.AutoMigrate())
			exit := func(stage string) int {
				if test.failure == stage {
					return 1
				}
				return 0
			}
			exe.scripts.ReadInfo = writeExecutorTestScript(t, "identity", `printf '{"barcode":"ABC001"}' > "$OUT"`)
			exe.scripts.Encrypt = writeExecutorTestScript(t, "encrypt", fmt.Sprintf(
				"printf '%%s' \"$KEY_FILE\" > \"$TAPE_DIR/key-path\"\nexit %d", exit("encrypt")))
			exe.scripts.Mkfs = writeExecutorTestScript(t, "format", fmt.Sprintf("exit %d", exit("format")))
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
			backend := exe.NewMediaBackend(1, nil, nil)
			ctx := context.Background()
			var finalize func() error
			var createErr error

			// A successful constructor transfers key/mount ownership to Finalize.
			if test.read {
				stored, err := exe.lib.CreateMedia(ctx, &library.Media{
					Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001",
					Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack(),
				})
				require.NoError(t, err)
				target := (&entity.ReadTapeTarget{Device: "/dev/nst0"}).Pack()
				target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
				session, err := backend.NewReadSession(ctx, target)
				createErr = err
				if err == nil {
					finalize = func() error { return session.Finalize(ctx) }
				}
			} else {
				session, err := backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{
					Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
				}).Pack())
				createErr = err
				if err == nil {
					finalize = func() error { _, err := session.Finalize(ctx, false); return err }
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
				require.NoError(t, finalize())
			} else {
				require.Error(t, createErr)
				if test.unmountFails {
					require.ErrorContains(t, createErr, "remove mounted LTFS index failed")
					require.ErrorContains(t, createErr, "unmount Tape failed")
				}
			}
			require.NoFileExists(t, string(key))

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
