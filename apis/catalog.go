package apis

import (
	"context"
	"errors"
	"os"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *fileCatalogService) GetVersion(ctx context.Context, req *entity.GetFileVersionRequest) (*entity.FileVersionReply, error) {
	if req == nil || req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "FileVersion ID is required")
	}
	release, err := s.api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	version, err := s.api.lib.GetFileVersion(ctx, req.Id)
	if err != nil {
		return nil, onlineError(err)
	}
	file, err := s.api.lib.GetFile(ctx, version.FileID)
	if err != nil {
		return nil, onlineError(err)
	}
	if err := s.api.hydrateFileTags(ctx, file); err != nil {
		return nil, err
	}
	reply := &entity.FileVersionReply{Version: version.ToEntity(), File: convertFiles(file)[0]}
	if s.api.exe.Previews() == nil || len(version.Hash) != 32 {
		return reply, nil
	}
	signature, _ := library.NewFileSignature(version.Hash, version.Size)
	manifest, err := s.api.exe.Previews().Manifest(signature)
	if err == nil {
		reply.Preview = manifest
	} else if !errors.Is(err, os.ErrNotExist) {
		logrus.WithContext(ctx).WithError(err).Warnf("read preview metadata failed, file_version_id=%d", version.ID)
	}
	return reply, nil
}

type fileCatalogService struct {
	entity.UnimplementedFileCatalogServiceServer
	api *API
}

func (s *fileCatalogService) InspectSelection(ctx context.Context, req *entity.InspectSelectionRequest) (*entity.InspectSelectionReply, error) {
	reply, err := s.api.exe.InspectSelections(ctx, req)
	return reply, onlineError(err)
}

func (s *fileCatalogService) GetState(ctx context.Context, req *entity.GetFileStateRequest) (*entity.FileStateReply, error) {
	if req == nil || req.FileId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "File ID is required")
	}
	release, err := s.api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	reply, err := s.api.lib.FileState(ctx, req.FileId)
	if err != nil {
		return nil, onlineError(err)
	}
	// Availability errors remain observations, not a failure to load saved version history.
	_ = s.api.exe.ObserveFileContent(ctx, req.FileId, reply.Summary)
	if reply.Summary != nil {
		reply.ArchivedCopies, reply.HealthyCopies = reply.Summary.ArchivedCopies, reply.Summary.HealthyCopies
		reply.UncheckedCopies, reply.UnhealthyCopies = reply.Summary.UncheckedCopies, reply.Summary.UnhealthyCopies
		reply.Coverage = entity.ContentCoverage_CONTENT_UNKNOWN
		if reply.Summary.SignatureKnown && reply.Summary.CurrentObservationValid {
			reply.Coverage = entity.ContentCoverage_NO_ARCHIVED_COPY
			if reply.Summary.ArchivedCopies > 0 {
				reply.Coverage = entity.ContentCoverage_ARCHIVED_CONTENT
			}
		}
	}
	return reply, nil
}

func (s *fileCatalogService) ListVersions(ctx context.Context, req *entity.ListFileVersionsRequest) (*entity.ListFileVersionsReply, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "version page is required")
	}
	limit, err := onlineLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.api.lib.ListFileVersions(ctx, req.FileId, req.AfterId, limit)
	if err != nil {
		return nil, onlineError(err)
	}
	reply := &entity.ListFileVersionsReply{HasMore: more}
	for _, row := range rows {
		reply.Versions = append(reply.Versions, row.ToEntity())
	}
	return reply, nil
}

func (s *fileCatalogService) ListCopies(ctx context.Context, req *entity.ListContentCopiesRequest) (*entity.ListContentCopiesReply, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "content page is required")
	}
	limit, err := onlineLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.api.lib.ListContentCopies(ctx, req.Signature, req.AfterId, limit)
	if err != nil {
		return nil, onlineError(err)
	}
	return &entity.ListContentCopiesReply{Positions: convertPositions(rows...), HasMore: more}, nil
}

func (s *fileCatalogService) ListDuplicates(ctx context.Context, req *entity.ListContentDuplicatesRequest) (*entity.ListContentDuplicatesReply, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "duplicate content page is required")
	}
	release, err := s.api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	limit, err := onlineLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.api.lib.ListContentDuplicates(ctx, req.Signature, req.AfterFileId, limit)
	if err != nil {
		return nil, onlineError(err)
	}
	return &entity.ListContentDuplicatesReply{Files: convertFiles(rows...), HasMore: more}, nil
}

func (s *fileCatalogService) ImportPositions(ctx context.Context, req *entity.ImportArchivePositionsRequest) (*entity.ImportArchivePositionsReply, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "archive position selection is required")
	}
	ids, err := s.api.lib.ImportArchivePositions(ctx, req.PositionIds)
	if err != nil {
		return nil, onlineError(err)
	}
	return &entity.ImportArchivePositionsReply{FileIds: ids}, nil
}
