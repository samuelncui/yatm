package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *filesService) RelocateOriginal(ctx context.Context, req *entity.RelocateOriginalRequest) (*entity.RelocateOriginalResponse, error) {
	// Resolve a concrete user-selected regular file under its current registration.
	if req.GetFileId() <= 0 || req.GetReference() == nil {
		return nil, status.Error(codes.InvalidArgument, "File and original reference are required")
	}
	if executor.IsLocationTrashPath(req.Reference.Path) {
		return nil, status.Error(codes.FailedPrecondition, "move the file out of Trash before associating it")
	}
	location, _, info, err := s.api.exe.ResolveLocationEntry(ctx, req.Reference)
	if err != nil {
		return nil, apiError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, status.Error(codes.InvalidArgument, "select an ordinary file")
	}

	// Publish the selected path's current facts without changing its bytes.
	mtime, err := dataformat.Nanoseconds(info.ModTime())
	if err != nil {
		return nil, fmt.Errorf("observe original mtime failed, path=%q, %w", req.Reference.Path, err)
	}
	keys := executor.ObserveTracking(location, info)
	observation := &library.ObservedEntry{Path: req.Reference.Path, Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: mtime, TrackingKeys: keys}
	if err := s.api.lib.RelocateOriginal(ctx, req.FileId, location.ID, observation); err != nil {
		return nil, apiError(err)
	}

	// Return the refreshed File detail after the association changes.
	detail, err := s.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: req.FileId}}})
	if err != nil {
		return nil, err
	}
	return &entity.RelocateOriginalResponse{Detail: detail.Detail}, nil
}
