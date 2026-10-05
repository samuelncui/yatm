package library

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLocationPathsContinuationSkipsReturnedSubtrees(t *testing.T) {
	// A directory cursor must skip every descendant but retain its exact binary successor.
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	paths := []string{"outside", "parent/a", "parent/a/first", "parent/a/last", "parent/a0", "parent/b/child", "parent/z", "unrelated"}
	for i, name := range paths {
		file := &File{Name: fmt.Sprintf("file-%d", i)}
		createFileRows(t, db, file)
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: name}).Error)
	}

	// Mixed file/directory names keep the existing lexical, distinct immediate-child contract.
	after := ""
	var got []string
	for {
		page, more, err := lib.ListLocationPaths(context.Background(), location.ID, "parent/", after, 1)
		require.NoError(t, err)
		got = append(got, page...)
		if !more {
			break
		}
		require.Len(t, page, 1)
		after = page[0]
	}
	require.Equal(t, []string{"parent/a", "parent/a/", "parent/a0", "parent/b/", "parent/z"}, got)

	// Empty results and cancellation preserve the existing operation boundary.
	page, more, err := lib.ListLocationPaths(context.Background(), location.ID, "parent/", "parent/z", 2)
	require.NoError(t, err)
	require.Empty(t, page)
	require.False(t, more)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = lib.ListLocationPaths(ctx, location.ID, "parent/", "parent/a/", 2)
	require.ErrorIs(t, err, context.Canceled)
}

func TestLocationPathsReadCostDoesNotRescanEarlierPages(t *testing.T) {
	// Each child has several SQL pages of descendants, so restarting at the parent is costly.
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	const children, descendants, pageSize = 12, 200, 3
	for child := 0; child < children; child++ {
		for leaf := 0; leaf < descendants; leaf++ {
			name := fmt.Sprintf("parent/child-%02d/leaf-%03d", child, leaf)
			file := &File{Name: fmt.Sprintf("file-%02d-%03d", child, leaf)}
			createFileRows(t, db, file)
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: name}).Error)
		}
	}

	// Count database rows actually returned, including the bounded look-ahead overfetch.
	reads, consumed := 0, int64(0)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("location-path-cost", func(tx *gorm.DB) {
		if tx.Statement.Table == "file_locations" {
			reads++
			consumed += tx.RowsAffected
		}
	}))
	after := ""
	var got []string
	for {
		page, more, err := lib.ListLocationPaths(context.Background(), location.ID, "parent/", after, pageSize)
		require.NoError(t, err)
		got = append(got, page...)
		if !more {
			break
		}
		require.Len(t, page, pageSize)
		after = page[len(page)-1]
	}
	require.Len(t, got, children)
	for i, value := range got {
		require.Equal(t, fmt.Sprintf("parent/child-%02d/", i), value)
	}
	pages := (children + pageSize - 1) / pageSize
	require.LessOrEqual(t, consumed, int64(children*descendants+pages*batchSize))
	require.LessOrEqual(t, reads, children*descendants/batchSize+pages+1)
}
