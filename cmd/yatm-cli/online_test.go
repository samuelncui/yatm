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
	page    *entity.ListLocationEntriesRequest
	list    *entity.ListLocationsRequest
}

func (s *stubLocationService) Create(_ context.Context, request *entity.CreateLocationRequest) (*entity.LocationReply, error) {
	s.created = proto.Clone(request).(*entity.CreateLocationRequest)
	return &entity.LocationReply{}, nil
}

func (s *stubLocationService) ListEntries(_ context.Context, request *entity.ListLocationEntriesRequest) (*entity.ListLocationEntriesReply, error) {
	s.page = proto.Clone(request).(*entity.ListLocationEntriesRequest)
	return &entity.ListLocationEntriesReply{}, nil
}

func (s *stubLocationService) List(_ context.Context, request *entity.ListLocationsRequest) (*entity.ListLocationsReply, error) {
	s.list = proto.Clone(request).(*entity.ListLocationsRequest)
	return &entity.ListLocationsReply{}, nil
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

func (s *stubAnalyzeJobService) Create(_ context.Context, request *entity.CreateScanJobRequest) (*entity.CreateScanJobReply, error) {
	s.created = proto.Clone(request).(*entity.CreateScanJobRequest)
	return &entity.CreateScanJobReply{}, nil
}

func (s *stubAnalyzeJobService) GetProgress(context.Context, *entity.GetScanJobProgressRequest) (*entity.GetScanJobProgressReply, error) {
	return &entity.GetScanJobProgressReply{}, nil
}

func (s *stubAnalyzeJobService) ListEntries(_ context.Context, request *entity.ListScanJobEntriesRequest) (*entity.ListScanJobEntriesReply, error) {
	s.page = proto.Clone(request).(*entity.ListScanJobEntriesRequest)
	return &entity.ListScanJobEntriesReply{}, nil
}

func TestOnlineCommandsPreserveBindingAndPageArguments(t *testing.T) {
	// Exercise the real parser and transport, including verbatim Ignore text and revision-bound pages.
	sources, syncJobs := &stubLocationService{}, &stubAnalyzeJobService{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterLocationServiceServer(server, sources)
		entity.RegisterScanJobServiceServer(server, syncJobs)
	}, nil, nil)
	ignoreFile := filepath.Join(t.TempDir(), "ignore")
	require.NoError(t, os.WriteFile(ignoreFile, []byte("# comment\n/downloads/\nwork/logs\n!keep*\n"), 0600))
	commands := [][]string{
		{"location", "create", "--name", "Originals", "--root", "/source/originals", "--ignore-file", ignoreFile},
		{"location", "entries", "4", "--parent", "photos/", "--cursor", "observation-cursor", "--name", "jpg", "--limit", "7"},
		{"analyze", "create", "4", "--mode", "force", "--preview-policy", "regenerate-all"},
		{"analyze", "entries", "8", "--after-id", "9", "--limit", "6"},
	}
	for _, args := range commands {
		exit, _, stderr := executeTestCLI(server.URL, "", args...)
		require.Equal(t, exitSuccess, exit, stderr)
	}

	// Keep Ignore rules, source identity, and Preview policy independent from bare Source selections.
	require.True(t, proto.Equal(&entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: "/source/originals", Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "# comment\n/downloads/\nwork/logs\n!keep*\n"}}}, sources.created))
	require.True(t, proto.Equal(&entity.ListLocationEntriesRequest{LocationId: 4, ParentPath: "photos/", Cursor: "observation-cursor", NameFilter: "jpg", Limit: 7}, sources.page))
	require.True(t, proto.Equal(&entity.ScanJobSpec{LocationId: 4, SignaturePolicy: entity.ScanSignaturePolicy_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, PreviewPolicy: entity.PreviewPolicy_PREVIEW_REGENERATE_ALL}, syncJobs.created.Spec))
	require.True(t, proto.Equal(&entity.ListScanJobEntriesRequest{Id: 8, AfterId: proto.Int64(9), Limit: 6}, syncJobs.page))
}

func TestArchiveCreateSelectsLibraryIdentityWithoutSourceResolution(t *testing.T) {
	// Capture selected IDs through the public CLI without registering any filesystem resolver.
	var created *entity.CreateArchiveJobRequest
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterArchiveJobServiceServer(server, &stubArchiveJobService{create: func(_ context.Context, request *entity.CreateArchiveJobRequest) (*entity.CreateArchiveJobReply, error) {
			created = proto.Clone(request).(*entity.CreateArchiveJobRequest)
			return &entity.CreateArchiveJobReply{}, nil
		}})
	}, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "archive", "create", "--file-id", "42", "--file-id", "43")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Len(t, created.Spec.Selections, 2)
	require.EqualValues(t, 42, created.Spec.Selections[0].GetLibrary().FileId)
	require.EqualValues(t, 43, created.Spec.Selections[1].GetLibrary().FileId)
	require.Empty(t, created.Spec.Sources)
}

func TestInvalidOnlineOptionsFailBeforeNetworkRequests(t *testing.T) {
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
