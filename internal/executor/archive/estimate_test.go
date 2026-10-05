package archive

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestArchiveEstimateResolvesDefaultLibraryScope(t *testing.T) {
	// File details submit a DEFAULT Library selection for their current original.
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	_, file, _ := publishArchiveOriginal(t, exe, "estimate", []byte("content"))
	selection := archiveSelections(file.ID)[0]
	selection.Scope = entity.FileScope_FILE_SCOPE_DEFAULT
	request := &entity.EstimateArchiveJobRequest{Selections: []*entity.FileSelection{selection}}

	// Inspection reads Settings through its transaction and freezes the response scope.
	reply, err := (&service{exe: exe}).Estimate(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 1, reply.Result.FileCount)
	require.EqualValues(t, len("content"), reply.Result.TotalBytes)
	require.Equal(t, entity.FileScope_FILE_SCOPE_ALL, reply.Result.Selections[0].Scope)
	require.Equal(t, entity.FileScope_FILE_SCOPE_DEFAULT, selection.Scope)
}

func TestArchiveDirectoryEstimatesAndPreparesOnlyFilesWithOriginals(t *testing.T) {
	// The selected directory contains both a live original and a File with no original.
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	_, current, _ := publishArchiveOriginal(t, exe, "selected", []byte("content"))
	missing := &library.File{Name: "missing.txt", ParentID: current.ParentID, Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, exe.Lib().SaveFile(ctx, missing))
	selection := archiveSelections(current.ParentID)
	api := &service{exe: exe}

	// Review counts only the usable original and reports the skipped File separately.
	estimate, err := api.Estimate(ctx, &entity.EstimateArchiveJobRequest{Selections: selection})
	require.NoError(t, err)
	require.EqualValues(t, 1, estimate.Result.FileCount)
	require.EqualValues(t, len("content"), estimate.Result.TotalBytes)
	require.EqualValues(t, 1, estimate.Result.MissingOriginalCount)
	require.Zero(t, estimate.Result.UnknownSizeFileCount)

	// Preparation applies the same filter to the durable manifest.
	created, err := api.Create(ctx, &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{Selections: selection}})
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, waitIndexed(t, exe, created.Job.Id).Status)
	files, err := api.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: created.Job.Id})
	require.NoError(t, err)
	require.Equal(t, []string{"Unforged/selected/original.txt"}, archiveTargetPaths(files))
}
