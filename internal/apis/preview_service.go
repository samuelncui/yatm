package apis

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type previewService struct {
	entity.UnimplementedPreviewServiceServer
	api *API
}

func (s *previewService) GetCapabilities(ctx context.Context, _ *entity.GetPreviewCapabilitiesRequest) (*entity.GetPreviewCapabilitiesResponse, error) {
	if s.api.exe.Previews() == nil {
		return &entity.GetPreviewCapabilitiesResponse{Reason: "Preview module is not configured"}, nil
	}
	return s.api.exe.Previews().Capabilities(ctx)
}

// Get reads the assets stored for one content identity. Preview storage is content-addressed, so
// the request carries no target to resolve: callers publish the signature of the content they are
// looking at - a current original, a saved version or a recorded archive Position.
func (s *previewService) Get(_ context.Context, req *entity.GetPreviewRequest) (*entity.GetPreviewResponse, error) {
	// Preview storage errors affect this region only, independently of basic detail reads.
	signature, err := previewSignature(req)
	if err != nil {
		return nil, err
	}
	if s.api.exe.Previews() == nil {
		return &entity.GetPreviewResponse{Availability: entity.PreviewAvailability_PREVIEW_AVAILABILITY_NOT_GENERATED}, nil
	}
	manifest, err := s.api.exe.Previews().Manifest(signature)
	if errors.Is(err, os.ErrNotExist) {
		return &entity.GetPreviewResponse{Availability: entity.PreviewAvailability_PREVIEW_AVAILABILITY_NOT_GENERATED}, nil
	}
	if err != nil {
		return nil, err
	}
	query := url.Values{"content": {base64.RawURLEncoding.EncodeToString(signature)}}
	reply := &entity.GetPreviewResponse{Availability: entity.PreviewAvailability_PREVIEW_AVAILABILITY_READY}
	for _, asset := range manifest.Assets {
		query.Set("role", asset.Role)
		reply.Assets = append(reply.Assets, &entity.PreviewResource{Role: asset.Role, MediaType: asset.MediaType,
			WidthPx: asset.WidthPx, HeightPx: asset.HeightPx, Url: "/files/preview?" + query.Encode()})
	}
	return reply, nil
}

// previewSignature validates the content identity a Preview read names. Preview reads neither
// admit, hash nor observe anything, so an unknown or unusable identity is the caller's problem.
func previewSignature(req *entity.GetPreviewRequest) ([]byte, error) {
	if req == nil || len(req.Signature) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Preview content signature is required")
	}
	if err := library.ValidateFileSignature(req.Signature); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return req.Signature, nil
}

func (api *API) servePreview(ctx *gin.Context) {
	// The content identity in the URL is the request, and the manifest owns which roles exist.
	encoded := ctx.Query("content")
	if len(encoded) > base64.RawURLEncoding.EncodedLen(library.SignatureSize) {
		ctx.Status(http.StatusBadRequest)
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || library.ValidateFileSignature(signature) != nil || api.exe.Previews() == nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	asset, err := api.exe.Previews().Open(signature, ctx.Query("role"))
	if err != nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	defer asset.Close()

	// Stored bytes are immutable until regeneration, so browsers revalidate instead of downloading again.
	ctx.Header("ETag", asset.ETag)
	ctx.Header("Cache-Control", "no-cache")
	if etagMatches(ctx.GetHeader("If-None-Match"), asset.ETag) {
		ctx.Status(http.StatusNotModified)
		return
	}
	ctx.Header("Content-Type", safeMediaType(asset.MediaType))
	ctx.Header("Content-Length", strconv.FormatInt(asset.Size, 10))
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Status(http.StatusOK)
	if _, err := io.Copy(ctx.Writer, asset); err != nil {
		ctx.Abort()
	}
}

func etagMatches(header, etag string) bool {
	for candidate := range strings.SplitSeq(header, ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == etag || candidate == "*" {
			return true
		}
	}
	return false
}

func safeMediaType(value string) string {
	switch value {
	case "image/jpeg", "image/png", "image/webp", "video/mp4", "audio/mpeg", "application/json", "text/vtt":
		return value
	default:
		return "application/octet-stream"
	}
}
