package library

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDuplicateGroupsPageGloballyAndRetainFilteredMembers(t *testing.T) {
	// Three opaque-content groups span two Locations and more than one summary/member page.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	one, two := onlineTestSource(t, lib), onlineTestSource(t, lib)
	signatures := [][]byte{{0, 255, 0}, {0, 255, 1}, {255, 0}}
	var groups [][]*File
	for group, count := range []int{2, 3, 4} {
		var files []*File
		for index := 0; index < count; index++ {
			file := &File{Name: fmt.Sprintf("group-%d-file-%d", group, index), Note: "independent note"}
			require.NoError(t, db.Create(file).Error)
			locationID := one.ID
			if index == 0 {
				locationID = two.ID
			}
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: locationID,
				Path: file.Name, Signature: signatures[group], Size: int64(10 + group)}).Error)
			files = append(files, file)
		}
		groups = append(groups, files)
	}
	require.NoError(t, db.Create(&FileTag{FileID: groups[1][0].ID, Tag: "review"}).Error)
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: 1, Path: "copy", Signature: signatures[1], Size: 11}))
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: 2, Path: "another-copy", Signature: signatures[1], Size: 11}))
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: 1, Path: "dir/", IsDir: true, Signature: signatures[1]}))

	// A one-group page still counts every original and each physical copy only once.
	cursor := ""
	for index, signature := range signatures {
		page, err := lib.ListDuplicateGroups(ctx, "", cursor, 1)
		require.NoError(t, err)
		require.Len(t, page.Groups, 1)
		group := page.Groups[0]
		require.Equal(t, signature, group.Signature)
		require.Equal(t, int64(index+2), group.OriginalCount)
		require.Equal(t, group.OriginalCount, group.MatchingCount)
		require.Equal(t, int64(2), group.LocationCount)
		require.Equal(t, int64(10+index), group.GetSize())
		require.NotEmpty(t, page.IndexRevision)
		cursor = page.NextCursor
		if index < 2 {
			require.NotEmpty(t, cursor)
		}
	}
	require.Empty(t, cursor)

	// Multi-group replies must own their size values under the module's Go 1.20 loop semantics.
	all, err := lib.ListDuplicateGroups(ctx, "", "", 10)
	require.NoError(t, err)
	require.Len(t, all.Groups, 3)
	for index, group := range all.Groups {
		require.Equal(t, int64(10+index), group.GetSize())
		members, err := lib.ListDuplicateMembers(ctx, group.Signature, "", "", 10)
		require.NoError(t, err)
		require.Equal(t, group, members.Group)
	}

	// Filters select whole groups while expansion marks the nonmatching independent Files.
	query := "tag:review AND has:archive"
	filtered, err := lib.ListDuplicateGroups(ctx, query, "", 1)
	require.NoError(t, err)
	require.Len(t, filtered.Groups, 1)
	require.Equal(t, int64(3), filtered.Groups[0].OriginalCount)
	require.Equal(t, int64(1), filtered.Groups[0].MatchingCount)
	require.Equal(t, int64(2), filtered.Groups[0].ArchivedCopies)
	cursor = ""
	for index, file := range groups[1] {
		page, err := lib.ListDuplicateMembers(ctx, signatures[1], query, cursor, 1)
		require.NoError(t, err)
		require.Equal(t, filtered.Groups[0], page.Group)
		require.Len(t, page.Members, 1)
		member := page.Members[0]
		require.Equal(t, file.ID, member.File.ID)
		require.Equal(t, index == 0, member.MatchesFilter)
		require.Equal(t, "/"+file.Name, member.LibraryPath)
		require.Equal(t, file.Name, member.Original.Path)
		require.NotEmpty(t, member.LocationName)
		require.Equal(t, "independent note", member.File.Note)
		require.Equal(t, int64(2), member.File.ContentSummary.ArchivedCopies)
		if index == 0 {
			require.Equal(t, []string{"review"}, member.File.Tags)
		}
		cursor = page.NextCursor
	}
	require.Empty(t, cursor)

	// A single filtered Location member retains matches elsewhere, including unavailable indexes.
	two.RootPath = "/missing/duplicate-review"
	require.NoError(t, db.Save(two).Error)
	page, err := lib.ListDuplicateGroups(ctx, fmt.Sprintf("location:%d", two.ID), "", 10)
	require.NoError(t, err)
	require.Len(t, page.Groups, 3)
	for _, group := range page.Groups {
		require.Equal(t, int64(1), group.MatchingCount)
	}
	require.NotEqual(t, filtered.IndexRevision, page.IndexRevision)
	var versions int64
	require.NoError(t, db.Model(&FileVersion{}).Count(&versions).Error)
	require.EqualValues(t, 3, versions, "inventory publication creates covered versions; browsing adds none")
}

func TestDuplicateGroupsExcludeHistoryUnknownAndSingletons(t *testing.T) {
	// Equal archived signatures do not supply missing current content facts.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	files := []*File{{Name: "one"}, {Name: "two"}, {Name: "unknown"}, {Name: "history"}, {Name: "unique"}}
	require.NoError(t, db.Create(files).Error)
	for index, signature := range [][]byte{[]byte("same"), []byte("same"), nil, []byte("unique")} {
		file := files[index]
		if index == 3 {
			file = files[4]
		}
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: file.Name,
			Signature: signature, Size: int64(index)}).Error)
	}
	for _, file := range files[2:] {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("same")}).Error)
	}

	// Inconsistent size facts are not silently presented as a trustworthy shared size.
	page, err := lib.ListDuplicateGroups(ctx, "", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Groups, 1)
	require.Equal(t, int64(2), page.Groups[0].OriginalCount)
	require.Equal(t, int64(1), page.Groups[0].LocationCount)
	require.Nil(t, page.Groups[0].Size)
	page, err = lib.ListDuplicateGroups(ctx, "name:history OR name:unknown", "", 10)
	require.NoError(t, err)
	require.Empty(t, page.Groups)

	// An old expansion receives an explicit obsolete summary rather than a false duplicate singleton.
	require.NoError(t, db.Delete(&FileLocation{}, "file_id = ?", files[0].ID).Error)
	members, err := lib.ListDuplicateMembers(ctx, []byte("same"), "", "", 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), members.Group.OriginalCount)
	require.Empty(t, members.Members)
	page, err = lib.ListDuplicateGroups(ctx, "", "", 10)
	require.NoError(t, err)
	require.Empty(t, page.Groups)
	require.NoError(t, db.Delete(&FileLocation{}, "file_id = ?", files[1].ID).Error)
	members, err = lib.ListDuplicateMembers(ctx, []byte("same"), "", "", 10)
	require.NoError(t, err)
	require.Nil(t, members.Group)
}

func TestDuplicateGroupCursorsAndLargeMemberPages(t *testing.T) {
	// A single group larger than the normal page must remain one summary and many bounded member pages.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	for index := 0; index < 105; index++ {
		file := &File{Name: fmt.Sprintf("member-%03d", index)}
		require.NoError(t, db.Create(file).Error)
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID,
			Path: file.Name, Signature: []byte{0, 254}}).Error)
	}
	page, err := lib.ListDuplicateGroups(ctx, "name:member", "", 1)
	require.NoError(t, err)
	require.Len(t, page.Groups, 1)
	require.Equal(t, int64(105), page.Groups[0].OriginalCount)
	require.Empty(t, page.NextCursor)
	first, err := lib.ListDuplicateMembers(ctx, []byte{0, 254}, "name:member", "", 100)
	require.NoError(t, err)
	require.Len(t, first.Members, 100)
	last, err := lib.ListDuplicateMembers(ctx, []byte{0, 254}, "name:member", first.NextCursor, 100)
	require.NoError(t, err)
	require.Len(t, last.Members, 5)
	require.Empty(t, last.NextCursor)
	require.Less(t, first.Members[99].File.ID, last.Members[0].File.ID)

	// Cursors cannot cross query, content or operation boundaries, and malformed requests fail closed.
	_, err = lib.ListDuplicateMembers(ctx, []byte{0, 255}, "name:member", first.NextCursor, 100)
	require.Error(t, err)
	_, err = lib.ListDuplicateMembers(ctx, []byte{0, 254}, "name:other", first.NextCursor, 100)
	require.Error(t, err)
	_, err = lib.ListDuplicateGroups(ctx, "name:member", first.NextCursor, 1)
	require.Error(t, err)
	_, err = lib.ListDuplicateGroups(ctx, "", "bad-cursor", 1)
	require.Error(t, err)
	_, err = lib.ListDuplicateGroups(ctx, "unsupported:value", "", 1)
	require.Error(t, err)
	_, err = lib.ListDuplicateGroups(ctx, "", "", 501)
	require.Error(t, err)
	_, err = lib.ListDuplicateMembers(ctx, nil, "", "", 1)
	require.Error(t, err)
}
