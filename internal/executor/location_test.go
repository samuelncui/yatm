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
