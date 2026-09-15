package apis_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func TestVolumeInitializeAndMediaDeletePreservePhysicalFiles(t *testing.T) {
	// Admit one isolated directory through the ordinary mounted-Volume boundary.
	ctx := context.Background()
	root := t.TempDir()
	volumeDiscoveryRoot := filepath.Join(root, "volumes")
	volumeRoot := filepath.Join(volumeDiscoveryRoot, "offline-disk")
	require.NoError(t, os.MkdirAll(volumeRoot, 0o755))
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	exe := executor.New(executorDB, lib, nil, executor.Paths{
		Work: filepath.Join(root, "work"), Volumes: []string{volumeDiscoveryRoot},
	}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	api := apis.New(lib, exe)

	// Initialization publishes a marker and independent Media metadata.
	reply, err := api.VolumeInitialize(ctx, &entity.VolumeInitializeRequest{
		MountPoint: volumeRoot, Name: "Offline Disk",
		Profile: &entity.VolumeMediaProfile{
			SerialNumber: "serial-1", Type: entity.VolumeType_VOLUME_TYPE_HDD,
		},
	})
	require.NoError(t, err)
	require.Equal(t, entity.MediaKind_MEDIA_KIND_VOLUME, reply.Media.Kind)
	require.NotNil(t, reply.Media.Mounted)
	require.True(t, reply.Media.GetMounted())
	require.NotNil(t, reply.Media.FilesystemAvailableBytes)
	require.FileExists(t, filepath.Join(volumeRoot, mediapkg.VolumeMarkerName))
	marker, err := os.ReadFile(filepath.Join(volumeRoot, mediapkg.VolumeMarkerName))
	require.NoError(t, err)
	userFile := filepath.Join(volumeRoot, "user-file.txt")
	require.NoError(t, os.WriteFile(userFile, []byte("keep"), 0o644))

	// Metadata deletion leaves both user bytes and the immutable marker intact.
	_, err = api.MediaDelete(ctx, &entity.MediaDeleteRequest{Ids: []int64{reply.Media.Id}})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(volumeRoot, mediapkg.VolumeMarkerName))
	require.FileExists(t, userFile)
	_, err = lib.GetMedia(ctx, reply.Media.Id)
	require.Error(t, err)

	// Registering the existing marker retains physical identity under a new catalog row.
	registered, err := api.VolumeRegister(ctx, &entity.VolumeRegisterRequest{
		MountPoint: volumeRoot, Name: "Registered Disk",
	})
	require.NoError(t, err)
	require.NotEqual(t, reply.Media.Id, registered.Media.Id)
	require.Equal(t, reply.Media.Identity, registered.Media.Identity)
	require.Equal(t, reply.Media.Profile, registered.Media.Profile)
	require.Equal(t, "Registered Disk", registered.Media.Name)
	require.FileExists(t, userFile)
	currentMarker, err := os.ReadFile(filepath.Join(volumeRoot, mediapkg.VolumeMarkerName))
	require.NoError(t, err)
	require.Equal(t, marker, currentMarker)

	// Repeated registration does not override an existing catalog name.
	retried, err := api.VolumeRegister(ctx, &entity.VolumeRegisterRequest{
		MountPoint: volumeRoot, Name: "Ignored Retry Name",
	})
	require.NoError(t, err)
	require.Equal(t, registered.Media.Id, retried.Media.Id)
	require.Equal(t, "Registered Disk", retried.Media.Name)
}

func TestVolumeInitializeRejectsMountOutsideConfiguredRoots(t *testing.T) {
	// Configure a narrow discovery root next to an equally writable but unauthorized directory.
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.Mkdir(allowed, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	exe := executor.New(executorDB, lib, nil, executor.Paths{Volumes: []string{allowed}}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())

	// Initialization cannot create a marker outside the approved discovery namespace.
	_, err = apis.New(lib, exe).VolumeInitialize(context.Background(), &entity.VolumeInitializeRequest{
		MountPoint: outside, Name: "Outside",
		Profile: &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD},
	})
	require.ErrorContains(t, err, "not a configured discovery candidate")
	require.NoFileExists(t, filepath.Join(outside, mediapkg.VolumeMarkerName))
}
