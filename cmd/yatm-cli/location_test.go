package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type stubLocationService struct {
	entity.UnimplementedLocationServiceServer
	created *entity.CreateLocationRequest
	list    *entity.ListLocationsRequest
}

func (s *stubLocationService) Create(_ context.Context, request *entity.CreateLocationRequest) (*entity.CreateLocationResponse, error) {
	s.created = proto.Clone(request).(*entity.CreateLocationRequest)
	return &entity.CreateLocationResponse{}, nil
}

func (s *stubLocationService) List(_ context.Context, request *entity.ListLocationsRequest) (*entity.ListLocationsResponse, error) {
	s.list = proto.Clone(request).(*entity.ListLocationsRequest)
	return &entity.ListLocationsResponse{}, nil
}

func TestLocationRecommendationListFilterPreservesExplicitFalse(t *testing.T) {
	service := &stubLocationService{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterLocationServiceServer(server, service)
	}, nil, nil)
	for _, value := range []string{"", "true", "false"} {
		args := []string{"location", "list", "--limit", "2", "--after-id", "8"}
		if value != "" {
			args = append(args, "--restore-target", value)
		}
		exit, _, stderr := executeTestCLI(server.URL, "", args...)
		require.Equal(t, exitSuccess, exit, stderr)
		require.EqualValues(t, 8, service.list.AfterId)
		if value == "" {
			require.Nil(t, service.list.RestoreTarget)
			continue
		}
		require.NotNil(t, service.list.RestoreTarget)
		require.Equal(t, value == "true", *service.list.RestoreTarget)
	}
}

type stubAnalyzeJobService struct {
	entity.UnimplementedScanJobServiceServer
	created *entity.CreateScanJobRequest
	page    *entity.ListScanJobEntriesRequest
}

func (s *stubAnalyzeJobService) Create(_ context.Context, request *entity.CreateScanJobRequest) (*entity.CreateScanJobResponse, error) {
	s.created = proto.Clone(request).(*entity.CreateScanJobRequest)
	return &entity.CreateScanJobResponse{}, nil
}

func (s *stubAnalyzeJobService) GetProgress(context.Context, *entity.GetScanJobProgressRequest) (*entity.GetScanJobProgressResponse, error) {
	return &entity.GetScanJobProgressResponse{}, nil
}

func (s *stubAnalyzeJobService) ListEntries(_ context.Context, request *entity.ListScanJobEntriesRequest) (*entity.ListScanJobEntriesResponse, error) {
	s.page = proto.Clone(request).(*entity.ListScanJobEntriesRequest)
	return &entity.ListScanJobEntriesResponse{}, nil
}

func TestLocationCommandsPreserveBindingAndPageArguments(t *testing.T) {
	// Exercise the real parser and transport, including verbatim Ignore text and revision-bound pages.
	sources, scanJobs := &stubLocationService{}, &stubAnalyzeJobService{}
	files := &filesCommandServer{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterLocationServiceServer(server, sources)
		entity.RegisterFilesServiceServer(server, files)
		entity.RegisterScanJobServiceServer(server, scanJobs)
	}, nil, nil)
	ignoreFile := filepath.Join(t.TempDir(), "ignore")
	require.NoError(t, os.WriteFile(ignoreFile, []byte("# comment\n/downloads/\nwork/logs\n!keep*\n"), 0600))
	commands := [][]string{
		{"location", "create", "--name", "Originals", "--root", "/source/originals", "--ignore-file", ignoreFile, "--use-mmap"},
		{"ls", "--location-id", "4", "--path", "photos", "--cursor", "observation-cursor", "--query", "jpg", "--limit", "7"},
		{"analyze", "create", "4", "--mode", "force", "--preview-policy", "regenerate-all"},
		{"analyze", "entries", "8", "--cursor", "9", "--limit", "6"},
	}
	for _, args := range commands {
		exit, _, stderr := executeTestCLI(server.URL, "", args...)
		require.Equal(t, exitSuccess, exit, stderr)
	}

	// Keep Ignore rules, source identity, and Preview policy independent from bare Source selections.
	require.True(t, proto.Equal(&entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: "/source/originals", Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "# comment\n/downloads/\nwork/logs\n!keep*\n"}, UseMmap: true}}}, sources.created))
	require.True(t, proto.Equal(&entity.SearchFilesRequest{Directory: locationReference(4, "photos"), Cursor: "observation-cursor", Query: "jpg", Limit: 7, Scope: entity.FileScope_FILE_SCOPE_ALL}, files.search.Load()))
	require.True(t, proto.Equal(&entity.ScanJobSpec{Selections: locationSelections(4, nil), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL}, scanJobs.created.Spec))
	require.True(t, proto.Equal(&entity.ListScanJobEntriesRequest{Id: 8, Cursor: "9", Limit: 6, Order: entity.JobResultOrder_JOB_RESULT_ORDER_ASCENDING}, scanJobs.page))
}

func TestArchiveCreateSelectsLibraryIdentityWithoutSourceResolution(t *testing.T) {
	// Capture selected IDs through the public CLI without registering any filesystem resolver.
	var created *entity.CreateArchiveJobRequest
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{create: func(_ context.Context, request *entity.CreateArchiveJobRequest) (*entity.CreateArchiveJobResponse, error) {
			created = proto.Clone(request).(*entity.CreateArchiveJobRequest)
			return &entity.CreateArchiveJobResponse{}, nil
		}})
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "archive", "create", "--file-id", "42", "--file-id", "43")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Len(t, created.Spec.Selections, 2)
	require.EqualValues(t, 42, created.Spec.Selections[0].GetLibrary().FileId)
	require.EqualValues(t, 43, created.Spec.Selections[1].GetLibrary().FileId)
}

func TestInvalidLocationOptionsFailBeforeNetworkRequests(t *testing.T) {
	// Invalid identity and policy combinations are rejected locally.
	commands := [][]string{
		{"archive", "create", "raw/path", "--file-id", "42"},
		{"analyze", "create", "4", "--preview-policy", "invalid"},
		{"location", "delete", "4", "--revision", "91"},
	}
	for _, args := range commands {
		exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
}
