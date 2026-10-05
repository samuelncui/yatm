package preview

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestBundlePublicationFailureReleasesFiles(t *testing.T) {
	for _, name := range []string{"missing asset", "asset read failure", "rename failure"} {
		t.Run(name, func(t *testing.T) {
			// Preserve an existing destination while a new bundle fails before publication.
			root := t.TempDir()
			bundle := filepath.Join(root, "existing.zip")
			asset := filepath.Join(root, "asset.txt")
			manifest := &entity.PreviewManifest{Assets: []*entity.PreviewAsset{{Name: "asset.txt", MediaType: "text/plain"}}}
			if name == "rename failure" {
				require.NoError(t, os.Mkdir(bundle, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(bundle, "keep"), []byte("existing"), 0600))
				require.NoError(t, os.WriteFile(asset, []byte("new asset"), 0600))
			} else {
				require.NoError(t, os.WriteFile(bundle, []byte("existing"), 0600))
				if name == "asset read failure" {
					require.NoError(t, os.Mkdir(asset, 0700))
				}
			}
			before, err := os.ReadDir("/dev/fd")
			if err != nil {
				t.Skip("descriptor inventory unavailable")
			}

			// Repeated failures must release both per-asset and bundle descriptors immediately.
			for range 8 {
				require.Error(t, publishBundle(context.Background(), bundle, root, manifest))
			}
			after, err := os.ReadDir("/dev/fd")
			require.NoError(t, err)
			require.LessOrEqual(t, len(after), len(before))
			temporary, err := filepath.Glob(filepath.Join(root, ".bundle-*"))
			require.NoError(t, err)
			require.Empty(t, temporary)
			if name == "rename failure" {
				bundle = filepath.Join(bundle, "keep")
			}
			data, err := os.ReadFile(bundle)
			require.NoError(t, err)
			require.Equal(t, "existing", string(data))
		})
	}
}
