package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func TestReadSessionDoesNotDependOnRestoreCopies(t *testing.T) {
	// A neutral read Session accepts a Job DB with no Restore-specific tables.
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
	jobDB, err := resource.OpenSQLite(filepath.Join(root, "job.db"))
	require.NoError(t, err)
	exe := New(nil, lib, nil, Paths{Volumes: []string{volumeRoot}}, Scripts{}, nil)
	require.True(t, exe.beginAttempt(1, func() {}))
	t.Cleanup(func() { exe.endAttempt(1) })

	// The frozen expectation validates the marker/catalog identity without querying a runner's manifest.
	target := (&entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}).Pack()
	target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
	session, err := exe.NewMediaBackend(1, nil).NewReadSession(ctx, jobDB, target)
	require.NoError(t, err)
	require.Equal(t, stored.ID, session.Inspect().ID)
	require.NoError(t, session.Finalize(ctx))
	require.False(t, jobDB.Migrator().HasTable("copies"))
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
	require.True(t, exe.beginAttempt(1, func() {}))
	t.Cleanup(func() { exe.endAttempt(1) })
	target := (&entity.ReadTapeTarget{Device: "fixture-device"}).Pack()
	target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, "DIFFERENT", stored.Profile

	// No encryption or mount side effect is permitted for a mismatched cartridge.
	_, err = exe.NewMediaBackend(1, nil).NewReadSession(ctx, db, target)
	require.ErrorContains(t, err, "differs from the frozen expectation")
	require.NoFileExists(t, mutationMarker)
}

func TestTapeReadFinalizeAlwaysUnmountsAfterIdentityFailure(t *testing.T) {
	// A different ending barcode invalidates observations without skipping the normal cleanup boundary.
	root := t.TempDir()
	readInfo := writeExecutorTestScript(t, "identity", `printf '%s\n' '{"barcode":"OTHER1"}' > "$OUT"`)
	unmounted := filepath.Join(root, "unmounted")
	umount := writeExecutorTestScript(t, "umount", fmt.Sprintf(": > %s", strconv.Quote(unmounted)))
	exe := New(nil, nil, nil, Paths{}, Scripts{ReadInfo: readInfo, Umount: umount}, nil)
	mountPoint := filepath.Join(root, "mounted")
	require.NoError(t, os.Mkdir(mountPoint, 0755))
	recycled := false
	session := &tapeReadSession{backend: exe.NewMediaBackend(1, nil).(*mediaBackend),
		media: &library.Media{Identity: "ABC001"}, device: "fixture-device", mountPoint: mountPoint,
		tapeDir: root, recycleKey: func() { recycled = true }}

	// Finalization reports the invalid identity but releases the mount directory and temporary key once.
	err := session.Finalize(context.Background())
	require.ErrorContains(t, err, "identity changed")
	require.FileExists(t, unmounted)
	require.NoDirExists(t, mountPoint)
	require.True(t, recycled)
}
