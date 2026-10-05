package main

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type identicalCommandStub struct {
	entity.UnimplementedFilesServiceServer
	find      *entity.FindIdenticalRequest
	rows      *entity.ListIdenticalRowsRequest
	positions *entity.LookupIdenticalPositionsRequest
	close     *entity.CloseIdenticalResultRequest
	groups    *entity.ListIdenticalGroupsRequest
	members   *entity.ListIdenticalMembersRequest
	merge     *entity.MergeIdenticalRequest
	remove    *entity.RemoveFileVersionRequest
}

func (s *identicalCommandStub) FindIdentical(_ context.Context, request *entity.FindIdenticalRequest) (*entity.FindIdenticalResponse, error) {
	s.find = proto.Clone(request).(*entity.FindIdenticalRequest)
	return &entity.FindIdenticalResponse{ResultId: "result", AllRowCount: 7}, nil
}

func (s *identicalCommandStub) ListIdenticalRows(_ context.Context, request *entity.ListIdenticalRowsRequest) (*entity.ListIdenticalRowsResponse, error) {
	s.rows = proto.Clone(request).(*entity.ListIdenticalRowsRequest)
	return &entity.ListIdenticalRowsResponse{TotalRowCount: 7}, nil
}

func (s *identicalCommandStub) LookupIdenticalPositions(_ context.Context, request *entity.LookupIdenticalPositionsRequest) (*entity.LookupIdenticalPositionsResponse, error) {
	s.positions = proto.Clone(request).(*entity.LookupIdenticalPositionsRequest)
	return &entity.LookupIdenticalPositionsResponse{}, nil
}

func (s *identicalCommandStub) CloseIdenticalResult(_ context.Context, request *entity.CloseIdenticalResultRequest) (*entity.CloseIdenticalResultResponse, error) {
	s.close = proto.Clone(request).(*entity.CloseIdenticalResultRequest)
	return &entity.CloseIdenticalResultResponse{}, nil
}

func (s *identicalCommandStub) ListIdenticalGroups(_ context.Context, request *entity.ListIdenticalGroupsRequest) (*entity.ListIdenticalGroupsResponse, error) {
	s.groups = proto.Clone(request).(*entity.ListIdenticalGroupsRequest)
	return &entity.ListIdenticalGroupsResponse{Groups: []*entity.IdenticalGroup{{Id: "group", Fingerprint: "fingerprint", MemberCount: 3}}}, nil
}

func TestIdenticalResultCommandsUseOneFindResult(t *testing.T) {
	// A reusable result ID reaches every page and explicit release command unchanged.
	service := &identicalCommandStub{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, service)
	}, nil, nil)

	exit, stdout, stderr := executeTestCLI(server.URL, "", "identical", "find", "--source", "locations", "--root", "7")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "result")
	require.Equal(t, entity.IdenticalSource_IDENTICAL_SOURCE_LOCATIONS, service.find.GetScope().GetSource())

	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "rows", "--result", "result", "--offset", "20", "--limit", "10", "--include-hidden")
	require.Equal(t, exitSuccess, exit, stderr)
	require.True(t, proto.Equal(&entity.ListIdenticalRowsRequest{ResultId: "result", Offset: 20, Limit: 10, IncludeHidden: true}, service.rows))
	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "rows", "--result", "result", "--sort-key", "name", "--order", "desc")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME, service.rows.GetSortKey())
	require.Equal(t, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC, service.rows.GetSortOrder())
	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "rows", "--result", "result", "--sort-key", "size")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC, service.rows.GetSortOrder())
	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "rows", "--result", "result", "--sort-key", "size", "--order", "asc")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC, service.rows.GetSortOrder())
	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "rows", "--result", "result")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Zero(t, service.rows.GetLimit(), "the service applies the 100-row default")

	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "positions", "--result", "result", "--file-id", "4", "--file-id", "6")
	require.Equal(t, exitSuccess, exit, stderr)
	require.True(t, proto.Equal(&entity.LookupIdenticalPositionsRequest{ResultId: "result", FileIds: []int64{4, 6}}, service.positions))
	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "positions", "--result", "result", "--file-id", "4", "--sort-key", "size")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE, service.positions.GetSortKey())
	require.Equal(t, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC, service.positions.GetSortOrder())
	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "positions", "--result", "result", "--file-id", "4", "--sort-key", "size", "--order", "asc")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC, service.positions.GetSortOrder())

	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "groups", "--result", "result", "--cursor", "later")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, "result", service.groups.GetResultId())
	require.Equal(t, "later", service.groups.GetCursor())
	require.EqualValues(t, 20, service.groups.GetLimit())

	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "close", "--result", "result")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, "result", service.close.GetResultId())
}

func TestIdenticalCLIRequiresResultForContinuation(t *testing.T) {
	// The client must not send a cursor-only request that could start a new global Find.
	service := &identicalCommandStub{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, service)
	}, nil, nil)
	for _, args := range [][]string{
		{"identical", "groups", "--cursor", "next"},
		{"identical", "members", "--group", "1", "--cursor", "next"},
	} {
		exit, _, stderr := executeTestCLI(server.URL, "", args...)
		require.Equal(t, exitUsage, exit, stderr)
		require.Contains(t, stderr, "--result")
	}
	require.Nil(t, service.groups)
	require.Nil(t, service.members)
}

func (s *identicalCommandStub) ListIdenticalMembers(_ context.Context, request *entity.ListIdenticalMembersRequest) (*entity.ListIdenticalMembersResponse, error) {
	s.members = proto.Clone(request).(*entity.ListIdenticalMembersRequest)
	return &entity.ListIdenticalMembersResponse{Group: &entity.IdenticalGroup{Id: request.GroupId}}, nil
}

func (s *identicalCommandStub) MergeIdentical(_ context.Context, request *entity.MergeIdenticalRequest) (*entity.MergeIdenticalResponse, error) {
	s.merge = proto.Clone(request).(*entity.MergeIdenticalRequest)
	return &entity.MergeIdenticalResponse{TargetFileId: request.TargetFileId}, nil
}

func (s *identicalCommandStub) RemoveVersion(_ context.Context, request *entity.RemoveFileVersionRequest) (*entity.RemoveFileVersionResponse, error) {
	s.remove = proto.Clone(request).(*entity.RemoveFileVersionRequest)
	return &entity.RemoveFileVersionResponse{}, nil
}

func TestIdenticalCommandsPreserveScopeAndMutationInputs(t *testing.T) {
	// The public parser carries opaque group state unchanged and keeps destructive actions explicit.
	service := &identicalCommandStub{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, service)
	}, nil, nil)

	exit, stdout, stderr := executeTestCLI(server.URL, "", "identical", "groups", "--result", "result", "--cursor", "groups-after", "--limit", "2")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "fingerprint")
	require.True(t, proto.Equal(&entity.ListIdenticalGroupsRequest{
		Scope: &entity.IdenticalScope{Source: entity.IdenticalSource_IDENTICAL_SOURCE_LIBRARY}, Cursor: "groups-after", Limit: 2, ResultId: "result",
	}, service.groups))

	exit, _, stderr = executeTestCLI(server.URL, "", "identical", "members", "--source", "locations", "--root", "7", "--root", "8", "--group", "group", "--result", "result", "--cursor", "members-after", "--limit", "3")
	require.Equal(t, exitSuccess, exit, stderr)
	require.True(t, proto.Equal(&entity.ListIdenticalMembersRequest{
		Scope:   &entity.IdenticalScope{Source: entity.IdenticalSource_IDENTICAL_SOURCE_LOCATIONS, Roots: []*entity.IdenticalRoot{{LocationId: 7}, {LocationId: 8}}},
		GroupId: "group", Cursor: "members-after", Limit: 3, ResultId: "result",
	}, service.members))

	exit, stdout, stderr = executeTestCLI(server.URL, "", "identical", "merge", "--group", "group", "--fingerprint", "fingerprint", "--target-file", "9")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "9")
	require.True(t, proto.Equal(&entity.MergeIdenticalRequest{
		Scope: &entity.IdenticalScope{Source: entity.IdenticalSource_IDENTICAL_SOURCE_LIBRARY}, GroupId: "group", Fingerprint: "fingerprint", TargetFileId: 9,
	}, service.merge))

	exit, _, stderr = executeTestCLI(server.URL, "", "files", "remove-version", "--file-id", "9", "--version-id", "12")
	require.Equal(t, exitSuccess, exit, stderr)
	require.True(t, proto.Equal(&entity.RemoveFileVersionRequest{FileId: 9, VersionId: 12}, service.remove))
}

func TestIdenticalMutationsCarryTheDryRunDecision(t *testing.T) {
	// The request owns the dry-run decision; the CLI adds no confirmation state of its own.
	service := &identicalCommandStub{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, service)
	}, nil, nil)

	exit, _, stderr := executeTestCLI(server.URL, "", "identical", "merge", "--group", "group", "--fingerprint", "fingerprint", "--target-file", "9", "--dryrun")
	require.Equal(t, exitSuccess, exit, stderr)
	require.True(t, service.merge.GetDryrun())

	exit, _, stderr = executeTestCLI(server.URL, "", "files", "remove-version", "--file-id", "9", "--version-id", "12", "--dryrun")
	require.Equal(t, exitSuccess, exit, stderr)
	require.True(t, service.remove.GetDryrun())
}
