package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestListProjectionFlagsUseOneRequest(t *testing.T) {
	// Empty include is a real lightweight request, not a detail response with fewer output fields.
	for _, tc := range []struct {
		name    string
		flags   []string
		include []entity.FilesInclude
	}{
		{name: "minimal"},
		{name: "attributes", flags: []string{"-l"}, include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES}},
		{name: "status", flags: []string{"--status"}, include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_STATUS}},
		{name: "both", flags: []string{"-l", "--status"}, include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS}},
		{name: "interactive", flags: []string{"--include", "operations", "--include", "navigation"}, include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_OPERATIONS, entity.FilesInclude_FILES_INCLUDE_NAVIGATION}},
		{name: "deduplicated", flags: []string{"-l", "--include", "attributes"}, include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &filesCommandServer{}
			server, recorder := newGRPCWebTestServer(t, func(s *grpc.Server) { entity.RegisterFilesServiceServer(s, service) }, nil, nil)
			args := append([]string{"ls", "--file-id", "0", "--query", "name:photo", "--recursive", "--cursor", "next", "--limit", "2"}, tc.flags...)
			exit, _, stderr := executeTestCLI(server.URL, "", args...)
			require.Equal(t, exitSuccess, exit, stderr)
			require.Equal(t, tc.include, service.search.Load().Include)
			require.True(t, service.search.Load().Recursive)
			require.Equal(t, "next", service.search.Load().Cursor)
			require.Equal(t, 1, recorder.count(entity.FilesService_Search_FullMethodName))
			require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))
			require.Zero(t, recorder.count(entity.PreviewService_Get_FullMethodName))
		})
	}
}

type previewReadServer struct {
	entity.UnimplementedPreviewServiceServer
	request *entity.GetPreviewRequest
}

func (s *previewReadServer) Get(_ context.Context, r *entity.GetPreviewRequest) (*entity.GetPreviewResponse, error) {
	s.request = proto.Clone(r).(*entity.GetPreviewRequest)
	return &entity.GetPreviewResponse{Availability: entity.PreviewAvailability_PREVIEW_AVAILABILITY_NOT_GENERATED}, nil
}

// previewIdentityServer answers the identity lookups the command resolves before a Preview read.
type previewIdentityServer struct {
	entity.UnimplementedFilesServiceServer
	detail  *entity.FilesDetail
	version *entity.FileVersion
}

func (s *previewIdentityServer) Get(context.Context, *entity.GetFileRequest) (*entity.GetFileResponse, error) {
	return &entity.GetFileResponse{Detail: s.detail}, nil
}

func (s *previewIdentityServer) GetVersion(context.Context, *entity.GetFileVersionRequest) (*entity.GetFileVersionResponse, error) {
	return &entity.GetFileVersionResponse{Version: s.version}, nil
}

func TestPreviewGetResolvesOneNamedIdentityToItsSignature(t *testing.T) {
	// Preview storage is content-addressed, so the command resolves whichever identity the
	// operator named into a signature and the read itself takes only that signature.
	signature := bytes.Repeat([]byte{0x7f}, library.SignatureSize)
	for _, tc := range []struct {
		name     string
		args     []string
		detail   *entity.FilesDetail
		version  *entity.FileVersion
		identity int
	}{
		{name: "signature", args: []string{"--signature", hex.EncodeToString(signature)}},
		{name: "file", args: []string{"--file-id", "7"}, detail: &entity.FilesDetail{ContentSignature: signature}, identity: 1},
		{name: "location", args: []string{"--location-id", "4", "--path", "photo.jpg"}, detail: &entity.FilesDetail{ContentSignature: signature}, identity: 1},
		{name: "version", args: []string{"--version-id", "9"}, version: &entity.FileVersion{Id: 9, Signature: signature}, identity: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &previewReadServer{}
			files := &previewIdentityServer{detail: tc.detail, version: tc.version}
			server, recorder := newGRPCWebTestServer(t, func(s *grpc.Server) {
				entity.RegisterPreviewServiceServer(s, service)
				entity.RegisterFilesServiceServer(s, files)
			}, nil, nil)
			exit, stdout, stderr := executeTestCLI(server.URL, "", append([]string{"preview", "get"}, tc.args...)...)
			require.Equal(t, exitSuccess, exit, stderr)
			require.Contains(t, stdout, "NOT_GENERATED")
			require.True(t, proto.Equal(&entity.GetPreviewRequest{Signature: signature}, service.request))
			require.Equal(t, 1, recorder.count(entity.PreviewService_Get_FullMethodName))
			require.Equal(t, tc.identity, recorder.count(entity.FilesService_Get_FullMethodName)+recorder.count(entity.FilesService_GetVersion_FullMethodName))
			require.Zero(t, recorder.count(entity.ScanJobService_Create_FullMethodName))
		})
	}
}

func TestPreviewGetRejectsAnIdentityItCannotResolve(t *testing.T) {
	signature := bytes.Repeat([]byte{0x7f}, library.SignatureSize)
	for _, tc := range []struct {
		name   string
		args   []string
		detail *entity.FilesDetail
	}{
		{name: "no identity", args: nil},
		{name: "two identities", args: []string{"--signature", hex.EncodeToString(signature), "--file-id", "7"}},
		{name: "unusable entry", args: []string{"--file-id", "7"}, detail: &entity.FilesDetail{}},
		{name: "unusable version", args: []string{"--version-id", "9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &previewReadServer{}
			files := &previewIdentityServer{detail: tc.detail, version: &entity.FileVersion{Id: 9}}
			server, recorder := newGRPCWebTestServer(t, func(s *grpc.Server) {
				entity.RegisterPreviewServiceServer(s, service)
				entity.RegisterFilesServiceServer(s, files)
			}, nil, nil)
			exit, _, stderr := executeTestCLI(server.URL, "", append([]string{"preview", "get"}, tc.args...)...)
			require.NotEqual(t, exitSuccess, exit)
			require.NotEmpty(t, stderr)
			require.Zero(t, recorder.count(entity.PreviewService_Get_FullMethodName))
		})
	}
}

func TestMetadataSupportsBatchWithoutLibraryDetailHydration(t *testing.T) {
	service := &filesCommandServer{}
	server, recorder := newGRPCWebTestServer(t, func(s *grpc.Server) { entity.RegisterFilesServiceServer(s, service) }, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "files", "metadata", "--file-id", "7", "--file-id", "8", "--location", "4:photo.jpg", "--note", "", "--add-tag", "kept")
	require.Equal(t, exitSuccess, exit, stderr)
	request := service.metadata.Load()
	require.Len(t, request.References, 3)
	require.EqualValues(t, 7, request.References[0].GetFileId())
	require.EqualValues(t, 8, request.References[1].GetFileId())
	require.Equal(t, "observed.txt", request.References[2].GetLocation().Path)
	require.Equal(t, 1, recorder.count(entity.FilesService_Get_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.FilesService_UpdateMetadata_FullMethodName))
}

type archiveEstimateServer struct {
	entity.UnimplementedArchiveJobServiceServer
	request *entity.EstimateArchiveJobRequest
}

func (s *archiveEstimateServer) Estimate(_ context.Context, r *entity.EstimateArchiveJobRequest) (*entity.EstimateArchiveJobResponse, error) {
	s.request = proto.Clone(r).(*entity.EstimateArchiveJobRequest)
	return &entity.EstimateArchiveJobResponse{Result: &entity.SelectionInspectionResult{}}, nil
}

func TestArchiveEstimateRetainsRootsWithoutDetailReads(t *testing.T) {
	// Business expansion belongs to Estimate, never to per-entry CLI hydration or List.
	service := &archiveEstimateServer{}
	server, recorder := newGRPCWebTestServer(t, func(s *grpc.Server) { entity.RegisterArchiveJobServiceServer(s, service) }, nil, nil)
	exit, _, stderr := executeTestCLI(server.URL, "", "archive", "estimate", "--file-id", "7", "--location", "4:photos")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Len(t, service.request.Selections, 2)
	require.EqualValues(t, 7, service.request.Selections[0].GetLibrary().FileId)
	require.Equal(t, "photos", service.request.Selections[1].GetLocation().Path)
	require.Equal(t, 1, recorder.count(entity.ArchiveJobService_Estimate_FullMethodName))
	require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))
	require.Zero(t, recorder.count(entity.FilesService_List_FullMethodName))
}
