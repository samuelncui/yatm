package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type filesCommandServer struct {
	entity.UnimplementedFilesServiceServer
	list     atomic.Pointer[entity.ListFilesRequest]
	search   atomic.Pointer[entity.SearchFilesRequest]
	metadata atomic.Pointer[entity.UpdateFilesMetadataRequest]
}

func (s *filesCommandServer) Search(_ context.Context, r *entity.SearchFilesRequest) (*entity.SearchFilesResponse, error) {
	s.search.Store(r)
	return &entity.SearchFilesResponse{Scope: r.Scope, NextCursor: "next-page"}, nil
}

func (s *filesCommandServer) Get(_ context.Context, r *entity.GetFileRequest) (*entity.GetFileResponse, error) {
	ref := r.Reference
	if source := ref.GetLocation(); source != nil {
		// The resolved entry identity comes back from the server, not from the request.
		ref = locationReference(source.LocationId, "observed.txt")
	}
	content := locationReference(4, "current.txt")
	return &entity.GetFileResponse{Detail: &entity.FilesDetail{Entry: &entity.FilesEntry{Reference: ref, Name: "file.txt"}, ContentReference: content}}, nil
}
func (s *filesCommandServer) List(r *entity.ListFilesRequest, stream entity.FilesService_ListServer) error {
	s.list.Store(r)
	return stream.Send(&entity.ListFilesResponse{Scope: r.Scope})
}
func (s *filesCommandServer) UpdateMetadata(_ context.Context, r *entity.UpdateFilesMetadataRequest) (*entity.UpdateFilesMetadataResponse, error) {
	s.metadata.Store(r)
	return &entity.UpdateFilesMetadataResponse{Entries: []*entity.FilesDetail{{Entry: &entity.FilesEntry{Reference: r.References[0]}}}}, nil
}

func TestFilesAndScanCommandsThroughCLIProcess(t *testing.T) {
	// Build the actual CLI once; each command runs through main, argument parsing and HTTP/RPC transport.
	binary := filepath.Join(t.TempDir(), "yatm-cli")
	build := exec.Command("go", "build", "-o", binary, ".")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	files, scan := &filesCommandServer{}, &verifyCommandServer{}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, files)
		entity.RegisterScanJobServiceServer(server, scan)
	}, nil, nil)
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"--server", server.URL}, args...)...)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
		return output
	}

	// Pure listing keeps scope and query on the server, and does not collect entries.
	run("ls")
	require.Empty(t, files.list.Load().Include)
	require.EqualValues(t, 0, files.list.Load().Directory.GetFileId())
	require.Equal(t, 1, recorder.count(entity.FilesService_List_FullMethodName))
	require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))
	// A query switches the command to Search, which is what keeps a cursor and a page size.
	run("ls", "--file-id", "0", "--scope", "saved", "--query", "tag:keep", "--cursor", "first", "--limit", "2", "-l", "--status")
	require.Equal(t, entity.FileScope_FILE_SCOPE_SAVED, files.search.Load().Scope)
	require.Equal(t, "tag:keep", files.search.Load().Query)
	require.Equal(t, "first", files.search.Load().Cursor)
	require.Equal(t, int32(2), files.search.Load().Limit)
	require.Equal(t, []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS}, files.search.Load().Include)
	require.Equal(t, 1, recorder.count(entity.FilesService_List_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.FilesService_Search_FullMethodName))
	require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))
	run("files", "metadata", "--location", "4:a.txt", "--note", "", "--add-tag", "keep")
	require.NotNil(t, files.metadata.Load().Note)
	require.Equal(t, "", *files.metadata.Load().Note)
	require.Equal(t, "observed.txt", files.metadata.Load().References[0].GetLocation().Path)

	// Cache-only and Preview remain independent controls in one Scan request.
	run("scan", "create", "--location-id", "4", "--path", "photos", "--signature", "known-only", "--result", "originals", "--preview-policy", "missing-only", "--compare-library")
	require.Equal(t, entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY, scan.create.Load().Spec.SignaturePolicy)
	require.Equal(t, entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, scan.create.Load().Spec.ResultPolicy)
	require.Len(t, scan.create.Load().Spec.Selections, 1)
	require.EqualValues(t, 4, scan.create.Load().Spec.Selections[0].GetLocation().LocationId)
	require.Equal(t, "photos", scan.create.Load().Spec.Selections[0].GetLocation().Path)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY, scan.create.Load().Spec.PreviewPolicy)
	require.True(t, scan.create.Load().Spec.CompareLibrary)
	for _, test := range []struct {
		flag   string
		policy entity.PreviewPolicy
	}{
		{flag: "none", policy: entity.PreviewPolicy_PREVIEW_POLICY_NONE},
		{flag: "regenerate-all", policy: entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL},
	} {
		run("scan", "create", "--location-id", "4", "--signature", "known-only", "--preview-policy", test.flag)
		require.Equal(t, test.policy, scan.create.Load().Spec.PreviewPolicy)
		require.Equal(t, entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY, scan.create.Load().Spec.SignaturePolicy)
	}
	run("preview", "create", "--file-id", "17")
	require.Equal(t, entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY, scan.create.Load().Spec.PreviewPolicy)

	// Verification forces uncached reads without enabling Preview implicitly.
	run("scan", "create", "--media-id", "7", "--signature", "known-only", "--result", "verify")
	require.Equal(t, entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, scan.create.Load().Spec.SignaturePolicy)
	require.Equal(t, entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES, scan.create.Load().Spec.ResultPolicy)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_POLICY_NONE, scan.create.Load().Spec.PreviewPolicy)
	run("scan", "create", "--file-id", "17", "--location", "4:photos", "--result", "report")
	require.Len(t, scan.create.Load().Spec.Selections, 2)
	run("scan", "run", "3", "--device", "/dev/test-device")
	require.Equal(t, "/dev/test-device", scan.run.Load().Target.GetTape().Device)
}

func TestSharedCommandsRejectInvalidPolicyBeforeNetwork(t *testing.T) {
	for _, args := range [][]string{
		{"files", "get", "--file-id", "1", "--location-id", "4"},
		{"files", "get", "--file-id", "1", "--path", "a"},
		{"ls", "--location-id", "4", "--limit", "501", "--query", "type:file"},
		{"ls", "--location-id", "4", "--limit", "1"},
		{"ls", "--location-id", "4", "--cursor", "next"},
		{"files", "metadata", "--file-id", "1"},
		{"scan", "create", "--media-id", "7", "--location-id", "4"},
		{"scan", "create", "--file-id", "1", "--result", "verify"},
		{"scan", "create", "--media-id", "7", "--result", "originals"},
		{"scan", "create", "--file-id", "1", "--path", "extra"},
		{"scan", "create", "--file-id", "1", "--preview-policy", "invalid"},
	} {
		exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
}
