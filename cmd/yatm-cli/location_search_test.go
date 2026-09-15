package main

import (
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestLocationListPreservesLiteralSearch(t *testing.T) {
	// Capture the request through the real parser and RPC transport.
	service := &stubLocationService{}
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterLocationServiceServer(server, service)
	}, nil, nil)

	// Query, keyset cursor and explicit false recommendation remain independent.
	exit, _, stderr := executeTestCLI(server.URL, "", "location", "list", "--query", "100%_ Travel",
		"--limit", "20", "--after-id", "8", "--restore-target", "false")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Equal(t, "100%_ Travel", service.list.Query)
	require.EqualValues(t, 20, service.list.Limit)
	require.EqualValues(t, 8, service.list.AfterId)
	require.NotNil(t, service.list.RestoreTarget)
	require.False(t, service.list.GetRestoreTarget())
}
