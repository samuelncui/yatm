package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestLibraryExportUsesAtomicLocalPublication(t *testing.T) {
	// Serve one deterministic snapshot while counting export requests.
	var contentRequests atomic.Int64
	files := http.NewServeMux()
	files.HandleFunc("/library/_export", func(output http.ResponseWriter, request *http.Request) {
		contentRequests.Add(1)
		require.Empty(t, request.URL.RawQuery)
		_, _ = output.Write([]byte("snapshot data"))
	})
	server, _ := newGRPCWebTestServer(t, func(*grpc.Server) {}, files, nil)
	directory := t.TempDir()

	// Publish a completed snapshot and report the byte count as a JSON string integer.
	target := filepath.Join(directory, "library.jsonl")
	exit, stdout, stderr := executeTestCLI(
		server.URL,
		"",
		"library", "export", "--output", target,
	)
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{"output":"`+target+`","bytes":"13"}`, stdout)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, []byte("snapshot data"), data)

	// Existing content remains untouched without starting another export.
	exit, stdout, stderr = executeTestCLI(
		server.URL,
		"",
		"library", "export", "--output", target,
	)
	require.Equal(t, exitUsage, exit)
	require.Empty(t, stdout)
	require.Equal(t, int64(1), contentRequests.Load())
	data, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, []byte("snapshot data"), data)

	// Leave no temporary artifacts after successful publication.
	temporary, err := filepath.Glob(filepath.Join(directory, ".yatm-export-*"))
	require.NoError(t, err)
	require.Empty(t, temporary)
}

func TestFailedLibraryExportRemovesTemporaryFile(t *testing.T) {
	// Advertise a longer body so the client observes a truncated transfer.
	files := http.NewServeMux()
	files.HandleFunc("/library/_export", func(output http.ResponseWriter, _ *http.Request) {
		output.Header().Set("Content-Length", "10")
		_, _ = output.Write([]byte("short"))
	})
	server, _ := newGRPCWebTestServer(t, func(*grpc.Server) {}, files, nil)
	directory := t.TempDir()
	target := filepath.Join(directory, "incomplete.bin")

	// Keep both the final target and temporary publication path absent on failure.
	exit, stdout, _ := executeTestCLI(
		server.URL,
		"",
		"library", "export", "--output", target,
	)
	require.Equal(t, exitFailure, exit)
	require.Empty(t, stdout)
	_, err := os.Stat(target)
	require.ErrorIs(t, err, os.ErrNotExist)
	temporary, err := filepath.Glob(filepath.Join(directory, ".yatm-export-*"))
	require.NoError(t, err)
	require.Empty(t, temporary)
}

func TestLibraryExportPreservesTargetCreatedDuringTransfer(t *testing.T) {
	// Create a competing output after the CLI's initial destination check.
	directory := t.TempDir()
	target := filepath.Join(directory, "library.jsonl")
	files := http.NewServeMux()
	files.HandleFunc("/library/_export", func(output http.ResponseWriter, _ *http.Request) {
		require.NoError(t, os.WriteFile(target, []byte("existing snapshot"), 0600))
		_, _ = output.Write([]byte("new snapshot"))
	})
	server, _ := newGRPCWebTestServer(t, func(*grpc.Server) {}, files, nil)

	// Atomic publication refuses the new collision and removes its temporary output.
	exit, stdout, _ := executeTestCLI(server.URL, "", "library", "export", "--output", target)
	require.Equal(t, exitFailure, exit)
	require.Empty(t, stdout)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "existing snapshot", string(data))
	temporary, err := filepath.Glob(filepath.Join(directory, ".yatm-export-*"))
	require.NoError(t, err)
	require.Empty(t, temporary)
}

func TestContentDownloadCommandsAreUnavailable(t *testing.T) {
	// Unsupported content exports stop before any network request or local file publication.
	var requests atomic.Int64
	files := http.NewServeMux()
	files.HandleFunc("/", func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	})
	server, _ := newGRPCWebTestServer(t, func(*grpc.Server) {}, files, nil)
	for _, args := range [][]string{
		{"file", "download", "7"},
		{"files", "download", "--file-id", "7"},
		{"location", "download", "7", "--location-id", "4", "--revision", "1"},
		{"location", "download-live", "4", "--path", "original.txt"},
		{"preview", "download", "11", "--role", "poster"},
	} {
		target := filepath.Join(t.TempDir(), "output")
		exit, stdout, stderr := executeTestCLI(server.URL, "", append(args, "--output", target)...)
		require.Equal(t, exitUsage, exit, stderr)
		require.Empty(t, stdout)
		require.Contains(t, stderr, "Unknown command")
		require.NoFileExists(t, target)
	}
	require.Zero(t, requests.Load())
}

func TestLibraryExportSupportsFileAndStdout(t *testing.T) {
	// Serve one deterministic JSON Lines snapshot for both destinations.
	const snapshot = "{\"type\":\"media\"}\n{\"type\":\"file\"}\n"
	files := http.NewServeMux()
	files.HandleFunc("/library/_export", func(output http.ResponseWriter, _ *http.Request) {
		_, _ = output.Write([]byte(snapshot))
	})
	server, _ := newGRPCWebTestServer(t, func(*grpc.Server) {}, files, nil)

	// Publish a complete snapshot as a local file.
	target := filepath.Join(t.TempDir(), "library.jsonl")
	exit, _, stderr := executeTestCLI(server.URL, "", "library", "export", "--output", target)
	require.Equal(t, exitSuccess, exit, stderr)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, snapshot, string(data))

	// Preserve JSON Lines unchanged when stdout is selected.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "library", "export", "--output", "-")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, snapshot, stdout)
}

func TestLibraryImportChecksInputAndServiceBeforeOneUpload(t *testing.T) {
	// Record the HTTP health and import stages around a real gRPC-Web status check.
	var pings atomic.Int64
	var imports atomic.Int64
	var imported []byte
	var contentType string
	files := http.NewServeMux()
	files.HandleFunc("/ping", func(output http.ResponseWriter, _ *http.Request) {
		pings.Add(1)
		_, _ = output.Write([]byte(`{"result":"pong"}`))
	})
	files.HandleFunc("/library/_import", func(output http.ResponseWriter, request *http.Request) {
		imports.Add(1)
		contentType = request.Header.Get("Content-Type")
		var err error
		imported, err = io.ReadAll(request.Body)
		require.NoError(t, err)
		_, _ = output.Write([]byte(`{"result":"ok"}`))
	})
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{})
	}, files, nil)

	// An unreadable local file stops before the remote status checks.
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	exit, stdout, _ := executeTestCLI(
		server.URL,
		"",
		"library", "import", "--input", missing, "--confirm",
	)
	require.Equal(t, exitFailure, exit)
	require.Empty(t, stdout)
	require.Equal(t, int64(0), pings.Load())
	require.Equal(t, int64(0), imports.Load())

	// Stream stdin once after both service paths report healthy.
	const snapshot = "{\"type\":\"media\"}\n"
	exit, stdout, stderr := executeTestCLI(
		server.URL,
		snapshot,
		"library", "import", "--input", "-", "--confirm",
	)
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{"result":"ok"}`, stdout)
	require.Equal(t, int64(1), pings.Load())
	require.Equal(t, int64(1), imports.Load())
	require.Equal(t, 1, recorder.count(entity.JobService_List_FullMethodName))
	require.Equal(t, "application/x-ndjson", contentType)
	require.Equal(t, []byte(snapshot), imported)
}

func TestLibraryTrimChecksStatusBeforeOneMutation(t *testing.T) {
	// Serve both health paths and capture the eventual trim selection.
	files := http.NewServeMux()
	files.HandleFunc("/ping", func(output http.ResponseWriter, _ *http.Request) {
		_, _ = output.Write([]byte(`{"result":"pong"}`))
	})
	var trimmed *entity.LibraryTrimRequest
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{})
		entity.RegisterServiceServer(server, &stubService{libraryTrim: func(
			_ context.Context,
			request *entity.LibraryTrimRequest,
		) (*entity.LibraryTrimReply, error) {
			trimmed = proto.Clone(request).(*entity.LibraryTrimRequest)
			return &entity.LibraryTrimReply{}, nil
		}})
	}, files, nil)

	// Apply both selected trim operations through one mutation RPC.
	exit, stdout, stderr := executeTestCLI(
		server.URL,
		"",
		"library", "trim", "--positions", "--files", "--confirm",
	)
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{}`, stdout)
	require.True(t, trimmed.TrimPosition)
	require.True(t, trimmed.TrimFile)
	require.Equal(t, 1, recorder.count(entity.JobService_List_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.Service_LibraryTrim_FullMethodName))
}
