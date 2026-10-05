package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *filesService) GetVersion(ctx context.Context, req *entity.GetFileVersionRequest) (*entity.GetFileVersionResponse, error) {
	if req == nil || req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "FileVersion ID is required")
	}
	version, err := s.api.lib.GetFileVersion(ctx, req.Id)
	if err != nil {
		return nil, apiError(err)
	}
	return &entity.GetFileVersionResponse{Version: version.ToEntity()}, nil
}

func (s *filesService) ListVersions(ctx context.Context, req *entity.ListFileVersionsRequest) (*entity.ListFileVersionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "version page is required")
	}
	limit, err := pageLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.api.lib.ListFileVersions(ctx, req.FileId, req.AfterId, limit)
	if err != nil {
		return nil, apiError(err)
	}
	reply := &entity.ListFileVersionsResponse{HasMore: more}
	for _, row := range rows {
		reply.Versions = append(reply.Versions, row.ToEntity())
	}
	return reply, nil
}

func (s *filesService) ListCopies(ctx context.Context, req *entity.ListContentCopiesRequest) (*entity.ListContentCopiesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "content page is required")
	}
	limit, err := pageLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.api.lib.ListContentCopies(ctx, req.Signature, req.AfterId, limit)
	if err != nil {
		return nil, apiError(err)
	}
	return &entity.ListContentCopiesResponse{Positions: convertPositions(rows...), HasMore: more}, nil
}

func (s *filesService) ListDuplicates(ctx context.Context, req *entity.ListContentDuplicatesRequest) (*entity.ListContentDuplicatesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "duplicate content page is required")
	}
	limit, err := pageLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.api.lib.ListContentDuplicates(ctx, req.Signature, req.AfterFileId, limit)
	if err != nil {
		return nil, apiError(err)
	}
	entries, _, _, err := s.libraryEntries(ctx, rows, filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true, entity.FilesInclude_FILES_INCLUDE_STATUS: true, entity.FilesInclude_FILES_INCLUDE_OPERATIONS: true}, true)
	if err != nil {
		return nil, err
	}
	return &entity.ListContentDuplicatesResponse{Entries: entries, HasMore: more}, nil
}

func (s *filesService) ImportPositions(ctx context.Context, req *entity.ImportPositionsRequest) (*entity.ImportPositionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "archive position selection is required")
	}
	if req.MediaId != nil && req.GetMediaId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "import Media ID is invalid")
	}
	result, err := s.api.lib.ImportArchivePositionSelection(ctx, req.PositionIds, req.MediaId, req.Dryrun)
	if err != nil {
		return nil, apiError(err)
	}
	return &entity.ImportPositionsResponse{FileCount: result.Files, DirectoryCount: result.Directories, SkippedFileCount: result.SkippedFiles,
		ExistingCount: result.ExistingFiles, SkippedUnsignedCount: result.SkippedUnsigned}, nil
}
