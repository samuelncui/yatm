package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type countedDirectoryEntry struct {
	os.DirEntry
	calls *int
}

func (e countedDirectoryEntry) Info() (os.FileInfo, error) { *e.calls++; return e.DirEntry.Info() }

func TestLocationDirectoryConsumesEntriesOnceAndStops(t *testing.T) {
	// A scripted physical stream makes consumption observable independently of filesystem ordering.
	for _, stop := range []bool{false, true} {
		reads, consumed, visits := 0, 0, 0
		sentinel := errors.New("consumer stopped")
		err := readLocationEntries(context.Background(), func(n int) ([]os.DirEntry, error) {
			reads++
			if consumed == 100001 {
				return nil, io.EOF
			}
			n = min(n, 100001-consumed)
			consumed += n
			return make([]os.DirEntry, n), nil
		}, func(rows []os.DirEntry) error {
			visits += len(rows)
			if stop {
				return sentinel
			}
			return nil
		})
		if stop {
			require.ErrorIs(t, err, sentinel)
			require.Equal(t, 256, consumed)
			require.Equal(t, 1, reads)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, 100001, visits)
		require.Equal(t, (100001+255)/256+1, reads)
	}
}

func TestLocationDirectoryObservesOnlyRequestedBatch(t *testing.T) {
	// Count actual DirEntry.Info calls so a projected first batch cannot prefetch later metadata.
	root := t.TempDir()
	for i := 0; i < 513; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("f-%04d", i)), nil, 0600))
	}
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	calls := 0
	for i, entry := range entries {
		entries[i] = countedDirectoryEntry{DirEntry: entry, calls: &calls}
	}
	directory := &LocationDirectory{LocationDirectoryReader: &LocationDirectoryReader{Location: &library.Location{ID: 1}, Parent: &entity.LocationEntry{}}, entries: entries}
	rows, _, err := directory.Read(context.Background(), 0, 256)
	require.NoError(t, err)
	require.Len(t, rows, 256)
	require.Equal(t, 256, calls)

	// Cancellation performs no metadata work for the remainder.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = directory.Read(ctx, 256, 512)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 256, calls)
}

func TestLocationChildAccessUsesRegisteredRootRanges(t *testing.T) {
	// A nested administrator range cannot broaden a registration authorized through its parent.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), 0700))
	exe := New(nil, nil, nil, Paths{Access: []AccessRange{
		{Root: root, Ignore: "*.secret\n/anchored"}, {Root: filepath.Join(root, "nested")},
	}}, Scripts{}, nil)
	location := &library.Location{RootPath: root, ExecutorID: "local"}
	_, err = exe.CheckLocation(location)
	require.NoError(t, err)
	for _, parent := range []string{"", "nested"} {
		reader, err := exe.PrepareLocationDirectory(location, parent)
		require.NoError(t, err)
		for _, name := range []string{"file", "file.secret", "anchored"} {
			require.Equal(t, !location.AccessExcluded(filepath.ToSlash(filepath.Join(parent, name)), false), reader.Allowed(name, false))
		}
		require.False(t, reader.Allowed("file.secret", false))
	}
}

func TestObserveFileRowsIsolatesMissingAndExcludedParentsAndLeaves(t *testing.T) {
	// A single batch includes distinct parent failures, an excluded leaf and an allowed sibling.
	exe := setupTestExecutor(t)
	require.NoError(t, exe.lib.AutoMigrate())
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, "good"), 0700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "blocked"), 0700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "good", "directory"), 0700))
	for _, name := range []string{"good/allowed", "good/secret", "blocked/secret"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), nil, 0600))
	}
	location := &library.Location{Name: "Files", ExecutorID: "local", RootPath: root}
	require.NoError(t, exe.lib.CreateLocation(context.Background(), location))
	exe = New(nil, exe.lib, nil, Paths{Access: []AccessRange{{Root: root, Ignore: "/blocked/\n/good/secret\n/good/directory/"}}}, Scripts{}, nil)
	paths := []string{"missing/file", "good/secret", "blocked/secret", "good/allowed", "good/directory"}
	facts := make(map[int64]*library.FileReadFacts)
	for index, name := range paths {
		facts[int64(index+1)] = &library.FileReadFacts{Original: &library.FileLocation{LocationID: location.ID, Path: name}}
	}
	observations, err := exe.ObserveFileRows(context.Background(), facts)
	require.NoError(t, err)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, observations[1].Availability)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE, observations[2].Availability)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE, observations[3].Availability)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, observations[4].Availability)
	require.Nil(t, observations[2].Entry)
	require.Nil(t, observations[3].Entry)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE, observations[5].Availability)
}

func TestObserveListedRowsReusesOnlyTheirLocationMetadata(t *testing.T) {
	// Read a real batch, then remove its leaf to expose an accidental second metadata read.
	exe := setupTestExecutor(t)
	db := exe.db
	require.NoError(t, exe.lib.AutoMigrate())
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	locations := make([]*library.Location, 2)
	for i := range locations {
		directory := filepath.Join(root, fmt.Sprint(i))
		require.NoError(t, os.Mkdir(directory, 0700))
		locations[i] = &library.Location{Name: fmt.Sprint(i), ExecutorID: "local", RootPath: directory}
		require.NoError(t, exe.lib.CreateLocation(context.Background(), locations[i]))
	}
	exe = New(nil, exe.lib, nil, Paths{Access: []AccessRange{{Root: root}}}, Scripts{}, nil)
	filename := filepath.Join(locations[0].RootPath, "same")
	require.NoError(t, os.WriteFile(filename, []byte("data"), 0600))
	read, err := exe.EnumerateLocationDirectory(context.Background(), locations[0].ID, "")
	require.NoError(t, err)
	_, infos, err := read.Read(context.Background(), 0, 1)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filename))
	facts := map[int64]*library.FileReadFacts{
		1: {Original: &library.FileLocation{LocationID: locations[0].ID, Path: "same"}},
		2: {Original: &library.FileLocation{LocationID: locations[1].ID, Path: "same"}},
	}
	observed, err := exe.ObserveListedFileRows(context.Background(), facts, read.Location, infos)
	require.NoError(t, err)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, observed[1].Availability)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, observed[2].Availability)

	// A new operation refreshes observations; within it, source and parent setup are reused.
	queries := 0
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("count-observation-locations", func(tx *gorm.DB) {
		if tx.Statement.Table == "locations" {
			queries++
		}
	}))
	reader := exe.NewFileRowReader()
	for range 3 {
		observed, err = reader.Read(context.Background(), facts)
		require.NoError(t, err)
		require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, observed[1].Availability)
		require.Len(t, reader.sources, 2)
		require.Len(t, reader.guards, 2)
		require.Len(t, reader.allowed, 2)
	}
	require.Equal(t, 2, queries, "source SQL is per Location, not per projection batch")
}
