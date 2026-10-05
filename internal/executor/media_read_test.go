package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestReadSessionDoesNotDependOnRestoreCopies(t *testing.T) {
	// A neutral read Session accepts a target without Restore-specific tables.
	ctx := context.Background()
	root := t.TempDir()
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	volumeRoot := filepath.Join(root, "disk")
	require.NoError(t, os.Mkdir(volumeRoot, 0755))
	profile := &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}
	volume, err := mediapkg.InitializeVolume(volumeRoot, profile)
	require.NoError(t, err)
	stored, err := lib.CreateMedia(ctx, &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Profile: profile.Pack()})
	require.NoError(t, err)
	exe := New(nil, lib, nil, Paths{Volumes: []string{volumeRoot}}, Scripts{}, nil)
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	t.Cleanup(func() { exe.endAttempt(1) })

	// The frozen expectation validates the marker/catalog identity without querying a runner's manifest.
	target := (&entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}).Pack()
	target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
	session, err := exe.NewMediaBackend(1, nil, nil).NewReadSession(ctx, target)
	require.NoError(t, err)
	require.Equal(t, stored.ID, session.Media().ID)
	require.NoError(t, session.Finalize(ctx))
}

func TestTapeReadRejectsWrongExpectedIdentityBeforeMount(t *testing.T) {
	// The physical identity is known, but does not belong to the Job's frozen expected Media.
	ctx := context.Background()
	root := t.TempDir()
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	stored, err := lib.CreateMedia(ctx, &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001",
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack()})
	require.NoError(t, err)
	mutationMarker := filepath.Join(root, "mutated")
	readInfo := writeExecutorTestScript(t, "identity", `printf '%s\n' '{"barcode":"ABC001"}' > "$OUT"`)
	mutation := writeExecutorTestScript(t, "mutation", fmt.Sprintf(": > %s", strconv.Quote(mutationMarker)))
	exe := New(nil, lib, []string{"fixture-device"}, Paths{}, Scripts{ReadInfo: readInfo, Encrypt: mutation, Mount: mutation}, nil)
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	t.Cleanup(func() { exe.endAttempt(1) })
	target := (&entity.ReadTapeTarget{Device: "fixture-device"}).Pack()
	target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, "DIFFERENT", stored.Profile

	// No encryption or mount side effect is permitted for a mismatched cartridge.
	_, err = exe.NewMediaBackend(1, nil, nil).NewReadSession(ctx, target)
	require.ErrorContains(t, err, "differs from the frozen expectation")
	require.NoFileExists(t, mutationMarker)
}

func TestTapeReadFinalizeDoesNotReloadTheMountedCartridge(t *testing.T) {
	for _, unmountFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("unmountFails=%t", unmountFails), func(t *testing.T) {
			// ReadInfo would load the cartridge; a held read Session must never invoke it again.
			root := t.TempDir()
			reloaded := filepath.Join(root, "reloaded")
			readInfo := writeExecutorTestScript(t, "identity", fmt.Sprintf(": > %s\nexit 1", strconv.Quote(reloaded)))
			unmounted := filepath.Join(root, "unmounted")
			unmount := fmt.Sprintf(": > %s", strconv.Quote(unmounted))
			if unmountFails {
				unmount += "\nexit 1"
			}
			umount := writeExecutorTestScript(t, "umount", unmount)
			exe := New(nil, nil, nil, Paths{}, Scripts{ReadInfo: readInfo, Umount: umount}, nil)
			mountPoint := filepath.Join(root, "mounted")
			require.NoError(t, os.Mkdir(mountPoint, 0755))
			recycled := 0
			session := &tapeReadSession{backend: exe.NewMediaBackend(1, nil, nil).(*mediaBackend),
				media: &mediapkg.Descriptor{Identity: "ABC001"}, device: "fixture-device", mountPoint: mountPoint,
				tapeDir: root, recycleKey: func() { recycled++ }}

			// Unmount determines success, and either outcome releases the temporary key exactly once.
			err := session.Finalize(context.Background())
			if unmountFails {
				require.ErrorContains(t, err, "unmount Tape failed")
				require.DirExists(t, mountPoint)
			} else {
				require.NoError(t, err)
				require.NoDirExists(t, mountPoint)
			}
			require.NoFileExists(t, reloaded)
			require.FileExists(t, unmounted)
			require.Equal(t, 1, recycled)
		})
	}
}
