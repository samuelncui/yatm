package demo

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestWriteScaledIdenticalFilesPreservesExamples(t *testing.T) {
	// Cover the minimum, both rounding directions, and multiple physical write batches.
	for _, fixture := range []struct{ total, large, pairs, hidden int }{
		{207, 201, 3, 3}, {208, 202, 3, 3}, {209, 201, 4, 3},
		{1000, 500, 250, 7}, {1001, 501, 250, 7},
	} {
		t.Run(strconv.Itoa(fixture.total), func(t *testing.T) {
			// Start with the same small examples used by normal Demo preparation.
			root := t.TempDir()
			before, err := writeFixtureFiles(root, identicalPagingFiles(201, 0, 207))
			require.NoError(t, err)
			require.NoError(t, writeScaledIdenticalFiles(context.Background(), root, fixture.total))

			// Expansion must retain every original name, content and timestamp.
			for _, file := range before {
				filename := filepath.Join(root, file.Path)
				content, err := os.ReadFile(filename)
				require.NoError(t, err)
				digest := sha256.Sum256(content)
				require.Equal(t, file.Hash, digest[:])
				info, err := os.Stat(filename)
				require.NoError(t, err)
				require.True(t, file.ModTime.Equal(info.ModTime()), "existing mtime changed for %s", file.Path)
			}

			// Count real files and content groups, including complete pairs at odd totals.
			large, err := os.ReadDir(filepath.Join(root, "Large group"))
			require.NoError(t, err)
			pairs, err := os.ReadDir(filepath.Join(root, "Many groups"))
			require.NoError(t, err)
			require.Len(t, large, fixture.large)
			require.Len(t, pairs, fixture.pairs*2)
			require.Equal(t, fixture.total, len(large)+len(pairs))
			hidden := 0
			for _, file := range large {
				content, err := os.ReadFile(filepath.Join(root, "Large group", file.Name()))
				require.NoError(t, err)
				require.Equal(t, "Shared content for a group spanning several pages.\n", string(content))
				if strings.HasPrefix(file.Name(), ".") {
					hidden++
				}
			}
			require.Equal(t, fixture.hidden, hidden)
			groups := make(map[string]int)
			for _, file := range pairs {
				content, err := os.ReadFile(filepath.Join(root, "Many groups", file.Name()))
				require.NoError(t, err)
				groups[string(content)]++
			}
			require.Len(t, groups, fixture.pairs)
			for _, count := range groups {
				require.Equal(t, 2, count)
			}
		})
	}
}

func TestWriteScaledIdenticalFilesCanceled(t *testing.T) {
	// Cancellation precedes allocation or writes even for a very large requested count.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := filepath.Join(t.TempDir(), "unwritten")
	err := writeScaledIdenticalFiles(ctx, root, int(^uint(0)>>1))
	require.ErrorIs(t, err, context.Canceled)
	require.NoDirExists(t, root)
}

func TestPrepareIdenticalFilesRejectsBeforeResetOrWrites(t *testing.T) {
	// Invalid counts must preserve existing bytes whether or not reset was requested.
	for _, total := range []int{-1, 0, 1, 206} {
		for _, reset := range []bool{false, true} {
			t.Run(strconv.Itoa(total)+"/reset="+strconv.FormatBool(reset), func(t *testing.T) {
				// Use a sentinel database to detect both destructive reset and premature seeding.
				root := filepath.Join(t.TempDir(), "yatm-demo-invalid-scale")
				require.NoError(t, os.Mkdir(root, 0o755))
				database := filepath.Join(root, databaseName)
				require.NoError(t, os.WriteFile(database, []byte("keep"), 0o644))

				// Validation must happen before a database is opened or runtime files are written.
				err := Prepare(context.Background(), Options{
					Root: root, Listen: "127.0.0.1:18080", Reset: reset, IdenticalFiles: &total,
				})
				require.ErrorContains(t, err, "identical-files must be at least 207")
				content, err := os.ReadFile(database)
				require.NoError(t, err)
				require.Equal(t, "keep", string(content))
				entries, err := os.ReadDir(root)
				require.NoError(t, err)
				require.Len(t, entries, 1)
			})
		}
	}
}

func TestPrepareIdenticalFilesRequiresResetForReuse(t *testing.T) {
	// Even an explicit default count is an override; reuse cannot silently ignore it.
	for _, total := range []int{207, 10000} {
		t.Run(strconv.Itoa(total), func(t *testing.T) {
			// Existing configuration and data both belong to the reviewer until reset.
			root := filepath.Join(t.TempDir(), "yatm-demo-reuse-scale")
			require.NoError(t, os.Mkdir(root, 0o755))
			for _, name := range []string{databaseName, "config.yaml"} {
				require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("keep"), 0o644))
			}

			// Refusal happens before any rewrite, independently of the existing database format.
			err := Prepare(context.Background(), Options{
				Root: root, Listen: "127.0.0.1:18080", IdenticalFiles: &total,
			})
			require.ErrorContains(t, err, "identical-files override requires reset")
			for _, name := range []string{databaseName, "config.yaml"} {
				content, err := os.ReadFile(filepath.Join(root, name))
				require.NoError(t, err)
				require.Equal(t, "keep", string(content))
			}
			require.NoFileExists(t, filepath.Join(root, "read-info.sh"))
		})
	}
}

func TestPrepareCanceledBeforeReset(t *testing.T) {
	// Caller cancellation must leave the existing disposable root untouched.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := filepath.Join(t.TempDir(), "yatm-demo-canceled-scale")
	require.NoError(t, os.Mkdir(root, 0o755))
	sentinel := filepath.Join(root, "reviewer.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0o644))

	// Preparation owns reset, so it observes cancellation before discarding the fixture.
	err := Prepare(ctx, Options{Root: root, Listen: "127.0.0.1:18080", Reset: true})
	require.ErrorIs(t, err, context.Canceled)
	content, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "keep", string(content))
}

func TestPrepareScaledIdenticalFixture(t *testing.T) {
	// A fresh scaled root needs no reset; normal Scan publishes every added member.
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "yatm-demo-scaled")
	total := 1001
	require.NoError(t, Prepare(ctx, Options{
		Root: root, Listen: "127.0.0.1:18080", IdenticalFiles: &total,
	}))
	db, err := resource.OpenSQLite(filepath.Join(root, databaseName))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	lib := library.New(db)

	// Scaling adds only regular Files; Locations, retained Jobs and curated annotations stay small.
	requireTableCount(t, db, library.ModelFile, 363+1001-207)
	requireTableCount(t, db, library.ModelFileTag, 345)
	requireTableCount(t, db, executor.ModelJob, 13)
	locations, more, err := lib.ListLocations(ctx, library.LocationListFilter{Limit: 10})
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, locations, 5)
	requireReviewJobLinks(t, db, root, locations)
	var shared *library.Location
	for _, location := range locations {
		if location.Name == "Shared files" {
			shared = location
		}
	}
	require.NotNil(t, shared)
	require.Positive(t, shared.LastSyncJobID)
	require.Equal(t, shared.LastJobID, shared.LastSyncJobID)
	requireReviewIdenticalHistory(t, lib)

	// Retained groups include 501 popular members, 250 pairs and the existing groups of three and four.
	snapshot, err := lib.OpenIdenticalSnapshot(ctx, library.IdenticalScope{
		Source: library.IdenticalLocations, Roots: []library.IdenticalRoot{{LocationID: shared.ID}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, snapshot.Close()) })
	require.EqualValues(t, 253, snapshot.GroupCount)
	require.EqualValues(t, 1261, snapshot.AllRows)
	require.EqualValues(t, 1254, snapshot.VisibleRows)
	counts := make(map[int64]int)
	for cursor := ""; ; {
		page, err := snapshot.Groups(cursor, 100)
		require.NoError(t, err)
		for _, group := range page.Groups {
			counts[group.Count]++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	require.Equal(t, map[int64]int{501: 1, 2: 250, 3: 1, 4: 1}, counts)

	// Far offsets remain addressable with either hidden projection across the large group and pairs.
	for _, hidden := range []bool{false, true} {
		for _, offset := range []int64{0, 500, 1200} {
			rows, count, err := snapshot.Rows(offset, 100, hidden)
			require.NoError(t, err)
			require.Len(t, rows, int(min(100, count-offset)))
			require.Equal(t, offset, rows[0].Position)
		}
	}

	// Restart without an override preserves a reviewer change rather than regenerating scaled members.
	removed := filepath.Join(shared.RootPath, "Large group", "member-500.txt")
	require.NoError(t, os.Remove(removed))
	require.NoError(t, Prepare(ctx, Options{Root: root, Listen: "127.0.0.1:18080"}))
	require.NoFileExists(t, removed)
}
