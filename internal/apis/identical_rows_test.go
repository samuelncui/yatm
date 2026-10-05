//go:build linux || darwin

package apis

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/fileops"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIdenticalRetainedLocationRowsAfterMove(t *testing.T) {
	// Retain one Location result before a supported, sequential physical move.
	s, scope, dir := identicalFixture(t)
	ctx := context.Background()
	found, err := s.FindIdentical(ctx, &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	defer s.CloseIdenticalResult(ctx, &entity.CloseIdenticalResultRequest{ResultId: found.ResultId})
	before, err := s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, Offset: 1, Limit: 1})
	require.NoError(t, err)
	entry := before.Rows[0].Member.Entry
	require.NotEmpty(t, entry.Operations)

	// The normal operation updates the original association without rebuilding the Find result.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "moved"), 0755))
	var summary *entity.FileOperationSummary
	require.NoError(t, fileops.Move(s.api.exe, &entity.MoveFilesRequest{
		Sources: []*entity.FileOperationRef{entry.Reference},
		Destination: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{
			Location: &entity.LocationEntryRef{LocationId: scope.Roots[0].LocationId, Path: "moved"}}},
	}, fileOperationStream{context: ctx, send: func(result *entity.FileOperationResult) error {
		summary = result.Summary
		return nil
	}}))
	require.NotNil(t, summary)
	require.True(t, summary.Completed)
	require.Equal(t, int64(1), summary.SucceededCount)
	original, err := s.api.lib.GetFileLocation(ctx, entry.GetAssociatedFileId())
	require.NoError(t, err)
	require.Equal(t, "moved/"+entry.Name, original.Path)

	// A retained display must never expose an action targeting a different physical path.
	after, err := s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, Offset: 1, Limit: 1})
	require.NoError(t, err)
	retained := after.Rows[0].Member.Entry
	require.Equal(t, entry.Name, retained.Name)
	require.Equal(t, entry.Path, retained.Path)
	require.Equal(t, retained.Path, retained.Reference.GetLocation().Path)
	require.Empty(t, retained.Operations)
	require.Nil(t, retained.Status, "a current observation of the moved original does not describe the retained path")
	require.Nil(t, retained.SizeBytes)
	require.Nil(t, retained.MtimeNs)
	members, err := s.ListIdenticalMembers(ctx, &entity.ListIdenticalMembersRequest{
		Scope: scope, ResultId: found.ResultId, GroupId: before.Rows[0].Group.Id, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, retained, members.Members[0].Entry)
	require.FileExists(t, filepath.Join(dir, original.Path))
}

func TestIdenticalContinuationsRequireRetainedResult(t *testing.T) {
	// An initial legacy page may create a result, but later pages must not collect again.
	s, scope, dir := identicalFixture(t)
	for _, name := range []string{"d.txt", "e.txt"} {
		filename := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(filename, []byte("other content"), 0644))
		cacheContentSignature(t, context.Background(), filename)
	}
	admittedPage(t, &locationService{api: s.api}, scope.Roots[0].LocationId)
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)

	// Both legacy first-page methods still work without a result ID.
	groups, err := s.ListIdenticalGroups(context.Background(), &entity.ListIdenticalGroupsRequest{Scope: scope, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, groups.ResultId)
	require.NotEmpty(t, groups.NextCursor)
	legacyMembers, err := s.ListIdenticalMembers(context.Background(), &entity.ListIdenticalMembersRequest{
		Scope: scope, GroupId: groups.Groups[0].Id, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, legacyMembers.ResultId)
	_, err = s.CloseIdenticalResult(context.Background(), &entity.CloseIdenticalResultRequest{ResultId: legacyMembers.ResultId})
	require.NoError(t, err)
	members, err := s.ListIdenticalMembers(context.Background(), &entity.ListIdenticalMembersRequest{
		Scope: scope, GroupId: groups.Groups[0].Id, ResultId: groups.ResultId, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, members.NextCursor)

	// A missing ID fails before allocating another temporary SQLite result.
	_, err = s.ListIdenticalGroups(context.Background(), &entity.ListIdenticalGroupsRequest{Scope: scope, Cursor: groups.NextCursor, Limit: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.ListIdenticalMembers(context.Background(), &entity.ListIdenticalMembersRequest{
		Scope: scope, GroupId: groups.Groups[0].Id, Cursor: members.NextCursor, Limit: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Len(t, s.api.identicalResults.sessions, 1)
	entries, err := os.ReadDir(temp)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// An expired ID also fails without building a replacement result.
	_, err = s.CloseIdenticalResult(context.Background(), &entity.CloseIdenticalResultRequest{ResultId: groups.ResultId})
	require.NoError(t, err)
	_, err = s.ListIdenticalGroups(context.Background(), &entity.ListIdenticalGroupsRequest{
		Scope: scope, ResultId: groups.ResultId, Cursor: groups.NextCursor, Limit: 1})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = s.ListIdenticalMembers(context.Background(), &entity.ListIdenticalMembersRequest{
		Scope: scope, GroupId: groups.Groups[0].Id, ResultId: groups.ResultId, Cursor: members.NextCursor, Limit: 1})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	entries, err = os.ReadDir(temp)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestIdenticalServiceCloseRemovesRetainedSQLite(t *testing.T) {
	// Clean shutdown releases this process's temporary results after requests have drained.
	s, scope, _ := identicalFixture(t)
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	for range 2 {
		_, err := s.FindIdentical(context.Background(), &entity.FindIdenticalRequest{Scope: scope})
		require.NoError(t, err)
	}
	entries, err := os.ReadDir(temp)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.NoError(t, s.api.Close())
	entries, err = os.ReadDir(temp)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Empty(t, s.api.identicalResults.sessions)
}

func TestIdenticalFindRowsLookupAndClose(t *testing.T) {
	// A retained result has stable row positions and can be used after Find's context ends.
	s, scope, _ := identicalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	found, err := s.FindIdentical(ctx, &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	require.NotEmpty(t, found.ResultId)
	require.Equal(t, int64(1), found.GroupCount)
	require.Equal(t, int64(4), found.AllRowCount)
	require.Equal(t, int64(4), found.VisibleRowCount)
	cancel()
	page, err := s.ListIdenticalRows(context.Background(), &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, Offset: 1, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, int64(4), page.TotalRowCount)
	require.Len(t, page.Rows, 2)
	require.Equal(t, int64(1), page.Rows[0].Position)
	require.Equal(t, int64(0), page.Rows[0].GroupHeaderPosition)
	require.Equal(t, int64(3), page.Rows[0].DisplayMemberCount)
	require.NotNil(t, page.Rows[0].Member)
	require.Equal(t, page.Rows[0].Group.Fingerprint, page.Rows[1].Group.Fingerprint)
	lookup, err := s.LookupIdenticalPositions(context.Background(), &entity.LookupIdenticalPositionsRequest{
		ResultId: found.ResultId, FileIds: []int64{page.Rows[0].Member.Entry.GetAssociatedFileId(), 999999}})
	require.NoError(t, err)
	require.Len(t, lookup.Positions, 1)
	require.Equal(t, int64(1), lookup.Positions[0].AllPosition)
	require.Equal(t, int64(1), *lookup.Positions[0].VisiblePosition)
	empty, err := s.ListIdenticalRows(context.Background(), &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, Offset: 100})
	require.NoError(t, err)
	require.Empty(t, empty.Rows)
	require.Equal(t, int64(4), empty.TotalRowCount)
	_, err = s.ListIdenticalRows(context.Background(), &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, Limit: 201})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.LookupIdenticalPositions(context.Background(), &entity.LookupIdenticalPositionsRequest{ResultId: found.ResultId, FileIds: make([]int64, 1001)})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	groups, err := s.ListIdenticalGroups(context.Background(), &entity.ListIdenticalGroupsRequest{Scope: scope, ResultId: found.ResultId})
	require.NoError(t, err)
	require.Equal(t, found.ResultId, groups.ResultId)
	members, err := s.ListIdenticalMembers(context.Background(), &entity.ListIdenticalMembersRequest{Scope: scope,
		GroupId: groups.Groups[0].Id, ResultId: found.ResultId, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, found.ResultId, members.ResultId)
	_, err = s.ListIdenticalGroups(context.Background(), &entity.ListIdenticalGroupsRequest{Scope: &entity.IdenticalScope{Source: entity.IdenticalSource_IDENTICAL_SOURCE_LIBRARY}, ResultId: found.ResultId})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.CloseIdenticalResult(context.Background(), &entity.CloseIdenticalResultRequest{ResultId: found.ResultId})
	require.NoError(t, err)
	_, err = s.ListIdenticalRows(context.Background(), &entity.ListIdenticalRowsRequest{ResultId: found.ResultId})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestIdenticalSortedRowsAndLookupShareRequestedOrder(t *testing.T) {
	// One Find supports member ordering and matching positions without rebuilding the result.
	s, scope, _ := identicalFixture(t)
	ctx := context.Background()
	found, err := s.FindIdentical(ctx, &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	defer s.CloseIdenticalResult(ctx, &entity.CloseIdenticalResultRequest{ResultId: found.ResultId})
	page, err := s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId,
		SortKey:   entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME,
		SortOrder: entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC})
	require.NoError(t, err)
	require.Equal(t, int64(4), page.TotalRowCount)
	require.Len(t, page.Rows, 4)
	require.Nil(t, page.Rows[0].Member)
	require.Equal(t, []string{"c.txt", "b.txt", "a.txt"},
		[]string{page.Rows[1].Member.Entry.Name, page.Rows[2].Member.Entry.Name, page.Rows[3].Member.Entry.Name})
	ids := []int64{page.Rows[1].Member.Entry.GetAssociatedFileId(), page.Rows[3].Member.Entry.GetAssociatedFileId()}
	lookup, err := s.LookupIdenticalPositions(ctx, &entity.LookupIdenticalPositionsRequest{ResultId: found.ResultId,
		FileIds: ids, SortKey: entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME,
		SortOrder: entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC})
	require.NoError(t, err)
	require.Len(t, lookup.Positions, 2)
	require.Equal(t, int64(1), lookup.Positions[0].AllPosition)
	require.Equal(t, int64(3), lookup.Positions[1].AllPosition)
	require.Equal(t, int64(1), *lookup.Positions[0].VisiblePosition)
	require.Zero(t, lookup.Positions[1].AllGroupHeaderPosition)
	require.Len(t, s.api.identicalResults.sessions, 1)
	ascending, err := s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId,
		SortKey: entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME})
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt", "b.txt", "c.txt"},
		[]string{ascending.Rows[1].Member.Entry.Name, ascending.Rows[2].Member.Entry.Name, ascending.Rows[3].Member.Entry.Name})

	// Unsupported enum values fail as input errors for both request types.
	_, err = s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, SortKey: 99})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.LookupIdenticalPositions(ctx, &entity.LookupIdenticalPositionsRequest{ResultId: found.ResultId,
		FileIds: ids, SortKey: 99})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, SortOrder: 99})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.LookupIdenticalPositions(ctx, &entity.LookupIdenticalPositionsRequest{ResultId: found.ResultId,
		FileIds: ids, SortOrder: 99})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestIdenticalResultExpiryAndActiveReader(t *testing.T) {
	// Expiration and explicit Close invalidate IDs while an acquired reader finishes safely.
	s, scope, _ := identicalFixture(t)
	base := time.Now()
	now := base
	s.api.identicalResults.now = func() time.Time { return now }
	result, err := s.FindIdentical(context.Background(), &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	reader, err := s.api.identicalResults.acquire(result.ResultId)
	require.NoError(t, err)
	_, err = s.CloseIdenticalResult(context.Background(), &entity.CloseIdenticalResultRequest{ResultId: result.ResultId})
	require.NoError(t, err)
	rows, _, err := reader.snapshot.Rows(0, 1, true)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	s.api.identicalResults.release(reader)
	_, err = s.api.identicalResults.acquire(result.ResultId)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	result, err = s.FindIdentical(context.Background(), &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	now = base.Add(identicalResultIdle + time.Second)
	_, err = s.ListIdenticalRows(context.Background(), &entity.ListIdenticalRowsRequest{ResultId: result.ResultId})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestIdenticalResultLimitAndLegacyResultIDs(t *testing.T) {
	// Legacy pages create a retained result; a third result evicts the oldest inactive one.
	s, scope, _ := identicalFixture(t)
	first, err := s.ListIdenticalGroups(context.Background(), &entity.ListIdenticalGroupsRequest{Scope: scope, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, first.ResultId)
	second, err := s.ListIdenticalMembers(context.Background(), &entity.ListIdenticalMembersRequest{Scope: scope,
		GroupId: first.Groups[0].Id, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, second.ResultId)
	third, err := s.FindIdentical(context.Background(), &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	require.NotEmpty(t, third.ResultId)
	_, err = s.ListIdenticalRows(context.Background(), &entity.ListIdenticalRowsRequest{ResultId: first.ResultId})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = s.CloseIdenticalResult(context.Background(), &entity.CloseIdenticalResultRequest{ResultId: second.ResultId})
	require.NoError(t, err)
	_, err = s.CloseIdenticalResult(context.Background(), &entity.CloseIdenticalResultRequest{ResultId: third.ResultId})
	require.NoError(t, err)
}

func TestIdenticalResultCapacityRejectsBeforeBuild(t *testing.T) {
	// Two active readers occupy both slots; a new Find fails before collecting another result.
	s, scope, _ := identicalFixture(t)
	first, err := s.FindIdentical(context.Background(), &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	firstReader, err := s.api.identicalResults.acquire(first.ResultId)
	require.NoError(t, err)
	second, err := s.FindIdentical(context.Background(), &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	secondReader, err := s.api.identicalResults.acquire(second.ResultId)
	require.NoError(t, err)
	_, err = s.FindIdentical(context.Background(), &entity.FindIdenticalRequest{Scope: scope})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Zero(t, s.api.identicalResults.building)
	s.api.identicalResults.release(firstReader)
	s.api.identicalResults.release(secondReader)
	_, err = s.CloseIdenticalResult(context.Background(), &entity.CloseIdenticalResultRequest{ResultId: first.ResultId})
	require.NoError(t, err)
	_, err = s.CloseIdenticalResult(context.Background(), &entity.CloseIdenticalResultRequest{ResultId: second.ResultId})
	require.NoError(t, err)
}

func TestIdenticalResultRowsSurviveMergeAndTrim(t *testing.T) {
	// Merge and Trim remove source File rows, but a retained result still supplies their display identity.
	s, _, _ := identicalFixture(t)
	ctx := context.Background()
	scope := &entity.IdenticalScope{Source: entity.IdenticalSource_IDENTICAL_SOURCE_LIBRARY}
	found, err := s.FindIdentical(ctx, &entity.FindIdenticalRequest{Scope: scope})
	require.NoError(t, err)
	defer s.CloseIdenticalResult(ctx, &entity.CloseIdenticalResultRequest{ResultId: found.ResultId})
	before, err := s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, IncludeHidden: true})
	require.NoError(t, err)
	require.Len(t, before.Rows, 4)
	group := before.Rows[0].Group
	target := before.Rows[1].Member.Entry.Reference.GetFileId()
	removed := before.Rows[2].Member.Entry
	_, err = s.MergeIdentical(ctx, &entity.MergeIdenticalRequest{Scope: scope, GroupId: group.Id,
		Fingerprint: group.Fingerprint, TargetFileId: target})
	require.NoError(t, err)
	trimmed, err := s.api.lib.Trim(ctx, false, true, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), trimmed.Files)

	// Header and missing member pages retain the original order, name, path and File reference.
	header, err := s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, Offset: 0, Limit: 1})
	require.NoError(t, err)
	require.Nil(t, header.Rows[0].Member)
	after, err := s.ListIdenticalRows(ctx, &entity.ListIdenticalRowsRequest{ResultId: found.ResultId, Offset: 2, Limit: 1})
	require.NoError(t, err)
	require.Len(t, after.Rows, 1)
	require.Equal(t, int64(4), after.TotalRowCount)
	require.Equal(t, removed.Name, after.Rows[0].Member.Entry.Name)
	require.Equal(t, removed.Path, after.Rows[0].Member.Entry.Path)
	require.Equal(t, removed.Reference.GetFileId(), after.Rows[0].Member.Entry.Reference.GetFileId())
	require.Nil(t, after.Rows[0].Member.Entry.Status)
	require.Empty(t, after.Rows[0].Member.Entry.Operations)
	require.Nil(t, after.Rows[0].Member.Entry.SizeBytes)
	members, err := s.ListIdenticalMembers(ctx, &entity.ListIdenticalMembersRequest{Scope: scope, GroupId: group.Id,
		ResultId: found.ResultId, Limit: 3})
	require.NoError(t, err)
	require.Len(t, members.Members, 3)
}
