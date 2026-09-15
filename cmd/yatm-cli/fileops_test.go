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
	"google.golang.org/protobuf/encoding/protojson"
)

type fileOperationLocationStub struct {
	entity.UnimplementedFilesServiceServer
}

func (*fileOperationLocationStub) Get(_ context.Context, request *entity.GetFilesEntryRequest) (*entity.FilesEntry, error) {
	ref := request.Reference.GetLocation()
	return &entity.FilesEntry{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: ref.LocationId, Path: ref.Path, BindingToken: "binding"}}}}, nil
}

type fileOperationStub struct {
	entity.UnimplementedFileOperationServiceServer
	execute func(*entity.ExecuteFileOperationRequest, entity.FileOperationService_ExecuteServer) error
}

func (s *fileOperationStub) Execute(request *entity.ExecuteFileOperationRequest, stream entity.FileOperationService_ExecuteServer) error {
	return s.execute(request, stream)
}

func TestFileOperationCLIStreamsResultsWithoutJobs(t *testing.T) {
	// Every mutation resolves live references, streams results and exits without any Job calls.
	cases := []struct {
		name        string
		kind        entity.FileOperationKind
		sources     bool
		destination bool
	}{
		{"move", entity.FileOperationKind_MOVE, true, true},
		{"mkdir", entity.FileOperationKind_MAKE_DIRECTORY, false, true},
		{"delete", entity.FileOperationKind_DELETE, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterFilesServiceServer(server, &fileOperationLocationStub{})
				entity.RegisterFileOperationServiceServer(server, &fileOperationStub{execute: func(request *entity.ExecuteFileOperationRequest, stream entity.FileOperationService_ExecuteServer) error {
					require.Equal(t, tc.kind, request.Spec.Kind)
					require.Equal(t, tc.sources, len(request.Spec.Sources) > 0)
					if tc.sources {
						require.EqualValues(t, 4, request.Spec.Sources[0].GetLocation().LocationId)
						require.Equal(t, "source.txt", request.Spec.Sources[0].GetLocation().Path)
					}
					require.Equal(t, tc.destination, request.Spec.Destination != nil)
					if tc.destination {
						require.Equal(t, "", request.Spec.Destination.GetLocation().Path)
						require.Equal(t, "binding", request.Spec.Destination.GetLocation().BindingToken)
					}
					require.Equal(t, tc.name == "delete", request.ConfirmDelete)
					if err := stream.Send(&entity.FileOperationUpdate{Entry: &entity.FileOperationEntry{Id: 1, SourcePath: "source.txt", Outcome: entity.FileOperationOutcome_SUCCEEDED}}); err != nil {
						return err
					}
					return stream.Send(&entity.FileOperationUpdate{Summary: &entity.FileOperationSummary{Completed: true, TotalItems: 1, Succeeded: 1}})
				}})
			}, nil, nil)
			args := []string{"fileops", "run", "--kind", tc.name, "--location", "4"}
			expectedLookups := 0
			if tc.sources {
				args = append(args, "--source", "source.txt")
				expectedLookups++
			}
			if tc.destination {
				args = append(args, "--destination", ".", "--name", "output")
				expectedLookups++
			}
			if tc.name == "delete" {
				args = append(args, "--confirm-delete")
			}
			exit, stdout, stderr := executeTestCLI(server.URL, "", args...)
			require.Equal(t, exitSuccess, exit, stderr)
			lines := strings.Split(strings.TrimSpace(stdout), "\n")
			require.Len(t, lines, 2)
			result := new(entity.FileOperationUpdate)
			require.NoError(t, protojson.Unmarshal([]byte(lines[1]), result))
			require.True(t, result.Summary.Completed)
			require.EqualValues(t, 1, result.Summary.Succeeded)
			require.Equal(t, expectedLookups, recorder.count(entity.FilesService_Get_FullMethodName))
			require.Zero(t, recorder.count(entity.JobService_Get_FullMethodName))
			require.Zero(t, recorder.count(entity.JobService_List_FullMethodName))
		})
	}
}

func TestFileOperationCLIIncompleteResultsFail(t *testing.T) {
	// Completed describes a settled stream, not an assertion that every item succeeded.
	cases := []struct {
		name    string
		summary *entity.FileOperationSummary
	}{
		{name: "failed", summary: &entity.FileOperationSummary{Completed: true, Failed: 1}},
		{name: "unprocessed", summary: &entity.FileOperationSummary{Completed: true, Unprocessed: 1}},
		{name: "publication", summary: &entity.FileOperationSummary{Completed: true, PublicationPending: 1}},
		{name: "unfinished", summary: &entity.FileOperationSummary{Succeeded: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterFileOperationServiceServer(server, &fileOperationStub{execute: func(_ *entity.ExecuteFileOperationRequest, stream entity.FileOperationService_ExecuteServer) error {
					return stream.Send(&entity.FileOperationUpdate{Summary: tc.summary})
				}})
			}, nil, nil)
			exit, stdout, stderr := executeTestCLI(server.URL, "", "fileops", "run", "--kind", "mkdir", "--location", "4")
			require.Equal(t, exitFailure, exit)
			require.NotEmpty(t, stdout)
			require.Contains(t, stderr, "incomplete")
		})
	}
}

func TestFileOperationCLIDeadlineCancelsServer(t *testing.T) {
	// The request deadline reaches active work; there is no detached execution after timeout.
	stopped := make(chan struct{})
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFileOperationServiceServer(server, &fileOperationStub{execute: func(_ *entity.ExecuteFileOperationRequest, stream entity.FileOperationService_ExecuteServer) error {
			<-stream.Context().Done()
			close(stopped)
			return stream.Context().Err()
		}})
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "--timeout", "100ms", "fileops", "run", "--kind", "mkdir", "--location", "4")
	require.Equal(t, exitFailure, exit)
	require.Contains(t, stderr, "deadline_exceeded")
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("file operation continued after request deadline")
	}
}

func TestFileOperationCLIDeleteRequiresExplicitConfirmation(t *testing.T) {
	// No server lookup occurs before automation explicitly authorizes permanent deletion.
	exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", "fileops", "run", "--kind", "delete", "--location", "4", "--source", "file.txt")
	require.Equal(t, exitUsage, exit)
	require.Contains(t, stderr, "--confirm-delete")
}

func TestLibraryOrganizationCLIUsesSharedExecution(t *testing.T) {
	// Familiar Library commands and explicit fileops requests send the same typed operation contract.
	cases := []struct {
		name        string
		args        []string
		kind        entity.FileOperationKind
		source      int64
		destination int64
		leaf        string
		lookups     int
	}{
		{name: "rename", args: []string{"file", "edit", "7", "--name", "nested/renamed.txt"}, kind: entity.FileOperationKind_MOVE, source: 7, destination: 3, leaf: "nested/renamed.txt", lookups: 1},
		{name: "move", args: []string{"file", "edit", "7", "--parent-id", "0"}, kind: entity.FileOperationKind_MOVE, source: 7},
		{name: "mkdir", args: []string{"file", "mkdir", "0", "nested/directory"}, kind: entity.FileOperationKind_MAKE_DIRECTORY, leaf: "nested/directory"},
		{name: "explicit-move", args: []string{"fileops", "run", "--library", "--kind", "move", "--source", "7", "--destination", "0"}, kind: entity.FileOperationKind_MOVE, source: 7},
		{name: "explicit-mkdir", args: []string{"fileops", "run", "--library", "--kind", "mkdir", "--destination", "0", "--name", "nested/directory"}, kind: entity.FileOperationKind_MAKE_DIRECTORY, leaf: "nested/directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Resolve a name-only edit's parent and capture execution without registering any Job service.
			var executions atomic.Int64
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterServiceServer(server, &stubService{fileGet: func(_ context.Context, request *entity.FileGetRequest) (*entity.FileGetReply, error) {
					return &entity.FileGetReply{File: &entity.File{Id: request.Id, ParentId: 3}}, nil
				}})
				entity.RegisterFileOperationServiceServer(server, &fileOperationStub{execute: func(request *entity.ExecuteFileOperationRequest, stream entity.FileOperationService_ExecuteServer) error {
					executions.Add(1)
					require.Equal(t, tc.kind, request.Spec.Kind)
					require.Equal(t, tc.leaf, request.Spec.Name)
					require.IsType(t, &entity.FileOperationRef_FileId{}, request.Spec.Destination.Target)
					require.Equal(t, tc.destination, request.Spec.Destination.GetFileId())
					require.Nil(t, request.Spec.Destination.GetLocation())
					if tc.source == 0 {
						require.Empty(t, request.Spec.Sources)
					} else {
						require.Len(t, request.Spec.Sources, 1)
						require.Equal(t, tc.source, request.Spec.Sources[0].GetFileId())
						require.Nil(t, request.Spec.Sources[0].GetLocation())
					}
					fileID := int64(7)
					if err := stream.Send(&entity.FileOperationUpdate{Entry: &entity.FileOperationEntry{Id: 1, FileId: &fileID, Outcome: entity.FileOperationOutcome_SUCCEEDED}}); err != nil {
						return err
					}
					return stream.Send(&entity.FileOperationUpdate{Summary: &entity.FileOperationSummary{Completed: true, TotalItems: 1, Succeeded: 1}})
				}})
			}, nil, nil)

			// Both command forms expose the resulting File identity and one settled execution stream.
			exit, stdout, stderr := executeTestCLI(server.URL, "", tc.args...)
			require.Equal(t, exitSuccess, exit, stderr)
			lines := strings.Split(strings.TrimSpace(stdout), "\n")
			require.Len(t, lines, 2)
			entry := new(entity.FileOperationUpdate)
			require.NoError(t, protojson.Unmarshal([]byte(lines[0]), entry))
			require.EqualValues(t, 7, entry.Entry.GetFileId())
			require.EqualValues(t, 1, executions.Load())
			require.Equal(t, tc.lookups, recorder.count(entity.Service_FileGet_FullMethodName))
			require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))
			require.Zero(t, recorder.count(entity.JobService_Get_FullMethodName))
		})
	}
}

func TestFileOperationCLIValidatesNamespaceBeforeRequests(t *testing.T) {
	// Scope, logical identity and destructive authorization errors must not reach a server.
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing-scope", args: []string{"fileops", "run", "--kind", "mkdir"}, want: "exactly one"},
		{name: "both-scopes", args: []string{"fileops", "run", "--kind", "mkdir", "--library", "--location", "4"}, want: "exactly one"},
		{name: "library-copy", args: []string{"fileops", "run", "--kind", "copy", "--library"}, want: "copy"},
		{name: "root-source", args: []string{"fileops", "run", "--kind", "move", "--library", "--source", "0", "--destination", "4"}, want: "must be positive"},
		{name: "path-as-id", args: []string{"fileops", "run", "--kind", "move", "--library", "--source", "file.txt"}, want: "invalid Library File ID"},
		{name: "library-delete", args: []string{"fileops", "run", "--kind", "delete", "--library", "--source", "7"}, want: "--confirm-delete"},
		{name: "empty-rename", args: []string{"file", "edit", "7", "--name", " "}, want: "file name is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", tc.args...)
			require.Equal(t, exitUsage, exit)
			require.Contains(t, stderr, tc.want)
		})
	}
}

func TestLibraryRenameDoesNotMutateMissingFile(t *testing.T) {
	// A missing name-only source must not invent a root destination and execute the edit.
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterServiceServer(server, &stubService{})
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "file", "edit", "7", "--name", "renamed.txt")
	require.Equal(t, exitFailure, exit)
	require.Contains(t, stderr, "not_found")
	require.Zero(t, recorder.count(entity.FileOperationService_Execute_FullMethodName))
}
