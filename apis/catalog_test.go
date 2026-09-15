package apis

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/preview"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
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
	service := &fileCatalogService{api: New(lib, exe)}
	reply, err := service.GetVersion(ctx, &entity.GetFileVersionRequest{Id: version.ID})
	require.NoError(t, err)
	require.Equal(t, version.ID, reply.Version.Id)
	require.Equal(t, version.Signature, reply.Version.Signature)
	require.Equal(t, file.Name, reply.File.Name)
	require.Nil(t, reply.Preview)
}

func TestFileResponsesExcludeCatalogReplacement(t *testing.T) {
	for _, operation := range []string{"get", "search", "parents", "version", "duplicates", "preview"} {
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
				if attempted || tx.Statement.Table != "files" {
					return
				}
				attempted = true
				backup := fmt.Sprintf(`{"files":[{"id":%d,"name":"replacement.txt","mode":420}]}`, file.ID)
				importErr = lib.Import(ctx, strings.NewReader(backup))
			}))
			switch operation {
			case "get":
				_, err = api.FileGet(ctx, &entity.FileGetRequest{Id: file.ID})
			case "search":
				_, err = api.FileSearch(ctx, &entity.FileSearchRequest{Query: "name:original.txt"})
			case "parents":
				_, err = api.FileListParents(ctx, &entity.FileListParentsRequest{Id: file.ID})
			case "version":
				_, err = (&fileCatalogService{api: api}).GetVersion(ctx, &entity.GetFileVersionRequest{Id: version.ID})
			case "duplicates":
				_, err = (&fileCatalogService{api: api}).ListDuplicates(ctx, &entity.ListContentDuplicatesRequest{Signature: version.Signature})
			case "preview":
				response := httptest.NewRecorder()
				api.Uploader().ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/previews/%d/thumbnail", file.ID), nil))
				require.Equal(t, http.StatusNotFound, response.Code)
			}
			require.True(t, attempted)
			require.ErrorIs(t, importErr, library.ErrOnlineBusy)
			require.NoError(t, err)
			// The request releases admission after its complete response has been assembled.
			require.NoError(t, lib.Import(ctx, strings.NewReader(`{"files":[]}`)))
		})
	}
}
