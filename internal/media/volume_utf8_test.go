package media

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestVolumeRootPreservesLiteralUTF8Names(t *testing.T) {
	// A trimmed sibling must never receive another directory's marker or identity.
	root := t.TempDir()
	plain := filepath.Join(root, "volume")
	require.NoError(t, os.Mkdir(plain, 0o755))
	for _, name := range []string{"volume ", " \t\n", `back\slash`, "quote'\"\n照片"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			// Initialize the exact native directory and retain its canonical spelling.
			candidate := filepath.Join(root, name)
			require.NoError(t, os.Mkdir(candidate, 0o755))
			want, err := filepath.EvalSymlinks(candidate)
			require.NoError(t, err)
			volume, err := InitializeVolume(candidate, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
			require.NoError(t, err)
			require.Equal(t, want, volume.Root)
			require.FileExists(t, filepath.Join(candidate, VolumeMarkerName))
			require.NoFileExists(t, filepath.Join(plain, VolumeMarkerName))

			// Both explicit selection and discovery return the same literal directory.
			selected, err := ValidateVolumeCandidate([]string{root}, candidate)
			require.NoError(t, err)
			require.Equal(t, want, selected)
			discovered, err := DiscoverVolume([]string{root}, volume.Marker.UUID)
			require.NoError(t, err)
			require.True(t, SameVolume(volume, discovered))
		})
	}
}

func TestVolumeRootRejectsInvalidUTF8BeforeResolution(t *testing.T) {
	// Even a component removed by path cleaning cannot disguise unsupported raw input.
	for _, name := range []string{"invalid\xff", "nul\x00"} {
		_, err := canonicalDirectory(t.TempDir() + "/" + name + "/..")
		require.ErrorContains(t, err, "UTF-8 text without NUL")
	}
}
