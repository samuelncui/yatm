package apis

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSettingsErrorsUseActionableStatus(t *testing.T) {
	require.Equal(t, codes.InvalidArgument, status.Code(apiError(settingspkg.ErrInvalid)))
	require.Equal(t, codes.FailedPrecondition, status.Code(apiError(settingspkg.ErrStored)))
}

func TestSettingsBrowseEnforcesAdministratorRulesBeforePagination(t *testing.T) {
	// A large directory exposes only authorized subdirectories, including normal dot directories.
	ctx := context.Background()
	api, _, root := setupLocationAPI(t)
	api.exe = executor.New(nil, api.lib, nil, executor.Paths{
		Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: root, Ignore: "a*/\n!a-allowed/\nsecret/\n!secret/visible/\n"}},
	}, executor.Scripts{}, nil)
	service := &locationService{api: api}
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
	api, locations, root := setupLocationAPI(t)
	api.exe = executor.New(nil, api.lib, nil, executor.Paths{
		Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: root, Ignore: "secret/\n"}},
	}, executor.Scripts{}, nil)
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{
		Name: "Restore", RootPath: root,
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "local-only/\n!secret/\n"}},
	}})
	require.NoError(t, err)
	location, err := api.lib.GetLocation(ctx, created.Location.Id)
	require.NoError(t, err)
	_, err = api.exe.CheckLocation(location)
	require.NoError(t, err)
	require.True(t, location.Excluded("secret/file.txt", false))
	require.True(t, location.Excluded("local-only/file.txt", false))
	require.NoError(t, os.Mkdir(filepath.Join(root, "local-only"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "secret"), 0755))
	browser := &filesService{api: api}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID}}}
	page, err := browser.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Query: "type:dir"})
	require.NoError(t, err)
	require.Empty(t, page.Entries, "Location Ignore hides writable destinations from browsing")
	// Ignore is visibility, not authorization: the ignored directory stays an explicit destination.
	ignored, err := browser.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID, Path: "local-only"}}}})
	require.NoError(t, err)
	require.Equal(t, entity.EntryKind_ENTRY_KIND_DIRECTORY, ignored.Detail.Entry.Kind)
	denied := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID, Path: "secret"}}}
	_, err = listFiles(t, ctx, browser, &entity.ListFilesRequest{Directory: denied})
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

	// An imported destination stays usable without a confirmation step.
	var backup bytes.Buffer
	require.NoError(t, api.lib.Export(ctx, &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_LOCATION,
	}))
	require.NoError(t, api.lib.Import(ctx, &backup, false))
	_, err = api.exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{LocationId: location.ID})
	require.NoError(t, err)
	_, err = listFiles(t, ctx, browser, &entity.ListFilesRequest{Directory: directory})
	require.NoError(t, err)
	_, err = api.exe.FreezeRestoreDestination(ctx, &entity.RestoreDestination{LocationId: location.ID})
	require.NoError(t, err, "restoring does not require a scan after explicit import confirmation")
}

func TestSettingsLibraryReplacesOneTypedGroup(t *testing.T) {
	// The local Settings surface replaces the complete typed group without a revision protocol.
	ctx := context.Background()
	api, _, _ := setupLocationAPI(t)
	service := &settingsService{api: api}
	before, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_LIBRARY})
	require.NoError(t, err)
	require.True(t, before.GetValue().GetLibrary().IncludeUnbackedFiles)
	saved, err := service.Update(ctx, &entity.UpdateSettingsRequest{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Library{Library: &entity.LibrarySettings{}}}})
	require.NoError(t, err)
	require.False(t, saved.GetValue().GetLibrary().IncludeUnbackedFiles)

	// A later complete value replaces the earlier one; a missing typed value remains invalid.
	replaced, err := service.Update(ctx, &entity.UpdateSettingsRequest{Value: before.Value})
	require.NoError(t, err)
	require.True(t, replaced.GetValue().GetLibrary().IncludeUnbackedFiles)
	_, err = service.Update(ctx, &entity.UpdateSettingsRequest{Value: &entity.SettingsValue{}})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestSettingsJobExecutionDefaultsRoundTripAndRejections(t *testing.T) {
	// The service publishes complete defaults before any explicit edit.
	ctx := context.Background()
	api, _, _ := setupLocationAPI(t)
	service := &settingsService{api: api}
	libraryBefore, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_LIBRARY})
	require.NoError(t, err)
	beforeValue, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_JOB})
	require.NoError(t, err)
	before := beforeValue.GetValue().GetJob()
	require.Equal(t, int32(256), before.Execution.ReadBatch)
	require.Equal(t, int32(4096), before.Execution.ReadBufferMax)
	require.Equal(t, int32(4096), before.Execution.WriteBufferMax)
	require.Equal(t, int32(256), before.Execution.WriteBatchSize)
	require.Equal(t, int32(1000), before.Execution.FlushIntervalMs)

	// An explicit group round-trips through the generic typed Settings API.
	execution := &entity.JobExecutionSettings{ReadBatch: 8, ReadBufferMax: 64, WriteBufferMax: 32, WriteBatchSize: 16, FlushIntervalMs: 250}
	saved, err := service.Update(ctx, &entity.UpdateSettingsRequest{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Job{Job: &entity.JobSettings{Execution: execution}}}})
	require.NoError(t, err)
	require.True(t, proto.Equal(execution, saved.GetValue().GetJob().Execution))
	currentValue, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_JOB})
	require.NoError(t, err)
	current := currentValue.GetValue().GetJob()
	require.True(t, proto.Equal(execution, current.Execution))
	// Editing the Job group leaves the Library group alone.
	libraryValue, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_LIBRARY})
	require.NoError(t, err)
	require.True(t, proto.Equal(libraryBefore.GetValue().GetLibrary(), libraryValue.GetValue().GetLibrary()))

	// Every unsupported limit is an invalid argument, and a rejected edit changes nothing.
	valid := entity.JobExecutionSettings{ReadBatch: 8, ReadBufferMax: 64, WriteBufferMax: 32, WriteBatchSize: 16, FlushIntervalMs: 250}
	tests := []struct {
		name   string
		change func(*entity.JobExecutionSettings)
	}{
		{name: "zero read batch", change: func(settings *entity.JobExecutionSettings) { settings.ReadBatch = 0 }},
		{name: "negative read batch", change: func(settings *entity.JobExecutionSettings) { settings.ReadBatch = -1 }},
		{name: "zero read buffer", change: func(settings *entity.JobExecutionSettings) { settings.ReadBufferMax = 0 }},
		{name: "oversized read buffer", change: func(settings *entity.JobExecutionSettings) { settings.ReadBufferMax = 1_000_001 }},
		{name: "zero write buffer", change: func(settings *entity.JobExecutionSettings) { settings.WriteBufferMax = 0 }},
		{name: "oversized write buffer", change: func(settings *entity.JobExecutionSettings) { settings.WriteBufferMax = 1_000_001 }},
		{name: "zero write batch", change: func(settings *entity.JobExecutionSettings) { settings.WriteBatchSize = 0 }},
		{name: "write batch above the write buffer", change: func(settings *entity.JobExecutionSettings) { settings.WriteBatchSize = settings.WriteBufferMax + 1 }},
		{name: "zero flush interval", change: func(settings *entity.JobExecutionSettings) { settings.FlushIntervalMs = 0 }},
		{name: "short flush interval", change: func(settings *entity.JobExecutionSettings) { settings.FlushIntervalMs = 99 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rejected := proto.Clone(&valid).(*entity.JobExecutionSettings)
			test.change(rejected)
			_, err := service.Update(ctx, &entity.UpdateSettingsRequest{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Job{Job: &entity.JobSettings{Execution: rejected}}}})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	afterValue, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_JOB})
	require.NoError(t, err)
	after := afterValue.GetValue().GetJob()
	require.True(t, proto.Equal(execution, after.Execution))
}

func TestLocationRecommendationFiltersBeforePagination(t *testing.T) {
	ctx := context.Background()
	api, service, root := setupLocationAPI(t)
	var recommended []int64
	for index := 0; index < 55; index++ {
		location := &library.Location{Name: fmt.Sprintf("Location %d", index), RootPath: filepath.Join(root, fmt.Sprint(index)), ExecutorID: "local", RestoreTarget: index >= 53}
		require.NoError(t, api.lib.CreateLocation(ctx, location))
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
