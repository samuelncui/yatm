package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *fileCatalogService) RelocateOriginal(ctx context.Context, req *entity.RelocateOriginalRequest) (*entity.FileStateReply, error) {
	// Resolve a concrete user-selected regular file under its current binding gate.
	if req.GetFileId() <= 0 || req.GetReference() == nil {
		return nil, status.Error(codes.InvalidArgument, "File and original reference are required")
	}
	release, err := s.api.lib.UseOnlineSource(req.Reference.LocationId)
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	if old := req.ExpectedOriginal; old != nil && old.LocationId != req.Reference.LocationId {
		// Relinking changes both associations; do not race an analysis of the previous Location.
		releaseOld, err := s.api.lib.UseOnlineSource(old.LocationId)
		if err != nil {
			return nil, onlineError(err)
		}
		defer releaseOld()
	}
	location, full, info, err := s.api.exe.ResolveLocationEntry(ctx, req.Reference)
	if err != nil {
		return nil, onlineError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, status.Error(codes.InvalidArgument, "select an ordinary file")
	}
	keys, err := executor.ObserveTracking(location, full, info)
	if err != nil {
		return nil, onlineError(err)
	}
	observation := &library.OnlinePosition{Path: req.Reference.Path, Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), TrackingKeys: keys}
	observation.CopyResult, err = s.api.lib.CopyAdmission(ctx, location.ID, location.BindingToken, req.Reference.Facts.Identity)
	if err != nil {
		return nil, onlineError(err)
	}
	if _, _, _, err := s.api.exe.ResolveLocationEntry(ctx, req.Reference); err != nil {
		return nil, onlineError(err)
	}
	if err := s.api.lib.RelocateOriginal(ctx, req.FileId, req.ExpectedOriginal, location.ID, location.BindingToken, observation); err != nil {
		return nil, onlineError(err)
	}
	return s.api.lib.FileState(ctx, req.FileId)
}
