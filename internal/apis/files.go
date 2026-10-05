package apis

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/sirupsen/logrus"
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
			ctx.JSON(http.StatusConflict, gin.H{"process_id": os.Getpid(), "running_job_ids": ids})
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"process_id": os.Getpid(), "running_job_ids": ids})
	})

	// Stream complete Library snapshots directly between the database and HTTP clients.
	r.GET("/library/_export", func(ctx *gin.Context) {
		ctx.Header("Content-Type", library.JSONLContentType)
		ctx.Header("Content-Disposition", `attachment; filename="library.jsonl"`)
		if err := api.lib.Export(ctx, ctx.Writer, []entity.LibraryEntityType{
			entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA,
			entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE,
			entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_POSITION,
			entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION,
			entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_LOCATION,
		}); err != nil {
			logrus.WithContext(ctx).WithError(err).Error("export library failed")
			ctx.Abort()
		}
	})
	r.POST("/library/_import", func(ctx *gin.Context) {
		logrus.WithContext(ctx).Infof("get library import request, %t %t", ctx == nil, ctx.Request == nil)

		// A dry run validates the snapshot and rolls the replacement back.
		dryRun := ctx.Query("dryrun") == "true"
		defer ctx.Request.Body.Close()
		if err := api.lib.Import(ctx, ctx.Request.Body, dryRun); err != nil && !errors.Is(err, library.ErrImportDryRun) {
			panic(err)
		}

		ctx.JSON(http.StatusOK, gin.H{"result": "ok", "dryrun": dryRun})
	})

	r.GET("/preview", api.servePreview)

	return r
}
