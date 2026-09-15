package apis

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func (api *API) Uploader() *gin.Engine {
	// Apply one recovery boundary to all file-transfer endpoints.
	r := gin.Default()
	r.Use(func(ctx *gin.Context) {
		defer func() {
			err := recover()
			if err == nil {
				return
			}

			method := ctx.Request.Method
			path := ctx.Request.URL.Path
			status := 500
			remoteAddr := ctx.Request.RemoteAddr
			clientIP := ctx.ClientIP()

			var e error
			switch v := err.(type) {
			case error:
				e = v
			default:
				e = fmt.Errorf("%v", v)
			}

			logrus.WithContext(ctx).
				WithError(e).WithField("stack", string(debug.Stack())).
				Errorf(
					"panic recover: method= %s path= %s status= %d remote_addr= %s client_ip= %s",
					method, path, status, remoteAddr, clientIP,
				)

			reason := e.Error()
			ctx.JSON(status, gin.H{"reason": reason})
			ctx.Abort()
		}()

		ctx.Next()
	})

	// Register the health endpoint separately from Library data transfer.
	r.GET("/ping", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"result": "pong"})
	})
	upgrade := r.Group("/_upgrade")
	upgrade.Use(func(ctx *gin.Context) {
		host, _, err := net.SplitHostPort(ctx.Request.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() || ctx.GetHeader("X-Forwarded-For") != "" {
			ctx.AbortWithStatus(http.StatusNotFound)
			return
		}
		ctx.Next()
	})
	upgrade.GET("/status", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"process_id": os.Getpid(), "running_job_ids": api.exe.RunningJobIDs()})
	})
	upgrade.POST("/quiesce", func(ctx *gin.Context) {
		ids, ready := api.exe.TryQuiesce()
		if !ready {
			ctx.JSON(http.StatusConflict, gin.H{"running_job_ids": ids})
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"running_job_ids": ids})
	})

	// Stream complete Library snapshots directly between the database and HTTP clients.
	r.GET("/library/_export", func(ctx *gin.Context) {
		ctx.Header("Content-Type", library.JSONLContentType)
		ctx.Header("Content-Disposition", `attachment; filename="library.jsonl"`)
		if err := api.lib.Export(ctx, ctx.Writer, []entity.LibraryEntityType{
			entity.LibraryEntityType_MEDIA,
			entity.LibraryEntityType_FILE,
			entity.LibraryEntityType_POSITION,
			entity.LibraryEntityType_LOCATION,
			entity.LibraryEntityType_FILE_LOCATION,
		}); err != nil {
			logrus.WithContext(ctx).WithError(err).Error("export library failed")
			ctx.Abort()
		}
	})
	r.POST("/library/_import", func(ctx *gin.Context) {
		logrus.WithContext(ctx).Infof("get library import request, %t %t", ctx == nil, ctx.Request == nil)

		defer ctx.Request.Body.Close()
		if err := api.lib.Import(ctx, ctx.Request.Body); err != nil {
			panic(err)
		}

		ctx.JSON(http.StatusOK, gin.H{"result": "ok"})
	})

	// Serve online-readable physical Positions without creating a Restore Job.
	r.GET("/content/:position_id", api.servePositionContent)
	r.HEAD("/content/:position_id", api.servePositionContent)
	r.GET("/originals/:file_id", api.serveOnlineContent)
	r.HEAD("/originals/:file_id", api.serveOnlineContent)
	r.GET("/locations/:location_id/content", api.serveLocationContent)
	r.HEAD("/locations/:location_id/content", api.serveLocationContent)
	r.GET("/content", api.serveFilesContent)
	r.HEAD("/content", api.serveFilesContent)

	// Resolve public asset roles through the Library file's content signature.
	r.GET("/previews/:id/:role", func(ctx *gin.Context) {
		if api.exe.Previews() == nil {
			ctx.Status(http.StatusNotFound)
			return
		}
		id, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
		if err != nil {
			ctx.Status(http.StatusBadRequest)
			return
		}
		release, err := api.lib.UseOnlineRead()
		if err != nil {
			ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
			return
		}
		defer release()
		file, err := api.lib.GetFile(ctx, id)
		if errors.Is(err, library.ErrFileNotFound) {
			ctx.Status(http.StatusNotFound)
			return
		}
		if err != nil {
			panic(err)
		}
		hash, size := file.Hash, file.Size
		if value := ctx.Query("version_id"); value != "" {
			versionID, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				ctx.Status(http.StatusBadRequest)
				return
			}
			version, err := api.lib.GetFileVersion(ctx, versionID)
			if err != nil || version.FileID != file.ID {
				ctx.Status(http.StatusNotFound)
				return
			}
			hash, size = version.Hash, version.Size
		}
		signature, err := library.NewFileSignature(hash, size)
		if err != nil {
			ctx.Status(http.StatusNotFound)
			return
		}
		manifest, err := api.exe.Previews().Manifest(signature)
		if errors.Is(err, os.ErrNotExist) {
			ctx.Status(http.StatusNotFound)
			return
		}
		if err != nil {
			panic(err)
		}

		role := ctx.Param("role")
		var mediaType string
		for _, asset := range manifest.Assets {
			if asset.Role == role {
				mediaType = asset.MediaType
				break
			}
		}
		if mediaType == "" {
			ctx.Status(http.StatusNotFound)
			return
		}
		reader, err := api.exe.Previews().Open(signature, role)
		if err != nil {
			panic(err)
		}
		defer reader.Close()
		ctx.Header("Content-Type", mediaType)
		ctx.Header("Cache-Control", "no-cache")
		if _, err := io.Copy(ctx.Writer, reader); err != nil {
			ctx.Abort()
		}
	})

	return r
}

func (api *API) servePositionContent(ctx *gin.Context) {
	// Resolve the requested durable Position and its immutable Media profile.
	id, err := strconv.ParseInt(ctx.Param("position_id"), 10, 64)
	if err != nil || id <= 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{"reason": "invalid Position ID"})
		return
	}
	release, err := api.lib.UseOnlineRead()
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
		return
	}
	defer release()
	position, err := api.lib.GetPosition(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ctx.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		panic(err)
	}
	if position.IsDir {
		ctx.JSON(http.StatusConflict, gin.H{"reason": "Position is not a published file"})
		return
	}
	stored, err := api.lib.GetMedia(ctx, position.MediaID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ctx.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		panic(err)
	}
	capabilities, err := mediapkg.CapabilitiesForProfile(stored.Profile)
	if err != nil {
		panic(err)
	}
	if stored.Kind != entity.MediaKind_MEDIA_KIND_VOLUME || !capabilities.OnlineReadable() {
		ctx.JSON(http.StatusConflict, gin.H{"reason": "Media requires Restore before use"})
		return
	}

	// Revalidate the mounted identity and resolve the exact file without following symlinks.
	volume, err := mediapkg.DiscoverVolume(api.exe.Paths().Volumes, stored.Identity)
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
		return
	}
	profile := stored.Profile.GetVolume()
	if profile == nil || !mediapkg.SameVolumeProfile(profile, volume.Marker.Profile) {
		ctx.JSON(http.StatusConflict, gin.H{"reason": "Volume marker conflicts with Library"})
		return
	}
	filename, err := mediapkg.ResolveSourcePath(volume.Root, position.Path)
	if err != nil {
		ctx.JSON(http.StatusConflict, gin.H{"reason": err.Error()})
		return
	}
	opened, err := os.Open(filename)
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
	if !info.Mode().IsRegular() || info.Size() != position.Size || uint32(info.Mode()) != position.Mode ||
		info.ModTime().UnixNano() != position.ModTime.UnixNano() {
		ctx.JSON(http.StatusConflict, gin.H{"reason": "Volume file differs from its Library Position; run Scan"})
		return
	}

	// A version view may use its own logical name; inventory browsing uses the physical name.
	displayName := path.Base(position.Path)
	if value := ctx.Query("version_id"); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			ctx.Status(http.StatusBadRequest)
			return
		}
		version, err := api.lib.GetFileVersion(ctx, id)
		if err != nil || !bytes.Equal(version.Signature, position.Signature) {
			ctx.Status(http.StatusConflict)
			return
		}
		if file, err := api.lib.GetFile(ctx, version.FileID); err == nil {
			displayName = file.Name
		}
	}
	mediaType := mime.TypeByExtension(strings.ToLower(path.Ext(displayName)))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	disposition := "attachment"
	if onlineInlineType(mediaType) {
		disposition = "inline"
	}
	ctx.Header("Content-Type", mediaType)
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Header("Content-Security-Policy", "sandbox; default-src 'none'")
	ctx.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": displayName}))
	http.ServeContent(ctx.Writer, ctx.Request, displayName, info.ModTime(), opened)
}
