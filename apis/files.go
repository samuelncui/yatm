package apis

import (
	"fmt"
	"io"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func (api *API) Uploader() *gin.Engine {
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
	r.GET("/ping", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"result": "pong"})
	})
	r.POST("/library/_import", func(ctx *gin.Context) {
		logrus.WithContext(ctx).Infof("get library import request, %t %t", ctx == nil, ctx.Request == nil)

		defer ctx.Request.Body.Close()
		buf, err := io.ReadAll(ctx.Request.Body)
		if err != nil {
			panic(err)
		}

		if err := api.lib.Import(ctx, buf); err != nil {
			panic(err)
		}

		ctx.JSON(http.StatusOK, gin.H{"result": "ok"})
	})

	return r
}
