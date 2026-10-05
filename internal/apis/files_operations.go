package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/fileops"
)

type fileOperationStream struct {
	context context.Context
	send    func(*entity.FileOperationResult) error
}

func (s fileOperationStream) Context() context.Context                      { return s.context }
func (s fileOperationStream) Send(result *entity.FileOperationResult) error { return s.send(result) }

func (s *filesService) Mkdir(req *entity.MkdirFilesRequest, stream entity.FilesService_MkdirServer) error {
	return fileops.Mkdir(s.api.exe, req, fileOperationStream{context: stream.Context(), send: func(result *entity.FileOperationResult) error {
		return stream.Send(&entity.MkdirFilesResponse{Result: result})
	}})
}

func (s *filesService) Move(req *entity.MoveFilesRequest, stream entity.FilesService_MoveServer) error {
	return fileops.Move(s.api.exe, req, fileOperationStream{context: stream.Context(), send: func(result *entity.FileOperationResult) error {
		return stream.Send(&entity.MoveFilesResponse{Result: result})
	}})
}

func (s *filesService) Remove(req *entity.RemoveFilesRequest, stream entity.FilesService_RemoveServer) error {
	return fileops.Remove(s.api.exe, req, fileOperationStream{context: stream.Context(), send: func(result *entity.FileOperationResult) error {
		return stream.Send(&entity.RemoveFilesResponse{Result: result})
	}})
}
