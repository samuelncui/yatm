package library

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestLibraryJSONLRoundTrip(t *testing.T) {
	// Isolate the export and import sides of the round trip.
	ctx := context.Background()
	source := newJSONLTestLibrary(t)
	target := newJSONLTestLibrary(t)

	// Seed more than one batch across every exported entity type.
	hash := []byte("01234567890123456789012345678901")
	files := make([]*File, 0, batchSize+1)
	for index := 1; index <= batchSize+1; index++ {
		files = append(files, &File{
			ID: int64(index), Name: fmt.Sprintf("file-%03d", index), Mode: 0o644, Size: int64(index), Hash: hash,
		})
	}
	files[0].Note = "keep this note"
	trash := &File{
		ID: TrashFileID, Name: ".Trash", Kind: entity.FileKind_FILE_KIND_DIRECTORY, Note: "retained trash metadata",
	}
	files = append(files, trash)
	recordedAt := time.Unix(100, 0).UTC()
	order := make([]byte, 17)
	order[0] = 'b'
	binary.BigEndian.PutUint64(order[1:9], 7)
	require.NoError(t, source.db.CreateInBatches(files, batchSize).Error)
	require.NoError(t, source.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{
		AddTags: []string{"archive", "favorite"},
	}))
	require.NoError(t, source.EditFileMetadata(ctx, []int64{TrashFileID}, FileMetadataEdit{
		AddTags: []string{"system"},
	}))
	require.NoError(t, source.db.Create(&Media{
		ID: 7, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC007",
		Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack(), CreateTime: recordedAt,
	}).Error)
	require.NoError(t, source.db.Create(&Position{
		ID: 9, MediaID: 7, Path: "file-001", Mode: 0o644, Size: 1, Hash: hash, WriteTime: recordedAt,
		StorageOrder: order,
		StorageMetadata: (&entity.LTFSMetadata{Extents: []*entity.LTFSExtent{{
			Partition: "b", StartBlock: 7, ByteCount: 1,
		}}}).Pack(),
	}).Error)
	require.NoError(t, source.db.Create(&Position{
		ID: 10, MediaID: 7, Path: "directory/", IsDir: true, StorageOrder: []byte{},
	}).Error)
	require.NoError(t, source.db.Create(&Position{
		ID: 11, MediaID: 7, Path: "directory/file-002", Mode: 0o644, Size: 2,
		Hash: hash, WriteTime: recordedAt, StorageOrder: order,
	}).Error)

	// Stream the snapshot and verify its stable framing and entity order.
	var snapshot bytes.Buffer
	require.NoError(t, source.Export(ctx, &snapshot, []entity.LibraryEntityType{
		entity.LibraryEntityType_FILE,
		entity.LibraryEntityType_MEDIA,
		entity.LibraryEntityType_POSITION,
		entity.LibraryEntityType_FILE,
	}))
	lines := bytes.Split(bytes.TrimSpace(snapshot.Bytes()), []byte("\n"))
	header := new(jsonlInputRecord)
	require.NoError(t, json.Unmarshal(lines[0], header))
	require.Equal(t, "yatm-library-backup", header.Format)
	require.Equal(t, jsonlFormatVersion, header.Version)
	require.Equal(t, []string{recordTypeMedia, recordTypeFile, recordTypeFileVersion, recordTypeFileVersionArchive, recordTypePosition}, header.Entities)
	end := new(jsonlInputRecord)
	require.NoError(t, json.Unmarshal(lines[len(lines)-1], end))
	require.Equal(t, recordTypeEnd, end.Type)

	// Import the complete stream into an empty Library.
	require.NoError(t, target.Import(ctx, bytes.NewReader(snapshot.Bytes())))

	// Verify every entity crossed the JSON Lines boundary with stable IDs and relationships.
	var fileCount, mediaCount, positionCount int64
	require.NoError(t, target.db.Model(ModelFile).Count(&fileCount).Error)
	require.NoError(t, target.db.Model(ModelMedia).Count(&mediaCount).Error)
	require.NoError(t, target.db.Model(ModelPosition).Count(&positionCount).Error)
	require.Equal(t, int64(batchSize+2), fileCount)
	importedFile := new(File)
	require.NoError(t, target.db.First(importedFile, 1).Error)
	require.Equal(t, "keep this note", importedFile.Note)
	fileTags, err := target.MGetFileTags(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"archive", "favorite"}, fileTags[1])
	importedTrash := new(File)
	require.NoError(t, target.db.Where("id = ?", TrashFileID).First(importedTrash).Error)
	require.Equal(t, "retained trash metadata", importedTrash.Note)
	trashTags, err := target.MGetFileTags(ctx, TrashFileID)
	require.NoError(t, err)
	require.Equal(t, []string{"system"}, trashTags[TrashFileID])
	require.Equal(t, int64(1), mediaCount)
	importedMedia := new(Media)
	require.NoError(t, target.db.First(importedMedia, 7).Error)
	require.Equal(t, TapeFormatLTFSV1, importedMedia.Profile.GetTape().Format)
	require.Equal(t, int64(3), positionCount)

	position := new(Position)
	require.NoError(t, target.db.First(position, 9).Error)
	require.False(t, target.db.Migrator().HasColumn(&Position{}, "file_id"))
	require.Equal(t, int64(7), position.MediaID)
	require.Equal(t, "file-001", position.Path)
	require.Equal(t, hash, position.Hash)
	require.Equal(t, order, position.StorageOrder)
	require.Equal(t, uint64(7), position.StorageMetadata.GetLtfs().Extents[0].StartBlock)
	directory := new(Position)
	require.NoError(t, target.db.Where("path = ?", "directory/").First(directory).Error)
	require.True(t, directory.IsDir)
	require.Empty(t, directory.StorageOrder)
}

func TestLibraryRejectsPreviousDraftFormatsWithoutMutation(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4, 5} {
		lib := newJSONLTestLibrary(t)
		require.NoError(t, lib.db.Create(&File{ID: 7, Name: "kept"}).Error)
		snapshot := fmt.Sprintf("{\"type\":\"header\",\"format\":\"yatm-library\",\"version\":%d,\"entities\":[\"file\"]}\n{\"type\":\"end\"}\n", version)
		require.ErrorContains(t, lib.Import(context.Background(), strings.NewReader(snapshot)), "invalid Library header")
		kept, err := lib.GetFile(context.Background(), 7)
		require.NoError(t, err)
		require.Equal(t, "kept", kept.Name)
	}
}

func TestLibraryJSONLImportRollsBackTruncatedSnapshot(t *testing.T) {
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)

	// Preserve an existing row that would be deleted before the truncated stream is detected.
	require.NoError(t, lib.db.Create(&File{ID: 1, Name: "existing", Mode: 0o644}).Error)
	snapshot := bytes.NewBufferString(
		"{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1,\"entities\":[\"file\"]}\n" +
			"{\"type\":\"file\",\"data\":{\"id\":2,\"name\":\"replacement\",\"mode\":420}}\n",
	)

	// Reject the missing end marker and roll back the preceding delete and insert.
	err := lib.Import(ctx, snapshot)
	require.ErrorContains(t, err, "missing Library end record")

	files := make([]*File, 0, 1)
	require.NoError(t, lib.db.Order("id ASC").Find(&files).Error)
	require.Len(t, files, 1)
	require.Equal(t, int64(1), files[0].ID)
	require.Equal(t, "existing", files[0].Name)
}

func TestLibraryJSONLImportClearsOnlyDeclaredEntities(t *testing.T) {
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)

	// Seed two entity types, then import an explicitly empty File snapshot.
	require.NoError(t, lib.db.Create(&File{ID: 1, Name: "file", Mode: 0o644}).Error)
	require.NoError(t, lib.db.Create(&Media{
		ID: 1, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001",
		Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV0}).Pack(),
	}).Error)
	snapshot := bytes.NewBufferString(
		"{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1,\"entities\":[\"file\"]}\n" +
			"{\"type\":\"end\"}\n",
	)
	require.NoError(t, lib.Import(ctx, snapshot))

	// The header distinguishes an empty selected table from an entity absent from the snapshot.
	var fileCount, mediaCount int64
	require.NoError(t, lib.db.Model(ModelFile).Count(&fileCount).Error)
	require.NoError(t, lib.db.Model(ModelMedia).Count(&mediaCount).Error)
	require.Zero(t, fileCount)
	require.Equal(t, int64(1), mediaCount)
}

func TestLibraryImportsLegacyJSONBackupAndReindexesPositions(t *testing.T) {
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	recordedAt := time.Unix(100, 0).UTC()
	require.NoError(t, lib.db.Create(&File{ID: 99, Name: "replaced", Mode: 0o644}).Error)

	// legacy exported one JSON object containing whole-table arrays and no Position parent index.
	backup, err := json.Marshal(map[string]any{
		"files": []*legacyLibraryFile{{ID: 1, Name: "file.txt", Mode: 0o644, Size: 7}},
		"tapes": []*legacyLibraryTape{{ID: 2, Barcode: "ABC002", CreateTime: recordedAt}},
		"positions": []*legacyLibraryPosition{
			{ID: 3, FileID: 1, TapeID: 2, Path: "directory/file.txt", Mode: 0o644, Size: 7},
			{ID: 4, FileID: 1, TapeID: 2, Path: "root-copy.txt", Mode: 0o644, Size: 7},
		},
	})
	require.NoError(t, err)
	require.NoError(t, lib.Import(ctx, bytes.NewReader(backup)))

	// Import preserves legacy identities and materializes directly queryable directory rows.
	var files []*File
	require.NoError(t, lib.db.Order("id").Find(&files).Error)
	require.Len(t, files, 1)
	require.Equal(t, int64(1), files[0].ID)
	root, err := lib.ListPositions(ctx, 2, "")
	require.NoError(t, err)
	require.Len(t, root, 2)
	require.Equal(t, "directory/", root[0].Path)
	require.True(t, root[0].IsDir)
	require.Equal(t, int64(7), root[0].Size)
	require.Equal(t, "root-copy.txt", root[1].Path)
	children, err := lib.ListPositions(ctx, 2, "directory/")
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, int64(3), children[0].ID)
	require.Equal(t, "directory/file.txt", children[0].Path)
	media := new(Media)
	require.NoError(t, lib.db.First(media, 2).Error)
	require.Equal(t, TapeFormatLTFSV0, media.Profile.GetTape().Format)
}

func TestLibraryLegacyImportConvertsDirectoryModeAtTheBoundary(t *testing.T) {
	// legacy has no Kind field; directories and Trash are represented by their legacy mode.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	backup, err := json.Marshal(map[string]any{"files": []*legacyLibraryFile{
		{ID: TrashFileID, Name: ".Trash", Mode: uint32(fs.ModeDir | 0o755)},
		{ID: 9, Name: "documents", Mode: uint32(fs.ModeDir | 0o700)},
		{ID: 10, ParentID: 9, Name: "document.txt", Mode: 0o644},
	}})
	require.NoError(t, err)
	require.NoError(t, lib.Import(ctx, bytes.NewReader(backup)))

	// The current catalog persists explicit types and keeps the original hierarchy.
	files, err := lib.MGetFile(ctx, TrashFileID, 9, 10)
	require.NoError(t, err)
	require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, files[TrashFileID].Kind)
	require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, files[9].Kind)
	require.Equal(t, entity.FileKind_FILE_KIND_REGULAR, files[10].Kind)
	require.EqualValues(t, 9, files[10].ParentID)
	child, err := lib.MkdirAll(ctx, 9, "nested", 0o755)
	require.NoError(t, err)
	require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, child.Kind)
}

func TestLibraryLegacyJSONImportValidatesBeforeReplacingData(t *testing.T) {
	// Preserve an existing row behind a syntactically invalid replacement.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	require.NoError(t, lib.db.Create(&File{ID: 1, Name: "existing.txt", Mode: 0o644}).Error)
	backup := `{"files":[{"id":2,"name":"replacement.txt","mode":420}]} {}`

	// Reject trailing JSON before the replacement transaction starts.
	err := lib.Import(ctx, strings.NewReader(backup))
	require.ErrorContains(t, err, "unexpected trailing data")
	files := make([]*File, 0, 1)
	require.NoError(t, lib.db.Order("id").Find(&files).Error)
	require.Len(t, files, 1)
	require.Equal(t, "existing.txt", files[0].Name)
}

func TestLibraryLegacyJSONImportRollsBackInvalidPositionIndex(t *testing.T) {
	ctx := context.Background()
	_, lib := newTestLibrary(t)

	// Seed one complete Library that must survive a rejected replacement snapshot.
	hash := []byte("01234567890123456789012345678901")
	recordedAt := time.Unix(100, 0).UTC()
	require.NoError(t, lib.db.Create(&File{ID: 1, Name: "existing.txt", Mode: 0o644, Hash: hash}).Error)
	require.NoError(t, lib.db.Create(&Media{
		ID: 1, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001",
		Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV0}).Pack(), CreateTime: recordedAt,
	}).Error)
	require.NoError(t, lib.db.Create(&Position{
		ID: 1, MediaID: 1, Path: "existing.txt", Hash: hash,
	}).Error)

	// Import a syntactically valid legacy snapshot whose Position cannot be indexed safely.
	backup, err := json.Marshal(map[string]any{
		"files":     []*legacyLibraryFile{{ID: 2, Name: "replacement.txt", Mode: 0o644}},
		"tapes":     []*legacyLibraryTape{{ID: 2, Barcode: "ABC002", CreateTime: recordedAt}},
		"positions": []*legacyLibraryPosition{{ID: 2, FileID: 2, TapeID: 2, Path: "../replacement.txt"}},
	})
	require.NoError(t, err)
	err = lib.Import(ctx, bytes.NewReader(backup))
	require.ErrorContains(t, err, "path")

	// The import transaction must restore every original entity after reindexing fails.
	var files []*File
	require.NoError(t, lib.db.Order("id").Find(&files).Error)
	require.Len(t, files, 1)
	require.Equal(t, "existing.txt", files[0].Name)
	var media []*Media
	require.NoError(t, lib.db.Order("id").Find(&media).Error)
	require.Len(t, media, 1)
	require.Equal(t, "ABC001", media[0].Identity)
	positions, err := lib.ListPositions(ctx, 1, "")
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "existing.txt", positions[0].Path)
}

func TestLibraryAutoMigrateRejectsLegacyPositionSchema(t *testing.T) {
	db := openTestLibraryDB(t)
	require.NoError(t, db.Exec("CREATE TABLE positions (id INTEGER PRIMARY KEY, tape_id INTEGER, path TEXT)").Error)

	err := New(db).AutoMigrate()
	require.ErrorContains(t, err, "yatm-migrate")
}

func TestLibraryJSONLImportRejectsInvalidSnapshots(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		want     string
	}{
		{name: "empty", want: "detect library import failed"},
		{name: "malformed header", snapshot: "{\n", want: "detect library import failed"},
		{
			name:     "wrong header type",
			snapshot: "{\"type\":\"file\",\"format\":\"yatm-library-backup\",\"version\":1}\n",
			want:     "invalid Library header",
		},
		{
			name:     "wrong format",
			snapshot: "{\"type\":\"header\",\"format\":\"other\",\"version\":1}\n",
			want:     "invalid Library header",
		},
		{
			name:     "wrong version",
			snapshot: "{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":2}\n",
			want:     "unsupported Library backup revision",
		},
		{
			name: "unknown entity",
			snapshot: "{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1," +
				"\"entities\":[\"unknown\"]}\n",
			want: "unknown Library entity",
		},
		{
			name: "duplicate entity",
			snapshot: "{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1," +
				"\"entities\":[\"file\",\"file\"]}\n",
			want: "duplicate Library entity",
		},
		{
			name: "undeclared record",
			snapshot: "{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1}\n" +
				"{\"type\":\"file\",\"data\":{\"id\":2}}\n{\"type\":\"end\"}\n",
			want: "undeclared Library record type",
		},
		{
			name: "invalid record",
			snapshot: "{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1," +
				"\"entities\":[\"file\"]}\n" +
				"{\"type\":\"file\",\"data\":{\"id\":\"invalid\"}}\n{\"type\":\"end\"}\n",
			want: "cannot unmarshal",
		},
		{
			name: "invalid File metadata",
			snapshot: "{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1," +
				"\"entities\":[\"file\"]}\n" +
				"{\"type\":\"file\",\"data\":{\"id\":2,\"name\":\"bad\",\"tags\":[\"\"]}}\n{\"type\":\"end\"}\n",
			want: "Tag must not be empty",
		},
		{
			name: "duplicate record",
			snapshot: "{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1," +
				"\"entities\":[\"file\"]}\n" +
				"{\"type\":\"file\",\"data\":{\"id\":2,\"name\":\"a\"}}\n" +
				"{\"type\":\"file\",\"data\":{\"id\":2,\"name\":\"b\"}}\n{\"type\":\"end\"}\n",
			want: "insert library file records failed",
		},
		{
			name: "trailing record",
			snapshot: "{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1}\n" +
				"{\"type\":\"end\"}\n{\"type\":\"end\"}\n",
			want: "unexpected data after library end record",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Seed data whose preservation proves all post-header failures roll back.
			lib := newJSONLTestLibrary(t)
			require.NoError(t, lib.db.Create(&File{ID: 1, Name: "existing", Mode: 0o644}).Error)
			require.NoError(t, lib.db.Create(&FileTag{FileID: 1, Tag: "existing"}).Error)

			// Reject the malformed snapshot at its semantic boundary.
			err := lib.Import(context.Background(), strings.NewReader(test.snapshot))
			require.ErrorContains(t, err, test.want)

			// Keep the original Library unchanged after every rejected import.
			files := make([]*File, 0, 1)
			require.NoError(t, lib.db.Order("id ASC").Find(&files).Error)
			require.Len(t, files, 1)
			require.Equal(t, int64(1), files[0].ID)
			require.Equal(t, "existing", files[0].Name)
			var tagCount int64
			require.NoError(t, lib.db.Model(ModelFileTag).Where("file_id = ?", 1).Count(&tagCount).Error)
			require.Equal(t, int64(1), tagCount)
		})
	}
}

func TestLibraryJSONLImportBoundsRecordSize(t *testing.T) {
	lib := newJSONLTestLibrary(t)

	// Place an oversized second record after a valid header to enter the transactional path.
	snapshot := strings.NewReader(
		"{\"type\":\"header\",\"format\":\"yatm-library-backup\",\"version\":1,\"entities\":[\"file\"]}\n" +
			strings.Repeat("x", maxJSONLRecordSize+1) + "\n",
	)

	// Bound malformed input independently of the total snapshot size.
	err := lib.Import(context.Background(), snapshot)
	require.ErrorContains(t, err, "token too long")
}

func TestLibraryJSONLExportPropagatesWriterFailure(t *testing.T) {
	lib := newJSONLTestLibrary(t)
	require.NoError(t, lib.db.Create(&Media{
		ID: 1, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001",
		Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV0}).Pack(),
	}).Error)

	// Allow the header write and fail while emitting the first selected row.
	writerErr := errors.New("writer failed")
	writer := &failAfterWriter{remainingWrites: 1, err: writerErr}
	err := lib.Export(context.Background(), writer, []entity.LibraryEntityType{entity.LibraryEntityType_MEDIA})
	require.ErrorIs(t, err, writerErr)
	require.ErrorContains(t, err, "encode library media record failed")
}

func newJSONLTestLibrary(t *testing.T) *Library {
	t.Helper()
	_, lib := newTestLibrary(t)
	return lib
}

type failAfterWriter struct {
	remainingWrites int
	err             error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.remainingWrites == 0 {
		return 0, w.err
	}
	w.remainingWrites--
	return len(p), nil
}
