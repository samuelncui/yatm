package apis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

func TestRelocateOriginalPreservesOrganizationAndRejectsStaleOrOccupiedTargets(t *testing.T) {
	// An explicit relink changes only the original association, not the disk or logical organization.
	ctx := context.Background()
	api, locations, root := setupOnlineAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	for _, name := range []string{"before.txt", "chosen.txt", "occupied.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0644))
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir}})
	require.NoError(t, err)
	entry, err := locations.GetEntry(ctx, &entity.GetLocationEntryRequest{LocationId: created.Location.Id, Path: "before.txt"})
	require.NoError(t, err)
	admitted, err := locations.Admit(ctx, entry.Reference)
	require.NoError(t, err)
	note := "keep my organization"
	require.NoError(t, api.lib.EditFileMetadata(ctx, []int64{admitted.File.Id}, library.FileMetadataEdit{Note: &note, AddTags: []string{"keep"}}))
	chosen, err := locations.GetEntry(ctx, &entity.GetLocationEntryRequest{LocationId: created.Location.Id, Path: "chosen.txt"})
	require.NoError(t, err)
	service := &fileCatalogService{api: api}
	request := &entity.RelocateOriginalRequest{FileId: admitted.File.Id, Reference: chosen.Reference, ExpectedOriginal: admitted.Original}
	state, err := service.RelocateOriginal(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "chosen.txt", state.Original.Path)
	require.Empty(t, state.Original.Signature)
	file, err := api.lib.GetFile(ctx, admitted.File.Id)
	require.NoError(t, err)
	require.Equal(t, admitted.File.Name, file.Name)
	require.Equal(t, note, file.Note)
	tags, err := api.lib.MGetFileTags(ctx, file.ID)
	require.NoError(t, err)
	require.Contains(t, tags[file.ID], "keep")
	require.FileExists(t, filepath.Join(dir, "before.txt"))

	// The expected original is a compare-and-swap guard; occupied and changed objects cannot be adopted.
	_, err = service.RelocateOriginal(ctx, request)
	require.Error(t, err)
	occupied, err := locations.GetEntry(ctx, &entity.GetLocationEntryRequest{LocationId: created.Location.Id, Path: "occupied.txt"})
	require.NoError(t, err)
	_, err = locations.Admit(ctx, occupied.Reference)
	require.NoError(t, err)
	_, err = service.RelocateOriginal(ctx, &entity.RelocateOriginalRequest{FileId: admitted.File.Id, Reference: occupied.Reference, ExpectedOriginal: state.Original})
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "chosen.txt"), []byte("replaced content"), 0644))
	_, err = service.RelocateOriginal(ctx, &entity.RelocateOriginalRequest{FileId: admitted.File.Id, Reference: chosen.Reference, ExpectedOriginal: state.Original})
	require.Error(t, err)
}

func TestRelocateCopiedOriginalOverridesProvenance(t *testing.T) {
	// The completed-copy receipt is a fixture; ACP transfer and receipt creation have file-operation E2E coverage.
	ctx := context.Background()
	api, locations, root := setupOnlineAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	for _, name := range []string{"source.txt", "copy.txt", "next-copy.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("copied content"), 0644))
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir}})
	require.NoError(t, err)
	id := created.Location.Id
	get := func(name string) *entity.LocationEntry {
		entry, err := locations.GetEntry(ctx, &entity.GetLocationEntryRequest{LocationId: id, Path: name})
		require.NoError(t, err)
		return entry
	}
	copy, next := get("copy.txt"), get("next-copy.txt")
	for _, entry := range []*entity.LocationEntry{copy, next} {
		require.NoError(t, api.lib.PublishFileOperation(ctx, &library.FileOperationResult{
			OperationID: uuid.NewString(), ItemID: 1, LocationID: id, BindingToken: entry.Reference.BindingToken,
			Kind: entity.FileOperationKind_COPY, SourcePath: "source.txt", TargetPath: entry.Path, OutputIdentity: entry.Reference.Facts.Identity,
		}))
	}
	selected := &library.File{Name: "Chosen identity", Kind: entity.FileKind_FILE_KIND_REGULAR, Note: "chosen note"}
	require.NoError(t, api.lib.SaveFile(ctx, selected))
	catalog := &fileCatalogService{api: api}
	state, err := catalog.RelocateOriginal(ctx, &entity.RelocateOriginalRequest{FileId: selected.ID, Reference: copy.Reference})
	require.NoError(t, err)

	// An unadmitted copy follows the explicit File choice after an external rename, not a new inherited identity.
	require.NoError(t, os.Rename(filepath.Join(dir, "copy.txt"), filepath.Join(dir, "renamed.txt")))
	again, err := locations.Admit(ctx, get("renamed.txt").Reference)
	require.NoError(t, err)
	require.Equal(t, selected.ID, again.File.Id)
	require.Equal(t, selected.Note, again.File.Note)

	// Choosing another original releases the first receipt and claims only the explicitly checked new copy.
	state, err = catalog.RelocateOriginal(ctx, &entity.RelocateOriginalRequest{FileId: selected.ID, ExpectedOriginal: again.Original, Reference: next.Reference})
	require.NoError(t, err)
	require.Equal(t, "next-copy.txt", state.Original.Path)
	oldReceipt, err := api.lib.CopyAdmission(ctx, id, copy.Reference.BindingToken, copy.Reference.Facts.Identity)
	require.NoError(t, err)
	require.Nil(t, oldReceipt.AdmittedFileID)
	newReceipt, err := api.lib.CopyAdmission(ctx, id, next.Reference.BindingToken, next.Reference.Facts.Identity)
	require.NoError(t, err)
	require.Equal(t, &selected.ID, newReceipt.AdmittedFileID)
	former, err := locations.Admit(ctx, get("renamed.txt").Reference)
	require.NoError(t, err)
	require.NotEqual(t, selected.ID, former.File.Id)
	require.Empty(t, former.File.Note)
	require.NoError(t, os.Rename(filepath.Join(dir, "next-copy.txt"), filepath.Join(dir, "chosen-renamed.txt")))
	chosenAgain, err := locations.Admit(ctx, get("chosen-renamed.txt").Reference)
	require.NoError(t, err)
	require.Equal(t, selected.ID, chosenAgain.File.Id)

	// A stale object reference cannot change copy provenance or replace the chosen binding.
	_, err = catalog.RelocateOriginal(ctx, &entity.RelocateOriginalRequest{FileId: selected.ID, ExpectedOriginal: chosenAgain.Original, Reference: copy.Reference})
	require.Error(t, err)
}

func TestLiveSelectionEstimateIncludesExplicitIgnoredChild(t *testing.T) {
	// A selected directory excludes ignored children, but an explicitly selected ordinary child remains included.
	ctx := context.Background()
	api, locations, root := setupOnlineAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("abc"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("12345"), 0644))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir,
		Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "ignored.txt\n"}}})
	require.NoError(t, err)
	var selections []*entity.FileSelection
	for _, path := range []string{"", "ignored.txt", "visible.txt", "ignored.txt"} {
		selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: created.Location.Id, Path: path}}})
	}
	estimate, err := api.exe.InspectSelections(ctx, &entity.InspectSelectionRequest{Selections: selections})
	require.NoError(t, err)
	require.Equal(t, int64(2), estimate.Files)
	require.Equal(t, int64(8), estimate.Bytes)
	require.Zero(t, estimate.UnknownSizeFiles)
}
