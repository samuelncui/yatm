package main

import (
	"context"
	"math"
	"strconv"
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
	*restoreInspectionService
}

func (s *restorePolicyService) Estimate(ctx context.Context, req *entity.EstimateRestoreJobRequest) (*entity.EstimateRestoreJobResponse, error) {
	return s.restoreInspectionService.Estimate(ctx, req)
}

func (s *restorePolicyService) Create(_ context.Context, req *entity.CreateRestoreJobRequest) (*entity.CreateRestoreJobResponse, error) {
	s.created = proto.Clone(req).(*entity.CreateRestoreJobRequest)
	return &entity.CreateRestoreJobResponse{}, nil
}

type restoreInspectionService struct {
	entity.UnimplementedRestoreJobServiceServer
	request *entity.EstimateRestoreJobRequest
}

func (s *restoreInspectionService) Estimate(_ context.Context, req *entity.EstimateRestoreJobRequest) (*entity.EstimateRestoreJobResponse, error) {
	s.request = proto.Clone(req).(*entity.EstimateRestoreJobRequest)
	return &entity.EstimateRestoreJobResponse{Result: &entity.SelectionInspectionResult{}}, nil
}

func TestRestorePolicyCommandsPreserveCutoffAndExplicitOverrides(t *testing.T) {
	// Capture real parser/transport requests at both review and creation boundaries.
	restore, inspection := &restorePolicyService{}, &restoreInspectionService{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		restore.restoreInspectionService = inspection
		entity.RegisterRestoreJobServiceServer(server, restore)
	}, nil, nil)
	before := "2026-09-01T12:30:00.123456789+08:00"
	cutoff, err := time.Parse(time.RFC3339Nano, before)
	require.NoError(t, err)
	stamp := cutoff.UnixNano()

	// Explicit versions are separate from automatic File selections; both retain the policy.
	exit, _, stderr := executeTestCLI(server.URL, "", "restore", "create", "42", "--file-id", "3",
		"--target-location", "7", "--before", before, "--skip-unmatched-versions")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, []int64{42}, restore.created.Spec.FileVersionIds)
	require.EqualValues(t, 3, restore.created.Spec.Selections[0].GetLibrary().FileId)
	require.Equal(t, &stamp, restore.created.Spec.VersionPolicy.BeforeAtNs)
	require.True(t, restore.created.Spec.SkipUnmatchedVersions)
	exit, _, stderr = executeTestCLI(server.URL, "", "restore", "estimate", "--file-id", "3",
		"--version-id", "42", "--before", before, "--skip-unmatched-versions")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, &stamp, inspection.request.VersionPolicy.BeforeAtNs)
	require.True(t, inspection.request.SkipUnmatchedVersions)
	require.Equal(t, []int64{42}, inspection.request.FileVersionIds)
}

func TestRestorePolicyRejectsAmbiguousOrMisplacedOptions(t *testing.T) {
	// Invalid policies fail before contacting an installation or creating a Job.
	for _, args := range [][]string{
		{"restore", "create", "--file-id", "3", "--target-location", "7", "--before", "2026-09-01"},
		{"restore", "create", "--file-id", "3", "--target-location", "7", "--before", "1600-09-01T00:00:00Z"},
		{"restore", "create", "--file-id", "3", "--target-location", "7", "--skip-unmatched-versions"},
		{"archive", "estimate", "--file-id", "3", "--before", "2026-09-01T00:00:00Z"},
		{"restore", "estimate", "--file-id", "3", "--skip-unmatched-versions"},
	} {
		exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
}

func TestRestorePolicySignedNanosecondBoundaries(t *testing.T) {
	// Review and creation receive the same exact cutoff, including a present epoch zero.
	restore, inspection := &restorePolicyService{}, &restoreInspectionService{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		restore.restoreInspectionService = inspection
		entity.RegisterRestoreJobServiceServer(server, restore)
	}, nil, nil)
	for _, stamp := range []int64{math.MinInt64, -1, 0, 1_234_567_890_123_456_789, math.MaxInt64} {
		t.Run(strconv.FormatInt(stamp, 10), func(t *testing.T) {
			before := time.Unix(0, stamp).UTC().Format(time.RFC3339Nano)
			exit, _, stderr := executeTestCLI(server.URL, "", "restore", "create", "--file-id", "3",
				"--target-location", "7", "--before", before)
			require.Equal(t, exitSuccess, exit, stderr)
			require.Equal(t, &stamp, restore.created.Spec.VersionPolicy.BeforeAtNs)
			exit, _, stderr = executeTestCLI(server.URL, "", "restore", "estimate", "--file-id", "3", "--before", before)
			require.Equal(t, exitSuccess, exit, stderr)
			require.Equal(t, &stamp, inspection.request.VersionPolicy.BeforeAtNs)
		})
	}
}

func TestRestorePolicyRejectsUnrepresentableDatesBeforeRPC(t *testing.T) {
	// A user-supplied date cannot use the missing-time sentinel or silently wrap beyond either endpoint.
	for _, before := range []string{
		time.Unix(0, math.MinInt64).Add(-time.Nanosecond).UTC().Format(time.RFC3339Nano),
		time.Unix(0, math.MaxInt64).Add(time.Nanosecond).UTC().Format(time.RFC3339Nano),
		"0001-01-01T00:00:00Z", "9999-12-31T23:59:59Z",
	} {
		for _, command := range []string{"create", "estimate"} {
			t.Run(command+"/"+before, func(t *testing.T) {
				args := []string{"restore", command, "--file-id", "3", "--before", before}
				if command == "create" {
					args = append(args, "--target-location", "7")
				}
				exit, stdout, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
				require.Equal(t, exitUsage, exit, stderr)
				require.Empty(t, stdout)
				require.Contains(t, stderr, "outside the signed Unix nanosecond range")
			})
		}
	}
}
