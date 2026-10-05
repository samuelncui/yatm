package apis_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestListVolumeCandidatesReportsEveryDiscoveryState(t *testing.T) {
	// Build one discovery root holding every state the Add Volume dialog must distinguish.
	ctx := context.Background()
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	volumes := filepath.Join(root, "volumes")
	require.NoError(t, os.MkdirAll(volumes, 0o755))
	exe := executor.New(executorDB, lib, nil, executor.Paths{Volumes: []string{volumes}}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	service := entity.NewMediaServiceClient(domainConnection(t, apis.New(lib, exe)))

	// One uninitialized disk, one marker awaiting registration, and one registered marker.
	for _, name := range []string{"new-disk", "unregistered", "registered", "conflict", "invalid"} {
		require.NoError(t, os.MkdirAll(filepath.Join(volumes, name), 0o755))
	}
	unregistered, err := mediapkg.InitializeVolume(filepath.Join(volumes, "unregistered"), &entity.VolumeMediaProfile{
		SerialNumber: "SER-2", Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)
	registered, err := mediapkg.InitializeVolume(filepath.Join(volumes, "registered"), &entity.VolumeMediaProfile{
		SerialNumber: "SER-3", Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)
	conflict, err := mediapkg.InitializeVolume(filepath.Join(volumes, "conflict"), &entity.VolumeMediaProfile{
		SerialNumber: "SER-4", Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(volumes, "invalid", mediapkg.VolumeMarkerName), []byte("{incomplete marker"), 0o644))
	for _, seeded := range []struct {
		volume *mediapkg.Volume
		name   string
		kind   entity.VolumeType
	}{
		{registered, "Registered Disk", entity.VolumeType_VOLUME_TYPE_HDD},
		{conflict, "Conflicting Disk", entity.VolumeType_VOLUME_TYPE_HM_SMR},
	} {
		_, err := lib.CreateMedia(ctx, &library.Media{
			Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: seeded.volume.Marker.UUID, Name: seeded.name,
			Profile: &entity.MediaProfile{Kind: &entity.MediaProfile_Volume{Volume: &entity.VolumeMediaProfile{
				SerialNumber: seeded.volume.Marker.Profile.SerialNumber, Type: seeded.kind,
			}}}, CreatedAtNS: seeded.volume.Marker.CreatedAtNS,
		})
		require.NoError(t, err)
	}

	// Discovery preserves distinct registration states and the actual marker metadata.
	reply, err := service.ListVolumeCandidates(ctx, &entity.ListVolumeCandidatesRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{volumes}, reply.DiscoveryRoots)
	states := make(map[string]*entity.VolumeCandidate, len(reply.Candidates))
	for _, candidate := range reply.Candidates {
		states[filepath.Base(candidate.MountPoint)] = candidate
	}
	// The discovery root itself is a candidate too, matching the accepted registration set.
	require.Len(t, states, 6)
	require.Equal(t, entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_UNINITIALIZED, states["volumes"].State)

	require.Equal(t, entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_UNINITIALIZED, states["new-disk"].State)
	require.Equal(t, "new-disk", states["new-disk"].Name)
	require.False(t, states["new-disk"].SeparateFilesystem)
	require.Nil(t, states["new-disk"].Media)

	require.Equal(t, entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_UNREGISTERED, states["unregistered"].State)
	require.True(t, proto.Equal(unregistered.Marker.Profile, states["unregistered"].Profile))
	require.Nil(t, states["unregistered"].Media)

	require.Equal(t, entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_REGISTERED, states["registered"].State)
	require.Equal(t, "Registered Disk", states["registered"].Name)
	require.Equal(t, registered.Marker.UUID, states["registered"].Media.GetIdentity())
	require.True(t, states["registered"].Media.GetMounted())

	require.Equal(t, entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_CONFLICT, states["conflict"].State)
	require.Contains(t, states["conflict"].Detail, "conflicts with the Library profile")
	require.Nil(t, states["conflict"].Media)

	require.Equal(t, entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_INVALID, states["invalid"].State)
	require.Contains(t, states["invalid"].Detail, "marker")
	require.Nil(t, states["invalid"].Profile)
}
