package main

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type restorePolicyService struct {
	entity.UnimplementedRestoreJobServiceServer
	created *entity.CreateRestoreJobRequest
}

func (s *restorePolicyService) Create(_ context.Context, req *entity.CreateRestoreJobRequest) (*entity.CreateRestoreJobReply, error) {
	s.created = proto.Clone(req).(*entity.CreateRestoreJobRequest)
	return &entity.CreateRestoreJobReply{}, nil
}

type restoreInspectionService struct {
	entity.UnimplementedFileCatalogServiceServer
	request *entity.InspectSelectionRequest
}

func (s *restoreInspectionService) InspectSelection(_ context.Context, req *entity.InspectSelectionRequest) (*entity.InspectSelectionReply, error) {
	s.request = proto.Clone(req).(*entity.InspectSelectionRequest)
	return &entity.InspectSelectionReply{}, nil
}

func TestRestorePolicyCommandsPreserveCutoffAndExplicitOverrides(t *testing.T) {
	// Capture real parser/transport requests at both review and creation boundaries.
	restore, inspection := &restorePolicyService{}, &restoreInspectionService{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterRestoreJobServiceServer(server, restore)
		entity.RegisterFileCatalogServiceServer(server, inspection)
	}, nil, nil)
	before := "2026-09-01T12:30:00+08:00"
	cutoff, err := time.Parse(time.RFC3339, before)
	require.NoError(t, err)
	stamp := cutoff.UnixMilli()

	// Explicit versions are separate from automatic File selections; both retain the policy.
	exit, _, stderr := executeTestCLI(server.URL, "", "restore", "create", "42", "--file-id", "3",
		"--target-location", "7", "--before", before, "--skip-unmatched-versions")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, []int64{42}, restore.created.Spec.FileVersionIds)
	require.EqualValues(t, 3, restore.created.Spec.Selections[0].GetLibrary().FileId)
	require.Equal(t, &stamp, restore.created.Spec.VersionPolicy.BeforeAtMs)
	require.True(t, restore.created.Spec.SkipUnmatchedVersions)
	exit, _, stderr = executeTestCLI(server.URL, "", "file", "inspect-selection", "--restore", "--file-id", "3",
		"--version-id", "42", "--before", before, "--skip-unmatched-versions")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, &stamp, inspection.request.VersionPolicy.BeforeAtMs)
	require.True(t, inspection.request.SkipUnmatchedVersions)
	require.Equal(t, []int64{42}, inspection.request.FileVersionIds)
}

func TestRestorePolicyRejectsAmbiguousOrMisplacedOptions(t *testing.T) {
	// Invalid policies fail before contacting an installation or creating a Job.
	for _, args := range [][]string{
		{"restore", "create", "--file-id", "3", "--target-location", "7", "--before", "2026-09-01"},
		{"restore", "create", "--file-id", "3", "--target-location", "7", "--before", "1960-09-01T00:00:00Z"},
		{"restore", "create", "--file-id", "3", "--target-location", "7", "--skip-unmatched-versions"},
		{"file", "inspect-selection", "--file-id", "3", "--before", "2026-09-01T00:00:00Z"},
		{"file", "inspect-selection", "--restore", "--file-id", "3", "--skip-unmatched-versions"},
	} {
		exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
}
