package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestLSPathsThroughCLIProcessAndRealFilesService(t *testing.T) {
	// Run the actual parser and transport against a private Library and filesystem.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	binary := filepath.Join(root, "yatm-cli")
	output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	require.NoError(t, err, string(output))
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	ctx := context.Background()
	add := func(parent int64, name string, kind entity.FileKind) *library.File {
		t.Helper()
		file := &library.File{ParentID: parent, Name: name, Kind: kind}
		require.NoError(t, lib.SaveFile(ctx, file))
		return file
	}

	// Distinct case and query punctuation are ordinary logical names, independent of the client cwd.
	photos := add(0, "Photos", entity.FileKind_FILE_KIND_DIRECTORY)
	year := add(photos.ID, "2026", entity.FileKind_FILE_KIND_DIRECTORY)
	saved := add(year.ID, "saved.jpg", entity.FileKind_FILE_KIND_REGULAR)
	add(year.ID, "unsaved.jpg", entity.FileKind_FILE_KIND_REGULAR)
	require.NoError(t, db.Create(&library.FileVersion{FileID: saved.ID, Signature: []byte("saved"), Size: 7}).Error)
	lower := add(0, "photos", entity.FileKind_FILE_KIND_DIRECTORY)
	add(lower.ID, "lowercase.txt", entity.FileKind_FILE_KIND_REGULAR)
	add(photos.ID, "readme.txt", entity.FileKind_FILE_KIND_REGULAR)
	special := add(photos.ID, `100% ready?# "OR" * 照片`, entity.FileKind_FILE_KIND_DIRECTORY)
	add(special.ID, "literal-result.txt", entity.FileKind_FILE_KIND_REGULAR)
	client := filepath.Join(root, "client")
	require.NoError(t, os.MkdirAll(filepath.Join(client, "LocalOnly"), 0755))

	// More than one page of candidates exercises the production cursor and directory predicate.
	paged := add(0, "Paged", entity.FileKind_FILE_KIND_DIRECTORY)
	for i := range 501 {
		add(paged.ID, fmt.Sprintf("a-%03d", i), entity.FileKind_FILE_KIND_DIRECTORY)
	}
	target := add(paged.ID, "zz-target", entity.FileKind_FILE_KIND_DIRECTORY)
	add(target.ID, "found.txt", entity.FileKind_FILE_KIND_REGULAR)

	// Register one physical root with an ignored directory and a symlink outside its boundary.
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.MkdirAll(filepath.Join(physical, "DCIM"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "DCIM", "live.jpg"), []byte("live"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(physical, "hidden"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "hidden", "ignored.txt"), []byte("hidden"), 0644))
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.MkdirAll(outside, 0755))
	require.NoError(t, os.Symlink(outside, filepath.Join(physical, "escape")))
	location := &library.Location{Name: "Home NAS", ExecutorID: "local", RootPath: physical,
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "hidden/"}}}
	require.NoError(t, lib.CreateLocation(ctx, location))
	exe := executor.New(db, lib, nil,
		executor.Paths{Source: root, Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: physical}}},
		executor.Scripts{}, nil)
	api := apis.New(lib, exe)
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { api.RegisterLocations(server) }, nil, nil)
	var originalFiles int64
	require.NoError(t, db.Table("files").Count(&originalFiles).Error)
	list := func(args ...string) []*entity.FilesEntry {
		t.Helper()
		// Preserve the JSON Lines contract while invoking from an unrelated local working directory.
		cmd := exec.Command(binary, append([]string{"--server", server.URL, "ls"}, args...)...)
		cmd.Dir = client
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
		var entries []*entity.FilesEntry
		scanner := bufio.NewScanner(bytes.NewReader(output))
		for scanner.Scan() {
			var reply entity.ListFilesResponse
			require.NoError(t, protojson.Unmarshal(scanner.Bytes(), &reply), scanner.Text())
			entries = append(entries, reply.Entries...)
		}
		require.NoError(t, scanner.Err())
		return entries
	}

	// Every root spelling uses the original direct List without a preliminary lookup.
	for _, args := range [][]string{nil, {"/"}, {"."}, {"./"}, {"library:///"}} {
		entries := list(args...)
		require.Len(t, entries, 3)
		require.Zero(t, recorder.count(entity.FilesService_Search_FullMethodName))
	}
	for _, operand := range []string{"/Photos/2026", "Photos/2026", "./Photos/2026", "library:///Photos/2026"} {
		entries := list(operand, "--scope", "saved")
		require.Len(t, entries, 1)
		require.Equal(t, saved.ID, entries[0].Reference.GetFileId())
	}
	entries := list("/photos")
	require.Len(t, entries, 1)
	require.Equal(t, "lowercase.txt", entries[0].Name)
	for _, operand := range []string{"/Photos/" + special.Name, "library:///Photos/" + url.PathEscape(special.Name)} {
		entries := list(operand)
		require.Len(t, entries, 1)
		require.Equal(t, "literal-result.txt", entries[0].Name)
	}

	// A target beyond the first candidate page is found, while an ordinary large listing stays complete.
	before := recorder.count(entity.FilesService_Search_FullMethodName)
	entries = list("/Paged/zz-target")
	require.Len(t, entries, 1)
	require.Equal(t, "found.txt", entries[0].Name)
	require.Equal(t, 3, recorder.count(entity.FilesService_Search_FullMethodName)-before)
	require.Len(t, list("/Paged"), 502)

	// Named roots, Location-relative paths and legacy ID selectors reach the same live entries.
	for _, operand := range []string{"location://Home NAS", "location://Home%20NAS/"} {
		entries := list(operand)
		require.NotEmpty(t, entries)
		for _, entry := range entries {
			require.Equal(t, location.ID, entry.Reference.GetLocation().LocationId)
			require.NotEqual(t, "hidden", entry.Name)
		}
	}
	for _, args := range [][]string{
		{"location://Home NAS/DCIM"},
		{"location://Home%20NAS/DCIM/../DCIM"},
		{"--location-id", strconv.FormatInt(location.ID, 10), "--path", "DCIM"},
	} {
		entries := list(args...)
		require.Len(t, entries, 1)
		require.Equal(t, "live.jpg", entries[0].Name)
		require.Equal(t, "DCIM/live.jpg", entries[0].Reference.GetLocation().Path)
	}
	entries = list("--file-id", strconv.FormatInt(year.ID, 10), "--scope", "saved")
	require.Len(t, entries, 1)
	require.Equal(t, saved.ID, entries[0].Reference.GetFileId())

	// Ignore hides contents without prohibiting explicit directory access, with either selector syntax.
	require.Empty(t, list("location://Home NAS/hidden"))
	require.Empty(t, list("--location-id", strconv.FormatInt(location.ID, 10), "--path", "hidden"))

	// Missing paths never fall back to local directories; server containment still applies.
	for _, operand := range []string{
		"/LocalOnly", "/PHOTOS", "/Photos/readme.txt", "/Photos/readme.txt/child",
		"location://home nas/DCIM", "location://missing/DCIM", "location://Home NAS/missing",
		"location://Home NAS/escape", "location://Home NAS/escape/child",
	} {
		cmd := exec.Command(binary, "--server", server.URL, "ls", operand)
		cmd.Dir = client
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var failed *exec.ExitError
		require.ErrorAs(t, err, &failed, operand)
		require.Equal(t, exitFailure, failed.ExitCode(), "%s: %s", operand, stderr.String())
		require.Empty(t, stdout.String(), operand)
	}

	// Browsing creates no logical identities or original associations and uses no mutation or detail RPC.
	var files, associations int64
	require.NoError(t, db.Table("files").Count(&files).Error)
	require.NoError(t, db.Table("file_locations").Count(&associations).Error)
	require.Equal(t, originalFiles, files)
	require.Zero(t, associations)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for method := range recorder.calls {
		require.Contains(t, []string{entity.FilesService_List_FullMethodName,
			entity.FilesService_Search_FullMethodName, entity.LocationService_List_FullMethodName}, method)
	}
}

func TestLSLiteralNamesThroughRealFilesService(t *testing.T) {
	// Exact path selection must work independently of SQLite's case folding and URI-like name prefixes.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	ctx := context.Background()
	physical := filepath.Join(root, "archive")
	require.NoError(t, os.Mkdir(physical, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(physical, "found.txt"), []byte("found"), 0644))
	location := &library.Location{Name: "Étage", ExecutorID: "local", RootPath: physical}
	require.NoError(t, lib.CreateLocation(ctx, location))
	for i := range 100 {
		require.NoError(t, lib.CreateLocation(ctx, &library.Location{Name: fmt.Sprintf("Other %03d", i),
			ExecutorID: "local", RootPath: filepath.Join(root, "unused", strconv.Itoa(i))}))
	}
	exe := executor.New(db, lib, nil,
		executor.Paths{Source: root, Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: physical}}},
		executor.Scripts{}, nil)
	api := apis.New(lib, exe)
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { api.RegisterLocations(server) }, nil, nil)

	// Colons in plain Library operands retain the same identity with or without a root prefix.
	for _, name := range []string{"library:2026", "location:NAS"} {
		t.Run(name, func(t *testing.T) {
			// Give each directory a distinguishable result, then exercise the real parser and API.
			dir := &library.File{Name: name, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
			require.NoError(t, lib.SaveFile(ctx, dir))
			child := &library.File{ParentID: dir.ID, Name: "found.txt"}
			require.NoError(t, lib.SaveFile(ctx, child))
			for _, prefix := range []string{"", "/", "./", "library:///"} {
				exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", prefix+name)
				require.Equal(t, exitSuccess, exit, stderr)
				var reply entity.ListFilesResponse
				require.NoError(t, protojson.Unmarshal([]byte(strings.TrimSpace(stdout)), &reply))
				require.Len(t, reply.Entries, 1)
				require.Equal(t, child.ID, reply.Entries[0].Reference.GetFileId())
			}
		})
	}

	// Both Unicode spellings must survive candidate enumeration and check all pages for duplicates.
	for _, operand := range []string{"location://Étage", "location://" + url.PathEscape(location.Name)} {
		t.Run(operand, func(t *testing.T) {
			before := recorder.count(entity.LocationService_List_FullMethodName)
			exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", operand)
			require.Equal(t, exitSuccess, exit, stderr)
			require.Contains(t, stdout, `"name":"found.txt"`)
			require.Equal(t, 2, recorder.count(entity.LocationService_List_FullMethodName)-before)
		})
	}

	// A duplicate on a later metadata page is ambiguous even if only the first root is accessible.
	require.NoError(t, lib.CreateLocation(ctx, &library.Location{Name: location.Name,
		ExecutorID: "local", RootPath: filepath.Join(root, "duplicate")}))
	before := recorder.count(entity.FilesService_List_FullMethodName)
	exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", "location://Étage")
	require.Equal(t, exitUsage, exit, stderr)
	require.Contains(t, stderr, "ambiguous")
	require.Empty(t, stdout)
	require.Equal(t, before, recorder.count(entity.FilesService_List_FullMethodName))

	// An explicit ID still selects the intended Location after a display-name collision.
	exit, stdout, stderr = executeTestCLI(server.URL, "", "ls", "--location-id", strconv.FormatInt(location.ID, 10))
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"name":"found.txt"`)
}
