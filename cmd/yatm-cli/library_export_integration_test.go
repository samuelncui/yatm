package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

func TestLibraryExportReportsActualServerFailure(t *testing.T) {
	for _, fault := range []string{"before header", "header only", "flushed pages"} {
		for _, stdout := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stdout=%t", fault, stdout), func(t *testing.T) {
				// Exercise the actual HTTP export handler using isolated database failures.
				db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "library.db"))
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
				lib := library.New(db)
				require.NoError(t, lib.AutoMigrate())
				if fault == "before header" {
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
				server, _ := newGRPCWebTestServer(t, func(*grpc.Server) {}, apis.New(lib, nil).Uploader(), nil)

				// Both destinations must report failure; stdout can retain already streamed bytes.
				directory := t.TempDir()
				target := filepath.Join(directory, "library.jsonl")
				output := target
				if stdout {
					output = "-"
				}
				exit, data, stderr := executeTestCLI(server.URL, "", "library", "export", "--output", output)
				require.Equal(t, exitFailure, exit, stderr)
				require.NotEmpty(t, stderr)
				require.NoFileExists(t, target)
				temporary, err := filepath.Glob(filepath.Join(directory, ".yatm-export-*"))
				require.NoError(t, err)
				require.Empty(t, temporary)
				if !stdout || fault != "flushed pages" {
					require.Empty(t, data)
					return
				}
				require.Greater(t, len(data), 4096)
				require.NotContains(t, data, `{"type":"end"}`)
				require.NotContains(t, data, `"reason"`)
			})
		}
	}
}
