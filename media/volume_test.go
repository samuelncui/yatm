package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestInitializeAndDiscoverVolume(t *testing.T) {
	discoveryRoot := t.TempDir()
	volumeRoot := filepath.Join(discoveryRoot, "offline-disk")
	require.NoError(t, os.Mkdir(volumeRoot, 0o755))
	profile := &entity.VolumeMediaProfile{
		SerialNumber: "serial-1", Type: entity.VolumeType_VOLUME_TYPE_HM_SMR,
	}

	initialized, err := InitializeVolume(volumeRoot, profile)
	require.NoError(t, err)
	require.NotEmpty(t, initialized.Marker.UUID)
	require.Equal(t, profile, initialized.Marker.Profile)
	require.FileExists(t, filepath.Join(volumeRoot, VolumeMarkerName))

	discovered, err := DiscoverVolume([]string{discoveryRoot}, strings.ToUpper(initialized.Marker.UUID))
	require.NoError(t, err)
	require.True(t, SameVolume(initialized, discovered))
	capabilities, err := CapabilitiesForProfile(discovered.Marker.Profile.Pack())
	require.NoError(t, err)
	require.Equal(t, AccessConcurrentRandom, capabilities.Read)
	require.Equal(t, AccessSequential, capabilities.Write)
}

func TestInitializeVolumeDoesNotReplaceMarker(t *testing.T) {
	root := t.TempDir()
	profile := &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}
	first, err := InitializeVolume(root, profile)
	require.NoError(t, err)

	_, err = InitializeVolume(root, profile)
	require.ErrorContains(t, err, "marker already exists")
	opened, err := OpenVolume(root)
	require.NoError(t, err)
	require.True(t, SameVolume(first, opened))
}

func TestOpenVolumeRejectsChangedProfile(t *testing.T) {
	root := t.TempDir()
	initialized, err := InitializeVolume(root, &entity.VolumeMediaProfile{
		Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)
	markerPath := filepath.Join(root, VolumeMarkerName)
	require.NoError(t, os.WriteFile(markerPath, []byte(`{
  "version": 1,
  "uuid": "`+initialized.Marker.UUID+`",
  "created_at": "`+initialized.Marker.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")+`",
  "profile": {"type": 999}
}`), 0o644))

	_, err = OpenVolume(root)
	require.ErrorContains(t, err, "unsupported Volume type")
}

func TestOpenVolumeRejectsMarkerSymlink(t *testing.T) {
	// Initialize a valid marker outside the candidate Volume root.
	outside := t.TempDir()
	_, err := InitializeVolume(outside, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)

	// A marker symlink must never lend the outside Volume identity to this root.
	root := t.TempDir()
	require.NoError(t, os.Symlink(
		filepath.Join(outside, VolumeMarkerName),
		filepath.Join(root, VolumeMarkerName),
	))
	_, err = OpenVolume(root)
	require.ErrorContains(t, err, "marker is not a regular file")
}

func TestValidateVolumeCandidateAllowsOnlyRootOrDirectChild(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	nested := filepath.Join(child, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	resolved, err := ValidateVolumeCandidate([]string{root}, root)
	require.NoError(t, err)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	require.Equal(t, canonicalRoot, resolved)
	resolved, err = ValidateVolumeCandidate([]string{root}, child)
	require.NoError(t, err)
	canonicalChild, err := filepath.EvalSymlinks(child)
	require.NoError(t, err)
	require.Equal(t, canonicalChild, resolved)
	_, err = ValidateVolumeCandidate([]string{root}, nested)
	require.ErrorContains(t, err, "not a configured discovery candidate")
}

func TestDiscoverVolumeRejectsDuplicateUUID(t *testing.T) {
	root := t.TempDir()
	firstRoot := filepath.Join(root, "first")
	secondRoot := filepath.Join(root, "second")
	require.NoError(t, os.Mkdir(firstRoot, 0o755))
	require.NoError(t, os.Mkdir(secondRoot, 0o755))
	first, err := InitializeVolume(firstRoot, &entity.VolumeMediaProfile{
		Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)
	marker, err := os.ReadFile(filepath.Join(firstRoot, VolumeMarkerName))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, VolumeMarkerName), marker, 0o644))

	_, err = DiscoverVolume([]string{root}, first.Marker.UUID)
	require.ErrorContains(t, err, "mounted more than once")
}

func TestListVolumesTreatsMissingDiscoveryRootAsUnmounted(t *testing.T) {
	volumes, err := ListVolumes([]string{filepath.Join(t.TempDir(), "missing")})
	require.NoError(t, err)
	require.Empty(t, volumes)
}

func TestListVolumesDistinguishesAbsentAndInvalidMarkers(t *testing.T) {
	// A directory without a marker is ordinary; a valid sibling remains discoverable.
	root := t.TempDir()
	ordinary := filepath.Join(root, "ordinary")
	archive := filepath.Join(root, "archive")
	require.NoError(t, os.Mkdir(ordinary, 0755))
	require.NoError(t, os.Mkdir(archive, 0755))
	volume, err := InitializeVolume(archive, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	volumes, err := ListVolumes([]string{root})
	require.NoError(t, err)
	require.Len(t, volumes, 1)
	require.True(t, SameVolume(volume, volumes[0]))

	// Existing malformed metadata is a discovery failure, never permission to drop the directory.
	require.NoError(t, os.WriteFile(filepath.Join(ordinary, VolumeMarkerName), []byte("{invalid"), 0644))
	_, err = ListVolumes([]string{root})
	require.ErrorContains(t, err, "decode Volume marker failed")

	// A marker symlink is invalid even when it resolves to another complete archive marker.
	require.NoError(t, os.Remove(filepath.Join(ordinary, VolumeMarkerName)))
	require.NoError(t, os.Symlink(filepath.Join(archive, VolumeMarkerName), filepath.Join(ordinary, VolumeMarkerName)))
	_, err = ListVolumes([]string{root})
	require.ErrorContains(t, err, "Volume marker is not a regular file")
}
