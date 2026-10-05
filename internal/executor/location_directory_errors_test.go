package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

type failingDirectoryEntry struct {
	os.DirEntry
	name   string
	err    error
	before func()
}

func (e failingDirectoryEntry) Name() string { return e.name }

func (e failingDirectoryEntry) Info() (os.FileInfo, error) {
	// Inject the observation failure at the same boundary as a real DirEntry.Info call.
	if e.before != nil {
		e.before()
	}
	if e.err != nil {
		return nil, e.err
	}
	return e.DirEntry.Info()
}

func TestLocationDirectoryRetainsChildErrors(t *testing.T) {
	// Observe real metadata beside injected permission, unsupported-name and encoding failures.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("content"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "folder"), 0700))
	children, err := os.ReadDir(root)
	require.NoError(t, err)
	reader := &LocationDirectoryReader{Location: &library.Location{ID: 7}, Parent: &entity.LocationEntry{Path: "parent"}}
	entries := []os.DirEntry{
		failingDirectoryEntry{DirEntry: children[0], name: "a-good"},
		failingDirectoryEntry{DirEntry: children[1], name: "b-unreadable", err: os.ErrPermission},
		failingDirectoryEntry{DirEntry: children[0], name: "c-\xff", before: func() { t.Fatal("invalid UTF-8 reached stat") }},
		failingDirectoryEntry{DirEntry: children[0], name: "d-\x00", before: func() { t.Fatal("unsupported name reached stat") }},
		failingDirectoryEntry{DirEntry: children[0], name: "z-good"},
	}
	directory := &LocationDirectory{LocationDirectoryReader: reader, entries: entries}

	// A complete List batch retains failed slots, type hints and all independently usable siblings.
	rows, infos, err := directory.Read(context.Background(), 0, directory.Len())
	require.NoError(t, err)
	require.Len(t, rows, 5)
	require.Len(t, infos, 2)
	for _, index := range []int{0, 4} {
		require.NoError(t, rows[index].Error)
		require.NotNil(t, rows[index].Entry.Reference)
		require.EqualValues(t, 7, rows[index].Entry.Reference.LocationId)
		require.NotNil(t, infos[rows[index].Path])
	}
	for _, index := range []int{1, 2, 3} {
		require.Error(t, rows[index].Error)
		require.Nil(t, rows[index].Entry)
		require.NotContains(t, infos, rows[index].Path)
	}
	require.ErrorIs(t, rows[1].Error, os.ErrPermission)
	require.True(t, rows[1].Type.IsDir())
	require.Contains(t, rows[2].Error.Error(), `c-\xff`)

	// Workflow/Search/Measure observation still rejects the same failed child instead of omitting it.
	strict, strictInfos, err := reader.Observe(context.Background(), entries)
	require.ErrorIs(t, err, os.ErrPermission)
	require.Nil(t, strict)
	require.Nil(t, strictInfos)
}

func TestLocationDirectoryCancellationRemainsRequestFailure(t *testing.T) {
	// Cancel during the final observation to ensure even a one-row batch cannot claim success.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), nil, 0600))
	children, err := os.ReadDir(root)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &LocationDirectoryReader{Location: &library.Location{ID: 7}, Parent: &entity.LocationEntry{}}
	entries := []os.DirEntry{failingDirectoryEntry{DirEntry: children[0], name: "file", before: cancel}}
	directory := &LocationDirectory{LocationDirectoryReader: reader, entries: entries}
	rows, infos, err := directory.Read(ctx, 0, 1)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, rows)
	require.Nil(t, infos)

	// Empty and strict batches obey the same request cancellation boundary.
	_, _, err = directory.Read(ctx, 0, 0)
	require.ErrorIs(t, err, context.Canceled)
	_, _, err = reader.Observe(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestLocationDirectoryReadFailureIsNotAChildError(t *testing.T) {
	// A failed directory stream cannot establish an exact total, even if it returned some children.
	failure := errors.New("directory read failed")
	visited := false
	err := readLocationEntries(context.Background(), func(int) ([]os.DirEntry, error) {
		return make([]os.DirEntry, 1), failure
	}, func([]os.DirEntry) error {
		visited = true
		return nil
	})
	require.ErrorIs(t, err, failure)
	require.False(t, visited)

	// Cancellation after directory enumeration remains an operation failure through its consumer.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = readLocationEntries(ctx, func(int) ([]os.DirEntry, error) {
		return make([]os.DirEntry, 1), io.EOF
	}, func([]os.DirEntry) error {
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}
