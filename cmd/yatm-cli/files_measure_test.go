package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestMeasureThroughCLIProcessAndRealFilesService(t *testing.T) {
	// Build the actual CLI and register the production Files API against isolated physical/catalog data.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	binary := filepath.Join(root, "yatm-cli")
	output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	require.NoError(t, err, string(output))
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.MkdirAll(filepath.Join(physical, "folder"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "folder", "hidden-from-root-filter"), []byte("12345"), 0644))
	location := &library.Location{Name: "Originals", ExecutorID: "local", RootPath: physical}
	require.NoError(t, lib.CreateLocation(context.Background(), location))
	known := &library.File{Name: "saved"}
	require.NoError(t, lib.SaveFile(context.Background(), known))
	require.NoError(t, db.Create(&library.FileVersion{FileID: known.ID, Signature: []byte("saved"), Size: 7}).Error)
	unknown := &library.File{Name: "unknown"}
	require.NoError(t, lib.SaveFile(context.Background(), unknown))
	exe := executor.New(db, lib, nil, executor.Paths{Source: root, Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: root}}}, executor.Scripts{}, nil)
	api := apis.New(lib, exe)
	var calls atomic.Int64
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { api.RegisterLocations(server) }, nil,
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/services"+entity.FilesService_Measure_FullMethodName {
					calls.Add(1)
				}
				next.ServeHTTP(w, r)
			})
		})

	// Location uses the one measurement RPC, and preserves root-filter/full-descendant semantics.
	output, err = exec.Command(binary, "--server", server.URL, "du", "--location-id", strconv.FormatInt(location.ID, 10), "--query", "name:folder").CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), `"known_bytes":"5"`)
	require.Contains(t, string(output), `"complete":true`)
	require.EqualValues(t, 1, calls.Load())
	require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))
	require.Zero(t, recorder.count(entity.FilesService_List_FullMethodName))

	// A partial Library result retains its known total but fails the subprocess for automation.
	output, err = exec.Command(binary, "--server", server.URL, "du", "--file-id", "0").CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), `"known_bytes":"7"`)
	require.Contains(t, string(output), `"code":"incomplete"`)
	output, err = exec.Command(binary, "--server", server.URL, "du", "--query", "name:saved").CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), `"known_bytes":"7"`)
	require.EqualValues(t, 3, calls.Load())
}
