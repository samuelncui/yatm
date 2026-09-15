package executor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/media"
	"github.com/stretchr/testify/require"
)

func TestOnlineRootDistinguishesExclusionsFromInspectionFailures(t *testing.T) {
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
			_, err := exe.OnlineRoot(test.path)
			require.Equal(t, test.excluded, errors.Is(err, ErrAccessExcluded))
			require.Equal(t, test.missing, errors.Is(err, os.ErrNotExist))
			if !test.excluded && !test.missing {
				require.NoError(t, err)
			}
		})
	}
}

func TestRequiredExclusionsStayLiteralAndCannotBeReincluded(t *testing.T) {
	// Runtime paths are exact resources even when their names look like gitignore patterns.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	exe := New(nil, nil, nil, Paths{Work: filepath.Join(root, ".work")}, Scripts{}, nil)
	exe.SetOnlineRuntimePaths(filepath.Join(root, "snap[1]*"), filepath.Join(root, "snap[1]*", "child"),
		filepath.Join(root, ".state#!"), filepath.Join(root, "snap[1]*"))
	paths, err := exe.RequiredOnlineExclusions(root)
	require.NoError(t, err)
	require.Equal(t, []string{".state#!", ".work/jobs", "snap[1]*"}, paths)

	// User exceptions cannot reopen protected resources, or exclude similarly named ordinary files.
	location := &library.Location{RequiredExclusions: paths,
		Exclusions: &entity.OnlineExclusions{Format: "gitignore", Text: "!*\n!**/*"}}
	for _, path := range []string{".state#!", ".work/jobs/1/state.db", "snap[1]*/child/data"} {
		require.True(t, location.Excluded(path, false), path)
	}
	for _, path := range []string{".state", ".work/ordinary", "snap1", "snap[1]*-other/child"} {
		require.False(t, location.Excluded(path, false), path)
	}
}

func TestOnlineSeparationDoesNotIgnoreAnUnreadableArchiveMarker(t *testing.T) {
	// A configured archive discovery directory is excluded while its managed marker is valid.
	root := t.TempDir()
	archive := filepath.Join(root, "volumes", "archive")
	require.NoError(t, os.MkdirAll(archive, 0755))
	volume, err := media.InitializeVolume(archive, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	exe := New(nil, nil, nil, Paths{Work: filepath.Join(root, "work"), Volumes: []string{filepath.Join(root, "volumes")},
		Access: []AccessRange{{Root: root}}}, Scripts{}, nil)
	_, err = exe.RequiredOnlineExclusions(volume.Root)
	require.ErrorIs(t, err, ErrAccessExcluded)

	// Corrupt archive metadata cannot turn the same archive directory into an admitted original.
	require.NoError(t, os.WriteFile(filepath.Join(volume.Root, media.VolumeMarkerName), []byte("{incomplete marker"), 0644))
	_, err = exe.RequiredOnlineExclusions(volume.Root)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAccessExcluded)
}

func TestOnlineResourceOverlapIsAnExplicitExclusion(t *testing.T) {
	// Runtime-owned directories have an intentional denial distinct from failed resource discovery.
	root := t.TempDir()
	exe := New(nil, nil, nil, Paths{Work: root, Access: []AccessRange{{Root: root}}}, Scripts{}, nil)
	jobs := filepath.Join(root, "jobs")
	require.NoError(t, os.Mkdir(jobs, 0755))
	jobs, err := exe.OnlineRoot(jobs)
	require.NoError(t, err)
	_, err = exe.RequiredOnlineExclusions(jobs)
	require.ErrorIs(t, err, ErrAccessExcluded)

	// An invalid configured runtime path preserves the real filesystem error instead of hiding it.
	loop := filepath.Join(root, "loop")
	require.NoError(t, os.Symlink(loop, loop))
	exe.SetOnlineRuntimePaths(loop)
	_, err = exe.RequiredOnlineExclusions(root)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAccessExcluded)
}
