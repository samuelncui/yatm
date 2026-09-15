package main

import (
	"io"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

var rpcCommandBindings = map[string]string{
	entity.FilesService_List_FullMethodName:                       "files list",
	entity.FilesService_Get_FullMethodName:                        "files get",
	entity.FilesService_Inspect_FullMethodName:                    "files inspect",
	entity.FilesService_Collect_FullMethodName:                    "files collect",
	entity.FilesService_UpdateMetadata_FullMethodName:             "files metadata",
	entity.ScanJobService_ReadMedia_FullMethodName:                "scan run",
	entity.ScanJobService_ListScopes_FullMethodName:               "scan scopes",
	entity.FileCatalogService_InspectSelection_FullMethodName:     "file inspect-selection",
	entity.SettingsService_GetLibrary_FullMethodName:              "settings library",
	entity.SettingsService_UpdateLibrary_FullMethodName:           "settings library",
	entity.SettingsService_GetAccess_FullMethodName:               "settings access",
	entity.SettingsService_BrowsePaths_FullMethodName:             "settings browse",
	entity.FileCatalogService_GetState_FullMethodName:             "file state",
	entity.FileCatalogService_RelocateOriginal_FullMethodName:     "file locate-original",
	entity.FileCatalogService_GetVersion_FullMethodName:           "file version",
	entity.FileCatalogService_ListVersions_FullMethodName:         "file versions",
	entity.FileCatalogService_ListCopies_FullMethodName:           "file copies",
	entity.FileCatalogService_ListDuplicates_FullMethodName:       "file duplicates",
	entity.FileCatalogService_ListDuplicateGroups_FullMethodName:  "file duplicate-groups",
	entity.FileCatalogService_ListDuplicateMembers_FullMethodName: "file duplicate-members",
	entity.FileCatalogService_ImportPositions_FullMethodName:      "file import-positions",
	entity.LocationService_Create_FullMethodName:                  "location create",
	entity.LocationService_List_FullMethodName:                    "location list",
	entity.LocationService_Get_FullMethodName:                     "location get",
	entity.LocationService_Update_FullMethodName:                  "location update",
	entity.LocationService_Confirm_FullMethodName:                 "location confirm",
	entity.LocationService_Delete_FullMethodName:                  "location delete",
	entity.LocationService_ListEntries_FullMethodName:             "location entries",
	entity.LocationService_GetEntry_FullMethodName:                "location entry",
	entity.LocationService_Admit_FullMethodName:                   "location admit",
	entity.Service_FileGet_FullMethodName:                         "file get",
	entity.Service_FileListParents_FullMethodName:                 "file parents",
	entity.Service_FileMetadataEdit_FullMethodName:                "file metadata",
	entity.Service_FileSearch_FullMethodName:                      "file search",
	entity.Service_TagList_FullMethodName:                         "tag list",
	entity.Service_MediaList_FullMethodName:                       "media list",
	entity.Service_MediaInspect_FullMethodName:                    "media inspect tape",
	entity.Service_MediaDelete_FullMethodName:                     "media delete",
	entity.Service_MediaGetPositions_FullMethodName:               "media positions",
	entity.Service_VolumeInitialize_FullMethodName:                "volume initialize",
	entity.Service_VolumeRegister_FullMethodName:                  "volume register",
	entity.Service_DeviceList_FullMethodName:                      "tape device list",
	entity.Service_LibraryTrim_FullMethodName:                     "library trim",
	entity.JobService_Get_FullMethodName:                          "job get",
	entity.JobService_List_FullMethodName:                         "job list",
	entity.JobService_Delete_FullMethodName:                       "job delete",
	entity.JobService_Cancel_FullMethodName:                       "job cancel",
	entity.JobService_RetryIndex_FullMethodName:                   "job retry-index",
	entity.JobService_GetLog_FullMethodName:                       "job log",
	entity.ArchiveJobService_Create_FullMethodName:                "archive create",
	entity.ArchiveJobService_WriteMedia_FullMethodName:            "archive write volume",
	entity.ArchiveJobService_GetProgress_FullMethodName:           "job progress",
	entity.ArchiveJobService_ListFiles_FullMethodName:             "archive files",
	entity.RestoreJobService_Create_FullMethodName:                "restore create",
	entity.RestoreJobService_RestoreMedia_FullMethodName:          "restore run volume",
	entity.RestoreJobService_GetProgress_FullMethodName:           "job progress",
	entity.RestoreJobService_ListMedia_FullMethodName:             "restore media",
	entity.RestoreJobService_ListFiles_FullMethodName:             "restore files",
	entity.ScanJobService_Create_FullMethodName:                   "scan create",
	entity.ScanJobService_GetProgress_FullMethodName:              "scan progress",
	entity.ScanJobService_ListEntries_FullMethodName:              "scan results",
	entity.FileOperationService_Execute_FullMethodName:            "fileops run",
}

type httpRouteClass string

const (
	httpRouteBusiness httpRouteClass = "business"
	httpRouteInternal httpRouteClass = "internal"
	httpRouteStatic   httpRouteClass = "static"
)

type httpRouteBinding struct {
	class   httpRouteClass
	command string
	reason  string
}

var httpRouteBindings = map[string]httpRouteBinding{
	"GET /content":                         {class: httpRouteInternal, reason: "Guarded browser Open content and Range responses"},
	"HEAD /content":                        {class: httpRouteInternal, reason: "Guarded browser content metadata"},
	"GET /originals/:file_id":              {class: httpRouteInternal, reason: "Bound original browser Open content and Range responses"},
	"HEAD /originals/:file_id":             {class: httpRouteInternal, reason: "Bound original browser content metadata"},
	"GET /locations/:location_id/content":  {class: httpRouteInternal, reason: "Live browser Open content and Range responses"},
	"HEAD /locations/:location_id/content": {class: httpRouteInternal, reason: "Live browser content metadata"},
	"GET /ping":                            {class: httpRouteBusiness, command: "status"},
	"GET /library/_export":                 {class: httpRouteBusiness, command: "library export"},
	"POST /library/_import":                {class: httpRouteBusiness, command: "library import"},
	"GET /content/:position_id":            {class: httpRouteInternal, reason: "Mounted-copy browser Open content and Range responses"},
	"HEAD /content/:position_id":           {class: httpRouteInternal, reason: "Mounted-copy browser content metadata"},
	"GET /previews/:id/:role":              {class: httpRouteInternal, reason: "Browser rendering of current or saved-version Preview assets"},
	"GET /_upgrade/status":                 {class: httpRouteInternal, reason: "Local service upgrade coordination"},
	"POST /_upgrade/quiesce":               {class: httpRouteInternal, reason: "Local service upgrade coordination"},
}

var staticHTTPRouteBindings = map[string]httpRouteBinding{
	"/assets/": {class: httpRouteStatic},
	"/":        {class: httpRouteStatic},
}

func TestEveryPublicRPCBindsToCLILeaf(t *testing.T) {
	// Enumerate the generated descriptors so newly added public RPCs require an explicit binding.
	descriptors := []*grpc.ServiceDesc{
		&entity.Service_ServiceDesc,
		&entity.SettingsService_ServiceDesc,
		&entity.JobService_ServiceDesc,
		&entity.ArchiveJobService_ServiceDesc,
		&entity.RestoreJobService_ServiceDesc,
		&entity.ScanJobService_ServiceDesc,
		&entity.FileOperationService_ServiceDesc,
		&entity.FilesService_ServiceDesc,
		&entity.LocationService_ServiceDesc,
		&entity.FileCatalogService_ServiceDesc,
	}
	seen := make(map[string]struct{})
	for _, descriptor := range descriptors {
		for _, method := range descriptor.Methods {
			fullMethod := "/" + descriptor.ServiceName + "/" + method.MethodName
			commandPath, ok := rpcCommandBindings[fullMethod]
			require.Truef(t, ok, "public RPC has no CLI binding: %s", fullMethod)
			requireCLILeaf(t, commandPath)
			seen[fullMethod] = struct{}{}
		}
		for _, stream := range descriptor.Streams {
			fullMethod := "/" + descriptor.ServiceName + "/" + stream.StreamName
			commandPath, ok := rpcCommandBindings[fullMethod]
			require.Truef(t, ok, "public RPC has no CLI binding: %s", fullMethod)
			requireCLILeaf(t, commandPath)
			seen[fullMethod] = struct{}{}
		}
	}

	// Reject stale bindings so the catalog remains an exact coverage contract.
	for fullMethod := range rpcCommandBindings {
		_, ok := seen[fullMethod]
		require.Truef(t, ok, "CLI binding references a non-public RPC: %s", fullMethod)
	}
}

func TestDocumentedCommandTreeUsesRealLeaves(t *testing.T) {
	// Resolve every promised user-facing path as a concrete parser leaf.
	commands := []string{
		"status",
		"settings library", "settings access", "settings browse",
		"file get", "file list", "file parents", "file search", "file edit", "file metadata",
		"file mkdir", "file delete",
		"fileops run",
		"file state", "file version", "file versions", "file copies", "file duplicates",
		"file duplicate-groups", "file duplicate-members", "file import-positions", "file inspect-selection",
		"tag list",
		"media list", "media get", "media positions", "media inspect tape", "media inspect volume", "media delete",
		"volume initialize", "volume register",
		"tape device list",
		"job list", "job changes", "job get", "job progress", "job wait", "job log", "job cancel", "job retry-index", "job delete",
		"archive create", "archive files", "archive write volume", "archive write tape append", "archive write tape format",
		"restore create", "restore media", "restore files", "restore run volume", "restore run tape",
		"preview create",
		"scan create", "scan media", "scan progress", "scan results",
		"location create", "location list", "location get", "location update", "location confirm", "location delete",
		"location entries", "location entry", "location admit",
		"analyze create", "analyze progress", "analyze entries",
		"library export", "library import", "library trim",
	}
	for _, command := range commands {
		t.Run(strings.ReplaceAll(command, " ", "/"), func(t *testing.T) {
			requireCLILeaf(t, command)
		})
	}
}

func TestEveryUploaderRouteIsClassified(t *testing.T) {
	// Inspect the registered routes without opening a network listener.
	gin.SetMode(gin.TestMode)
	router := apis.New(nil, nil).Uploader()
	seen := make(map[string]struct{})

	// Enumerate Gin's registered transfer routes rather than maintaining a second route list.
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		binding, ok := httpRouteBindings[key]
		require.Truef(t, ok, "HTTP route is not classified: %s", key)
		if binding.class == httpRouteBusiness {
			requireCLILeaf(t, binding.command)
		} else {
			require.Equal(t, httpRouteInternal, binding.class)
			require.NotEmpty(t, binding.reason, "internal route needs an explicit purpose: %s", key)
			require.Empty(t, binding.command, "internal route must not claim a CLI command: %s", key)
		}
		seen[key] = struct{}{}
	}

	// Reject stale classifications so the inventory follows route removals too.
	for key := range httpRouteBindings {
		_, ok := seen[key]
		require.Truef(t, ok, "HTTP route classification is stale: %s", key)
	}
}

func TestFrontendRoutesAreExplicitlyClassified(t *testing.T) {
	// Keep the top-level static patterns from cmd/httpd distinct from business and upgrade routes.
	require.Equal(t, httpRouteStatic, staticHTTPRouteBindings["/assets/"].class)
	require.Equal(t, httpRouteStatic, staticHTTPRouteBindings["/"].class)
}

func requireCLILeaf(t *testing.T, commandPath string) {
	t.Helper()

	// Resolve the command through the same go-flags parser used by the executable.
	commandRuntime := &runtime{stdin: strings.NewReader(""), stdout: io.Discard}
	parser, err := newParser(new(options), commandRuntime)
	require.NoError(t, err)
	command := parser.Command
	for _, name := range strings.Fields(commandPath) {
		command = command.Find(name)
		require.NotNilf(t, command, "CLI command path does not exist: %s", commandPath)
	}
	require.Emptyf(t, command.Commands(), "CLI binding is not a leaf command: %s", commandPath)
}
