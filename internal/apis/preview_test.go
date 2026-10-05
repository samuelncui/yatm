package apis_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type previewFixture struct {
	manifest  *entity.PreviewManifest
	data      string
	signature []byte
}

func (*previewFixture) Supports(string, *entity.PreviewJobSettings) bool {
	return false
}

func (*previewFixture) Generate(context.Context, string, []byte, int64, int64, bool, *entity.PreviewJobSettings) ([]byte, error) {
	return nil, nil
}

func (p *previewFixture) Manifest(signature []byte) (*entity.PreviewManifest, error) {
	if p.signature != nil && !bytes.Equal(p.signature, signature) {
		return nil, os.ErrNotExist
	}
	return p.manifest, nil
}

func (p *previewFixture) Exists(signature []byte) (bool, error) {
	_, err := p.Manifest(signature)
	return err == nil, nil
}

func (p *previewFixture) Open(_ []byte, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(p.data)), nil
}

func TestPreviewReadsContentAddressedAssets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	signature, err := library.NewFileSignature(bytes.Repeat([]byte{0x2a}, 32), 4096)
	require.NoError(t, err)
	previews := &previewFixture{
		manifest:  &entity.PreviewManifest{Assets: []*entity.PreviewAsset{{Name: "thumbnail.webp", Role: "thumbnail", MediaType: "image/webp"}}},
		data:      "preview data",
		signature: signature,
	}
	api := apis.New(lib, executor.New(executorDB, lib, nil, executor.Paths{Work: root}, executor.Scripts{}, previews))
	client := entity.NewPreviewServiceClient(domainConnection(t, api))

	// The content identity is the whole request: no File, version or Position is consulted.
	reads := 0
	require.NoError(t, libraryDB.Callback().Query().After("gorm:after_query").Register("test:preview_reads", func(*gorm.DB) { reads++ }))
	reply, err := client.Get(ctx, &entity.GetPreviewRequest{Signature: signature})
	require.NoError(t, err)
	require.Zero(t, reads, "a Preview read resolves no catalog identity")
	require.Equal(t, entity.PreviewAvailability_PREVIEW_AVAILABILITY_READY, reply.Availability)
	require.Len(t, reply.Assets, 1)
	require.Equal(t, "thumbnail", reply.Assets[0].Role)
	assetURL := strings.TrimPrefix(reply.Assets[0].Url, "/files")
	response := httptest.NewRecorder()
	api.Uploader().ServeHTTP(response, httptest.NewRequest(http.MethodGet, assetURL, nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "image/webp", response.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "no-cache", response.Header().Get("Cache-Control"))
	require.Equal(t, previews.data, response.Body.String())

	// A stale content binding or an unknown role cannot retrieve a different resource.
	for _, suffix := range []string{"&content=invalid", "&role=arbitrary"} {
		parsed, err := url.Parse(assetURL)
		require.NoError(t, err)
		values, err := url.ParseQuery(strings.TrimPrefix(suffix, "&"))
		require.NoError(t, err)
		q := parsed.Query()
		for k, v := range values {
			q.Set(k, v[0])
		}
		parsed.RawQuery = q.Encode()
		response = httptest.NewRecorder()
		api.Uploader().ServeHTTP(response, httptest.NewRequest(http.MethodGet, parsed.String(), nil))
		require.Equal(t, http.StatusNotFound, response.Code)
	}

	// A missing derivative is a region-level result, not a failed detail request.
	unknown, err := library.NewFileSignature(bytes.Repeat([]byte{0x3b}, 32), 8192)
	require.NoError(t, err)
	reply, err = client.Get(ctx, &entity.GetPreviewRequest{Signature: unknown})
	require.NoError(t, err)
	require.Equal(t, entity.PreviewAvailability_PREVIEW_AVAILABILITY_NOT_GENERATED, reply.Availability)
	require.Empty(t, reply.Assets)
	response = httptest.NewRecorder()
	api.Uploader().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/files/preview?content=invalid&role=thumbnail", nil))
	require.Equal(t, http.StatusNotFound, response.Code)

	// Reading Preview never invents an identity for content the caller cannot name.
	for _, request := range []*entity.GetPreviewRequest{{}, {Signature: []byte("opaque")}} {
		_, err = client.Get(ctx, request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
}
