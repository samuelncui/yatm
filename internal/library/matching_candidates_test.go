package library

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeCandidatesExcludeFilesBoundToAnotherLocation(t *testing.T) {
	// Detached Files and this Location's originals are eligible; another Location keeps its File.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	local, other := locationTestSource(t, lib), locationTestSource(t, lib)
	var want []int64
	var observed []*FileTrackingKey
	for _, name := range []string{"detached", "local", "other", "unobserved"} {
		file := &File{Name: name}
		createFileRows(t, db, file)
		switch name {
		case "local":
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: local.ID, Path: name}).Error)
		case "other":
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: other.ID, Path: name}).Error)
		}
		require.NoError(t, db.Create(&FileTrackingKey{FileID: file.ID, Kind: TrackingNative, Scope: "fs:01", KeyValue: []byte(name)}).Error)
		if name != "unobserved" {
			observed = append(observed, &FileTrackingKey{Kind: TrackingNative, Scope: "fs:01", KeyValue: []byte(name)})
		}
		if name == "detached" || name == "local" {
			want = append(want, file.ID)
		}
	}

	// Page by immutable identity without omissions or duplicate candidates.
	var actual []int64
	var after int64
	for {
		page, err := lib.NativeCandidates(ctx, local.ID, observed, after, 1)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		require.Len(t, page, 1)
		actual = append(actual, page[0].FileID)
		after = page[0].FileID
	}
	require.Equal(t, want, actual)
}

func TestTrackingKeysNameOneObjectWithinOneScope(t *testing.T) {
	// Native values compare only within their filesystem; a lifetime guard counts only when both recorded one.
	key := func(scope string, birth int64) *FileTrackingKey {
		return &FileTrackingKey{Kind: TrackingNative, Scope: scope, KeyValue: []byte("42"), Details: TrackingDetails{BirthNS: birth}}
	}
	require.True(t, key("fs:01", 7).SameObject(key("fs:01", 7)))
	require.True(t, key("fs:01", 0).SameObject(key("fs:01", 7)))
	require.False(t, key("fs:01", 7).SameObject(key("fs:01", 8)))
	require.False(t, key("fs:01", 7).SameObject(key("fs:02", 7)))
	require.True(t, NativeReplaced([]*FileTrackingKey{key("fs:01", 7)}, []*FileTrackingKey{key("fs:01", 8)}))
	require.False(t, NativeReplaced([]*FileTrackingKey{key("fs:01", 7)}, []*FileTrackingKey{key("fs:02", 8)}))
	require.True(t, TrackingConfirmed([]*FileTrackingKey{key("fs:01", 0)}, []*FileTrackingKey{key("fs:01", 7)}))
	require.False(t, SameTrackingKeys([]*FileTrackingKey{key("fs:01", 0)}, []*FileTrackingKey{key("fs:01", 7)}))
}
