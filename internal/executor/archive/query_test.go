package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func archiveTargetPaths(reply *entity.ListArchiveJobFilesResponse) []string {
	paths := make([]string, 0, len(reply.Items))
	for _, item := range reply.Items {
		paths = append(paths, item.File.TargetPath)
	}
	return paths
}

func TestArchiveManifestPagesByPathInBothDirections(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	names := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}
	sources := make([]*archiveTestSource, 0, len(names))
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, name), []byte(name), 0o644))
		sources = append(sources, &archiveTestSource{Base: exe.Paths().Source, Path: []string{name}})
	}
	job := createArchiveJob(t, exe, sources...)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)

	// A forward page reads one sentinel row, so continuation needs no separate count.
	first, err := runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{Limit: 2, IncludeTotal: true})
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt", "b.txt"}, archiveTargetPaths(first))
	require.True(t, first.HasMore)
	require.Equal(t, int64(5), first.GetTotalFileCount())

	// The next page continues from the last returned order key.
	second, err := runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{Limit: 2, Cursor: "b.txt"})
	require.NoError(t, err)
	require.Equal(t, []string{"c.txt", "d.txt"}, archiveTargetPaths(second))
	require.True(t, second.HasMore)
	require.Nil(t, second.TotalFileCount, "an unrequested total stays absent")

	// A descending page walks the same keys backwards and reports its own continuation.
	backward, err := runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{
		Limit: 2, Cursor: "c.txt", Order: entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING, IncludeTotal: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"b.txt", "a.txt"}, archiveTargetPaths(backward))
	require.False(t, backward.HasMore)
	require.Equal(t, int64(5), backward.GetTotalFileCount())

	// An offset anchors a jump without changing the order key sequence.
	jump, err := runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{Limit: 2, Offset: proto.Int64(3)})
	require.NoError(t, err)
	require.Equal(t, []string{"d.txt", "e.txt"}, archiveTargetPaths(jump))
	require.False(t, jump.HasMore)

	// A cursor beyond the manifest is an empty final page, not an error.
	_, err = runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{Limit: 2, Cursor: "e.txt"})
	require.NoError(t, err)
}

func TestArchiveManifestCountsOnlyWhenRequested(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, "file.txt"), []byte("fixture"), 0o644))
	job := createArchiveJob(t, exe, &archiveTestSource{Base: exe.Paths().Source, Path: []string{"file.txt"}})
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)

	// Count the read queries one page issues; the total is an explicitly requested extra.
	queries := 0
	callback := "test:count_archive_page_queries"
	require.NoError(t, runner.db.Callback().Query().Before("gorm:query").Register(callback, func(*gorm.DB) { queries++ }))
	defer func() { _ = runner.db.Callback().Query().Remove(callback) }()

	_, err = runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{})
	require.NoError(t, err)
	require.Equal(t, 1, queries)

	queries = 0
	_, err = runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{IncludeTotal: true})
	require.NoError(t, err)
	require.Equal(t, 2, queries)
}
