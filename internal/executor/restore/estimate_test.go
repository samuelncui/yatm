package restore

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestRestoreEstimateResolvesDefaultLibraryScope(t *testing.T) {
	// Restore uses the same Library inspection path when callers submit a DEFAULT scope.
	ctx := context.Background()
	exe, lib, db := setupTestExecutorWithLibraryDB(t)
	file := &library.File{Name: "saved.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, file))
	require.NoError(t, db.Create(&library.FileVersion{FileID: file.ID, Signature: []byte("saved"), Size: 5}).Error)
	selection := &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: file.ID}}, Scope: entity.FileScope_FILE_SCOPE_DEFAULT}

	// The estimate freezes the scope and reports the saved version without a copy.
	reply, err := (&service{exe: exe}).Estimate(ctx, &entity.EstimateRestoreJobRequest{Selections: []*entity.FileSelection{selection}})
	require.NoError(t, err)
	require.EqualValues(t, 1, reply.Result.FileCount)
	require.EqualValues(t, 5, reply.Result.TotalBytes)
	require.EqualValues(t, 1, reply.Result.MissingCopyCount)
	require.Equal(t, entity.FileScope_FILE_SCOPE_ALL, reply.Result.Selections[0].Scope)
	require.Equal(t, entity.FileScope_FILE_SCOPE_DEFAULT, selection.Scope)
}
