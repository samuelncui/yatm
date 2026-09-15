package apis

import (
	"context"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *fileCatalogService) ListDuplicateGroups(
	ctx context.Context, req *entity.ListDuplicateGroupsRequest,
) (*entity.ListDuplicateGroupsReply, error) {
	// A group query is metadata-only and does not inspect or hash any original.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "duplicate group page is required")
	}
	return s.api.lib.ListDuplicateGroups(ctx, req.Query, req.Cursor, int64(req.Limit))
}

func (s *fileCatalogService) ListDuplicateMembers(
	ctx context.Context, req *entity.ListDuplicateMembersRequest,
) (*entity.ListDuplicateMembersReply, error) {
	// The Library read transaction supplies coherent summary, location and organization facts.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "duplicate member page is required")
	}
	page, err := s.api.lib.ListDuplicateMembers(ctx, req.Signature, req.Query, req.Cursor, int64(req.Limit))
	if err != nil {
		return nil, err
	}

	// Conversion preserves each independent File and flags out-of-filter members explicitly.
	reply := &entity.ListDuplicateMembersReply{Group: page.Group, NextCursor: page.NextCursor,
		IndexRevision: page.IndexRevision}
	for _, member := range page.Members {
		// Validate current members one page at a time, without rehashing or claiming global coverage.
		observation, err := s.observeDuplicateMember(ctx, member.Original)
		if err != nil {
			return nil, err
		}
		reply.Members = append(reply.Members, &entity.DuplicateMember{File: convertFiles(member.File)[0],
			LibraryPath: member.LibraryPath, Original: member.Original.ToEntity(),
			LocationName: member.LocationName, MatchesFilter: member.MatchesFilter, Observation: observation})
	}
	return reply, nil
}

func (s *fileCatalogService) observeDuplicateMember(ctx context.Context, original *library.FileLocation) (entity.ContentObservation, error) {
	// Compare the complete observation basis, including available native identity, without writing xattrs.
	location, err := s.api.lib.GetOnlineSource(ctx, original.LocationID)
	if err != nil {
		return entity.ContentObservation_UNAVAILABLE, err
	}
	full, info, err := s.api.exe.CheckLocationPath(location, original.Path)
	if err != nil {
		return entity.ContentObservation_UNAVAILABLE, nil
	}
	if !info.Mode().IsRegular() {
		return entity.ContentObservation_CHANGED, nil
	}
	location.WriteTrackingUUID = false
	keys, err := executor.ObserveTracking(location, full, info)
	if err != nil {
		return entity.ContentObservation_UNAVAILABLE, nil
	}
	matches, err := s.api.lib.MatchesObservation(ctx, location, &library.OnlinePosition{FileID: original.FileID,
		Path: original.Path, Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), TrackingKeys: keys})
	if err != nil {
		return entity.ContentObservation_UNAVAILABLE, err
	}
	if matches {
		return entity.ContentObservation_CONFIRMED, nil
	}
	return entity.ContentObservation_CHANGED, nil
}
