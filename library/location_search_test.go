package library

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLocationSearchFiltersBeforeStablePagination(t *testing.T) {
	// Unavailable roots still have searchable metadata, mixed with nonmatching/preference rows.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	root := t.TempDir()
	want := make([]int64, 0, 20)
	for index := 0; index < 120; index++ {
		name := "Other Location"
		if index%2 == 0 {
			name = "Travel photos"
		}
		location := &Location{Name: name, RootPath: filepath.Join(root, fmt.Sprint(index)),
			ExecutorID: "local", RestoreTarget: index%3 == 0}
		require.NoError(t, lib.CreateOnlineSource(ctx, location))
		if index%6 == 0 {
			want = append(want, location.ID)
		}
	}

	// Both conditions precede the page boundary, without reading any original directories.
	after := int64(0)
	found := make([]int64, 0, len(want))
	for {
		rows, more, err := lib.ListLocations(ctx, LocationListFilter{
			AfterID: after, Limit: 3, Query: "tRaVeL", RestoreTarget: proto.Bool(true),
		})
		require.NoError(t, err)
		require.LessOrEqual(t, len(rows), 3)
		for _, row := range rows {
			require.Greater(t, row.ID, after)
			found = append(found, row.ID)
			after = row.ID
		}
		if !more {
			break
		}
		require.Len(t, rows, 3)
	}
	require.Equal(t, want, found)

	// The internal wrapper retains unfiltered listing and explicit false preference semantics.
	rows, more, err := lib.ListOnlineSources(ctx, 0, 1000, proto.Bool(false))
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, rows, 80)
}

func TestLocationSearchUsesLiteralNameAndRootPath(t *testing.T) {
	// Store metacharacters literally; no physical directory creation is needed for catalog search.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	root := t.TempDir()
	for index, name := range []string{"Travel 100%_!", "Travel 100AA", "Other"} {
		location := &Location{Name: name, ExecutorID: "local", RootPath: filepath.Join(root, fmt.Sprintf("Root-%d", index))}
		require.NoError(t, lib.CreateOnlineSource(ctx, location))
	}

	// Punctuation is not SQL syntax or a wildcard; root searches are also case-insensitive.
	for _, test := range []struct {
		query string
		name  string
	}{
		{query: "100%_!", name: "Travel 100%_!"},
		{query: "%", name: "Travel 100%_!"},
		{query: "_", name: "Travel 100%_!"},
		{query: "!", name: "Travel 100%_!"},
		{query: "rOoT-2", name: "Other"},
		{query: "unknown"},
		{query: "' OR 1=1 --"},
	} {
		t.Run(test.query, func(t *testing.T) {
			rows, more, err := lib.ListLocations(ctx, LocationListFilter{Limit: 20, Query: test.query})
			require.NoError(t, err)
			require.False(t, more)
			if test.name == "" {
				require.Empty(t, rows)
				return
			}
			require.Len(t, rows, 1)
			require.Equal(t, test.name, rows[0].Name)
		})
	}
}

func TestLocationSearchRejectsInvalidBounds(t *testing.T) {
	// Reject malformed cursor and page controls at the shared query boundary.
	_, lib := newTestLibrary(t)
	for _, filter := range []LocationListFilter{
		{Limit: 0}, {Limit: -1}, {Limit: 1001}, {Limit: 1, AfterID: -1},
	} {
		_, _, err := lib.ListLocations(context.Background(), filter)
		require.Error(t, err)
	}
}
