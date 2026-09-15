package apis

import (
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
)

func (api *API) serveLocationContent(ctx *gin.Context) {
	// Live transfer authorizes an observed object, without requiring a Library File.
	locationID, err := strconv.ParseInt(ctx.Param("location_id"), 10, 64)
	encoded := ctx.Query("ref")
	data, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
	ref := &entity.LocationEntryRef{}
	if err != nil || locationID <= 0 || len(encoded) > 16384 || decodeErr != nil || protojson.Unmarshal(data, ref) != nil || ref.LocationId != locationID {
		ctx.JSON(http.StatusBadRequest, gin.H{"reason": "invalid Location reference"})
		return
	}
	api.serveObservedContent(ctx, ref)
}

func (api *API) serveFilesContent(ctx *gin.Context) {
	// Both panels pass the guarded content reference returned by Files.Get unchanged.
	encoded := ctx.Query("ref")
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	ref := &entity.FileOperationRef{}
	if len(encoded) > 16384 || err != nil || protojson.Unmarshal(data, ref) != nil || ref.GetLocation() == nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"reason": "an observed content reference is required"})
		return
	}
	api.serveObservedContent(ctx, ref.GetLocation())
}

func (api *API) serveObservedContent(ctx *gin.Context, ref *entity.LocationEntryRef) {
	// One descriptor and passive response policy serve all provider-independent content requests.
	release, err := api.lib.UseOnlineRead()
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
		return
	}
	defer release()
	opened, err := api.exe.OpenLocationEntry(ctx, ref)
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
		return
	}
	defer opened.Close()
	info, err := opened.Stat()
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
		return
	}
	location, err := api.lib.GetOnlineSource(ctx, ref.LocationId)
	if err != nil || location.BindingToken != ref.BindingToken {
		ctx.JSON(http.StatusConflict, gin.H{"reason": "Location changed; reload"})
		return
	}

	// Reuse passive response policy and one descriptor for HEAD/Range/body.
	name := path.Base(ref.Path)
	mediaType := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	disposition := "attachment"
	if onlineInlineType(mediaType) {
		disposition = "inline"
	}
	ctx.Header("Content-Type", mediaType)
	ctx.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Header("Content-Security-Policy", "sandbox; default-src 'none'")
	ctx.Header("Cache-Control", "no-store")
	http.ServeContent(ctx.Writer, ctx.Request, name, info.ModTime(), opened)
}

func (api *API) serveOnlineContent(ctx *gin.Context) {
	// A stable File ID alone does not authorize a newer original or binding.
	fileID, err := strconv.ParseInt(ctx.Param("file_id"), 10, 64)
	if err != nil || fileID <= 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{"reason": "invalid File ID"})
		return
	}
	locationID, err := strconv.ParseInt(ctx.Query("location_id"), 10, 64)
	if err != nil || locationID <= 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{"reason": "expected Location ID is required"})
		return
	}
	revision, err := strconv.ParseInt(ctx.Query("revision"), 10, 64)
	if err != nil || revision <= 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{"reason": "expected Location revision is required"})
		return
	}
	release, err := api.lib.UseOnlineRead()
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
		return
	}
	defer release()

	// Missing metadata and stale identity are distinguished before opening source bytes.
	state, err := api.lib.FileState(ctx, fileID)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, library.ErrFileNotFound) {
		ctx.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		panic(err)
	}
	if state.Original == nil || state.Location == nil || state.Location.Id != locationID || state.Location.Revision != revision {
		ctx.JSON(http.StatusConflict, gin.H{"reason": "original changed; reload and synchronize the Location"})
		return
	}
	current := state.Original
	position := &library.OnlinePosition{FileID: fileID, SourceID: locationID, Path: current.Path, Size: current.Size,
		Mode: current.Mode, MtimeNS: current.MtimeNs, Hash: current.Sha256, Signature: current.Signature,
		ObservedBindingToken: current.ObservedBindingToken}
	file, err := api.lib.GetFile(ctx, fileID)
	if errors.Is(err, library.ErrFileNotFound) {
		ctx.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		panic(err)
	}
	opened, err := api.exe.OpenOnlinePosition(ctx, position, &entity.ExpectedFile{FileId: fileID, Signature: current.Signature, Sha256: current.Sha256, Size: current.Size})
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error() + "; synchronize the source"})
		return
	}
	defer opened.Close()
	latest, err := api.lib.GetOnlineSource(ctx, locationID)
	if err != nil || latest.Revision != revision {
		ctx.JSON(http.StatusConflict, gin.H{"reason": "Location changed; reload"})
		return
	}
	info, err := opened.Stat()
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
		return
	}

	// Only explicitly passive formats may render inline; extension-based MIME is never trusted for HTML/SVG.
	mediaType := mime.TypeByExtension(strings.ToLower(path.Ext(file.Name)))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	disposition := "attachment"
	if onlineInlineType(mediaType) {
		disposition = "inline"
	}
	ctx.Header("Content-Type", mediaType)
	ctx.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": file.Name}))
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Header("Content-Security-Policy", "sandbox; default-src 'none'")
	ctx.Header("Cache-Control", "no-store")
	http.ServeContent(ctx.Writer, ctx.Request, file.Name, info.ModTime(), opened)
}

func onlineInlineType(mediaType string) bool {
	switch strings.Split(mediaType, ";")[0] {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "video/mp4", "video/webm", "audio/mpeg", "audio/ogg", "audio/wav", "text/plain":
		return true
	default:
		return false
	}
}
