package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *filesService) FindIdentical(ctx context.Context, req *entity.FindIdenticalRequest) (*entity.FindIdenticalResponse, error) {
	scope, err := identicalScope(req.GetScope())
	if err != nil {
		return nil, err
	}
	result, err := s.identicalSnapshot(ctx, "", scope)
	if err != nil {
		return nil, err
	}
	defer s.api.identicalResults.release(result)
	return &entity.FindIdenticalResponse{ResultId: result.id, AllRowCount: result.snapshot.AllRows,
		VisibleRowCount: result.snapshot.VisibleRows, GroupCount: result.snapshot.GroupCount}, nil
}

func (s *filesService) ListIdenticalRows(ctx context.Context, req *entity.ListIdenticalRowsRequest) (*entity.ListIdenticalRowsResponse, error) {
	// The row range and result ID are validated before any page hydration.
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 200 || req.GetOffset() < 0 {
		return nil, status.Error(codes.InvalidArgument, "identical row offset must be nonnegative and limit between 1 and 200")
	}
	if !validIdenticalSortKey(req.GetSortKey()) {
		return nil, status.Error(codes.InvalidArgument, "invalid identical sort key")
	}
	if !validIdenticalSortOrder(req.GetSortOrder()) {
		return nil, status.Error(codes.InvalidArgument, "invalid identical sort order")
	}
	result, err := s.api.identicalResults.acquire(req.GetResultId())
	if err != nil {
		return nil, err
	}
	defer s.api.identicalResults.release(result)
	rows, total, err := result.snapshot.SortedRows(req.GetOffset(), limit, req.GetIncludeHidden(), req.GetSortKey(), req.GetSortOrder())
	if err != nil {
		return nil, apiError(err)
	}
	reply := &entity.ListIdenticalRowsResponse{TotalRowCount: total}
	if len(rows) == 0 {
		return reply, nil
	}

	// Hydrate only the member rows in this bounded page, then attach retained group metadata.
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.FileID != 0 {
			ids = append(ids, row.FileID)
		}
	}
	members, err := result.snapshot.MembersByIDs(ids)
	if err != nil {
		return nil, apiError(err)
	}
	byID := make(map[int64]*entity.IdenticalMember, len(members))
	if len(members) > 0 {
		entries, observations, err := s.memberEntries(ctx, members)
		if err != nil {
			return nil, err
		}
		for i, member := range members {
			byID[member.FileID] = identicalMember(result.scope, member, entries[i], observations[member.FileID])
		}
	}
	for _, row := range rows {
		item := &entity.IdenticalRow{Position: row.Position, Group: identicalGroup(&row.Group),
			GroupHeaderPosition: row.HeaderPosition, DisplayMemberCount: row.DisplayCount}
		if row.FileID != 0 {
			item.Member = byID[row.FileID]
			if item.Member == nil {
				return nil, status.Error(codes.FailedPrecondition, "identical member is no longer available")
			}
		}
		reply.Rows = append(reply.Rows, item)
	}
	return reply, nil
}

func (s *filesService) LookupIdenticalPositions(_ context.Context, req *entity.LookupIdenticalPositionsRequest) (*entity.LookupIdenticalPositionsResponse, error) {
	// Validate the requested order and IDs before touching the retained result.
	if !validIdenticalSortKey(req.GetSortKey()) {
		return nil, status.Error(codes.InvalidArgument, "invalid identical sort key")
	}
	if !validIdenticalSortOrder(req.GetSortOrder()) {
		return nil, status.Error(codes.InvalidArgument, "invalid identical sort order")
	}
	if len(req.GetFileIds()) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "at most 1000 File IDs can be looked up")
	}
	for _, id := range req.GetFileIds() {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "File IDs must be positive")
		}
	}

	// Resolve positions in the same member order used by ListIdenticalRows.
	result, err := s.api.identicalResults.acquire(req.GetResultId())
	if err != nil {
		return nil, err
	}
	defer s.api.identicalResults.release(result)
	positions, err := result.snapshot.LookupSortedPositions(req.GetFileIds(), req.GetSortKey(), req.GetSortOrder())
	if err != nil {
		return nil, apiError(err)
	}

	// Preserve the lookup's caller order and optional visible positions.
	reply := &entity.LookupIdenticalPositionsResponse{}
	for _, position := range positions {
		reply.Positions = append(reply.Positions, &entity.IdenticalPosition{FileId: position.FileID, GroupId: position.GroupID,
			AllPosition: position.AllPosition, VisiblePosition: position.VisiblePosition,
			AllGroupHeaderPosition: position.AllHeaderPosition, VisibleGroupHeaderPosition: position.VisibleHeaderPosition})
	}
	return reply, nil
}

func validIdenticalSortKey(key entity.IdenticalSortKey) bool {
	switch key {
	case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID,
		entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME,
		entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE:
		return true
	default:
		return false
	}
}

func validIdenticalSortOrder(order entity.IdenticalSortOrder) bool {
	switch order {
	case entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED,
		entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC,
		entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC:
		return true
	default:
		return false
	}
}

func (s *filesService) CloseIdenticalResult(_ context.Context, req *entity.CloseIdenticalResultRequest) (*entity.CloseIdenticalResultResponse, error) {
	if err := s.api.identicalResults.close(req.GetResultId()); err != nil {
		return nil, err
	}
	return &entity.CloseIdenticalResultResponse{}, nil
}
