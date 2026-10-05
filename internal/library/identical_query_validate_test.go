package library

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestOpenIdenticalComponentLibraryTransitiveAndStale(t *testing.T) {
	// Saved history bridges current signatures, while Trash and unrelated Files stay outside staging.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	location := locationTestSource(t, lib)
	files := []*File{
		{Name: "a", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "b", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "c", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "unrelated", Kind: entity.FileKind_FILE_KIND_REGULAR},
	}
	createFileRows(t, db, files...)
	for i, signature := range []string{"x", "y", "z", "other"} {
		require.NoError(t, db.Create(&FileLocation{FileID: files[i].ID, LocationID: location.ID,
			Path: files[i].Name, Signature: []byte(signature)}).Error)
	}
	require.NoError(t, db.Create(&FileVersion{FileID: files[1].ID, Signature: []byte("x")}).Error)
	require.NoError(t, db.Create(&FileVersion{FileID: files[2].ID, Signature: []byte("y")}).Error)
	createFileRows(t, db, &File{ID: TrashFileID, Name: ".Trash", Kind: entity.FileKind_FILE_KIND_DIRECTORY})
	trashed := File{ParentID: TrashFileID, Name: "trashed", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, &trashed)
	require.NoError(t, db.Create(&FileVersion{FileID: trashed.ID, Signature: []byte("x")}).Error)
	scope := IdenticalScope{Source: IdenticalLibrary}

	// The target component has the same group identity, fingerprint, and streamed membership as a full read.
	full, err := lib.OpenIdenticalSnapshot(ctx, scope)
	require.NoError(t, err)
	defer full.Close()
	component, err := lib.OpenIdenticalComponent(ctx, scope, files[2].ID)
	require.NoError(t, err)
	defer component.Close()
	groups, err := full.Groups("", 20)
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	group, err := component.Group(groups.Groups[0].ID)
	require.NoError(t, err)
	require.Equal(t, groups.Groups[0], *group)
	var staged int64
	require.NoError(t, component.db.Model(&identicalNode{}).Count(&staged).Error)
	require.Equal(t, int64(3), staged)
	var ids []int64
	require.NoError(t, component.WalkMembers(group.ID, func(members []IdenticalMember) error {
		for _, member := range members {
			ids = append(ids, member.FileID)
		}
		return nil
	}))
	require.Equal(t, []int64{files[0].ID, files[1].ID, files[2].ID}, ids)

	// A new peer and a removed bridge both change the target's current group fingerprint.
	added := File{Name: "added", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, &added)
	require.NoError(t, db.Create(&FileVersion{FileID: added.ID, Signature: []byte("z")}).Error)
	withAddition, err := lib.OpenIdenticalComponent(ctx, scope, files[2].ID)
	require.NoError(t, err)
	defer withAddition.Close()
	changed, err := withAddition.Group(group.ID)
	require.NoError(t, err)
	require.Equal(t, int64(4), changed.Count)
	require.NotEqual(t, group.Fingerprint, changed.Fingerprint)
	require.NoError(t, db.Where("file_id = ? AND signature = ?", files[2].ID, []byte("y")).Delete(&FileVersion{}).Error)
	withoutBridge, err := lib.OpenIdenticalComponent(ctx, scope, files[2].ID)
	require.NoError(t, err)
	defer withoutBridge.Close()
	current, err := withoutBridge.Groups("", 20)
	require.NoError(t, err)
	require.Len(t, current.Groups, 1)
	require.Equal(t, strconv.FormatInt(files[2].ID, 10), current.Groups[0].ID)
	require.NotEqual(t, group.Fingerprint, current.Groups[0].Fingerprint)
}

func TestIdenticalUnsignedOriginalChangesLibraryFingerprint(t *testing.T) {
	// Saved versions connect the Files, while the source's unsigned original remains operation identity.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	one, two := locationTestSource(t, lib), locationTestSource(t, lib)
	target, source, isolated := &File{Name: "target", Kind: entity.FileKind_FILE_KIND_REGULAR},
		&File{Name: "source", Kind: entity.FileKind_FILE_KIND_REGULAR},
		&File{Name: "isolated", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, target, source, isolated)
	for _, file := range []*File{target, source} {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("saved")}).Error)
	}
	original := FileLocation{FileID: source.ID, LocationID: one.ID, Path: "before/source"}
	require.NoError(t, db.Create(&original).Error)
	require.NoError(t, db.Create(&FileLocation{FileID: isolated.ID, LocationID: one.ID, Path: "isolated"}).Error)
	scope := IdenticalScope{Source: IdenticalLibrary}
	read := func() (*IdenticalSnapshot, *IdenticalSnapshot, IdenticalGroup) {
		full, err := lib.OpenIdenticalSnapshot(ctx, scope)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, full.Close()) })
		groups, err := full.Groups("", 20)
		require.NoError(t, err)
		require.Len(t, groups.Groups, 1)
		require.Equal(t, int64(2), groups.Groups[0].Count)
		component, err := lib.OpenIdenticalComponent(ctx, scope, target.ID)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, component.Close()) })
		current, err := component.Group(groups.Groups[0].ID)
		require.NoError(t, err)
		require.Equal(t, groups.Groups[0], *current)
		return full, component, groups.Groups[0]
	}

	// Both paths must retain the unsigned original without adding a content edge.
	full, component, initial := read()
	for _, snapshot := range []*IdenticalSnapshot{full, component} {
		members, err := snapshot.Members(initial.ID, "", 20)
		require.NoError(t, err)
		require.Len(t, members.Members, 2)
		require.Equal(t, source.ID, members.Members[1].FileID)
		require.NotNil(t, members.Members[1].Original)
		require.Empty(t, members.Members[1].Original.Signature)
		require.Equal(t, original.Path, members.Members[1].Original.Path)
	}
	locations, err := lib.OpenIdenticalSnapshot(ctx, IdenticalScope{Source: IdenticalLocations, Roots: []IdenticalRoot{{LocationID: one.ID}}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, locations.Close()) })
	locationGroups, err := locations.Groups("", 20)
	require.NoError(t, err)
	require.Empty(t, locationGroups.Groups)

	// Moving the unsigned original within a Location, then to another Location, invalidates the old token.
	original.Path = "after/source"
	require.NoError(t, db.Save(&original).Error)
	_, _, moved := read()
	require.NotEqual(t, initial.Fingerprint, moved.Fingerprint)
	original.LocationID = two.ID
	require.NoError(t, db.Save(&original).Error)
	_, _, relocated := read()
	require.NotEqual(t, moved.Fingerprint, relocated.Fingerprint)
}

func TestOpenIdenticalComponentLocationsScopeAndRemoval(t *testing.T) {
	// Location components use current signed originals only and respect selected roots and Trash paths.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	one, two, outside := locationTestSource(t, lib), locationTestSource(t, lib), locationTestSource(t, lib)
	files := make([]*File, 5)
	for i := range files {
		files[i] = &File{Name: fmt.Sprintf("file-%d", i), Kind: entity.FileKind_FILE_KIND_REGULAR}
	}
	createFileRows(t, db, files...)
	for i, location := range []int64{one.ID, two.ID, outside.ID, one.ID, one.ID} {
		path := files[i].Name
		if i == 4 {
			path = ".trash/file"
		}
		require.NoError(t, db.Create(&FileLocation{FileID: files[i].ID, LocationID: location,
			Path: path, Signature: []byte("same")}).Error)
	}
	scope := IdenticalScope{Source: IdenticalLocations, Roots: []IdenticalRoot{{LocationID: two.ID}, {LocationID: one.ID}, {LocationID: one.ID}}}
	full, err := lib.OpenIdenticalSnapshot(ctx, scope)
	require.NoError(t, err)
	defer full.Close()
	component, err := lib.OpenIdenticalComponent(ctx, scope, files[1].ID)
	require.NoError(t, err)
	defer component.Close()
	groups, err := full.Groups("", 20)
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	group, err := component.Group(groups.Groups[0].ID)
	require.NoError(t, err)
	require.Equal(t, groups.Groups[0], *group)
	require.Equal(t, int64(3), group.Count)
	var staged int64
	require.NoError(t, component.db.Model(&identicalNode{}).Count(&staged).Error)
	require.Equal(t, int64(3), staged)
	require.NoError(t, db.Delete(&FileLocation{}, files[1].ID).Error)
	withoutMember, err := lib.OpenIdenticalComponent(ctx, scope, files[0].ID)
	require.NoError(t, err)
	defer withoutMember.Close()
	changed, err := withoutMember.Group(group.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), changed.Count)
	require.NotEqual(t, group.Fingerprint, changed.Fingerprint)
	require.NoError(t, db.Delete(&FileLocation{}, files[3].ID).Error)
	dissolved, err := lib.OpenIdenticalComponent(ctx, scope, files[0].ID)
	require.NoError(t, err)
	defer dissolved.Close()
	_, err = dissolved.Group(group.ID)
	require.Error(t, err)
}

func TestOpenIdenticalComponentLargeFrontierAndDetachedContext(t *testing.T) {
	// A popular signature is paged from the source, while the retained result remains readable after request cancellation.
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	files := make([]*File, 550)
	for i := range files {
		files[i] = &File{Name: fmt.Sprintf("large-%04d", i), Kind: entity.FileKind_FILE_KIND_REGULAR}
	}
	createFileRows(t, db, files...)
	originals := make([]FileLocation, 0, len(files))
	for _, file := range files {
		originals = append(originals, FileLocation{FileID: file.ID, LocationID: location.ID,
			Path: file.Name, Signature: []byte("shared")})
	}
	require.NoError(t, db.CreateInBatches(&originals, identicalBatch).Error)
	ctx, cancel := context.WithCancel(context.Background())
	snapshot, err := lib.OpenIdenticalComponent(ctx,
		IdenticalScope{Source: IdenticalLocations, Roots: []IdenticalRoot{{LocationID: location.ID}}}, files[549].ID)
	require.NoError(t, err)
	defer snapshot.Close()
	cancel()
	groups, err := snapshot.Groups("", 20)
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	require.Equal(t, int64(550), groups.Groups[0].Count)
	count := 0
	require.NoError(t, snapshot.WalkMembers(groups.Groups[0].ID, func(members []IdenticalMember) error {
		require.LessOrEqual(t, len(members), identicalBatch)
		count += len(members)
		return nil
	}))
	require.Equal(t, 550, count)
}
