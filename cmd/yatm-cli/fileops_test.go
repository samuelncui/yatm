package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type operationTestStream interface {
	Context() context.Context
	Send(*entity.FileOperationResult) error
}
type operationTestStreamAdapter struct {
	ctx  context.Context
	send func(*entity.FileOperationResult) error
}

func (s operationTestStreamAdapter) Context() context.Context { return s.ctx }
func (s operationTestStreamAdapter) Send(result *entity.FileOperationResult) error {
	return s.send(result)
}

type fileOperationStub struct {
	entity.UnimplementedFilesServiceServer
	execute func(*entity.FileOperationSpec, operationTestStream) error
	get     func(context.Context, *entity.GetFileRequest) (*entity.FilesDetail, error)
}

func (s *fileOperationStub) Get(ctx context.Context, request *entity.GetFileRequest) (*entity.GetFileResponse, error) {
	if s.get != nil {
		detail, err := s.get(ctx, request)
		return &entity.GetFileResponse{Detail: detail}, err
	}
	ref := request.Reference.GetLocation()
	return &entity.GetFileResponse{Detail: &entity.FilesDetail{Entry: &entity.FilesEntry{Reference: locationReference(ref.LocationId, ref.Path)}}}, nil
}
func (s *fileOperationStub) Mkdir(r *entity.MkdirFilesRequest, stream entity.FilesService_MkdirServer) error {
	return s.execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR, Destination: r.Destination, Name: r.Name}, operationTestStreamAdapter{stream.Context(), func(result *entity.FileOperationResult) error {
		return stream.Send(&entity.MkdirFilesResponse{Result: result})
	}})
}
func (s *fileOperationStub) Move(r *entity.MoveFilesRequest, stream entity.FilesService_MoveServer) error {
	return s.execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MOVE, Sources: r.Sources, Destination: r.Destination, Name: r.Name}, operationTestStreamAdapter{stream.Context(), func(result *entity.FileOperationResult) error {
		return stream.Send(&entity.MoveFilesResponse{Result: result})
	}})
}
func (s *fileOperationStub) Remove(r *entity.RemoveFilesRequest, stream entity.FilesService_RemoveServer) error {
	return s.execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, Sources: r.Sources}, operationTestStreamAdapter{stream.Context(), func(result *entity.FileOperationResult) error {
		return stream.Send(&entity.RemoveFilesResponse{Result: result})
	}})
}
func TestFileOperationCLIStreamsResultsWithoutJobs(t *testing.T) {
	// Named operations use the same request-scoped stream for both providers.
	for _, library := range []bool{false, true} {
		for _, tc := range []struct {
			name                 string
			kind                 entity.FileOperationKind
			sources, destination bool
		}{
			{"mv", entity.FileOperationKind_FILE_OPERATION_KIND_MOVE, true, true},
			{"mkdir", entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR, false, true},
			{"rm", entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, true, false},
		} {
			t.Run(tc.name+map[bool]string{true: "Library", false: "Location"}[library], func(t *testing.T) {
				var calls atomic.Int64
				service := &fileOperationStub{execute: func(r *entity.FileOperationSpec, stream operationTestStream) error {
					calls.Add(1)
					require.Equal(t, tc.kind, r.Kind)
					require.Equal(t, tc.sources, len(r.Sources) > 0)
					require.Equal(t, tc.destination, r.Destination != nil)
					if library && tc.sources {
						require.EqualValues(t, 7, r.Sources[0].GetFileId())
					}
					if !library && tc.sources {
						require.Equal(t, "source.txt", r.Sources[0].GetLocation().Path)
					}
					if tc.destination {
						require.Equal(t, "output", r.Name)
					}
					if err := stream.Send(&entity.FileOperationResult{Entry: &entity.FileOperationEntry{Outcome: entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_SUCCEEDED}}); err != nil {
						return err
					}
					return stream.Send(&entity.FileOperationResult{Summary: &entity.FileOperationSummary{Completed: true, TotalItemCount: 1, SucceededCount: 1}})
				}}
				server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) { entity.RegisterFilesServiceServer(server, service) }, nil, nil)
				args := []string{tc.name}
				source, destination := "source.txt", "."
				if library {
					args = append(args, "--library")
					source, destination = "7", "0"
				} else {
					args = append(args, "--location", "4")
				}
				lookups := 0
				if tc.sources {
					args = append(args, "--source", source)
					lookups++
				}
				if tc.destination {
					args = append(args, "--destination", destination, "--name", "output")
					lookups++
				}
				exit, stdout, stderr := executeTestCLI(server.URL, "", args...)
				require.Equal(t, exitSuccess, exit, stderr)
				require.Len(t, strings.Split(strings.TrimSpace(stdout), "\n"), 2)
				require.EqualValues(t, 1, calls.Load())
				if library {
					lookups = 0
				}
				require.Equal(t, lookups, recorder.count(entity.FilesService_Get_FullMethodName))
				require.Zero(t, recorder.count(entity.JobService_Get_FullMethodName))
				require.Zero(t, recorder.count(entity.JobService_List_FullMethodName))
			})
		}
	}
}
func TestFileOperationCLIIncompleteResultsFail(t *testing.T) {
	// Settlement does not imply all items succeeded or were published.
	for _, summary := range []*entity.FileOperationSummary{
		{Completed: true, FailedCount: 1}, {Completed: true, UnprocessedCount: 1}, {Completed: true, PublicationPendingCount: 1}, {},
	} {
		server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
			entity.RegisterFilesServiceServer(server, &fileOperationStub{execute: func(_ *entity.FileOperationSpec, s operationTestStream) error {
				return s.Send(&entity.FileOperationResult{Summary: summary})
			}})
		}, nil, nil)
		exit, _, stderr := executeTestCLI(server.URL, "", "mkdir", "--library", "--destination", "0", "--name", "new")
		require.Equal(t, exitFailure, exit, stderr)
		require.Contains(t, stderr, "incomplete")
	}
}
func TestFileOperationCLIDeadlineCancelsServer(t *testing.T) {
	stopped := make(chan struct{})
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, &fileOperationStub{execute: func(_ *entity.FileOperationSpec, s operationTestStream) error {
			<-s.Context().Done()
			close(stopped)
			return s.Context().Err()
		}})
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "--timeout", "100ms", "mkdir", "--library", "--destination", "0", "--name", "new")
	require.Equal(t, exitFailure, exit)
	require.Contains(t, stderr, "deadline_exceeded")
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("operation continued after deadline")
	}
}
func TestFileOperationCLIValidatesIntentBeforeAnyRequest(t *testing.T) {
	// A bare invocation writes, so intent validation is the only gate left before the request.
	for _, args := range [][]string{
		{"mkdir"}, {"mkdir", "--library", "--location", "4"}, {"mv", "--library", "--source", "0", "--destination", "4"},
		{"mv", "--library", "--source", "file.txt", "--destination", "0"},
		{"mkdir", "--library", "--destination", "0", "--name", ""}, {"rm", "--library", "--source", "7", "--name", "x"},
		{"mv", "--library", "--source", "7"}, {"rm", "--library", "--source", "7", "-f"},
		{"fileops", "run", "--kind", "copy", "--library"},
	} {
		exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
}

func TestLocationRenameDoesNotMutateMissingFile(t *testing.T) {
	var mutated atomic.Bool
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, &fileOperationStub{
			get: func(context.Context, *entity.GetFileRequest) (*entity.FilesDetail, error) {
				return nil, status.Error(codes.NotFound, "missing original")
			},
			execute: func(*entity.FileOperationSpec, operationTestStream) error { mutated.Store(true); return nil },
		})
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "mv", "--location", "4", "--source", "missing", "--destination", ".", "--name", "renamed")
	require.Equal(t, exitFailure, exit)
	require.Contains(t, stderr, "not_found")
	require.False(t, mutated.Load())
}

func TestFileOperationCommandsCarryTheDryRunDecision(t *testing.T) {
	// The request owns the dry-run decision for mv, mkdir and rm alike.
	for _, tc := range []struct {
		name          string
		kind          entity.FileOperationKind
		sources, dest bool
		expectedArgs  []string
	}{
		{"mv", entity.FileOperationKind_FILE_OPERATION_KIND_MOVE, true, true, []string{"mv", "--library", "--source", "7", "--destination", "0", "--dryrun"}},
		{"mkdir", entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR, false, true, []string{"mkdir", "--library", "--destination", "0", "--name", "new", "--dryrun"}},
		{"rm", entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, true, false, []string{"rm", "--library", "--source", "7", "--dryrun"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fileOperationStub{execute: func(r *entity.FileOperationSpec, stream operationTestStream) error {
				require.Equal(t, tc.kind, r.Kind)
				return stream.Send(&entity.FileOperationResult{Summary: &entity.FileOperationSummary{Completed: true, Dryrun: true, TotalItemCount: 3, UnprocessedCount: 3}})
			}}
			server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterFilesServiceServer(server, service)
			}, nil, nil)
			exit, stdout, stderr := executeTestCLI(server.URL, "", tc.expectedArgs...)
			require.Equal(t, exitSuccess, exit, stderr)
			require.Contains(t, stdout, `"dryrun":true`)
		})
	}
}

func TestFileOperationDryRunKeepsPlanningFailures(t *testing.T) {
	// A failed preflight remains a failure even though every dry-run entry is unprocessed.
	service := &fileOperationStub{execute: func(_ *entity.FileOperationSpec, stream operationTestStream) error {
		if err := stream.Send(&entity.FileOperationResult{Entry: &entity.FileOperationEntry{
			Outcome: entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_UNPROCESSED, Error: "target already exists"}}); err != nil {
			return err
		}
		return stream.Send(&entity.FileOperationResult{Summary: &entity.FileOperationSummary{
			Completed: true, Dryrun: true, TotalItemCount: 1, UnprocessedCount: 1}})
	}}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, service)
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "mv", "--library", "--source", "7", "--destination", "0", "--dryrun")
	require.NotEqual(t, exitSuccess, exit)
	require.Contains(t, stderr, "target already exists")
}

type keepDryRunStub struct {
	fileOperationStub
}

func (s *keepDryRunStub) KeepIdentical(req *entity.KeepIdenticalRequest, stream entity.FilesService_KeepIdenticalServer) error {
	return stream.Send(&entity.KeepIdenticalResponse{Result: &entity.FileOperationResult{
		Summary: &entity.FileOperationSummary{Completed: true, Dryrun: req.Dryrun, TotalItemCount: 4, UnprocessedCount: 4},
	}})
}

func TestKeepDryRunSucceedsWithPlannedRemovals(t *testing.T) {
	// Keep uses the same CLI result interpretation as ordinary Remove and never claims planned files were deleted.
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, &keepDryRunStub{})
	}, nil, nil)
	exit, stdout, stderr := executeTestCLI(server.URL, "", "identical", "keep", "--source", "locations", "--root", "7", "--group", "1",
		"--fingerprint", "group-facts", "--keep-location", "7", "--keep-path", "kept.txt", "--dryrun")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, `"unprocessed_count":"4"`)
}
