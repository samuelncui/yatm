package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type rpcCallRecorder struct {
	mu    sync.Mutex
	calls map[string]int
}

func newRPCCallRecorder() *rpcCallRecorder {
	return &rpcCallRecorder{calls: make(map[string]int)}
}

func (r *rpcCallRecorder) interceptor(
	ctx context.Context,
	request any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	r.mu.Lock()
	r.calls[info.FullMethod]++
	r.mu.Unlock()
	return handler(ctx, request)
}

// streamInterceptor records streaming procedures, which a unary interceptor never sees.
func (r *rpcCallRecorder) streamInterceptor(
	service any,
	stream grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	r.mu.Lock()
	r.calls[info.FullMethod]++
	r.mu.Unlock()
	return handler(service, stream)
}

func (r *rpcCallRecorder) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[method]
}

type stubJobService struct {
	entity.UnimplementedJobServiceServer
	list     func(context.Context, *entity.ListJobsRequest) (*entity.ListJobsResponse, error)
	get      func(context.Context, *entity.GetJobRequest) (*entity.GetJobResponse, error)
	delete   func(context.Context, *entity.DeleteJobsRequest) (*entity.DeleteJobsResponse, error)
	getLog   func(context.Context, *entity.GetJobLogRequest) (*entity.GetJobLogResponse, error)
	logLines func(context.Context, *entity.ListJobLogLinesRequest) (*entity.ListJobLogLinesResponse, error)
}

func (s *stubJobService) List(
	ctx context.Context,
	request *entity.ListJobsRequest,
) (*entity.ListJobsResponse, error) {
	if s.list != nil {
		return s.list(ctx, request)
	}
	return &entity.ListJobsResponse{}, nil
}

func (s *stubJobService) Get(
	ctx context.Context,
	request *entity.GetJobRequest,
) (*entity.GetJobResponse, error) {
	if s.get != nil {
		return s.get(ctx, request)
	}
	return &entity.GetJobResponse{}, nil
}

func (s *stubJobService) Delete(
	ctx context.Context,
	request *entity.DeleteJobsRequest,
) (*entity.DeleteJobsResponse, error) {
	if s.delete != nil {
		return s.delete(ctx, request)
	}
	return &entity.DeleteJobsResponse{}, nil
}

func (s *stubJobService) GetLog(
	ctx context.Context,
	request *entity.GetJobLogRequest,
) (*entity.GetJobLogResponse, error) {
	if s.getLog != nil {
		return s.getLog(ctx, request)
	}
	return &entity.GetJobLogResponse{}, nil
}

func (s *stubJobService) ListLogLines(ctx context.Context, request *entity.ListJobLogLinesRequest) (*entity.ListJobLogLinesResponse, error) {
	if s.logLines != nil {
		return s.logLines(ctx, request)
	}
	return &entity.ListJobLogLinesResponse{}, nil
}

type stubArchiveJobService struct {
	entity.UnimplementedArchiveJobServiceServer
	create      func(context.Context, *entity.CreateArchiveJobRequest) (*entity.CreateArchiveJobResponse, error)
	writeMedia  func(context.Context, *entity.WriteArchiveMediaRequest) (*entity.WriteArchiveMediaResponse, error)
	getProgress func(context.Context, *entity.GetArchiveJobProgressRequest) (*entity.GetArchiveJobProgressResponse, error)
}

func (s *stubArchiveJobService) Create(
	ctx context.Context,
	request *entity.CreateArchiveJobRequest,
) (*entity.CreateArchiveJobResponse, error) {
	if s.create != nil {
		return s.create(ctx, request)
	}
	return &entity.CreateArchiveJobResponse{}, nil
}

func (s *stubArchiveJobService) WriteMedia(
	ctx context.Context,
	request *entity.WriteArchiveMediaRequest,
) (*entity.WriteArchiveMediaResponse, error) {
	if s.writeMedia != nil {
		return s.writeMedia(ctx, request)
	}
	return &entity.WriteArchiveMediaResponse{}, nil
}

func (s *stubArchiveJobService) GetProgress(
	ctx context.Context,
	request *entity.GetArchiveJobProgressRequest,
) (*entity.GetArchiveJobProgressResponse, error) {
	if s.getProgress != nil {
		return s.getProgress(ctx, request)
	}
	return &entity.GetArchiveJobProgressResponse{}, nil
}

type stubRestoreJobService struct {
	entity.UnimplementedRestoreJobServiceServer
}

func (s *stubRestoreJobService) GetProgress(
	context.Context,
	*entity.GetRestoreJobProgressRequest,
) (*entity.GetRestoreJobProgressResponse, error) {
	return &entity.GetRestoreJobProgressResponse{}, nil
}

type stubScanJobService struct {
	entity.UnimplementedScanJobServiceServer
	getProgress func(context.Context, *entity.GetScanJobProgressRequest) (*entity.GetScanJobProgressResponse, error)
	listEntries func(context.Context, *entity.ListScanJobEntriesRequest) (*entity.ListScanJobEntriesResponse, error)
}

func (s *stubScanJobService) GetProgress(
	ctx context.Context,
	request *entity.GetScanJobProgressRequest,
) (*entity.GetScanJobProgressResponse, error) {
	if s.getProgress != nil {
		return s.getProgress(ctx, request)
	}
	return &entity.GetScanJobProgressResponse{}, nil
}

func (s *stubScanJobService) ListEntries(
	ctx context.Context,
	request *entity.ListScanJobEntriesRequest,
) (*entity.ListScanJobEntriesResponse, error) {
	if s.listEntries != nil {
		return s.listEntries(ctx, request)
	}
	return &entity.ListScanJobEntriesResponse{}, nil
}

func newGRPCWebTestServer(
	t *testing.T,
	register func(*grpc.Server),
	files http.Handler,
	wrap func(http.Handler) http.Handler,
) (*httptest.Server, *rpcCallRecorder) {
	t.Helper()

	// Serve the same stripped gRPC-Web and file paths as the production binary.
	recorder := newRPCCallRecorder()
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(recorder.interceptor), grpc.StreamInterceptor(recorder.streamInterceptor))
	register(grpcServer)
	mux := http.NewServeMux()
	mux.Handle("/services/", http.StripPrefix("/services", grpcweb.WrapServer(grpcServer)))
	if files != nil {
		mux.Handle("/files/", http.StripPrefix("/files", files))
	}
	handler := http.Handler(mux)
	if wrap != nil {
		handler = wrap(handler)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
		grpcServer.Stop()
	})
	return server, recorder
}

func executeTestCLI(serverURL, input string, args ...string) (int, string, string) {
	allArgs := append([]string{"--server", serverURL}, args...)
	return executeRawTestCLI(input, allArgs...)
}

func executeRawTestCLI(input string, args ...string) (int, string, string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exit := run(args, strings.NewReader(input), &stdout, &stderr)
	return exit, stdout.String(), stderr.String()
}

func TestStatusUsesGRPCWebAndBasicAuth(t *testing.T) {
	// Require the same credentials on both HTTP and gRPC-Web requests.
	files := http.NewServeMux()
	files.HandleFunc("/ping", func(output http.ResponseWriter, _ *http.Request) {
		_, _ = output.Write([]byte(`{"result":"pong"}`))
	})
	server, recorder := newGRPCWebTestServer(
		t,
		func(server *grpc.Server) {
			entity.RegisterJobServiceServer(server, &stubJobService{list: func(
				_ context.Context,
				request *entity.ListJobsRequest,
			) (*entity.ListJobsResponse, error) {
				require.NotNil(t, request.Filter)
				require.NotNil(t, request.Filter.Limit)
				require.Equal(t, int64(1), *request.Filter.Limit)
				return &entity.ListJobsResponse{}, nil
			}})
		},
		files,
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
				user, password, ok := request.BasicAuth()
				if !ok || user != "agent" || password != "secret" {
					output.WriteHeader(http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(output, request)
			})
		},
	)

	// Load the password from a local file and use it for both transports.
	passwordFile := filepath.Join(t.TempDir(), "password")
	require.NoError(t, os.WriteFile(passwordFile, []byte("secret\n"), 0o600))
	exit, stdout, stderr := executeTestCLI(
		server.URL,
		"",
		"--basic-user", "agent",
		"--basic-password-file", passwordFile,
		"status",
	)
	require.Equal(t, exitSuccess, exit, stderr)
	require.JSONEq(t, `{"http":true,"grpc_web":true}`, stdout)
	require.Empty(t, stderr)
	require.Equal(t, 1, recorder.count(entity.JobService_List_FullMethodName))

	// Preserve HTTP authentication failures in the stable error envelope.
	exit, stdout, stderr = executeTestCLI(
		server.URL,
		"",
		"--basic-user", "other",
		"--basic-password-file", passwordFile,
		"status",
	)
	require.Equal(t, exitFailure, exit)
	require.Empty(t, stdout)
	var output errorOutput
	require.NoError(t, json.Unmarshal([]byte(stderr), &output))
	require.Equal(t, "http_401", output.Code)
	require.Equal(t, 1, recorder.count(entity.JobService_List_FullMethodName))
}

func TestStatusHonorsTimeout(t *testing.T) {
	// Hold the HTTP health response until the client deadline cancels it.
	files := http.NewServeMux()
	files.HandleFunc("/ping", func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	})
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{})
	}, files, nil)

	// Verify the timeout stops before the second transport check.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "--timeout", "10ms", "status")
	require.Equal(t, exitFailure, exit)
	require.Empty(t, stdout)
	var output errorOutput
	require.NoError(t, json.Unmarshal([]byte(stderr), &output))
	require.Equal(t, "deadline_exceeded", output.Code)
	require.Equal(t, 0, recorder.count(entity.JobService_List_FullMethodName))
}

func TestConnectionConfigurationPriority(t *testing.T) {
	// Start one healthy endpoint that can be selected by either configuration source.
	files := http.NewServeMux()
	files.HandleFunc("/ping", func(output http.ResponseWriter, _ *http.Request) {
		_, _ = output.Write([]byte(`{"result":"pong"}`))
	})
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, &stubJobService{})
	}, files, nil)

	// Environment values override defaults.
	t.Setenv("YATM_SERVER", server.URL)
	exit, _, stderr := executeRawTestCLI("", "status")
	require.Equal(t, exitSuccess, exit, stderr)

	// Explicit flags override the corresponding environment values.
	t.Setenv("YATM_SERVER", "http://127.0.0.1:1")
	exit, _, stderr = executeRawTestCLI("", "--server", server.URL, "status")
	require.Equal(t, exitSuccess, exit, stderr)
}

func TestJobProgressDispatchesByKind(t *testing.T) {
	// Cover every common Job kind and its one typed progress endpoint.
	tests := []struct {
		name           string
		kind           entity.JobKind
		progressMethod string
	}{
		{name: "archive", kind: entity.JobKind_JOB_KIND_ARCHIVE, progressMethod: entity.ArchiveJobService_GetProgress_FullMethodName},
		{name: "restore", kind: entity.JobKind_JOB_KIND_RESTORE, progressMethod: entity.RestoreJobService_GetProgress_FullMethodName},
		{name: "scan", kind: entity.JobKind_JOB_KIND_SCAN, progressMethod: entity.ScanJobService_GetProgress_FullMethodName},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Return the selected kind while registering every possible typed service.
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterJobServiceServer(server, &stubJobService{get: func(
					context.Context,
					*entity.GetJobRequest,
				) (*entity.GetJobResponse, error) {
					return &entity.GetJobResponse{Job: &entity.Job{Id: 17, Kind: test.kind}}, nil
				}})
				entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{})
				entity.RegisterRestoreJobServiceServer(server, &stubRestoreJobService{})
				entity.RegisterScanJobServiceServer(server, &stubScanJobService{})
			}, nil, nil)

			// Verify one common lookup leads to exactly one typed progress request.
			exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "progress", "17")
			require.Equal(t, exitSuccess, exit, stderr)
			require.JSONEq(t, `{}`, stdout)
			require.Equal(t, 1, recorder.count(entity.JobService_Get_FullMethodName))
			require.Equal(t, 1, recorder.count(test.progressMethod))
		})
	}
}

func TestUnexpectedArgumentsAreUsageErrors(t *testing.T) {
	exit, stdout, stderr := executeTestCLI("http://127.0.0.1:1", "", "status", "unexpected")
	require.Equal(t, exitUsage, exit)
	require.Empty(t, stdout)
	var output errorOutput
	require.NoError(t, json.Unmarshal([]byte(stderr), &output))
	require.Equal(t, "usage", output.Code)
}

func TestTimeoutZeroHasNoDeadline(t *testing.T) {
	runtime := &runtime{options: &options{Timeout: 0}}
	ctx, cancel := runtime.context()
	defer cancel()
	_, hasDeadline := ctx.Deadline()
	require.False(t, hasDeadline)
	require.NoError(t, ctx.Err())
}
