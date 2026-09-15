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
	inspect  atomic.Pointer[entity.InspectFilesRequest]
	collect  atomic.Pointer[entity.CollectFilesRequest]
	metadata atomic.Pointer[entity.UpdateFilesMetadataRequest]
}

func (s *filesCommandServer) Get(_ context.Context, r *entity.GetFilesEntryRequest) (*entity.FilesEntry, error) {
	ref := r.Reference
	if source := ref.GetLocation(); source != nil {
		ref = locationReference(source.LocationId, source.Path)
		ref.GetLocation().BindingToken = "observed-binding"
	}
	content := locationReference(4, "current.txt")
	content.GetLocation().BindingToken = "content-binding"
	return &entity.FilesEntry{Reference: ref, Name: "file.txt", ContentReference: content}, nil
}
func (s *filesCommandServer) List(_ context.Context, r *entity.ListFilesRequest) (*entity.ListFilesReply, error) {
	s.list.Store(r)
	return &entity.ListFilesReply{Scope: r.Scope, NextCursor: "next-page"}, nil
}
func (s *filesCommandServer) Inspect(_ context.Context, r *entity.InspectFilesRequest) (*entity.InspectFilesReply, error) {
	s.inspect.Store(r)
	return &entity.InspectFilesReply{}, nil
}
func (s *filesCommandServer) Collect(_ context.Context, r *entity.CollectFilesRequest) (*entity.CollectFilesReply, error) {
	s.collect.Store(r)
	return &entity.CollectFilesReply{}, nil
}
func (s *filesCommandServer) UpdateMetadata(_ context.Context, r *entity.UpdateFilesMetadataRequest) (*entity.FilesEntry, error) {
	s.metadata.Store(r)
	return &entity.FilesEntry{Reference: r.Reference, File: &entity.File{Id: 42}}, nil
}

func TestFilesAndScanCommandsThroughCLIProcess(t *testing.T) {
	// Build the actual CLI once; each command runs through main, argument parsing and HTTP/RPC transport.
	binary := filepath.Join(t.TempDir(), "yatm-cli")
	build := exec.Command("go", "build", "-o", binary, ".")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	files, scan := &filesCommandServer{}, &verifyCommandServer{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
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
	run("files", "list", "--file-id", "0", "--scope", "saved", "--query", "tag:keep", "--cursor", "first", "--limit", "2", "--need-size")
	require.Equal(t, entity.FileScope_FILE_SCOPE_SAVED, files.list.Load().Scope)
	require.Equal(t, "tag:keep", files.list.Load().Query)
	require.Equal(t, "first", files.list.Load().Cursor)
	require.True(t, files.list.Load().NeedSize)
	require.Nil(t, files.collect.Load())
	run("files", "inspect", "--location", "4:a.txt", "--file-id", "17")
	require.Len(t, files.inspect.Load().References, 2)
	require.Equal(t, "observed-binding", files.inspect.Load().References[1].GetLocation().BindingToken)
	run("files", "collect", "--location", "4:a.txt", "--automatic")
	require.True(t, files.collect.Load().Automatic)
	run("files", "metadata", "--location-id", "4", "--path", "a.txt", "--note", "", "--add-tag", "keep")
	require.NotNil(t, files.metadata.Load().Note)
	require.Equal(t, "", *files.metadata.Load().Note)
	require.Equal(t, "observed-binding", files.metadata.Load().Reference.GetLocation().BindingToken)

	// Cache-only and Preview remain independent controls in one Scan request.
	run("scan", "create", "--location-id", "4", "--path", "photos", "--signature", "known-only", "--result", "originals", "--preview-policy", "missing-only", "--compare-library")
	require.Equal(t, entity.ScanSignaturePolicy_KNOWN_ONLY, scan.create.Load().Spec.SignaturePolicy)
	require.Equal(t, entity.ScanResultPolicy_PUBLISH_ORIGINALS, scan.create.Load().Spec.ResultPolicy)
	require.Equal(t, []string{"photos"}, scan.create.Load().Spec.Paths)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_MISSING_ONLY, scan.create.Load().Spec.PreviewPolicy)
	require.True(t, scan.create.Load().Spec.CompareLibrary)
	for _, test := range []struct {
		flag   string
		policy entity.PreviewPolicy
	}{
		{flag: "none", policy: entity.PreviewPolicy_PREVIEW_NONE},
		{flag: "regenerate-all", policy: entity.PreviewPolicy_PREVIEW_REGENERATE_ALL},
	} {
		run("scan", "create", "--location-id", "4", "--signature", "known-only", "--preview-policy", test.flag)
		require.Equal(t, test.policy, scan.create.Load().Spec.PreviewPolicy)
		require.Equal(t, entity.ScanSignaturePolicy_KNOWN_ONLY, scan.create.Load().Spec.SignaturePolicy)
	}
	run("preview", "create", "--file-id", "17")
	require.Equal(t, entity.PreviewPolicy_PREVIEW_MISSING_ONLY, scan.create.Load().Spec.PreviewPolicy)

	// Verification forces uncached reads without enabling Preview implicitly.
	run("scan", "create", "--media-id", "7", "--signature", "known-only", "--result", "verify")
	require.Equal(t, entity.ScanSignaturePolicy_FORCE_READ, scan.create.Load().Spec.SignaturePolicy)
	require.Equal(t, entity.ScanResultPolicy_VERIFY_COPIES, scan.create.Load().Spec.ResultPolicy)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_NONE, scan.create.Load().Spec.PreviewPolicy)
	run("scan", "create", "--file-id", "17", "--location", "4:photos", "--result", "report")
	require.Len(t, scan.create.Load().Spec.Selections, 2)
	run("scan", "run", "3", "--device", "/dev/test-device")
	require.Equal(t, "/dev/test-device", scan.run.Load().Target.GetTape().Device)
}

func TestSharedCommandsRejectInvalidPolicyBeforeNetwork(t *testing.T) {
	for _, args := range [][]string{
		{"files", "get", "--file-id", "1", "--location-id", "4"},
		{"files", "get", "--file-id", "1", "--path", "a"},
		{"files", "list", "--location-id", "4", "--limit", "501"},
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
