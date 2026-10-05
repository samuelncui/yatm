package apis

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func TestVersionMetadataSurvivesCorruptPreview(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	file := &library.File{Name: "photo.jpg", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, file))
	version := &library.FileVersion{FileID: file.ID, Signature: []byte("opaque"), Hash: make([]byte, 32), Size: 42, Mode: 0644}
	require.NoError(t, db.Create(version).Error)
	previews, err := preview.New(preview.Config{}, root)
	require.NoError(t, err)

	// A damaged derivative must not hide the independently saved version's metadata.
	signature, err := library.NewFileSignature(version.Hash, version.Size)
	require.NoError(t, err)
	encoded := hex.EncodeToString(signature)
	bundle := filepath.Join(previews.StorageRoot(), encoded[:2], encoded[2:4], encoded[4:6], encoded[6:]+".zip")
	require.NoError(t, os.MkdirAll(filepath.Dir(bundle), 0755))
	require.NoError(t, os.WriteFile(bundle, []byte("invalid ZIP"), 0644))
	_, err = previews.Manifest(signature)
	require.Error(t, err)
	exe := executor.New(db, lib, nil, executor.Paths{Work: root}, executor.Scripts{}, previews)
	service := &filesService{api: New(lib, exe)}
	reply, err := service.GetVersion(ctx, &entity.GetFileVersionRequest{Id: version.ID})
	require.NoError(t, err)
	require.Equal(t, version.ID, reply.Version.Id)
	require.Equal(t, version.Signature, reply.Version.Signature)
	require.Equal(t, file.ID, reply.Version.FileId)
	_, err = (&previewService{api: service.api}).Get(ctx, &entity.GetPreviewRequest{Signature: signature})
	require.Error(t, err)
}

func TestImportArchiveDirectoryReportsBoundedResults(t *testing.T) {
	// Shared logical parents serve several signed inventory Files and one unsigned entry.
	ctx := context.Background()
	root := t.TempDir()
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	media, err := lib.CreateMedia(ctx, &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "b95355a8-e71d-4528-a49d-434724d56e41", Name: "Archive",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	directory := &library.Position{MediaID: media.ID, Path: "folder/", IsDir: true}
	require.NoError(t, lib.SavePosition(ctx, directory))
	require.NoError(t, lib.SavePosition(ctx, &library.Position{MediaID: media.ID, Path: "folder/saved.txt", Signature: []byte("saved"), Mode: 0o644}))
	require.NoError(t, lib.SavePosition(ctx, &library.Position{MediaID: media.ID, Path: "folder/second.txt", Signature: []byte("second"), Mode: 0o644}))
	require.NoError(t, lib.SavePosition(ctx, &library.Position{MediaID: media.ID, Path: "folder/unknown.txt", Mode: 0o644}))

	// A public report counts each missing parent once without admitting any File.
	service := &filesService{api: New(lib, executor.New(db, lib, nil, executor.Paths{Work: root}, executor.Scripts{}, nil))}
	preview, err := service.ImportPositions(ctx, &entity.ImportPositionsRequest{PositionIds: []int64{directory.ID}, Dryrun: true})
	require.NoError(t, err)
	require.EqualValues(t, 2, preview.FileCount)
	require.EqualValues(t, 3, preview.DirectoryCount)
	var files int64
	require.NoError(t, db.Model(library.ModelFile).Count(&files).Error)
	require.Zero(t, files)

	// Applying the same selection reports exactly the announced public counts.
	reply, err := service.ImportPositions(ctx, &entity.ImportPositionsRequest{PositionIds: []int64{directory.ID}})
	require.NoError(t, err)
	require.Equal(t, preview, reply)
	require.Equal(t, int64(2), reply.FileCount)
	require.Equal(t, int64(1), reply.SkippedFileCount)
	require.Equal(t, int64(3), reply.DirectoryCount, "Unforged, the Media directory and the selected directory")
	require.Zero(t, reply.ExistingCount)
	require.Zero(t, reply.SkippedUnsignedCount)
}

func TestFileResponsesTolerateCatalogReplacement(t *testing.T) {
	// Catalog import is a quiesced operator action; this only proves responses keep their own view
	// if one ever races them.
	// Preview reads no catalog identity at all: the content signature a caller names is the
	// whole request, so there is nothing a replacement could retarget.
	for _, operation := range []string{"get", "search", "parents", "version", "duplicates"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
			require.NoError(t, err)
			lib := library.New(db)
			require.NoError(t, lib.AutoMigrate())
			parent := &library.File{Name: "folder", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
			require.NoError(t, lib.SaveFile(ctx, parent))
			file := &library.File{Name: "original.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
			require.NoError(t, lib.SaveFile(ctx, file))
			version := &library.FileVersion{FileID: file.ID, Signature: []byte("saved")}
			require.NoError(t, db.Create(version).Error)
			previews, err := preview.New(preview.Config{}, root)
			require.NoError(t, err)
			api := New(lib, executor.New(db, lib, nil, executor.Paths{Work: root}, executor.Scripts{}, previews))

			// Replace reused IDs between identity lookup and response hydration/mutation.
			attempted := false
			var importErr error
			require.NoError(t, db.Callback().Query().After("gorm:after_query").Register("test:catalog_replacement", func(tx *gorm.DB) {
				if attempted || (tx.Statement.Table != "files" && tx.Statement.Table != "file_locations" && tx.Statement.Table != "file_versions") {
					return
				}
				attempted = true
				backup := fmt.Sprintf(`{"files":[{"id":%d,"name":"replacement.txt","mode":420}]}`, file.ID)
				importErr = lib.Import(ctx, strings.NewReader(backup), false)
			}), false)
			switch operation {
			case "get":
				_, err = (&filesService{api: api}).Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: file.ID}}})
			case "search":
				_, err = (&filesService{api: api}).Search(ctx, &entity.SearchFilesRequest{Directory: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{}}, Query: "name:original.txt", Recursive: true})
			case "parents":
				_, err = listFiles(t, ctx, &filesService{api: api}, &entity.ListFilesRequest{Directory: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: parent.ID}}, Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_NAVIGATION}})
			case "version":
				_, err = (&filesService{api: api}).GetVersion(ctx, &entity.GetFileVersionRequest{Id: version.ID})
			case "duplicates":
				_, err = (&filesService{api: api}).ListDuplicates(ctx, &entity.ListContentDuplicatesRequest{Signature: version.Signature})
			}
			require.True(t, attempted)
			require.NoError(t, importErr, "catalog replacement is an explicit user action")
			// A request racing a replacement finishes its own view or reports the identity it lost;
			// it never silently retargets the reused numeric ID.
			require.True(t, err == nil || errors.Is(err, library.ErrFileNotFound) || status.Code(err) == codes.NotFound,
				"unexpected read error: %v", err)
			require.NoError(t, lib.Import(ctx, strings.NewReader(`{"files":[]}`), false))
		})
	}
}
