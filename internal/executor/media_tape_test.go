package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
)

func TestCancelledTapeMountCompletesPhysicalRelease(t *testing.T) {
	for _, releaseFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("release-fails-%t", releaseFails), func(t *testing.T) {
			// Stand-in scripts expose the window after mount but before its command returns.
			root := t.TempDir()
			mount, unmount := filepath.Join(root, "mount"), filepath.Join(root, "unmount")
			require.NoError(t, os.WriteFile(mount, []byte("#!/bin/sh\nprintf '%s' \"$MOUNT_POINT\" > \"$TAPE_DIR/mounted\"\nexec /usr/bin/tail -f /dev/null\n"), 0o755))
			exit := 0
			if releaseFails {
				exit = 1
			}
			require.NoError(t, os.WriteFile(unmount, []byte(fmt.Sprintf("#!/bin/sh\nprintf done > \"$TAPE_DIR/released\"\nexit %d\n", exit)), 0o755))
			device := "fixture-device"
			exe := New(nil, nil, []string{device}, Paths{}, Scripts{Mount: mount, Umount: unmount}, nil)
			require.NoError(t, exe.beginAttempt(1, func(error) {}))
			require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, device, nil))
			backend := exe.NewMediaBackend(1, nil, nil).(*mediaBackend)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := backend.mountTape(ctx, device, root); done <- err }()
			require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(root, "mounted")); return err == nil }, 5*time.Second, time.Millisecond)
			point, err := os.ReadFile(filepath.Join(root, "mounted"))
			require.NoError(t, err)
			mountPoint := strings.TrimSpace(string(point))
			t.Cleanup(func() { _ = os.Remove(mountPoint) })

			// Cancellation stops the mount command, but not the required release command.
			cancel()
			select {
			case err := <-done:
				require.ErrorContains(t, err, "mount Tape failed")
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled mount did not finish cleanup")
			}
			_, err = os.Stat(filepath.Join(root, "released"))
			require.NoError(t, err)
			exe.endAttempt(1)
			if releaseFails {
				require.Empty(t, exe.ListAvailableDevices())
			} else {
				require.Equal(t, []string{device}, exe.ListAvailableDevices())
				_, err = os.Stat(mountPoint)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestWaitForTapeIndexAcceptsDelayedCapture(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "ABC001.schema")
	written := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		written <- os.WriteFile(filename, []byte("index"), 0o600)
	}()
	require.NoError(t, waitForTapeIndex(context.Background(), filename))
	require.NoError(t, <-written)
}

func TestVolumeSessionsRejectSymlinkPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{
		Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "source.txt"), []byte("source"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "linked")))

	read := &volumeReadSession{volume: volume}
	_, err = read.SourcePath("linked/source.txt")
	require.ErrorContains(t, err, "contains a symlink")

	write := &volumeWriteSession{
		volume: volume, pathPrefix: "", available: 1 << 20,
	}
	_, err = write.TargetPath("linked/target.txt", 6)
	require.ErrorContains(t, err, "contains a symlink")
}

func TestMediaLeaseLivesUntilAttemptEnds(t *testing.T) {
	exe := New(nil, nil, []string{"/dev/nst0"}, Paths{}, Scripts{}, nil)
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	require.NoError(t, exe.beginAttempt(2, func(error) {}))
	require.NoError(t, exe.AcquireVolume(context.Background(), 1, "volume-id", nil))
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, "/dev/nst0", nil))
	require.Empty(t, exe.ListAvailableDevices())

	// One shared resource waits for the holder instead of failing the second Job.
	acquired := make(chan error, 1)
	waited := make(chan struct{})
	go func() {
		acquired <- exe.AcquireVolume(context.Background(), 2, "volume-id", func() { close(waited) })
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("second Volume lease never reported its wait")
	}
	select {
	case err := <-acquired:
		t.Fatalf("second Volume lease returned while the holder was active, err=%v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Ending the first attempt releases both resources to the waiting and later attempts.
	exe.endAttempt(1)
	select {
	case err := <-acquired:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("second Volume lease did not continue after release")
	}
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 2, "/dev/nst0", nil))
	exe.endAttempt(2)
	require.Equal(t, []string{"/dev/nst0"}, exe.ListAvailableDevices())
}

func TestTapeFinalizeStopsAfterUnmountFailureAndKeepsDeviceUnavailable(t *testing.T) {
	// A leased Tape with failed unmount has no usable physical result.
	ctx := context.Background()
	unmount := filepath.Join(t.TempDir(), "unmount")
	require.NoError(t, os.WriteFile(unmount, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	device := "/dev/nst0"
	exe := New(nil, nil, []string{device}, Paths{}, Scripts{Umount: unmount}, nil)
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, device, nil))
	backend := exe.NewMediaBackend(1, nil, nil).(*mediaBackend)
	indexPath := filepath.Join(t.TempDir(), "ABC001.schema")
	require.NoError(t, os.WriteFile(indexPath, []byte("invalid index"), 0o600))
	session := &tapeWriteSession{
		backend: backend, device: device,
		mountPoint: t.TempDir(), tapeDir: t.TempDir(), indexPath: indexPath,
		pathPrefix: ".", recycleKey: func() {},
	}

	// A failed normal unmount invalidates the attempt without waiting for or parsing an Index.
	result, err := session.Finalize(ctx, true)
	require.Nil(t, result)
	require.ErrorIs(t, err, mediapkg.ErrFinalizeUnusable)
	require.ErrorContains(t, err, "unmount Tape failed")
	require.NotErrorIs(t, err, errInvalidTapeIndex)

	// Ending the attempt keeps the uncertain physical device unavailable for later work.
	exe.endAttempt(1)
	require.Empty(t, exe.ListAvailableDevices())
}

func TestTapeFinalizeReturnsInvalidPostUnmountIndexForArchiveValidation(t *testing.T) {
	// A post-unmount Index remains a physical stream for Archive to validate against its manifest.
	ctx := context.Background()
	tapeDir := t.TempDir()
	unmount := filepath.Join(t.TempDir(), "unmount")
	require.NoError(t, os.WriteFile(unmount, []byte(
		"#!/bin/sh\ntest \"$DEVICE\" = \"/dev/nst0\"\nprintf 'invalid index\\n' > \"$TAPE_DIR/ABC001.schema\"\n",
	), 0o755))
	device := "/dev/nst0"
	exe := New(nil, nil, []string{device}, Paths{}, Scripts{Umount: unmount}, nil)
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, device, nil))
	backend := exe.NewMediaBackend(1, nil, nil).(*mediaBackend)
	session := &tapeWriteSession{
		backend: backend, barcode: "ABC001", device: device,
		mountPoint: t.TempDir(), tapeDir: tapeDir, indexPath: filepath.Join(tapeDir, "ABC001.schema"),
		pathPrefix: ".", recycleKey: func() {},
	}

	// Finalization exposes the bounded post-unmount Index for Archive to validate before publication.
	result, err := session.Finalize(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Inventory)
	err = result.Inventory(ctx, func(*mediapkg.LTFSIndexEntry) error { return nil })
	require.ErrorContains(t, err, "parse LTFS index failed")
	exe.endAttempt(1)
	require.Equal(t, []string{device}, exe.ListAvailableDevices())
}
