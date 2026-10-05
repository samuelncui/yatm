package library

import (
	"context"
	"fmt"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestIdenticalHistoricalComponentsAndScope(t *testing.T) {
	// Current and saved signatures connect a chain, while Trash remains excluded.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	location := locationTestSource(t, lib)
	files := []*File{{Name: "a", Kind: entity.FileKind_FILE_KIND_REGULAR}, {Name: "b", Kind: entity.FileKind_FILE_KIND_REGULAR}, {Name: "c", Kind: entity.FileKind_FILE_KIND_REGULAR}, {Name: "unknown", Kind: entity.FileKind_FILE_KIND_REGULAR}}
	createFileRows(t, db, files...)
	for i, sig := range []string{"x", "y", "z"} {
		require.NoError(t, db.Create(&FileLocation{FileID: files[i].ID, LocationID: location.ID, Path: fmt.Sprintf("dir/%d", i), Signature: []byte(sig)}).Error)
	}
	require.NoError(t, db.Create(&FileVersion{FileID: files[1].ID, Signature: []byte("x")}).Error)
	require.NoError(t, db.Create(&FileVersion{FileID: files[2].ID, Signature: []byte("y")}).Error)
	createFileRows(t, db, &File{ID: TrashFileID, Name: ".Trash", Kind: entity.FileKind_FILE_KIND_DIRECTORY})
	removed := File{ParentID: TrashFileID, Name: "old", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, &removed)
	require.NoError(t, db.Create(&FileVersion{FileID: removed.ID, Signature: []byte("x")}).Error)

	// Library membership is complete even when a member page contains only one row.
	snapshot, err := lib.OpenIdenticalSnapshot(ctx, IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	groups, err := snapshot.Groups("", 1)
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	require.Equal(t, int64(3), groups.Groups[0].Count)
	var ids []int64
	require.NoError(t, snapshot.WalkMembers(groups.Groups[0].ID, func(members []IdenticalMember) error {
		for _, m := range members {
			ids = append(ids, m.FileID)
		}
		return nil
	}))
	require.Equal(t, []int64{files[0].ID, files[1].ID, files[2].ID}, ids)
	first, err := snapshot.Members(groups.Groups[0].ID, "", 1)
	require.NoError(t, err)
	require.Len(t, first.Members, 1)
	require.NotEmpty(t, first.NextCursor)
	require.NotNil(t, first.Members[0].Original)

	// Locations do not include history and repeated selections do not duplicate membership.
	physical, err := lib.OpenIdenticalSnapshot(ctx, IdenticalScope{Source: IdenticalLocations, Roots: []IdenticalRoot{{LocationID: location.ID}, {LocationID: location.ID}}})
	require.NoError(t, err)
	defer physical.Close()
	groups, err = physical.Groups("", 20)
	require.NoError(t, err)
	require.Empty(t, groups.Groups)
}

func TestIdenticalLocationPaginationAndStaleCursors(t *testing.T) {
	// More than a staging batch shares a signature across two independently selected Locations.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	one, two, other := locationTestSource(t, lib), locationTestSource(t, lib), locationTestSource(t, lib)
	for i := 0; i < 270; i++ {
		file := File{Name: fmt.Sprintf("file-%03d", i), Kind: entity.FileKind_FILE_KIND_REGULAR}
		createFileRows(t, db, &file)
		location := one.ID
		if i%2 == 1 {
			location = two.ID
		}
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location, Path: fmt.Sprintf("dir/%03d", i), Signature: []byte("shared"), Size: 10}).Error)
	}
	outside := File{Name: "outside", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, &outside)
	require.NoError(t, db.Create(&FileLocation{FileID: outside.ID, LocationID: other.ID, Path: "other/file", Signature: []byte("shared")}).Error)
	scope := IdenticalScope{Source: IdenticalLocations, Roots: []IdenticalRoot{{LocationID: two.ID}, {LocationID: one.ID}, {LocationID: one.ID}}}
	// Snapshot reads use bounded source pages and never invoke source writes or detail hydration.
	reads, writes := 0, 0
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("identical_count_reads", func(*gorm.DB) { reads++ }))
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("identical_count_creates", func(*gorm.DB) { writes++ }))
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("identical_count_updates", func(*gorm.DB) { writes++ }))
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register("identical_count_deletes", func(*gorm.DB) { writes++ }))
	snapshot, err := lib.OpenIdenticalSnapshot(ctx, scope)
	require.NoError(t, err)
	defer snapshot.Close()
	require.Zero(t, writes)
	require.LessOrEqual(t, reads, 3, "Location snapshot uses only bounded original pages")
	groups, err := snapshot.Groups("", 20)
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	require.Equal(t, int64(270), groups.Groups[0].Count)
	group, err := snapshot.Group(groups.Groups[0].ID)
	require.NoError(t, err)
	require.Equal(t, groups.Groups[0], *group)
	page, err := snapshot.Members(group.ID, "", 2)
	require.NoError(t, err)
	require.Len(t, page.Members, 2)
	count := 0
	require.NoError(t, snapshot.WalkMembers(group.ID, func(members []IdenticalMember) error {
		count += len(members)
		for _, m := range members {
			require.NotEqual(t, outside.ID, m.FileID)
		}
		return nil
	}))
	require.Equal(t, 270, count)

	// Fresh read snapshots accept stable cursors until any relevant recorded content changes.
	unchanged, err := lib.OpenIdenticalSnapshot(ctx, scope)
	require.NoError(t, err)
	defer unchanged.Close()
	require.Equal(t, snapshot.Revision, unchanged.Revision)
	next, err := unchanged.Members(group.ID, page.NextCursor, 2)
	require.NoError(t, err)
	require.Greater(t, next.Members[0].FileID, page.Members[1].FileID)
	original := *page.Members[0].Original
	original.Path = original.Path + "-relocated"
	require.NoError(t, db.Save(&original).Error)
	changed, err := lib.OpenIdenticalSnapshot(ctx, scope)
	require.NoError(t, err)
	defer changed.Close()
	require.NotEqual(t, snapshot.Revision, changed.Revision)
	_, err = changed.Members(group.ID, page.NextCursor, 2)
	require.Error(t, err)
	fingerprint, err := changed.Fingerprint(group.ID)
	require.NoError(t, err)
	require.NotEqual(t, group.Fingerprint, fingerprint)
}

func TestIdenticalScopeNormalization(t *testing.T) {
	// Location roots are ordered and repeated selections do not duplicate content.
	scope, err := normalizeIdenticalScope(IdenticalScope{Source: IdenticalLocations, Roots: []IdenticalRoot{{LocationID: 2}, {LocationID: 1}, {LocationID: 1}}})
	require.NoError(t, err)
	require.Equal(t, []IdenticalRoot{{LocationID: 1}, {LocationID: 2}}, scope.Roots)
	require.True(t, identicalInRoots(FileLocation{LocationID: 1, Path: "a/b/file"}, scope.Roots))
	require.False(t, identicalInRoots(FileLocation{LocationID: 3, Path: "a/b/file"}, scope.Roots))
	require.False(t, identicalInRoots(FileLocation{LocationID: 1, Path: ".trash/file"}, scope.Roots))
	_, err = normalizeIdenticalScope(IdenticalScope{Source: IdenticalLocations})
	require.Error(t, err)
	_, err = normalizeIdenticalScope(IdenticalScope{Source: IdenticalLocations, Roots: []IdenticalRoot{{LocationID: 0}}})
	require.Error(t, err)
}

func TestIdenticalGroupPagesAndBoundedEvidence(t *testing.T) {
	// A long saved history must not inflate a visible member page or hide its actual shared evidence.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	files := []*File{{Name: "a", Kind: entity.FileKind_FILE_KIND_REGULAR}, {Name: "b", Kind: entity.FileKind_FILE_KIND_REGULAR}, {Name: "c", Kind: entity.FileKind_FILE_KIND_REGULAR}, {Name: "d", Kind: entity.FileKind_FILE_KIND_REGULAR}}
	createFileRows(t, db, files...)
	for i := 0; i < 30; i++ {
		require.NoError(t, db.Create(&FileVersion{FileID: files[0].ID, Signature: []byte(fmt.Sprintf("unique-%d", i))}).Error)
	}
	for i, file := range files {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte(fmt.Sprintf("group-%d", i/2))}).Error)
	}
	snapshot, err := lib.OpenIdenticalSnapshot(ctx, IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()

	// Group paging uses complete components and the displayed evidence only includes actual links.
	first, err := snapshot.Groups("", 1)
	require.NoError(t, err)
	require.Len(t, first.Groups, 1)
	require.NotEmpty(t, first.NextCursor)
	second, err := snapshot.Groups(first.NextCursor, 1)
	require.NoError(t, err)
	require.Len(t, second.Groups, 1)
	require.Empty(t, second.NextCursor)
	require.NotEqual(t, first.Groups[0].ID, second.Groups[0].ID)
	members, err := snapshot.Members(first.Groups[0].ID, "", 1)
	require.NoError(t, err)
	require.Len(t, members.Members, 1)
	require.Len(t, members.Members[0].Evidence, 1)
	require.Equal(t, []byte("group-0"), members.Members[0].Evidence[0].Signature)
}

func TestIdenticalLargeGroupAndCancellation(t *testing.T) {
	// One popular signature exercises disk reductions without pairwise member expansion.
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	files := make([]*File, 5000)
	for i := range files {
		files[i] = &File{Name: fmt.Sprintf("large-%05d", i), Kind: entity.FileKind_FILE_KIND_REGULAR}
	}
	createFileRows(t, db, files...)
	originals := make([]FileLocation, 0, len(files))
	for _, file := range files {
		originals = append(originals, FileLocation{FileID: file.ID, LocationID: location.ID, Path: file.Name, Signature: []byte("popular")})
	}
	require.NoError(t, db.CreateInBatches(&originals, identicalBatch).Error)
	scope := IdenticalScope{Source: IdenticalLocations, Roots: []IdenticalRoot{{LocationID: location.ID}}}
	snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), scope)
	require.NoError(t, err)
	defer snapshot.Close()
	groups, err := snapshot.Groups("", 20)
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	require.Equal(t, int64(5000), groups.Groups[0].Count)
	present, err := snapshot.HasMember(groups.Groups[0].ID, files[4999].ID)
	require.NoError(t, err)
	require.True(t, present)
	present, err = snapshot.HasMember(groups.Groups[0].ID, files[4999].ID+1)
	require.NoError(t, err)
	require.False(t, present)
	count := 0
	require.NoError(t, snapshot.WalkMembers(groups.Groups[0].ID, func(members []IdenticalMember) error {
		require.LessOrEqual(t, len(members), identicalBatch)
		count += len(members)
		return nil
	}))
	require.Equal(t, 5000, count)

	// Cancellation while source pages are being collected aborts staging instead of finishing the graph.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("identical_cancel_read", func(*gorm.DB) { cancel() }))
	cancelled, err := lib.OpenIdenticalSnapshot(ctx, scope)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, cancelled)
}

func TestIdenticalGroupFingerprintOnlyTracksRelatedIdentity(t *testing.T) {
	for _, source := range []IdenticalSource{IdenticalLibrary, IdenticalLocations} {
		t.Run(string(source), func(t *testing.T) {
			// Two independent groups and an unused Location distinguish result paging from operation authority.
			db, lib := newTestLibrary(t)
			ctx := context.Background()
			one, two := locationTestSource(t, lib), locationTestSource(t, lib)
			files := []*File{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}
			createFileRows(t, db, files...)
			originals := make([]FileLocation, 0, len(files))
			for i, file := range files {
				location, signature := one.ID, "first"
				if i >= 2 {
					location, signature = two.ID, "second"
				}
				originals = append(originals, FileLocation{FileID: file.ID, LocationID: location, Path: file.Name, Signature: []byte(signature)})
			}
			require.NoError(t, db.Create(&originals).Error)
			version := FileVersion{FileID: files[0].ID, Signature: []byte("first"), Size: 1}
			require.NoError(t, db.Create(&version).Error)
			scope := IdenticalScope{Source: source}
			if source == IdenticalLocations {
				scope.Roots = []IdenticalRoot{{LocationID: one.ID}, {LocationID: two.ID}}
			}
			open := func() *IdenticalSnapshot {
				snapshot, err := lib.OpenIdenticalSnapshot(ctx, scope)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, snapshot.Close()) })
				return snapshot
			}
			initial := open()
			groups, err := initial.Groups("", 1)
			require.NoError(t, err)
			require.Len(t, groups.Groups, 1)
			group := groups.Groups[0]

			// Unrelated content changes invalidate overall pagination, but not this group's operation.
			originals[3].Signature = []byte("different")
			require.NoError(t, db.Save(&originals[3]).Error)
			unrelated := open()
			fingerprint, err := unrelated.Fingerprint(group.ID)
			require.NoError(t, err)
			require.Equal(t, group.Fingerprint, fingerprint)
			require.NotEqual(t, initial.Revision, unrelated.Revision)
			_, err = unrelated.Groups(groups.NextCursor, 1)
			require.Error(t, err)

			// Auxiliary content facts and logical labels are not group membership or matching evidence.
			version.Size, version.Mode, version.MtimeNS, version.Hash = 99, 0600, 123, []byte("different-known-hash")
			require.NoError(t, db.Save(&version).Error)
			originals[0].Size, originals[0].Mode, originals[0].MtimeNS = 99, 0600, 123
			require.NoError(t, db.Save(&originals[0]).Error)
			require.NoError(t, db.Model(ModelFile).Where("id = ?", files[0].ID).Update("name", "renamed").Error)
			metadata := open()
			fingerprint, err = metadata.Fingerprint(group.ID)
			require.NoError(t, err)
			require.Equal(t, group.Fingerprint, fingerprint)

			// Relocating a member or changing its signature changes the authoritative group token.
			originals[0].Path = originals[0].Path + "-relocated"
			require.NoError(t, db.Save(&originals[0]).Error)
			rebound := open()
			fingerprint, err = rebound.Fingerprint(group.ID)
			require.NoError(t, err)
			require.NotEqual(t, group.Fingerprint, fingerprint)
			originals[0].Signature = []byte("new-content")
			require.NoError(t, db.Save(&originals[0]).Error)
			changed := open()
			changedFingerprint, err := changed.Fingerprint(group.ID)
			require.NoError(t, err)
			require.NotEqual(t, fingerprint, changedFingerprint)
		})
	}
}
