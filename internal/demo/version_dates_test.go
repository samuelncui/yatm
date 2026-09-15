package demo

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

func requireDatedRestoreFixtures(t *testing.T, lib *library.Library, fileIDs ...int64) {
	t.Helper()
	// Both text and image histories offer different versions on either side of September 1.
	cutoff := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	for _, fileID := range fileIDs {
		versions, more, err := lib.ListFileVersions(context.Background(), fileID, 0, 10)
		require.NoError(t, err)
		require.False(t, more)
		require.Len(t, versions, 3)
		require.Less(t, *versions[0].LastArchivedAt, *versions[1].LastArchivedAt)
		require.Less(t, *versions[1].LastArchivedAt, cutoff)
		require.Greater(t, *versions[2].LastArchivedAt, cutoff)
		match, err := lib.ResolveRestoreVersion(context.Background(), fileID, &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff})
		require.NoError(t, err)
		require.Equal(t, versions[1].ID, match.Version.Id)
		require.Equal(t, versions[1].LastArchivedAt, match.ArchivedAtMs)

		// Waitlist folder browsing must expose the same saved File and accept one nested override.
		file, err := lib.GetFile(context.Background(), fileID)
		require.NoError(t, err)
		children, err := lib.ListFiles(context.Background(), file.ParentID, entity.FileScope_FILE_SCOPE_SAVED, "", 100)
		require.NoError(t, err)
		found := false
		for _, child := range children.Files {
			if child.ID == fileID {
				found = true
			}
		}
		require.True(t, found)
		selections := []*entity.FileSelection{{Target: &entity.FileSelection_Library{
			Library: &entity.LibrarySelection{FileId: file.ParentID}}, Scope: entity.FileScope_FILE_SCOPE_SAVED}}
		require.NoError(t, lib.FreezeSelections(context.Background(), selections))
		matches := 0
		require.NoError(t, lib.WalkRestoreSelections(context.Background(), selections, []int64{versions[0].ID}, nil,
			func(item *library.RestoreSelectionItem) error {
				if item.File.ID != fileID {
					return nil
				}
				matches++
				require.Equal(t, versions[0].ID, item.Version.ID)
				return nil
			}))
		require.Equal(t, 1, matches)
	}
}
