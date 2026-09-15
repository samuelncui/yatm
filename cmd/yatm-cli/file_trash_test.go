package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type fileBrowseService struct {
	entity.UnimplementedServiceServer
}

func (*fileBrowseService) FileGet(
	_ context.Context, request *entity.FileGetRequest,
) (*entity.FileGetReply, error) {
	return &entity.FileGetReply{File: &entity.File{Id: request.Id, Name: ".Trash"}}, nil
}

func (*fileBrowseService) FileListParents(
	_ context.Context, request *entity.FileListParentsRequest,
) (*entity.FileListParentsReply, error) {
	return &entity.FileListParentsReply{Parents: []*entity.File{{Id: request.Id, Name: ".Trash"}}}, nil
}

func TestFileBrowseCommandsAcceptReservedTrashID(t *testing.T) {
	// Exercise the production argument parser and real gRPC-Web transport for every Trash browser entry.
	for _, test := range []struct {
		name, method string
	}{
		{"get", entity.Service_FileGet_FullMethodName},
		{"list", entity.Service_FileGet_FullMethodName},
		{"parents", entity.Service_FileListParents_FullMethodName},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Echo the received identity so the wire request cannot silently substitute root zero.
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterServiceServer(server, &fileBrowseService{})
			}, nil, nil)
			exit, stdout, stderr := executeTestCLI(server.URL, "", "file", test.name, "--", "-1")

			// The option delimiter preserves the negative positional identity and performs one read only.
			require.Equal(t, exitSuccess, exit, stderr)
			require.Empty(t, stderr)
			require.Contains(t, stdout, `"id":"-1"`)
			require.Equal(t, 1, recorder.count(test.method))
		})
	}
}

func TestFileBrowseCommandsKeepRootAndInvalidIDBoundaries(t *testing.T) {
	// Only list may browse root zero; all read commands reject negative identities other than Trash.
	for _, test := range []struct {
		name    string
		args    []string
		allowed bool
	}{
		{"get root", []string{"file", "get", "0"}, false},
		{"list root", []string{"file", "list", "0"}, true},
		{"list default root", []string{"file", "list"}, true},
		{"parents root", []string{"file", "parents", "0"}, false},
		{"get invalid", []string{"file", "get", "--", "-2"}, false},
		{"list invalid", []string{"file", "list", "--", "-2"}, false},
		{"parents invalid", []string{"file", "parents", "--", "-2"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Record actual calls, including the absence of network access for rejected arguments.
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterServiceServer(server, &fileBrowseService{})
			}, nil, nil)
			exit, _, stderr := executeTestCLI(server.URL, "", test.args...)
			if test.allowed {
				require.Equal(t, exitSuccess, exit, stderr)
				require.Equal(t, 1, recorder.count(entity.Service_FileGet_FullMethodName))
				return
			}

			// Invalid identities remain local usage errors instead of ambiguous server-side root lookups.
			require.Equal(t, exitUsage, exit, stderr)
			var output errorOutput
			require.NoError(t, json.Unmarshal([]byte(stderr), &output))
			require.Equal(t, "usage", output.Code)
			require.Zero(t, recorder.count(entity.Service_FileGet_FullMethodName))
			require.Zero(t, recorder.count(entity.Service_FileListParents_FullMethodName))
		})
	}
}

func TestTrashReadExceptionDoesNotRelaxMutations(t *testing.T) {
	// Read-only Trash support must not bypass the existing mutation-ID or deletion-confirmation policy.
	for _, args := range [][]string{
		{"file", "edit", "--name", "renamed", "--", "-1"},
		{"file", "metadata", "--note", "changed", "--", "-1"},
		{"file", "mkdir", "--", "-1", "child"},
		{"file", "delete", "--confirm", "--", "-1"},
	} {
		exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}

	// Deletion still requires explicit confirmation before resolving any target identity.
	exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", "file", "delete", "--", "-1")
	require.Equal(t, exitUsage, exit, stderr)
	var output errorOutput
	require.NoError(t, json.Unmarshal([]byte(stderr), &output))
	require.Equal(t, "safety", output.Code)
}
