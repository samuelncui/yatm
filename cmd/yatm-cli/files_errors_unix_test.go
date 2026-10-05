//go:build darwin || linux || freebsd

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestLSUnreadableChildrenThroughRealFilesService(t *testing.T) {
	// An isolated real service can enumerate this directory but cannot stat its child.
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the child-stat permission fixture")
	}
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, ".hidden"), []byte("content"), 0644))
	location := &library.Location{Name: "Originals", ExecutorID: "local", RootPath: physical}
	require.NoError(t, lib.CreateLocation(ctx, location))
	exe := executor.New(db, lib, nil, executor.Paths{Work: filepath.Join(root, "work"),
		Access: []executor.AccessRange{{Root: physical}}}, executor.Scripts{}, nil)
	api := apis.New(lib, exe)
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) { api.RegisterLocations(server) }, nil, nil)
	t.Cleanup(func() { require.NoError(t, os.Chmod(physical, 0755)) })
	require.NoError(t, os.Chmod(physical, 0400))

	// CLI preserves the exact UTF-8 hidden name and reports a failed read after serializing its row.
	args := []string{"ls", "--location-id", strconv.FormatInt(location.ID, 10), "-l", "--status"}
	exit, stdout, stderr := executeTestCLI(server.URL, "", args...)
	require.NotEqual(t, exitSuccess, exit)
	require.Contains(t, stderr, `"code":"incomplete"`)
	var reply entity.ListFilesResponse
	require.NoError(t, protojson.Unmarshal([]byte(stdout), &reply))
	require.EqualValues(t, 1, reply.GetTotalEntryCount())
	require.Len(t, reply.Entries, 1)
	row := reply.Entries[0]
	require.Equal(t, ".hidden", row.Name)
	require.Contains(t, row.Error, "permission denied")
	require.Nil(t, row.Reference)
	require.Nil(t, row.SizeBytes)
	require.Nil(t, row.MtimeNs)

	// A new read observes recovered permission and returns the same child as an ordinary usable row.
	require.NoError(t, os.Chmod(physical, 0755))
	exit, stdout, stderr = executeTestCLI(server.URL, "", args...)
	require.Equal(t, exitSuccess, exit, stderr)
	reply.Reset()
	require.NoError(t, protojson.Unmarshal([]byte(stdout), &reply))
	require.Len(t, reply.Entries, 1)
	require.Empty(t, reply.Entries[0].Error)
	require.NotNil(t, reply.Entries[0].Reference)
	require.Equal(t, ".hidden", reply.Entries[0].Name)
	require.EqualValues(t, 7, reply.Entries[0].GetSizeBytes())
	info, err := os.Stat(filepath.Join(physical, ".hidden"))
	require.NoError(t, err)
	mtime, err := dataformat.Nanoseconds(info.ModTime())
	require.NoError(t, err)
	require.Equal(t, &mtime, reply.Entries[0].MtimeNs)
}
