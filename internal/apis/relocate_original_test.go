package apis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestRelocateOriginalPreservesOrganizationAndRejectsOccupiedTargets(t *testing.T) {
	// An explicit relink changes only the original association, not the disk or logical organization.
	ctx := context.Background()
	api, locations, root := setupLocationAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	for _, name := range []string{"before.txt", "chosen.txt", "occupied.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0644))
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir}})
	require.NoError(t, err)
	service := &filesService{api: api}
	entry, err := service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: "before.txt"}}}})
	require.NoError(t, err)
	admitted, err := api.exe.AdmitLocationEntry(ctx, entry.Detail.Entry.Reference.GetLocation())
	require.NoError(t, err)
	note := "keep my organization"
	require.NoError(t, api.lib.EditFileMetadata(ctx, []int64{admitted.FileID}, library.FileMetadataEdit{Note: &note, AddTags: []string{"keep"}}))
	admittedDetail, err := service.Get(ctx, &entity.GetFileRequest{Reference: entry.Detail.Entry.Reference})
	require.NoError(t, err)
	chosen, err := service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: "chosen.txt"}}}})
	require.NoError(t, err)
	request := &entity.RelocateOriginalRequest{FileId: admitted.FileID, Reference: chosen.Detail.Entry.Reference.GetLocation()}
	state, err := service.RelocateOriginal(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "chosen.txt", state.Detail.Original.Path)
	relinked, err := api.lib.GetFileLocation(ctx, admitted.FileID)
	require.NoError(t, err)
	require.Empty(t, relinked.Signature)
	file, err := api.lib.GetFile(ctx, admitted.FileID)
	require.NoError(t, err)
	require.Equal(t, admittedDetail.Detail.Entry.Name, file.Name)
	require.Equal(t, note, file.Note)
	tags, err := api.lib.MGetFileTags(ctx, file.ID)
	require.NoError(t, err)
	require.Contains(t, tags[file.ID], "keep")
	require.FileExists(t, filepath.Join(dir, "before.txt"))

	// Repeating the explicit association is valid, but another File's path stays occupied.
	_, err = service.RelocateOriginal(ctx, request)
	require.NoError(t, err)
	occupied, err := service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: "occupied.txt"}}}})
	require.NoError(t, err)
	_, err = api.exe.AdmitLocationEntry(ctx, occupied.Detail.Entry.Reference.GetLocation())
	require.NoError(t, err)
	_, err = service.RelocateOriginal(ctx, &entity.RelocateOriginalRequest{FileId: admitted.FileID, Reference: occupied.Detail.Entry.Reference.GetLocation()})
	require.Error(t, err)
	// Replacing the object behind the path changes what is recorded, not whether the caller may relink it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "chosen.txt"), []byte("replaced content"), 0644))
	restored, err := service.RelocateOriginal(ctx, &entity.RelocateOriginalRequest{FileId: admitted.FileID, Reference: chosen.Detail.Entry.Reference.GetLocation()})
	require.NoError(t, err)
	require.Equal(t, "chosen.txt", restored.GetDetail().GetOriginal().GetPath())
}

func TestLiveSelectionEstimatePrunesIgnoredChildren(t *testing.T) {
	// A selected directory excludes ignored children, and an explicitly named ignored child is pruned too.
	ctx := context.Background()
	api, locations, root := setupLocationAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("abc"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("12345"), 0644))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir,
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "ignored.txt\n"}}}})
	require.NoError(t, err)
	var selections []*entity.FileSelection
	for _, path := range []string{"", "ignored.txt", "visible.txt", "ignored.txt"} {
		selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: created.Location.Id, Path: path}}})
	}
	estimate, err := api.exe.InspectSelections(ctx, &entity.SelectionInspection{Selections: selections})
	require.NoError(t, err)
	require.Equal(t, int64(1), estimate.FileCount)
	require.Equal(t, int64(3), estimate.TotalBytes)
	require.Zero(t, estimate.UnknownSizeFileCount)
}
