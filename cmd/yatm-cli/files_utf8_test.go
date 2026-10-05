package main

import (
	"bufio"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestLSUTF8FilenamesThroughRealFilesService(t *testing.T) {
	// Real logical and physical trees contain the same names, not their display-escaped versions.
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
	require.NoError(t, os.Mkdir(physical, 0o755))
	location := &library.Location{Name: "Literal", ExecutorID: "local", RootPath: physical}
	require.NoError(t, lib.CreateLocation(ctx, location))
	names := []string{"normal", `back\slash`, `literal\n`, "literal\n", " leading", "trailing ", " \t\n", `quotes'"`, "100%?#", "照片 😀", "\x01\x7f", "\uFFFD"}
	children := make(map[string]int64, len(names))
	for _, name := range names {
		dir, err := lib.MkdirAll(ctx, 0, name, 0o755)
		require.NoError(t, err)
		child := &library.File{ParentID: dir.ID, Name: name, Kind: entity.FileKind_FILE_KIND_REGULAR}
		require.NoError(t, lib.SaveFile(ctx, child))
		children[name] = child.ID
		require.NoError(t, os.Mkdir(filepath.Join(physical, name), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(physical, name, name), []byte(name), 0o644))
	}
	exe := executor.New(db, lib, nil,
		executor.Paths{Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: physical}}}, executor.Scripts{}, nil)
	api := apis.New(lib, exe)
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) { api.RegisterLocations(server) }, nil, nil)
	list := func(operand string) []*entity.FilesEntry {
		t.Helper()
		// Decode each complete JSONL response so escaped controls can never split an output row.
		exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", "-l", operand)
		require.Equal(t, exitSuccess, exit, stderr)
		var entries []*entity.FilesEntry
		scanner := bufio.NewScanner(strings.NewReader(stdout))
		for scanner.Scan() {
			var reply entity.ListFilesResponse
			require.NoError(t, protojson.Unmarshal(scanner.Bytes(), &reply))
			entries = append(entries, reply.Entries...)
		}
		require.NoError(t, scanner.Err())

		// Physical names and their exact stat timestamps survive the same JSONL round trip.
		if strings.HasPrefix(operand, "location://") {
			for _, entry := range entries {
				info, err := os.Lstat(filepath.Join(physical, entry.Reference.GetLocation().Path))
				require.NoError(t, err)
				mtime, err := dataformat.Nanoseconds(info.ModTime())
				require.NoError(t, err)
				require.Equal(t, &mtime, entry.MtimeNs)
			}
		}
		return entries
	}

	// Both complete directory listings retain all names once, with usable original references.
	for _, operand := range []string{"/", "location://Literal/"} {
		entries := list(operand)
		actual := make([]string, 0, len(entries))
		for _, entry := range entries {
			actual = append(actual, entry.Name)
			require.NotNil(t, entry.Reference)
			if operand != "/" {
				require.Equal(t, entry.Name, entry.Reference.GetLocation().Path)
			}
		}
		require.ElementsMatch(t, names, actual)
	}

	// Plain Library paths and once-decoded URIs navigate the exact directory and child identity.
	for _, name := range names {
		for _, operand := range []string{"/" + name, "library:///" + url.PathEscape(name), "location://Literal/" + url.PathEscape(name)} {
			entries := list(operand)
			require.Len(t, entries, 1)
			require.Equal(t, name, entries[0].Name)
			if strings.HasPrefix(operand, "location://") {
				require.Equal(t, name+"/"+name, entries[0].Reference.GetLocation().Path)
			} else {
				require.Equal(t, children[name], entries[0].Reference.GetFileId())
			}
		}
	}
}
