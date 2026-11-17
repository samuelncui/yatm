package library

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm/clause"
)

// These tests state Catalog behavior that a database dialect could change. They run on SQLite and,
// when YATM_TEST_MYSQL_DSN names a server, on MySQL.

func TestCatalogNamesCompareByteForByte(t *testing.T) {
	// Names differing in case, accents or Unicode normalization are distinct siblings.
	db, lib := newTestLibrary(t)
	names := []string{"resume.txt", "Resume.txt", "RESUME.txt", "résumé.txt", "résumé.txt",
		"Résumé.txt", "照片.jpg", "ß", "ss"}
	for _, name := range names {
		createFileRows(t, db, &File{Name: name, Kind: entity.FileKind_FILE_KIND_REGULAR})
	}
	listed := make([]string, 0, len(names))
	for _, file := range listTestChildren(t, lib, 0) {
		listed = append(listed, file.Name)
	}
	require.ElementsMatch(t, names, listed)

	// Only the identical name collides.
	require.Error(t, createFileRow(db, &File{Name: "résumé.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}))
}

func TestCatalogPathUniquenessCoversTheFullPath(t *testing.T) {
	// Two paths sharing far more than an index prefix are distinct; only identical paths collide.
	db, lib := newTestLibrary(t)
	shared := strings.Repeat(strings.Repeat("p", 250)+"/", 4)
	require.Greater(t, len(shared), 750)

	// Originals are unique per Location.
	for _, original := range []*FileLocation{
		{FileID: 1, LocationID: 1, Path: shared + "a"},
		{FileID: 2, LocationID: 1, Path: shared + "b"},
		{FileID: 3, LocationID: 2, Path: shared + "a"},
	} {
		require.NoError(t, db.Create(original).Error)
	}
	require.Error(t, db.Create(&FileLocation{FileID: 4, LocationID: 1, Path: shared + "a"}).Error)

	// Positions are unique per Media, and an ignored conflict on the path index writes nothing.
	position := func(path string) *Position { return &Position{MediaID: 1, Path: path, StorageOrder: []byte{}} }
	require.NoError(t, db.Create(position(shared+"a")).Error)
	require.NoError(t, db.Create(position(shared+"b")).Error)
	require.Error(t, db.Create(position(shared+"a")).Error)
	ignored := db.Clauses(clause.OnConflict{DoNothing: true}).Create(position(shared + "a"))
	require.NoError(t, ignored.Error)
	require.Zero(t, ignored.RowsAffected)
	var count int64
	require.NoError(t, db.Model(ModelPosition).Count(&count).Error)
	require.EqualValues(t, 2, count)

	// Location roots are unique per Executor.
	ctx := context.Background()
	root := "/" + shared
	require.NoError(t, lib.CreateLocation(ctx, &Location{Name: "First", ExecutorID: "local", RootPath: root + "a"}))
	require.NoError(t, lib.CreateLocation(ctx, &Location{Name: "Second", ExecutorID: "local", RootPath: root + "b"}))
	require.Error(t, lib.CreateLocation(ctx, &Location{Name: "Third", ExecutorID: "local", RootPath: root + "a"}))
	require.ErrorContains(t, lib.CreateLocation(ctx, &Location{Name: "Long", ExecutorID: "local",
		RootPath: "/" + strings.Repeat("r", entity.MaxPathBytes)}), "invalid Location root path")
}

func TestCatalogPathPagesFollowByteOrder(t *testing.T) {
	// Keyset pages over paths visit every row once, in the byte order Go sorts strings in.
	db, _ := newTestLibrary(t)
	paths := []string{"b", "B", "a", "a b", "a-b", "a/b", "a0", "Z", "e", "é", "é", "照片", "_x", "~x"}
	for index, value := range paths {
		require.NoError(t, db.Create(&FileLocation{FileID: int64(index + 1), LocationID: 1, Path: value}).Error)
	}
	var listed []string
	after := ""
	for {
		var page []string
		require.NoError(t, db.Model(&FileLocation{}).Where("location_id = ? AND path > ?", 1, after).
			Order("path").Limit(3).Pluck("path", &page).Error)
		if len(page) == 0 {
			break
		}
		listed = append(listed, page...)
		after = page[len(page)-1]
	}
	slices.Sort(paths)
	require.Equal(t, paths, listed)
}

func TestConcurrentMkdirCreatesOneDirectory(t *testing.T) {
	// Concurrent requests for one missing path all resolve to the single directory created.
	_, lib := newTestLibrary(t)
	const writers = 8
	ids := make([]int64, writers)
	var group errgroup.Group
	for index := range writers {
		group.Go(func() error {
			dir, err := lib.MkdirAll(context.Background(), 0, "shared/child", 0o755)
			if err != nil {
				return err
			}
			ids[index] = dir.ID
			return nil
		})
	}
	require.NoError(t, group.Wait())
	for _, id := range ids {
		require.Equal(t, ids[0], id)
	}
	children := listTestChildren(t, lib, 0)
	require.Len(t, children, 1)
	require.Equal(t, "shared", children[0].Name)
	require.Len(t, listTestChildren(t, lib, children[0].ID), 1)
}
