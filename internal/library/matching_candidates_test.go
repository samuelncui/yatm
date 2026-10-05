package library

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetachedMatchingIDsPreserveEligibilityAndPaging(t *testing.T) {
	// Retained evidence can span Locations, but bound originals and foreign Executors are excluded.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	local := locationTestSource(t, lib)
	foreign := &Location{Name: "Other", RootPath: "/other", ExecutorID: "other"}
	require.NoError(t, db.Create(foreign).Error)
	var want []int64
	for _, name := range []string{"first", "bound", "foreign", "second", "without-evidence"} {
		file := &File{Name: name}
		createFileRows(t, db, file)
		if name == "without-evidence" {
			continue
		}
		source := local
		if name == "foreign" {
			source = foreign
		}
		require.NoError(t, db.Create(&FileTrackingKey{FileID: file.ID, Kind: TrackingNative,
			LocationID: source.ID, Scope: "filesystem", KeyValue: []byte(name)}).Error)
		if name == "bound" {
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: source.ID, Path: name}).Error)
		} else if name != "foreign" {
			want = append(want, file.ID)
		}
	}

	// Page by immutable identity without omissions or duplicate candidates.
	var actual []int64
	var after int64
	for {
		page, err := lib.DetachedMatchingFileIDsPage(ctx, local.ExecutorID, after, 1)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		require.Len(t, page, 1)
		actual = append(actual, page...)
		after = page[0]
	}
	require.Equal(t, want, actual)
}
