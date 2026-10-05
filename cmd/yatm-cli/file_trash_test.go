package main

import (
	"context"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"testing"
)

type fileBrowseService struct {
	entity.UnimplementedFilesServiceServer
}

func (*fileBrowseService) Get(_ context.Context, r *entity.GetFileRequest) (*entity.GetFileResponse, error) {
	return &entity.GetFileResponse{Detail: &entity.FilesDetail{Entry: &entity.FilesEntry{Reference: r.Reference, Name: ".Trash"}}}, nil
}
func (*fileBrowseService) List(r *entity.ListFilesRequest, stream entity.FilesService_ListServer) error {
	return stream.Send(&entity.ListFilesResponse{Directory: &entity.FilesEntry{Reference: r.Directory, Name: ".Trash"}})
}
func TestFileBrowseCommandsAcceptReservedTrashID(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		method string
	}{
		{[]string{"files", "get", "--file-id=-1"}, entity.FilesService_Get_FullMethodName},
		{[]string{"ls", "--file-id=-1"}, entity.FilesService_List_FullMethodName},
	} {
		server, recorder := newGRPCWebTestServer(t, func(s *grpc.Server) { entity.RegisterFilesServiceServer(s, &fileBrowseService{}) }, nil, nil)
		exit, stdout, stderr := executeTestCLI(server.URL, "", tc.args...)
		require.Equal(t, exitSuccess, exit, stderr)
		require.Contains(t, stdout, `"file_id":"-1"`)
		require.Equal(t, 1, recorder.count(tc.method))
	}
}
func TestFileBrowseCommandsKeepRootAndInvalidIDBoundaries(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		allowed bool
	}{
		{[]string{"ls", "--file-id=0"}, true},
		{[]string{"files", "get", "--file-id=0"}, true},
		{[]string{"ls", "--file-id=-2"}, false},
		{[]string{"files", "get", "--file-id=-2"}, false},
		{[]string{"ls"}, true},
	} {
		server, _ := newGRPCWebTestServer(t, func(s *grpc.Server) { entity.RegisterFilesServiceServer(s, &fileBrowseService{}) }, nil, nil)
		exit, _, stderr := executeTestCLI(server.URL, "", tc.args...)
		if tc.allowed {
			require.Equal(t, exitSuccess, exit, stderr)
		} else {
			require.Equal(t, exitUsage, exit, stderr)
		}
	}
}
func TestTrashReadExceptionDoesNotRelaxMutations(t *testing.T) {
	for _, args := range [][]string{
		{"mv", "--library", "--source=-1", "--destination", "0"},
		{"mkdir", "--library", "--destination=-1", "--name", "child"},
		{"rm", "--library", "--source=-1"},
		{"rm", "--library", "--source=-1"},
	} {
		exit, _, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
	}
}
