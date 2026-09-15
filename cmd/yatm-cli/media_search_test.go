package main

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestMediaListSearchRequest(t *testing.T) {
	// Capture the actual protobuf sent by the CLI transport.
	var filter *entity.MediaFilter
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterServiceServer(server, &stubService{mediaList: func(
			_ context.Context, request *entity.MediaListRequest,
		) (*entity.MediaListReply, error) {
			filter = proto.Clone(request.GetList()).(*entity.MediaFilter)
			return &entity.MediaListReply{}, nil
		}})
	}, nil, nil)

	// A zero cursor explicitly chooses the ID-ascending first page; punctuation stays literal.
	exit, _, stderr := executeTestCLI(server.URL, "", "media", "list", "--query", "100%_ Travel",
		"--kind", "tape", "--limit", "20", "--after-id", "0")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, 1, recorder.count(entity.Service_MediaList_FullMethodName))
	require.Equal(t, "100%_ Travel", filter.Query)
	require.Equal(t, []entity.MediaKind{entity.MediaKind_MEDIA_KIND_TAPE}, filter.Kinds)
	require.EqualValues(t, 20, filter.GetLimit())
	require.NotNil(t, filter.AfterId)
	require.Zero(t, filter.GetAfterId())
	require.Nil(t, filter.Offset)
}

func TestMediaListRejectsInvalidSearchCursors(t *testing.T) {
	// Invalid cursor choices are usage errors, without sending an RPC.
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterServiceServer(server, &stubService{})
	}, nil, nil)
	for _, args := range [][]string{
		{"media", "list", "--after-id", "-1"},
		{"media", "list", "--after-id", "0", "--offset", "0"},
	} {
		exit, _, stderr := executeTestCLI(server.URL, "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
	require.Zero(t, recorder.count(entity.Service_MediaList_FullMethodName))
}
