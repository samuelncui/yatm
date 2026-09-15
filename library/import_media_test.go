package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestMediaOnlyImportRejectsRebindingRetainedPositions(t *testing.T) {
	// A retained physical copy belongs to the original Media identity, not merely its integer ID.
	ctx := context.Background()
	target := newJSONLTestLibrary(t)
	_, err := target.CreateMedia(ctx, &Media{ID: 1, Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Identity: "ORIGINAL", Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack()})
	require.NoError(t, err)
	require.NoError(t, target.SavePosition(ctx, &Position{ID: 1, MediaID: 1, Path: "saved.txt",
		Signature: []byte("saved-content"), Hash: bytes.Repeat([]byte{1}, 32), Size: 10, Mode: 0o644}))

	// A valid Media-only snapshot from another catalog happens to reuse that numeric ID.
	source := newJSONLTestLibrary(t)
	_, err = source.CreateMedia(ctx, &Media{ID: 1, Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Identity: "DIFFERENT", Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack()})
	require.NoError(t, err)
	var snapshot bytes.Buffer
	require.NoError(t, source.Export(ctx, &snapshot, []entity.LibraryEntityType{entity.LibraryEntityType_MEDIA}))

	// Reject the replacement atomically instead of claiming that the copy exists on different media.
	require.Error(t, target.Import(ctx, bytes.NewReader(snapshot.Bytes())))
	media, err := target.GetMedia(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "ORIGINAL", media.Identity)
	position, err := target.GetPosition(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), position.MediaID)
}

func TestMediaOnlyImportValidatesAllRetainedOwners(t *testing.T) {
	// Exercise metadata refresh and every immutable-owner boundary through the actual export format.
	for _, test := range []struct {
		name         string
		change       func(*Media)
		omitLast     bool
		wantConflict bool
	}{
		{name: "metadata refresh", change: func(value *Media) { value.Name, value.CapacityBytes = "Updated", 2048 }},
		{name: "profile changes", change: func(value *Media) { value.Profile = (&entity.TapeMediaProfile{Format: TapeFormatLTFSV0}).Pack() }, wantConflict: true},
		{name: "identity changes", change: func(value *Media) { value.Identity = "DIFFERENT" }, wantConflict: true},
		{name: "owner moves to another ID", change: func(value *Media) { value.ID += 1000 }, wantConflict: true},
		{name: "owner omitted", omitLast: true, wantConflict: true},
		{name: "Media kind changes", change: func(value *Media) {
			value.Kind, value.Identity = entity.MediaKind_MEDIA_KIND_VOLUME, "c22e342a-e679-47e8-a48a-b250e4e96661"
			value.Profile = (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()
		}, wantConflict: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Put the changed owner beyond the first page and retain separate File metadata for rollback checks.
			ctx := context.Background()
			target, source := newJSONLTestLibrary(t), newJSONLTestLibrary(t)
			require.NoError(t, target.SaveFile(ctx, &File{ID: 1, Name: "kept"}))
			require.NoError(t, source.SaveFile(ctx, &File{ID: 1, Name: "replacement"}))
			for index := 1; index <= batchSize+1; index++ {
				owner := &Media{ID: int64(index), Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: fmt.Sprintf("TAPE%03d", index),
					Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack()}
				_, err := target.CreateMedia(ctx, owner)
				require.NoError(t, err)
				require.NoError(t, target.SavePosition(ctx, &Position{ID: int64(index), MediaID: owner.ID, Path: "saved.txt"}))
				if index == batchSize+1 && test.omitLast {
					continue
				}
				if index == batchSize+1 && test.change != nil {
					test.change(owner)
				}
				_, err = source.CreateMedia(ctx, owner)
				require.NoError(t, err)
			}
			var snapshot bytes.Buffer
			require.NoError(t, source.Export(ctx, &snapshot, []entity.LibraryEntityType{entity.LibraryEntityType_MEDIA, entity.LibraryEntityType_FILE}))

			// Validate the whole replacement before exposing either imported Media or File organization.
			err := target.Import(ctx, bytes.NewReader(snapshot.Bytes()))
			if test.wantConflict {
				require.ErrorContains(t, err, "Media-only import")
				file, err := target.GetFile(ctx, 1)
				require.NoError(t, err)
				require.Equal(t, "kept", file.Name)
				owner, err := target.GetMedia(ctx, int64(batchSize+1))
				require.NoError(t, err)
				require.Equal(t, fmt.Sprintf("TAPE%03d", batchSize+1), owner.Identity)
				require.Equal(t, TapeFormatLTFSV1, owner.Profile.GetTape().Format)
				return
			}
			require.NoError(t, err)
			owner, err := target.GetMedia(ctx, int64(batchSize+1))
			require.NoError(t, err)
			require.Equal(t, "Updated", owner.Name)
			require.Equal(t, int64(2048), owner.CapacityBytes)
			position, err := target.GetPosition(ctx, int64(batchSize+1))
			require.NoError(t, err)
			require.Equal(t, owner.ID, position.MediaID)
		})
	}
}

func TestLegacyTapeOnlyImportPreservesRetainedOwners(t *testing.T) {
	// legacy partial backups use the same owner guard rather than trusting reused Tape IDs.
	for _, test := range []struct {
		name   string
		change func(*legacyLibraryTape)
		fail   bool
	}{
		{name: "metadata refresh", change: func(value *legacyLibraryTape) { value.Name = "Updated" }},
		{name: "different barcode", change: func(value *legacyLibraryTape) { value.Barcode = "DIFFERENT" }, fail: true},
		{name: "different encryption", change: func(value *legacyLibraryTape) { value.Encryption = "changed" }, fail: true},
		{name: "different ID", change: func(value *legacyLibraryTape) { value.ID = 2 }, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Preserve one known legacy-format copy while replacing only its Tape metadata.
			ctx := context.Background()
			lib := newJSONLTestLibrary(t)
			_, err := lib.CreateMedia(ctx, &Media{ID: 1, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ORIGINAL",
				Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV0}).Pack()})
			require.NoError(t, err)
			require.NoError(t, lib.SavePosition(ctx, &Position{ID: 1, MediaID: 1, Path: "saved.txt"}))
			tape := &legacyLibraryTape{ID: 1, Barcode: "ORIGINAL"}
			test.change(tape)
			snapshot, err := json.Marshal(map[string]any{"tapes": []*legacyLibraryTape{tape}})
			require.NoError(t, err)

			// Reject immutable changes, while accepting ordinary legacy metadata refreshes.
			err = lib.Import(ctx, bytes.NewReader(snapshot))
			if test.fail {
				require.ErrorContains(t, err, "Media-only import")
			} else {
				require.NoError(t, err)
			}
			owner, err := lib.GetMedia(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, "ORIGINAL", owner.Identity)
			require.Empty(t, owner.Profile.GetTape().Encryption)
			if !test.fail {
				require.Equal(t, "Updated", owner.Name)
			}
		})
	}
}
