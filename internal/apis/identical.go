package apis

import (
	"context"
	"path"
	"strconv"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func identicalScope(value *entity.IdenticalScope) (library.IdenticalScope, error) {
	if value == nil {
		return library.IdenticalScope{}, status.Error(codes.InvalidArgument, "scope is required")
	}
	scope := library.IdenticalScope{Source: library.IdenticalLibrary}
	switch value.Source {
	case entity.IdenticalSource_IDENTICAL_SOURCE_LIBRARY:
	case entity.IdenticalSource_IDENTICAL_SOURCE_LOCATIONS:
		scope.Source = library.IdenticalLocations
	default:
		return scope, status.Error(codes.InvalidArgument, "invalid identical-file source")
	}
	for _, root := range value.Roots {
		if root.GetLocationId() <= 0 {
			return scope, status.Error(codes.InvalidArgument, "Location IDs must be positive")
		}
		scope.Roots = append(scope.Roots, library.IdenticalRoot{LocationID: root.GetLocationId()})
	}
	if scope.Source == library.IdenticalLibrary && len(scope.Roots) != 0 {
		return scope, status.Error(codes.InvalidArgument, "Library scope cannot contain Location roots")
	}
	if scope.Source == library.IdenticalLocations && (len(scope.Roots) == 0 || len(scope.Roots) > 1000) {
		return scope, status.Error(codes.InvalidArgument, "select between 1 and 1000 Location roots")
	}
	return scope, nil
}

func identicalGroup(group *library.IdenticalGroup) *entity.IdenticalGroup {
	return &entity.IdenticalGroup{Id: group.ID, Name: group.Name, MemberCount: group.Count, Fingerprint: group.Fingerprint}
}

func (s *filesService) ListIdenticalGroups(ctx context.Context, req *entity.ListIdenticalGroupsRequest) (*entity.ListIdenticalGroupsResponse, error) {
	// Metadata snapshots compute complete groups, independently of the page rendered by clients.
	scope, err := identicalScope(req.GetScope())
	if err != nil {
		return nil, err
	}
	limit, err := pageLimit(req.GetLimit())
	if err != nil {
		return nil, err
	}
	if req.GetMemberLimit() < 0 || req.GetMemberLimit() > 1000 || int64(limit)*int64(req.GetMemberLimit()) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "initial member projection must contain at most 1000 rows")
	}
	if req.GetCursor() != "" && req.GetResultId() == "" {
		return nil, status.Error(codes.InvalidArgument, "identical result ID is required for a group continuation")
	}
	result, err := s.identicalSnapshot(ctx, req.GetResultId(), scope)
	if err != nil {
		return nil, err
	}
	completed := false
	defer func() {
		s.api.identicalResults.release(result)
		if !completed && req.GetResultId() == "" {
			_ = s.api.identicalResults.close(result.id)
		}
	}()
	snapshot := result.snapshot
	page, err := snapshot.Groups(req.GetCursor(), int64(limit))
	if err != nil {
		return nil, apiError(err)
	}
	reply := &entity.ListIdenticalGroupsResponse{NextCursor: page.NextCursor, ResultId: result.id}
	for _, group := range page.Groups {
		reply.Groups = append(reply.Groups, identicalGroup(&group))
	}
	if req.GetMemberLimit() > 0 {
		var members []library.IdenticalMember
		pages := make([]library.IdenticalMemberPage, 0, len(page.Groups))
		for _, group := range page.Groups {
			membersPage, err := snapshot.Members(group.ID, "", int64(req.MemberLimit))
			if err != nil {
				return nil, apiError(err)
			}
			pages = append(pages, membersPage)
			members = append(members, membersPage.Members...)
		}
		entries, observations, err := s.memberEntries(ctx, members)
		if err != nil {
			return nil, err
		}
		offset := 0
		for index, membersPage := range pages {
			size := len(membersPage.Members)
			memberReply := identicalMembers(scope, &page.Groups[index], membersPage, entries[offset:offset+size], observations)
			memberReply.ResultId = result.id
			reply.MemberPages = append(reply.MemberPages, memberReply)
			offset += size
		}
	}
	completed = true
	return reply, nil
}

func (s *filesService) ListIdenticalMembers(ctx context.Context, req *entity.ListIdenticalMembersRequest) (*entity.ListIdenticalMembersResponse, error) {
	// A bounded member page shares the ordinary row hydration and observation pipeline.
	scope, err := identicalScope(req.GetScope())
	if err != nil {
		return nil, err
	}
	limit, err := pageLimit(req.GetLimit())
	if err != nil {
		return nil, err
	}
	if req.GetCursor() != "" && req.GetResultId() == "" {
		return nil, status.Error(codes.InvalidArgument, "identical result ID is required for a member continuation")
	}
	result, err := s.identicalSnapshot(ctx, req.GetResultId(), scope)
	if err != nil {
		return nil, err
	}
	completed := false
	defer func() {
		s.api.identicalResults.release(result)
		if !completed && req.GetResultId() == "" {
			_ = s.api.identicalResults.close(result.id)
		}
	}()
	snapshot := result.snapshot
	group, err := snapshot.Group(req.GetGroupId())
	if err != nil {
		return nil, apiError(err)
	}
	page, err := snapshot.Members(req.GetGroupId(), req.GetCursor(), int64(limit))
	if err != nil {
		return nil, apiError(err)
	}
	entries, observations, err := s.memberEntries(ctx, page.Members)
	if err != nil {
		return nil, err
	}

	reply := identicalMembers(scope, group, page, entries, observations)
	reply.ResultId = result.id
	completed = true
	return reply, nil
}

func identicalMembers(scope library.IdenticalScope, group *library.IdenticalGroup, page library.IdenticalMemberPage, entries []*entity.FilesEntry, observations map[int64]*executor.FileReadObservation) *entity.ListIdenticalMembersResponse {
	// Physical references retain observed object identity; unavailable members remain visible.
	reply := &entity.ListIdenticalMembersResponse{Group: identicalGroup(group), NextCursor: page.NextCursor}
	for i, member := range page.Members {
		reply.Members = append(reply.Members, identicalMember(scope, member, entries[i], observations[member.FileID]))
	}
	return reply
}

func identicalMember(scope library.IdenticalScope, member library.IdenticalMember, entry *entity.FilesEntry, observation *executor.FileReadObservation) *entity.IdenticalMember {
	// A retained Location path cannot borrow facts or actions from a moved original.
	if scope.Source == library.IdenticalLocations {
		original := member.Original
		ref := &entity.LocationEntryRef{LocationId: original.LocationID, Path: original.Path}
		entry.Operations = nil
		if observation != nil && observation.Entry != nil {
			actual := observation.Entry.Reference
			if actual.GetLocationId() != original.LocationID || actual.GetPath() != original.Path {
				observation = nil
				entry.Status, entry.SizeBytes, entry.MtimeNs = nil, nil, nil
			}
		}
		if observation != nil && observation.Valid && observation.Entry != nil {
			ref = observation.Entry.Reference
			entry.Operations = []entity.FileOperationKind{entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, entity.FileOperationKind_FILE_OPERATION_KIND_MOVE, entity.FileOperationKind_FILE_OPERATION_KIND_SCAN, entity.FileOperationKind_FILE_OPERATION_KIND_ARCHIVE, entity.FileOperationKind_FILE_OPERATION_KIND_UPDATE_METADATA}
		}
		entry.Reference = &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: ref}}
		entry.Name, entry.Path, entry.AssociatedFileId = path.Base(original.Path), original.Path, &member.FileID
	}

	// Retain the matching evidence and only the Location name of an applicable observation.
	item := &entity.IdenticalMember{Entry: entry}
	if observation != nil && observation.Location != nil {
		item.LocationName = observation.Location.Name
	}
	for _, evidence := range member.Evidence {
		item.Evidence = append(item.Evidence, &entity.IdenticalEvidence{Signature: evidence.Signature, VersionId: evidence.VersionID})
	}
	return item
}

func (s *filesService) memberEntries(ctx context.Context, members []library.IdenticalMember) ([]*entity.FilesEntry, map[int64]*executor.FileReadObservation, error) {
	// Hydrate surviving Files only; the retained result owns display identity for removed members.
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.FileID)
	}
	rows, err := s.api.lib.ReadFileRows(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	files := make([]*library.File, 0, len(members))
	for _, id := range ids {
		if rows[id] != nil {
			files = append(files, rows[id])
		}
	}
	byID := make(map[int64]*entity.FilesEntry, len(files))
	observations := make(map[int64]*executor.FileReadObservation)
	if len(files) > 0 {
		live, observed, _, err := s.libraryEntries(ctx, files, filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true,
			entity.FilesInclude_FILES_INCLUDE_STATUS: true, entity.FilesInclude_FILES_INCLUDE_OPERATIONS: true}, true)
		if err != nil {
			return nil, nil, err
		}
		observations = observed
		for i, file := range files {
			byID[file.ID] = live[i]
		}
	}

	// Missing rows remain browsable but carry no invented current status or available operations.
	entries := make([]*entity.FilesEntry, 0, len(members))
	for _, member := range members {
		entry := byID[member.FileID]
		if entry == nil {
			entry = &entity.FilesEntry{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: member.FileID}},
				Name: member.Name, Path: member.Path, Kind: entity.EntryKind_ENTRY_KIND_FILE}
		}
		entries = append(entries, entry)
	}
	return entries, observations, nil
}

func (s *filesService) MergeIdentical(ctx context.Context, req *entity.MergeIdenticalRequest) (*entity.MergeIdenticalResponse, error) {
	// The target is one member; the backend supplies every source, including unloaded pages.
	scope, err := identicalScope(req.GetScope())
	if err != nil {
		return nil, err
	}
	if scope.Source != library.IdenticalLibrary || req.GetTargetFileId() <= 0 || req.GetFingerprint() == "" {
		return nil, status.Error(codes.InvalidArgument, "merge requires Library, one target and a fingerprint")
	}
	seed, err := strconv.ParseInt(req.GetGroupId(), 10, 64)
	if err != nil || seed <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid identical group ID")
	}
	var snapshot *library.IdenticalSnapshot
	defer func() {
		closeIdenticalSnapshot(snapshot)
	}()

	// Stream the authoritative group; neither validation nor mutation retains all member IDs.
	visit := func(yield func([]int64) error) error {
		return snapshot.WalkMembers(req.GroupId, func(members []library.IdenticalMember) error {
			ids := make([]int64, 0, len(members))
			for _, member := range members {
				if member.FileID != req.TargetFileId {
					ids = append(ids, member.FileID)
				}
			}
			return yield(ids)
		})
	}

	snapshot, err = s.api.lib.OpenIdenticalComponent(ctx, scope, seed)
	if err != nil {
		return nil, apiError(err)
	}
	group, err := snapshot.Group(req.GroupId)
	if err != nil {
		return nil, apiError(library.ErrLocationConflict)
	}
	if group.Fingerprint != req.Fingerprint {
		return nil, apiError(library.ErrLocationConflict)
	}
	found, err := snapshot.HasMember(group.ID, req.TargetFileId)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, status.Error(codes.InvalidArgument, "target is not a group member")
	}

	merged, err := s.api.lib.MergeFileMembers(ctx, req.TargetFileId, visit, req.GetDryrun())
	if err != nil {
		return nil, apiError(err)
	}
	return &entity.MergeIdenticalResponse{TargetFileId: req.TargetFileId, RemovedFileCount: merged}, nil
}

func (s *filesService) RemoveVersion(ctx context.Context, req *entity.RemoveFileVersionRequest) (*entity.RemoveFileVersionResponse, error) {
	removed, err := s.api.lib.RemoveFileVersion(ctx, req.GetFileId(), req.GetVersionId(), req.GetDryrun())
	if err != nil {
		return nil, apiError(err)
	}
	return &entity.RemoveFileVersionResponse{RemovedVersionCount: removed}, nil
}
