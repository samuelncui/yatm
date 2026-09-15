package main

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type stubDuplicateCatalog struct {
	entity.UnimplementedFileCatalogServiceServer
	groups  *entity.ListDuplicateGroupsRequest
	members *entity.ListDuplicateMembersRequest
}

func (s *stubDuplicateCatalog) ListDuplicateGroups(
	_ context.Context, req *entity.ListDuplicateGroupsRequest,
) (*entity.ListDuplicateGroupsReply, error) {
	s.groups = proto.Clone(req).(*entity.ListDuplicateGroupsRequest)
	return &entity.ListDuplicateGroupsReply{NextCursor: "next-group"}, nil
}

func (s *stubDuplicateCatalog) ListDuplicateMembers(
	_ context.Context, req *entity.ListDuplicateMembersRequest,
) (*entity.ListDuplicateMembersReply, error) {
	s.members = proto.Clone(req).(*entity.ListDuplicateMembersRequest)
	return &entity.ListDuplicateMembersReply{NextCursor: "next-member"}, nil
}

func TestDuplicateGroupCommands(t *testing.T) {
	// The real parser and gRPC-Web transport preserve opaque signatures and distinct cursors.
	service := &stubDuplicateCatalog{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFileCatalogServiceServer(server, service)
	}, nil, nil)
	exit, stdout, stderr := executeTestCLI(server.URL, "", "file", "duplicate-groups",
		"--query", "tag:review", "--cursor", "group-page", "--limit", "2")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "next-group")
	require.True(t, proto.Equal(&entity.ListDuplicateGroupsRequest{
		Query: "tag:review", Cursor: "group-page", Limit: 2}, service.groups))
	exit, stdout, stderr = executeTestCLI(server.URL, "", "file", "duplicate-members",
		"--signature", "00ff00", "--query", "location:2", "--cursor", "member-page", "--limit", "3")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "next-member")
	require.True(t, proto.Equal(&entity.ListDuplicateMembersRequest{Signature: []byte{0, 255, 0},
		Query: "location:2", Cursor: "member-page", Limit: 3}, service.members))
}
