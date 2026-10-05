//go:build linux || darwin

package apis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type identicalTestStream struct {
	grpc.ServerStream
	updates []*entity.FileOperationResult
	onSend  func(*entity.FileOperationResult)
}

func (s *identicalTestStream) Context() context.Context { return context.Background() }
func (s *identicalTestStream) Send(response *entity.KeepIdenticalResponse) error {
	update := response.Result
	if s.onSend != nil {
		s.onSend(update)
	}
	s.updates = append(s.updates, proto.Clone(update).(*entity.FileOperationResult))
	return nil
}

func TestIdenticalKeepAggregatesCrossLocationPartialFailure(t *testing.T) {
	// A later external failure cannot replay earlier Trash moves or remove the survivor.
	s, scope, dir := identicalFixture(t)
	ctx := context.Background()
	other := filepath.Join(filepath.Dir(dir), "other")
	require.NoError(t, os.Mkdir(other, 0755))
	locations := &locationService{api: s.api}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Other", RootPath: other}})
	require.NoError(t, err)
	filename := filepath.Join(other, "last.txt")
	require.NoError(t, os.WriteFile(filename, []byte("same content"), 0644))
	cacheContentSignature(t, ctx, filename)
	admittedPage(t, locations, created.Location.Id)
	scope.Roots = append(scope.Roots, &entity.IdenticalRoot{LocationId: created.Location.Id})
	groups, err := s.ListIdenticalGroups(ctx, &entity.ListIdenticalGroupsRequest{Scope: scope, Limit: 1, MemberLimit: 1})
	require.NoError(t, err)
	require.Len(t, groups.MemberPages, 1)
	keep := groups.MemberPages[0].Members[0].Entry.Reference
	group := groups.Groups[0]
	stream := &identicalTestStream{}
	stream.onSend = func(update *entity.FileOperationResult) {
		if len(stream.updates) == 0 {
			require.NoError(t, os.Remove(filename))
		}
	}
	require.NoError(t, s.KeepIdentical(&entity.KeepIdenticalRequest{Scope: scope, GroupId: group.Id, Fingerprint: group.Fingerprint, Keep: keep}, stream))
	result := stream.updates[len(stream.updates)-1].Summary
	require.True(t, result.Completed)
	require.Equal(t, int64(2), result.SucceededCount)
	require.Equal(t, int64(1), result.FailedCount)
	require.Zero(t, result.UnprocessedCount)
	require.FileExists(t, filepath.Join(dir, keep.GetLocation().Path))
}

func identicalFixture(t *testing.T) (*filesService, *entity.IdenticalScope, string) {
	t.Helper()
	api, locations, dir, id := admissionLocation(t)
	require.NoError(t, api.exe.AutoMigrate())
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		filename := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(filename, []byte("same content"), 0644))
		cacheContentSignature(t, context.Background(), filename)
	}
	admittedPage(t, locations, id)
	return &filesService{api: api}, &entity.IdenticalScope{Source: entity.IdenticalSource_IDENTICAL_SOURCE_LOCATIONS, Roots: []*entity.IdenticalRoot{{LocationId: id}}}, dir
}

func TestIdenticalKeepIncludesUnloadedMembersAndUsesTrash(t *testing.T) {
	// A one-row page must not limit the backend's complete Keep action.
	s, scope, dir := identicalFixture(t)
	ctx := context.Background()
	groups, err := s.ListIdenticalGroups(ctx, &entity.ListIdenticalGroupsRequest{Scope: scope, Limit: 1})
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	group := groups.Groups[0]
	page, err := s.ListIdenticalMembers(ctx, &entity.ListIdenticalMembersRequest{Scope: scope, GroupId: group.Id, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Members, 1)
	require.NotEmpty(t, page.NextCursor)
	keep := page.Members[0].Entry.Reference
	stream := &identicalTestStream{}
	require.NoError(t, s.KeepIdentical(&entity.KeepIdenticalRequest{Scope: scope, GroupId: group.Id, Fingerprint: group.Fingerprint, Keep: keep}, stream))
	result := stream.updates[len(stream.updates)-1].Summary
	require.True(t, result.Completed)
	require.Equal(t, int64(2), result.SucceededCount)
	require.Zero(t, result.FailedCount)
	for _, update := range stream.updates {
		if update.Entry != nil {
			require.NotNil(t, update.Entry.FileId)
			require.Positive(t, update.Entry.GetFileId())
		}
	}
	require.FileExists(t, filepath.Join(dir, keep.GetLocation().Path))
	var trashFiles int
	require.NoError(t, filepath.WalkDir(filepath.Join(dir, ".trash"), func(_ string, item os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !item.IsDir() && (item.Name() == "a.txt" || item.Name() == "b.txt" || item.Name() == "c.txt") {
			trashFiles++
		}
		return nil
	}))
	require.Equal(t, 2, trashFiles)
}

func TestIdenticalKeepRejectsChangedSurvivorBeforeAnyRemoval(t *testing.T) {
	s, scope, dir := identicalFixture(t)
	ctx := context.Background()
	groups, err := s.ListIdenticalGroups(ctx, &entity.ListIdenticalGroupsRequest{Scope: scope})
	require.NoError(t, err)
	group := groups.Groups[0]
	page, err := s.ListIdenticalMembers(ctx, &entity.ListIdenticalMembersRequest{Scope: scope, GroupId: group.Id})
	require.NoError(t, err)
	keep := page.Members[0].Entry.Reference
	require.NoError(t, os.WriteFile(filepath.Join(dir, keep.GetLocation().Path), []byte("changed"), 0644))
	stream := &identicalTestStream{}
	require.Error(t, s.KeepIdentical(&entity.KeepIdenticalRequest{Scope: scope, GroupId: group.Id, Fingerprint: group.Fingerprint, Keep: keep}, stream))
	require.Empty(t, stream.updates)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		require.FileExists(t, filepath.Join(dir, name))
	}
}

func TestIdenticalMergeValidatesTargetAndFingerprint(t *testing.T) {
	s, _, _ := identicalFixture(t)
	ctx := context.Background()
	scope := &entity.IdenticalScope{Source: entity.IdenticalSource_IDENTICAL_SOURCE_LIBRARY}
	groups, err := s.ListIdenticalGroups(ctx, &entity.ListIdenticalGroupsRequest{Scope: scope})
	require.NoError(t, err)
	group := groups.Groups[0]
	page, err := s.ListIdenticalMembers(ctx, &entity.ListIdenticalMembersRequest{Scope: scope, GroupId: group.Id, Limit: 1})
	require.NoError(t, err)
	target := page.Members[0].Entry.Reference.GetFileId()
	_, err = s.MergeIdentical(ctx, &entity.MergeIdenticalRequest{Scope: scope, GroupId: group.Id, Fingerprint: "stale", TargetFileId: target})
	require.Error(t, err)
	_, err = s.MergeIdentical(ctx, &entity.MergeIdenticalRequest{Scope: scope, GroupId: group.Id, Fingerprint: group.Fingerprint, TargetFileId: 999999})
	require.Error(t, err)
	merged, err := s.MergeIdentical(ctx, &entity.MergeIdenticalRequest{Scope: scope, GroupId: group.Id, Fingerprint: group.Fingerprint, TargetFileId: target})
	require.NoError(t, err)
	require.Equal(t, int64(2), merged.RemovedFileCount)
	groups, err = s.ListIdenticalGroups(ctx, &entity.ListIdenticalGroupsRequest{Scope: scope})
	require.NoError(t, err)
	require.Empty(t, groups.Groups)
}

func TestIdenticalKeepRejectsStaleComponent(t *testing.T) {
	// A newly admitted peer invalidates the selected group's fingerprint before physical removal.
	s, scope, dir := identicalFixture(t)
	ctx := context.Background()
	groups, err := s.ListIdenticalGroups(ctx, &entity.ListIdenticalGroupsRequest{Scope: scope})
	require.NoError(t, err)
	group := groups.Groups[0]
	page, err := s.ListIdenticalMembers(ctx, &entity.ListIdenticalMembersRequest{Scope: scope, GroupId: group.Id})
	require.NoError(t, err)
	filename := filepath.Join(dir, "new.txt")
	require.NoError(t, os.WriteFile(filename, []byte("same content"), 0644))
	cacheContentSignature(t, ctx, filename)
	admittedPage(t, &locationService{api: s.api}, scope.Roots[0].LocationId)
	stream := &identicalTestStream{}
	err = s.KeepIdentical(&entity.KeepIdenticalRequest{Scope: scope, GroupId: group.Id,
		Fingerprint: group.Fingerprint, Keep: page.Members[0].Entry.Reference}, stream)
	require.Equal(t, codes.Aborted, status.Code(err))
	require.Empty(t, stream.updates)
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "new.txt"} {
		require.FileExists(t, filepath.Join(dir, name))
	}
}

func TestIdenticalMemberObservationRejectsMetadataPreservingReplacement(t *testing.T) {
	// Native evidence detects a replaced inode without rehashing or trusting size and time alone.
	s, scope, dir := identicalFixture(t)
	ctx := context.Background()
	groups, err := s.ListIdenticalGroups(ctx, &entity.ListIdenticalGroupsRequest{Scope: scope, Limit: 10})
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	page, err := s.ListIdenticalMembers(ctx, &entity.ListIdenticalMembersRequest{Scope: scope, GroupId: groups.Groups[0].Id, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Members, 3)
	for _, member := range page.Members {
		require.NotEmpty(t, member.Entry.Operations)
	}

	// Preserve length, mode and timestamps on a new inode; the recorded original remains historical.
	filename := filepath.Join(dir, "a.txt")
	info, err := os.Stat(filename)
	require.NoError(t, err)
	require.NoError(t, os.Rename(filename, filename+".old"))
	require.NoError(t, os.WriteFile(filename, []byte("diff content"), info.Mode()))
	require.NoError(t, os.Chtimes(filename, info.ModTime(), info.ModTime()))

	after, err := s.ListIdenticalMembers(ctx, &entity.ListIdenticalMembersRequest{Scope: scope, GroupId: groups.Groups[0].Id, Limit: 10})
	require.NoError(t, err)
	require.Len(t, after.Members, 3)
	for _, member := range after.Members {
		if member.Entry.Name == "a.txt" {
			require.Empty(t, member.Entry.Operations, "a changed original is not an actionable member")
			continue
		}
		require.NotEmpty(t, member.Entry.Operations)
	}
}
