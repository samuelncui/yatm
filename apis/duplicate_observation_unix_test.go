//go:build linux || darwin

package apis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestDuplicateObservationRejectsMetadataPreservingReplacement(t *testing.T) {
	// Current duplicate membership uses native evidence as well as size/time, without rehashing.
	ctx := context.Background()
	api, locations, dir, id := admissionLocation(t)
	for _, name := range []string{"first.txt", "second.txt"} {
		filename := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(filename, []byte("equal content"), 0644))
		job, err := acp.New(ctx, acp.AccurateJob(filename, nil), acp.WithHash(true), acp.WithSignatureCache(true))
		require.NoError(t, err)
		require.NoError(t, job.WaitErr())
	}
	admittedPage(t, locations, id)
	service := &fileCatalogService{api: api}
	groups, err := service.ListDuplicateGroups(ctx, &entity.ListDuplicateGroupsRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	request := &entity.ListDuplicateMembersRequest{Signature: groups.Groups[0].Signature, Limit: 10}
	before, err := service.ListDuplicateMembers(ctx, request)
	require.NoError(t, err)
	for _, member := range before.Members {
		require.Equal(t, entity.ContentObservation_CONFIRMED, member.Observation)
	}

	// Preserve timestamps and length on a new inode; the old candidate remains only historical evidence.
	filename := filepath.Join(dir, "first.txt")
	info, err := os.Stat(filename)
	require.NoError(t, err)
	require.NoError(t, os.Rename(filename, filename+".old"))
	require.NoError(t, os.WriteFile(filename, []byte("other content"), info.Mode()))
	require.NoError(t, os.Chtimes(filename, info.ModTime(), info.ModTime()))
	after, err := service.ListDuplicateMembers(ctx, request)
	require.NoError(t, err)
	for _, member := range after.Members {
		expected := entity.ContentObservation_CONFIRMED
		if member.Original.Path == "first.txt" {
			expected = entity.ContentObservation_CHANGED
		}
		require.Equal(t, expected, member.Observation)
	}
}
