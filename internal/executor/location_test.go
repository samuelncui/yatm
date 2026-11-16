package executor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocationRootDistinguishesExclusionsFromInspectionFailures(t *testing.T) {
	// The same browse boundary contains allowed, deliberately excluded, and unavailable paths.
	root := t.TempDir()
	other := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "allowed"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "ignored"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("not a directory"), 0644))
	require.NoError(t, os.Symlink(filepath.Join(root, "allowed"), filepath.Join(root, "link")))
	exe := New(nil, nil, nil, Paths{Access: []AccessRange{
		{Root: root, Ignore: "/ignored/"}, {Root: other},
	}}, Scripts{}, nil)

	// Only policy exclusions may be omitted from a browser; filesystem failures must remain visible.
	tests := []struct {
		name     string
		path     string
		excluded bool
		missing  bool
	}{
		{name: "allowed", path: filepath.Join(root, "allowed")},
		{name: "administrator ignore", path: filepath.Join(root, "ignored"), excluded: true},
		{name: "symlink", path: filepath.Join(root, "link"), excluded: true},
		{name: "ordinary file", path: filepath.Join(root, "file"), excluded: true},
		{name: "outside", path: t.TempDir(), excluded: true},
		{name: "missing child beside unrelated boundary", path: filepath.Join(root, "missing"), missing: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := exe.LocationRoot(test.path)
			require.Equal(t, test.excluded, errors.Is(err, ErrAccessExcluded))
			require.Equal(t, test.missing, errors.Is(err, os.ErrNotExist))
			if !test.excluded && !test.missing {
				require.NoError(t, err)
			}
		})
	}
}

func TestCanonicalConfiguredPathFollowsDanglingSymlinks(t *testing.T) {
	// A configured path through a dangling link names where a write would land.
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(base, "real"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(base, "links"), 0755))
	require.NoError(t, os.Symlink("../real/missing", filepath.Join(base, "links", "relative")))
	require.NoError(t, os.Symlink(filepath.Join(base, "links", "relative"), filepath.Join(base, "absolute")))
	require.NoError(t, os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "existing")))

	for value, want := range map[string]string{
		filepath.Join(base, "links", "relative"):                filepath.Join(base, "real", "missing"),
		filepath.Join(base, "links", "relative", "child", "db"): filepath.Join(base, "real", "missing", "child", "db"),
		filepath.Join(base, "absolute", "child"):                filepath.Join(base, "real", "missing", "child"),
		filepath.Join(base, "existing", "missing"):              filepath.Join(base, "real", "missing"),
		filepath.Join(base, "plain", "missing"):                 filepath.Join(base, "plain", "missing"),
	} {
		actual, err := CanonicalConfiguredPath(value)
		require.NoError(t, err, value)
		require.Equal(t, want, actual, value)
	}

	// A link cycle remains an error instead of a plausible path.
	require.NoError(t, os.Symlink("loop-b", filepath.Join(base, "loop-a")))
	require.NoError(t, os.Symlink("loop-a", filepath.Join(base, "loop-b")))
	_, err = CanonicalConfiguredPath(filepath.Join(base, "loop-a", "child"))
	require.Error(t, err)
}

func TestLocationRootContainmentIsLexicallyLocal(t *testing.T) {
	// The range root and a dot-prefixed child are inside; its parent and a prefix-sharing sibling are not.
	base := t.TempDir()
	root := filepath.Join(base, "access")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "..child"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(base, "accessx"), 0755))
	exe := New(nil, nil, nil, Paths{Access: []AccessRange{{Root: root}}}, Scripts{}, nil)

	for _, path := range []string{root, filepath.Join(root, "..child")} {
		_, err := exe.LocationRoot(path)
		require.NoError(t, err, path)
	}
	for _, path := range []string{base, filepath.Join(base, "accessx")} {
		_, err := exe.LocationRoot(path)
		require.ErrorIs(t, err, ErrAccessExcluded, path)
	}
}
