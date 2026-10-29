package apis_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newExportLibrary(t *testing.T) (*gorm.DB, *library.Library) {
	// Each server and import target owns a separate disposable Catalog.
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "library.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	return db, lib
}

func exportHTTPServer(t *testing.T, handler http.Handler, http2 bool) *httptest.Server {
	// Exercise net/http's actual connection abort and HTTP/2 stream reset behavior.
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = http2
	if http2 {
		server.StartTLS()
	} else {
		server.Start()
	}
	server.Client().Timeout = 10 * time.Second
	t.Cleanup(server.Close)
	return server
}

func TestLibraryHTTPExportRoundTrip(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, populated := range []bool{false, true} {
			t.Run(fmt.Sprintf("http2=%t/populated=%t", http2, populated), func(t *testing.T) {
				// Export both empty and multi-page Libraries through the real protocol.
				_, source := newExportLibrary(t)
				if populated {
					for index := range 101 {
						require.NoError(t, source.SaveFile(context.Background(), &library.File{
							Name: fmt.Sprintf("file-%03d", index), Note: "retained metadata",
						}))
					}
				}
				server := exportHTTPServer(t, apis.New(source, nil).Uploader(), http2)
				response, err := server.Client().Get(server.URL + "/library/_export")
				require.NoError(t, err)
				defer response.Body.Close()
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, http2, response.ProtoMajor == 2)
				require.Equal(t, library.JSONLContentType, response.Header.Get("Content-Type"))
				require.Contains(t, response.Header.Get("Content-Disposition"), "attachment")
				data, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.True(t, bytes.HasSuffix(data, []byte("{\"type\":\"end\"}\n")))

				// The resulting stream must restore every selected File into another Catalog.
				targetDB, target := newExportLibrary(t)
				require.NoError(t, target.Import(context.Background(), bytes.NewReader(data), false))
				var count int64
				require.NoError(t, targetDB.Model(library.ModelFile).Count(&count).Error)
				want := int64(0)
				if populated {
					want = 101
					restored, err := target.GetFile(context.Background(), 1)
					require.NoError(t, err)
					require.Equal(t, "retained metadata", restored.Note)
				}
				require.Equal(t, want, count)
			})
		}
	}
}

func TestLibraryHTTPExportFailureFraming(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, fault := range []string{"before header", "header only", "flushed pages"} {
			t.Run(fmt.Sprintf("http2=%t/%s", http2, fault), func(t *testing.T) {
				// Inject failures before any write, after the 185-byte header, or after a full page.
				db, lib := newExportLibrary(t)
				if fault == "before header" {
					sqlDB, err := db.DB()
					require.NoError(t, err)
					require.NoError(t, sqlDB.Close())
				} else {
					if fault == "flushed pages" {
						for index := range 101 {
							require.NoError(t, lib.SaveFile(context.Background(), &library.File{
								Name: fmt.Sprintf("%03d-%s", index, strings.Repeat("x", 128)),
							}))
						}
					}
					pages := 0
					require.NoError(t, db.Callback().Query().Before("gorm:query").Register("export_failure", func(tx *gorm.DB) {
						if fault == "header only" && tx.Statement.Table == "media" {
							tx.AddError(errors.New("injected export failure"))
						}
						if fault == "flushed pages" && tx.Statement.Table == "files" {
							pages++
							if pages == 2 {
								tx.Statement.Context.(*gin.Context).Writer.Flush()
								tx.AddError(errors.New("injected export failure"))
							}
						}
					}))
				}

				// Observe the actual negotiated protocol even when no response headers arrive.
				protocol := make(chan string, 1)
				router := apis.New(lib, nil).Uploader()
				server := exportHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					protocol <- r.Proto
					router.ServeHTTP(w, r)
				}), http2)
				response, requestErr := server.Client().Get(server.URL + "/library/_export")
				wantProtocol := "HTTP/1.1"
				if http2 {
					wantProtocol = "HTTP/2.0"
				}
				require.Equal(t, wantProtocol, <-protocol)
				if fault == "before header" {
					require.NoError(t, requestErr)
					defer response.Body.Close()
					require.Equal(t, http.StatusInternalServerError, response.StatusCode)
					require.Empty(t, response.Header.Get("Content-Disposition"))
					require.Contains(t, response.Header.Get("Content-Type"), "application/json")
					data, err := io.ReadAll(response.Body)
					require.NoError(t, err)
					require.Contains(t, string(data), "database is closed")
					require.NotContains(t, string(data), "yatm-library-backup")
					return
				}
				if requestErr != nil {
					// A reset can reach the HTTP/2 client before it returns the flushed headers.
					return
				}

				// Started streams remain visibly incomplete and never receive appended JSON errors.
				defer response.Body.Close()
				data, readErr := io.ReadAll(response.Body)
				require.Error(t, readErr)
				require.NotContains(t, string(data), `"reason"`)
				require.NotContains(t, string(data), `{"type":"end"}`)
				if fault == "flushed pages" {
					require.Greater(t, len(data), 4096)
				}
			})
		}
	}
}

func TestUploaderRecoveryPreservesFailureFraming(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, started := range []bool{false, true} {
			t.Run(fmt.Sprintf("http2=%t/started=%t", http2, started), func(t *testing.T) {
				// Cover unexpected panics separately from the deliberate export abort sentinel.
				router := apis.New(nil, nil).Uploader()
				router.GET("/panic", func(ctx *gin.Context) {
					ctx.Header("Content-Disposition", "attachment")
					if started {
						_, _ = ctx.Writer.WriteString("partial response\n")
						ctx.Writer.Flush()
					}
					panic(errors.New("unexpected failure"))
				})
				server := exportHTTPServer(t, router, http2)
				response, err := server.Client().Get(server.URL + "/panic")
				if err != nil {
					require.True(t, started)
					return
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				if started {
					require.Error(t, err)
					require.Equal(t, "partial response\n", string(data))
					return
				}
				require.NoError(t, err)
				require.Equal(t, http.StatusInternalServerError, response.StatusCode)
				require.Empty(t, response.Header.Get("Content-Disposition"))
				require.JSONEq(t, `{"reason":"unexpected failure"}`, string(data))
			})
		}
	}
}

func TestLibraryHTTPExportHTTP10Stream(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%t", fail), func(t *testing.T) {
			// Use a complete first page to establish real close-delimited streaming.
			db, lib := newExportLibrary(t)
			for i := range 101 {
				require.NoError(t, lib.SaveFile(context.Background(), &library.File{
					Name: fmt.Sprintf("%03d-%s", i, strings.Repeat("x", 128)),
				}))
			}
			reached := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			pages := 0
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("pause_export", func(tx *gorm.DB) {
				if tx.Statement.Table != "files" {
					return
				}
				pages++
				if pages != 2 {
					return
				}
				tx.Statement.Context.(*gin.Context).Writer.Flush()
				close(reached)
				<-release
				if fail {
					tx.AddError(errors.New("injected export failure"))
				}
			}))
			server := exportHTTPServer(t, apis.New(lib, nil).Uploader(), false)
			c, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 5*time.Second)
			require.NoError(t, err)
			defer c.Close()
			require.NoError(t, c.SetDeadline(time.Now().Add(10*time.Second)))
			_, err = fmt.Fprint(c, "GET /library/_export HTTP/1.0\r\nHost: localhost\r\n\r\n")
			require.NoError(t, err)

			// The first response bytes arrive while the remaining export is blocked.
			response, err := http.ReadResponse(bufio.NewReader(c), nil)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "HTTP/1.0", response.Proto)
			require.Equal(t, int64(-1), response.ContentLength)
			require.Empty(t, response.TransferEncoding)
			prefix := make([]byte, 256)
			_, err = io.ReadFull(response.Body, prefix)
			require.NoError(t, err)
			<-reached
			release <- struct{}{}
			rest, err := io.ReadAll(response.Body)
			require.NoError(t, err, "close-delimited transport cannot distinguish producer failure")
			data := append(prefix, rest...)

			// Existing import validation distinguishes completed and interrupted snapshots.
			targetDB, target := newExportLibrary(t)
			err = target.Import(context.Background(), bytes.NewReader(data), false)
			if fail {
				require.ErrorContains(t, err, "missing Library end record")
				require.NotContains(t, string(data), `{"type":"end"}`)
			} else {
				require.NoError(t, err)
				require.True(t, bytes.HasSuffix(data, []byte("{\"type\":\"end\"}\n")))
			}
			var count int64
			require.NoError(t, targetDB.Model(library.ModelFile).Count(&count).Error)
			if fail {
				require.Zero(t, count)
			} else {
				require.Equal(t, int64(101), count)
			}
		})
	}
}
