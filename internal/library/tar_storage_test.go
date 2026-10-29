package library

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func tarTestMedia() *Media {
	return &Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "TAR001", Profile: (&entity.TapeMediaProfile{
		Format: TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{InitializationId: "11111111-1111-4111-8111-111111111111",
			RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 262144},
	}).Pack()}
}

func TestTarProfileAndLocatorJSONLRoundTrip(t *testing.T) {
	// Commit real typed BLOBs, including a zero-length member with paired physical anchors.
	ctx := context.Background()
	_, source := newTestLibrary(t)
	_, target := newTestLibrary(t)
	locator := &entity.TarMetadata{TapeFile: 2, HeaderOffsetBytes: 1024,
		Header: &entity.TapeBytePosition{LogicalObject: 3, OffsetBytes: 1024},
		End:    &entity.TapeBytePosition{LogicalObject: 4}}
	metadata := locator.Pack()
	media, err := source.CommitMedia(ctx, tarTestMedia(), mediaFileSource(&MediaFile{
		Path: "archives/0000000000000002/empty.txt", Mode: 0640, StorageOrder: entity.TarStorageOrder(locator), StorageMetadata: metadata,
	}))
	require.NoError(t, err)
	var backup bytes.Buffer
	require.NoError(t, source.Export(ctx, &backup, []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_POSITION}))

	// JSONL keeps the immutable cartridge and locator, rather than reconstructing physical placement.
	require.NoError(t, target.Import(ctx, &backup, false))
	restored, err := target.GetMedia(ctx, media.ID)
	require.NoError(t, err)
	require.True(t, proto.Equal(media.Profile, restored.Profile))
	var position Position
	require.NoError(t, target.db.Where("media_id = ? AND is_dir = ?", media.ID, false).First(&position).Error)
	require.True(t, proto.Equal(metadata, position.StorageMetadata))
	require.Equal(t, entity.TarStorageOrder(locator), position.StorageOrder)
	require.Equal(t, uint64(2), binary.BigEndian.Uint64(position.StorageOrder))
	require.Equal(t, uint64(1024), binary.BigEndian.Uint64(position.StorageOrder[8:]))
}

func TestLibraryJSONLImportsPositionsBeforeMedia(t *testing.T) {
	for _, format := range []string{TapeFormatLTFSV1, TapeFormatTarPAXV1} {
		t.Run(format, func(t *testing.T) {
			// Export ordinary typed inventory spanning more than one validation page.
			ctx := context.Background()
			_, source := newTestLibrary(t)
			_, target := newTestLibrary(t)
			media := tarTestMedia()
			media.Profile.GetTape().Format = format
			if format == TapeFormatLTFSV1 {
				media.Profile.GetTape().Tar = nil
			}
			stored, err := source.CommitMedia(ctx, media, func(_ context.Context, yield func(*MediaFile) error) error {
				for i := range batchSize + 1 {
					locator := &entity.TarMetadata{TapeFile: 1, HeaderOffsetBytes: uint64(i) * 512}
					file := &MediaFile{Path: fmt.Sprintf("archives/0000000000000001/file-%03d", i), Mode: 0644}
					if format == TapeFormatTarPAXV1 {
						file.StorageOrder, file.StorageMetadata = entity.TarStorageOrder(locator), locator.Pack()
					}
					if err := yield(file); err != nil {
						return err
					}
				}
				return nil
			})
			require.NoError(t, err)
			var backup bytes.Buffer
			require.NoError(t, source.Export(ctx, &backup, []entity.LibraryEntityType{
				entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_POSITION,
			}))

			// Keep framing intact while moving every Position ahead of its Media owner.
			lines := bytes.Split(bytes.TrimSpace(backup.Bytes()), []byte("\n"))
			slices.Reverse(lines[1 : len(lines)-1])
			require.NoError(t, target.Import(ctx, bytes.NewReader(bytes.Join(lines, []byte("\n"))), false))
			positions, err := target.ListMediaFilePositions(ctx, stored.ID, "", batchSize+2)
			require.NoError(t, err)
			require.Len(t, positions, batchSize+1)
			if format == TapeFormatTarPAXV1 {
				require.EqualValues(t, 1, positions[0].StorageMetadata.GetTar().TapeFile)
			}
		})
	}
}

func TestTarLocatorRejectsMalformedPublication(t *testing.T) {
	for name, mutate := range map[string]func(*Media, *MediaFile){
		"label namespace":  func(_ *Media, f *MediaFile) { f.StorageMetadata.GetTar().TapeFile = 0 },
		"header alignment": func(_ *Media, f *MediaFile) { f.StorageMetadata.GetTar().HeaderOffsetBytes = 1 },
		"order mismatch":   func(_ *Media, f *MediaFile) { f.StorageOrder[0] = 1 },
		"one anchor":       func(_ *Media, f *MediaFile) { f.StorageMetadata.GetTar().Header = &entity.TapeBytePosition{} },
		"record bound": func(_ *Media, f *MediaFile) {
			f.StorageMetadata.GetTar().Header = &entity.TapeBytePosition{OffsetBytes: 262144}
			f.StorageMetadata.GetTar().End = &entity.TapeBytePosition{LogicalObject: 1}
		},
		"address order": func(_ *Media, f *MediaFile) {
			f.StorageMetadata.GetTar().Header = &entity.TapeBytePosition{LogicalObject: 2}
			f.StorageMetadata.GetTar().End = &entity.TapeBytePosition{LogicalObject: 1}
		},
		"nonzero partition": func(_ *Media, f *MediaFile) {
			f.StorageMetadata.GetTar().Header = &entity.TapeBytePosition{Partition: 1, LogicalObject: 2}
			f.StorageMetadata.GetTar().End = &entity.TapeBytePosition{Partition: 1, LogicalObject: 3}
		},
		"containing record underflow": func(_ *Media, f *MediaFile) {
			f.StorageMetadata.GetTar().Header = &entity.TapeBytePosition{LogicalObject: 2, OffsetBytes: 512}
			f.StorageMetadata.GetTar().End = &entity.TapeBytePosition{LogicalObject: 3}
		},
		"wrong backend":    func(_ *Media, f *MediaFile) { f.StorageMetadata = (&entity.LtfsMetadata{}).Pack() },
		"invalid profile":  func(m *Media, _ *MediaFile) { m.Profile.GetTape().Tar.InitializationId = "invalid" },
		"LTFS tar profile": func(m *Media, _ *MediaFile) { m.Profile.GetTape().Format = TapeFormatLTFSV1 },
	} {
		t.Run(name, func(t *testing.T) {
			// A malformed locator must not register Media or publish any Position.
			db, lib := newTestLibrary(t)
			media := tarTestMedia()
			locator := &entity.TarMetadata{TapeFile: 1}
			file := &MediaFile{Path: "archives/0000000000000001/member", Mode: 0644, StorageOrder: entity.TarStorageOrder(locator), StorageMetadata: locator.Pack()}
			mutate(media, file)
			_, err := lib.CommitMedia(context.Background(), media, mediaFileSource(file))
			require.Error(t, err)
			var count int64
			require.NoError(t, db.Model(&Media{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestTarJSONLRejectsMalformedLocatorAtomically(t *testing.T) {
	// Export can preserve corrupt retained evidence, but import must validate its format combination.
	ctx := context.Background()
	source := newJSONLTestLibrary(t)
	target := newJSONLTestLibrary(t)
	media, err := source.CommitMedia(ctx, tarTestMedia(), func(_ context.Context, yield func(*MediaFile) error) error {
		for i := range batchSize + 1 {
			metadata := &entity.TarMetadata{TapeFile: 1, HeaderOffsetBytes: uint64(i) * 512}
			if err := yield(&MediaFile{
				Path: fmt.Sprintf("archives/0000000000000001/file-%03d", i), Mode: 0644,
				StorageOrder: entity.TarStorageOrder(metadata), StorageMetadata: metadata.Pack(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)

	// A corrupt member beyond the first validation page must roll back earlier valid records too.
	require.NoError(t, source.db.Model(&Position{}).
		Where("media_id = ? AND path = ?", media.ID, fmt.Sprintf("archives/0000000000000001/file-%03d", batchSize)).
		Update("storage_metadata", (&entity.TarMetadata{TapeFile: 2}).Pack()).Error)
	var backup bytes.Buffer
	require.NoError(t, source.Export(ctx, &backup, []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_POSITION}))

	// A mismatched member namespace rolls back Media as well as Position publication.
	require.Error(t, target.Import(ctx, &backup, false))
	var count int64
	require.NoError(t, target.db.Model(&Media{}).Count(&count).Error)
	require.Zero(t, count)
}
