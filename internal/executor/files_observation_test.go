package executor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestObserveFileRowsWithMixedOriginals(t *testing.T) {
	// Exercise a page with unlinked Files while observation workers publish other rows.
	exe := setupTestExecutor(t)
	require.NoError(t, exe.lib.AutoMigrate())
	facts := make(map[int64]*library.FileReadFacts, 1000)
	for id := int64(1); id <= 1000; id++ {
		row := &library.FileReadFacts{}
		if id%2 == 0 {
			row.Original = &library.FileLocation{LocationID: 1234, Path: "missing"}
		}
		facts[id] = row
	}

	// A missing Location is an unavailable observation, not a page-wide failure.
	for range 3 {
		observed, err := exe.ObserveFileRows(context.Background(), facts)
		require.NoError(t, err)
		require.Len(t, observed, len(facts))
		for id := range facts {
			want := entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNLINKED
			if id%2 == 0 {
				want = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE
			}
			require.Equal(t, want, observed[id].Availability)
		}
	}
}

func TestParentGuardChecksEachLocationSeparately(t *testing.T) {
	// Two Locations with the same relative parent must retain their own access decisions.
	base := t.TempDir()
	firstRoot := filepath.Join(base, "first")
	secondRoot := filepath.Join(base, "second")
	for _, root := range []string{firstRoot, secondRoot} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "shared"), 0755))
	}
	exe := New(nil, nil, nil, Paths{Access: []AccessRange{{Root: base}}}, Scripts{}, nil)
	first := &library.Location{ID: 1, RootPath: firstRoot}
	second := &library.Location{ID: 2, RootPath: secondRoot, AccessAllowed: func(relative string, _ bool) bool {
		return relative != "shared"
	}}

	// The first successful guard cannot authorize the second Location's excluded parent.
	guards := make(map[fileParentGuardKey]pathGuard)
	var mu sync.Mutex
	require.NoError(t, exe.parentGuard(first, firstRoot, "shared/a", guards, &mu))
	require.ErrorIs(t, exe.parentGuard(second, secondRoot, "shared/b", guards, &mu), ErrAccessExcluded)
	require.Len(t, guards, 2)
}

func TestParentGuardRejectsSymlinkParent(t *testing.T) {
	// A parent symlink must not lead file observation outside the registered Location.
	base := t.TempDir()
	root := filepath.Join(base, "registered")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.Mkdir(root, 0755))
	require.NoError(t, os.Mkdir(outside, 0755))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "shared")))
	exe := New(nil, nil, nil, Paths{Access: []AccessRange{{Root: base}}}, Scripts{}, nil)
	location := &library.Location{ID: 1, RootPath: root}

	// Check the parent itself rather than following it through a child file.
	guards := make(map[fileParentGuardKey]pathGuard)
	var mu sync.Mutex
	require.ErrorIs(t, exe.parentGuard(location, root, "shared/secret", guards, &mu), ErrAccessExcluded)
}
