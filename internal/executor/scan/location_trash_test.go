package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestLocationWalkExcludesTrashEvenWhenExplicitlySelected(t *testing.T) {
	// Whole-root enumeration skips the reserved subtree without relying on editable Ignore rules.
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".trash"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".trash/recycled"), []byte("retained"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "ordinary"), []byte("active"), 0600))
	source := &library.Location{RootPath: root}
	var paths []string
	yield := func(name string, _ os.FileInfo) error { paths = append(paths, name); return nil }
	require.NoError(t, walkSelection(context.Background(), locationWalkExecutor(t, source), source, "", yield))
	require.Equal(t, []string{"ordinary"}, paths)

	// Direct selection cannot bypass the business exclusion, including directory selections.
	for _, selected := range []string{".trash", ".trash/recycled"} {
		require.ErrorContains(t, walkSelection(context.Background(), locationWalkExecutor(t, source), source, selected, yield), "Trash")
	}
	require.Equal(t, []string{"ordinary"}, paths)
}
