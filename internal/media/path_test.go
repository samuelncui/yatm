package media

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveMediaPathRejectsSymlinksAndEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("source"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "linked")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(root, "file-link")))

	resolved, err := ResolveSourcePath(root, "source.txt")
	require.NoError(t, err)
	canonical, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(canonical, "source.txt"), resolved)
	_, err = ResolveSourcePath(root, "linked/outside.txt")
	require.ErrorContains(t, err, "contains a symlink")
	_, err = ResolveSourcePath(root, "file-link")
	require.ErrorContains(t, err, "contains a symlink")
	_, err = ResolveTargetPath(root, "linked/new.txt")
	require.ErrorContains(t, err, "contains a symlink")
	_, err = ResolveTargetPath(root, "../outside.txt")
	require.Error(t, err)
}

func TestResolveTargetPathAllowsMissingComponents(t *testing.T) {
	root := t.TempDir()
	resolved, err := ResolveTargetPath(root, "new/child.txt")
	require.NoError(t, err)
	canonical, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(canonical, "new", "child.txt"), resolved)
}
