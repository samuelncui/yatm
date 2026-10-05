package restore

import (
	"context"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRestoreResultsPageAcrossMedia(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	first, firstFile := createMediaFile(t, lib, newTapeMedia("ABC001"), ".", "a.txt", []byte("a"), []byte{1})
	second, secondFile := createMediaFile(t, lib, newTapeMedia("ABC002"), ".", "b.txt", []byte("b"), []byte{2})
	require.Less(t, first.ID, second.ID)
	job := createRestoreJob(t, exe, firstFile.ID, secondFile.ID)
	waitIndexed(t, exe, job.ID)
	runnerValue, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := runnerValue.(*jobRestoreRunner)

	// The Media view aggregates one group per required Media and continues by Media key.
	mediaPage, err := runner.queryMedia(ctx, &entity.ListRestoreJobMediaRequest{Limit: 1, IncludeTotal: true})
	require.NoError(t, err)
	require.Len(t, mediaPage.Media, 1)
	require.Equal(t, first.ID, mediaPage.Media[0].MediaId)
	require.Equal(t, int64(1), mediaPage.Media[0].FileCount)
	require.True(t, mediaPage.HasMore)
	require.Equal(t, int64(2), mediaPage.GetTotalMediaCount())

	mediaNext, err := runner.queryMedia(ctx, &entity.ListRestoreJobMediaRequest{Limit: 1, Cursor: strconv.FormatInt(first.ID, 10)})
	require.NoError(t, err)
	require.Len(t, mediaNext.Media, 1)
	require.Equal(t, second.ID, mediaNext.Media[0].MediaId)
	require.False(t, mediaNext.HasMore)
	require.Nil(t, mediaNext.TotalMediaCount, "an unrequested total stays absent")

	// The Files view spans every Media through one composite (media_id, id) key.
	filesPage, err := runner.queryFiles(ctx, &entity.ListRestoreJobFilesRequest{Limit: 5, IncludeTotal: true})
	require.NoError(t, err)
	require.Len(t, filesPage.Items, 2)
	require.False(t, filesPage.HasMore)
	require.Equal(t, int64(2), filesPage.GetTotalFileCount())
	require.Equal(t, first.ID, filesPage.Items[0].Candidate.MediaId)
	require.Equal(t, second.ID, filesPage.Items[1].Candidate.MediaId)

	// A descending page crosses the Media boundary in reverse.
	boundary := executor.JobResultPairValue(filesPage.Items[1].Candidate.MediaId, filesPage.Items[1].Id)
	backward, err := runner.queryFiles(ctx, &entity.ListRestoreJobFilesRequest{
		Limit: 1, Cursor: boundary, Order: entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING,
	})
	require.NoError(t, err)
	require.Len(t, backward.Items, 1)
	require.Equal(t, first.ID, backward.Items[0].Candidate.MediaId)
	require.False(t, backward.HasMore)

	// One Media filter narrows both the page and its total.
	filtered, err := runner.queryFiles(ctx, &entity.ListRestoreJobFilesRequest{MediaId: proto.Int64(second.ID), IncludeTotal: true})
	require.NoError(t, err)
	require.Len(t, filtered.Items, 1)
	require.Equal(t, second.ID, filtered.Items[0].Candidate.MediaId)
	require.Equal(t, int64(1), filtered.GetTotalFileCount())

	// An offset anchors a jump into the composite sequence.
	jump, err := runner.queryFiles(ctx, &entity.ListRestoreJobFilesRequest{Limit: 1, Offset: proto.Int64(1)})
	require.NoError(t, err)
	require.Len(t, jump.Items, 1)
	require.Equal(t, second.ID, jump.Items[0].Candidate.MediaId)
	require.False(t, jump.HasMore)
}
