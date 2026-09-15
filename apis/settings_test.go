package apis

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSettingsBrowseEnforcesAdministratorRulesBeforePagination(t *testing.T) {
	// A large directory exposes only authorized subdirectories, including normal dot directories.
	ctx := context.Background()
	api, _, root := setupOnlineAPI(t)
	api.exe = executor.New(nil, api.lib, nil, executor.Paths{
		Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: root, Ignore: "a*/\n!a-allowed/\nsecret/\n!secret/visible/\n"}},
	}, executor.Scripts{}, nil)
	service := &settingsService{api: api}
	for index := 0; index < 110; index++ {
		require.NoError(t, os.Mkdir(filepath.Join(root, fmt.Sprintf("a-%03d", index)), 0755))
	}
	for _, name := range []string{".notes", "a-allowed", "z-last", "secret/visible"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0755))
	}
	require.NoError(t, os.Symlink(filepath.Join(root, "z-last"), filepath.Join(root, "symlink")))

	// Filtering before the page boundary returns full authorized pages and path-bound continuations.
	page, err := service.BrowsePaths(ctx, &entity.BrowsePathsRequest{Path: root, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{".notes", "a-allowed"}, []string{page.Directories[0].Name, page.Directories[1].Name})
	require.NotEmpty(t, page.NextCursor)
	last, err := service.BrowsePaths(ctx, &entity.BrowsePathsRequest{Path: root, Cursor: page.NextCursor, Limit: 2})
	require.NoError(t, err)
	require.Len(t, last.Directories, 1)
	require.Equal(t, "z-last", last.Directories[0].Name)
	require.Empty(t, last.NextCursor)
	_, err = service.BrowsePaths(ctx, &entity.BrowsePathsRequest{Path: filepath.Join(root, "z-last"), Cursor: page.NextCursor})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// Explicit navigation cannot bypass a denied parent, symlink or out-of-range root.
	for _, denied := range []string{filepath.Join(root, "secret", "visible"), filepath.Join(root, "symlink"), t.TempDir()} {
		_, err := service.BrowsePaths(ctx, &entity.BrowsePathsRequest{Path: denied})
		require.Equal(t, codes.PermissionDenied, status.Code(err), denied)
	}
}

func TestLocationIgnoreCannotExpandAccessAndRestoreNeedsNoScanOrRecommendation(t *testing.T) {
	// A non-recommended Location remains a valid destination within administrator policy.
	ctx := context.Background()
	api, locations, root := setupOnlineAPI(t)
	api.exe = executor.New(nil, api.lib, nil, executor.Paths{
		Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: root, Ignore: "secret/\n"}},
	}, executor.Scripts{}, nil)
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{
		Name: "Restore", RootPath: root,
		Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "local-only/\n!secret/\n"},
	}})
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, created.Location.Binding)
	location, err := api.lib.GetOnlineSource(ctx, created.Location.Id)
	require.NoError(t, err)
	_, err = api.exe.CheckOnlineSource(location)
	require.NoError(t, err)
	require.True(t, location.Excluded("secret/file.txt", false))
	require.True(t, location.Excluded("local-only/file.txt", false))
	require.NoError(t, os.Mkdir(filepath.Join(root, "local-only"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "secret"), 0755))
	browser := &settingsService{api: api}
	page, err := browser.BrowsePaths(ctx, &entity.BrowsePathsRequest{LocationId: location.ID})
	require.NoError(t, err)
	require.Len(t, page.Directories, 1)
	require.Equal(t, "local-only", page.Directories[0].Name, "index Ignore does not remove writable destinations")
	_, err = browser.BrowsePaths(ctx, &entity.BrowsePathsRequest{LocationId: location.ID, Path: "secret"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// Restore destinations are usable immediately, while their index Ignore never authorizes writes.
	frozen, err := api.exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{LocationId: location.ID})
	require.NoError(t, err)
	_, err = api.exe.RestoreOutputPath(ctx, frozen, "local-only/restored.txt")
	require.NoError(t, err, "Location Ignore controls indexing, not restore authorization")
	_, err = api.exe.RestoreOutputPath(ctx, frozen, "secret/restored.txt")
	require.Error(t, err)
	selection := &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: location.ID}}}
	require.NoError(t, api.lib.FreezeSelections(ctx, []*entity.FileSelection{selection}))
	require.NotZero(t, selection.GetLocation().Revision, "selection freezes the published index; execution checks each observation")

	// Imported destinations require local confirmation even when their paths still exist.
	var backup bytes.Buffer
	require.NoError(t, api.lib.Export(ctx, &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_FILE, entity.LibraryEntityType_LOCATION, entity.LibraryEntityType_FILE_LOCATION,
	}))
	require.NoError(t, api.lib.Import(ctx, &backup))
	_, err = api.exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{LocationId: location.ID})
	require.ErrorIs(t, err, library.ErrOnlineUnverified)
	_, err = browser.BrowsePaths(ctx, &entity.BrowsePathsRequest{LocationId: location.ID})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	location, err = api.lib.GetOnlineSource(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_UNCONFIRMED, location.Binding)
	_, err = api.lib.UpdateOnlineSource(ctx, location, true)
	require.NoError(t, err)
	_, err = api.exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{LocationId: location.ID})
	require.NoError(t, err, "restoring does not require a scan after explicit import confirmation")
}

func TestSettingsLibraryUsesConflictStatus(t *testing.T) {
	// The service exposes persistent preferences with an optimistic revision.
	ctx := context.Background()
	api, _, _ := setupOnlineAPI(t)
	service := &settingsService{api: api}
	before, err := service.GetLibrary(ctx, &entity.GetLibrarySettingsRequest{})
	require.NoError(t, err)
	require.True(t, before.IncludeUnbackedFiles)
	saved, err := service.UpdateLibrary(ctx, &entity.UpdateLibrarySettingsRequest{Settings: &entity.LibrarySettings{Revision: before.Revision}})
	require.NoError(t, err)
	require.False(t, saved.Settings.IncludeUnbackedFiles)

	// A stale request reports a real concurrency error rather than silently overwriting Settings.
	_, err = service.UpdateLibrary(ctx, &entity.UpdateLibrarySettingsRequest{Settings: before})
	require.Equal(t, codes.Aborted, status.Code(err))
	_, err = service.UpdateLibrary(ctx, &entity.UpdateLibrarySettingsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestLocationRecommendationFiltersBeforePagination(t *testing.T) {
	ctx := context.Background()
	api, service, root := setupOnlineAPI(t)
	var recommended []int64
	for index := 0; index < 55; index++ {
		location := &library.Location{Name: fmt.Sprintf("Location %d", index), RootPath: filepath.Join(root, fmt.Sprint(index)), ExecutorID: "local", RestoreTarget: index >= 53}
		require.NoError(t, api.lib.CreateOnlineSource(ctx, location))
		if location.RestoreTarget {
			recommended = append(recommended, location.ID)
		}
	}
	first, err := service.List(ctx, &entity.ListLocationsRequest{Limit: 1, RestoreTarget: proto.Bool(true)})
	require.NoError(t, err)
	require.Len(t, first.Locations, 1)
	require.Equal(t, recommended[0], first.Locations[0].Id)
	require.True(t, first.HasMore)
	last, err := service.List(ctx, &entity.ListLocationsRequest{AfterId: first.Locations[0].Id, Limit: 1, RestoreTarget: proto.Bool(true)})
	require.NoError(t, err)
	require.Equal(t, recommended[1], last.Locations[0].Id)
	require.False(t, last.HasMore)
	ordinary, err := service.List(ctx, &entity.ListLocationsRequest{Limit: 100, RestoreTarget: proto.Bool(false)})
	require.NoError(t, err)
	require.Len(t, ordinary.Locations, 53)
	all, err := service.List(ctx, &entity.ListLocationsRequest{Limit: 100})
	require.NoError(t, err)
	require.Len(t, all.Locations, 55)
}

func TestBrowsePathsDoesNotConvertResourceFailuresIntoEmptyPages(t *testing.T) {
	// Fail a runtime-resource check only after the selected root passed admission.
	ctx := context.Background()
	api, _, root := setupOnlineAPI(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "documents"), 0755))
	loop := filepath.Join(root, "broken-resource")
	require.NoError(t, os.Symlink(loop, loop))
	checks := 0
	api.exe.SetOnlineRuntimePathProvider(func() []string {
		checks++
		if checks == 1 {
			return nil
		}
		return []string{loop}
	})

	// Failure to establish exclusions is an error, not evidence that no allowed directory exists.
	page, err := (&settingsService{api: api}).BrowsePaths(ctx, &entity.BrowsePathsRequest{Path: root})
	require.Error(t, err)
	require.Nil(t, page)
	require.Greater(t, checks, 1)
}
