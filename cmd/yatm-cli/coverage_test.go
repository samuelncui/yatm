package main

import (
	"io"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

var rpcCommandBindings = map[string]string{
	entity.FilesService_Mkdir_FullMethodName:             "mkdir",
	entity.FilesService_Move_FullMethodName:              "mv",
	entity.FilesService_Remove_FullMethodName:            "rm",
	entity.PreviewService_Get_FullMethodName:             "preview get",
	entity.PreviewService_GetCapabilities_FullMethodName: "preview capabilities",
	entity.ArchiveJobService_Estimate_FullMethodName:     "archive estimate",
	entity.RestoreJobService_Estimate_FullMethodName:     "restore estimate",
	entity.FilesService_List_FullMethodName:              "ls",
	entity.FilesService_Search_FullMethodName:            "ls",
	entity.FilesService_Measure_FullMethodName:           "du",
	entity.FilesService_Get_FullMethodName:               "files get",

	entity.FilesService_UpdateMetadata_FullMethodName: "files metadata",
	entity.ScanJobService_ReadMedia_FullMethodName:    "scan run",

	entity.SettingsService_Get_FullMethodName:         "settings library",
	entity.SettingsService_Update_FullMethodName:      "settings library",
	entity.LocationService_GetAccess_FullMethodName:   "settings access",
	entity.LocationService_BrowsePaths_FullMethodName: "settings browse",

	entity.FilesService_RelocateOriginal_FullMethodName:         "files locate-original",
	entity.FilesService_GetVersion_FullMethodName:               "files version",
	entity.FilesService_ListVersions_FullMethodName:             "files versions",
	entity.FilesService_ListCopies_FullMethodName:               "files copies",
	entity.FilesService_ListDuplicates_FullMethodName:           "files duplicates",
	entity.FilesService_RemoveVersion_FullMethodName:            "files remove-version",
	entity.FilesService_FindIdentical_FullMethodName:            "identical find",
	entity.FilesService_ListIdenticalRows_FullMethodName:        "identical rows",
	entity.FilesService_LookupIdenticalPositions_FullMethodName: "identical positions",
	entity.FilesService_CloseIdenticalResult_FullMethodName:     "identical close",
	entity.FilesService_ListIdenticalGroups_FullMethodName:      "identical groups",
	entity.FilesService_ListIdenticalMembers_FullMethodName:     "identical members",
	entity.FilesService_KeepIdentical_FullMethodName:            "identical keep",
	entity.FilesService_MergeIdentical_FullMethodName:           "identical merge",
	entity.FilesService_ImportPositions_FullMethodName:          "files import-positions",
	entity.LocationService_Create_FullMethodName:                "location create",
	entity.LocationService_List_FullMethodName:                  "location list",
	entity.LocationService_Get_FullMethodName:                   "location get",
	entity.LocationService_Update_FullMethodName:                "location update",
	entity.LocationService_Delete_FullMethodName:                "location delete",

	entity.LibraryService_ListTags_FullMethodName:           "tag list",
	entity.MediaService_List_FullMethodName:                 "media list",
	entity.MediaService_Inspect_FullMethodName:              "media inspect tape",
	entity.MediaService_Delete_FullMethodName:               "media delete",
	entity.MediaService_ListPositions_FullMethodName:        "media positions",
	entity.MediaService_InitializeVolume_FullMethodName:     "volume initialize",
	entity.MediaService_RegisterVolume_FullMethodName:       "volume register",
	entity.MediaService_ListVolumeCandidates_FullMethodName: "volume candidates",
	entity.MediaService_ListDevices_FullMethodName:          "tape device list",
	entity.LibraryService_Trim_FullMethodName:               "library trim",
	entity.JobService_Get_FullMethodName:                    "job get",
	entity.JobService_List_FullMethodName:                   "job list",
	entity.JobService_Delete_FullMethodName:                 "job delete",
	entity.JobService_Cancel_FullMethodName:                 "job cancel",
	entity.JobService_GetLog_FullMethodName:                 "job log",
	entity.JobService_ListLogLines_FullMethodName:           "job log-lines",
	entity.ArchiveJobService_Create_FullMethodName:          "archive create",
	entity.ArchiveJobService_GetCreation_FullMethodName:     "archive creation",
	entity.ArchiveJobService_WriteMedia_FullMethodName:      "archive write volume",
	entity.ArchiveJobService_GetProgress_FullMethodName:     "job progress",
	entity.ArchiveJobService_ListFiles_FullMethodName:       "archive files",
	entity.RestoreJobService_Create_FullMethodName:          "restore create",
	entity.RestoreJobService_GetCreation_FullMethodName:     "restore creation",
	entity.RestoreJobService_RestoreMedia_FullMethodName:    "restore run volume",
	entity.RestoreJobService_GetProgress_FullMethodName:     "job progress",
	entity.RestoreJobService_ListMedia_FullMethodName:       "restore media",
	entity.RestoreJobService_ListFiles_FullMethodName:       "restore files",
	entity.ScanJobService_Create_FullMethodName:             "scan create",
	entity.ScanJobService_GetCreation_FullMethodName:        "scan creation",
	entity.ScanJobService_GetProgress_FullMethodName:        "scan progress",
	entity.ScanJobService_ListEntries_FullMethodName:        "scan results",
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
	"GET /ping":              {class: httpRouteBusiness, command: "status"},
	"GET /library/_export":   {class: httpRouteBusiness, command: "library export"},
	"POST /library/_import":  {class: httpRouteBusiness, command: "library import"},
	"GET /preview":           {class: httpRouteInternal, reason: "Content-bound Preview resource returned by PreviewService"},
	"GET /_upgrade/status":   {class: httpRouteInternal, reason: "Local service upgrade coordination"},
	"POST /_upgrade/quiesce": {class: httpRouteInternal, reason: "Local service upgrade coordination"},
}

var staticHTTPRouteBindings = map[string]httpRouteBinding{
	"/assets/": {class: httpRouteStatic},
	"/":        {class: httpRouteStatic},
}

func TestEveryPublicRPCBindsToCLILeaf(t *testing.T) {
	// Enumerate the generated descriptors so newly added public RPCs require an explicit binding.
	descriptors := []*grpc.ServiceDesc{
		&entity.MediaService_ServiceDesc,
		&entity.LibraryService_ServiceDesc,
		&entity.PreviewService_ServiceDesc,
		&entity.SettingsService_ServiceDesc,
		&entity.JobService_ServiceDesc,
		&entity.ArchiveJobService_ServiceDesc,
		&entity.RestoreJobService_ServiceDesc,
		&entity.ScanJobService_ServiceDesc,

		&entity.FilesService_ServiceDesc,
		&entity.LocationService_ServiceDesc,
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
		"ls", "mv", "rm", "mkdir", "files get", "files metadata", "files version", "files versions", "files copies", "files duplicates",
		"files remove-version", "files import-positions",
		"identical find", "identical rows", "identical positions", "identical close",
		"identical groups", "identical members", "identical keep", "identical merge",
		"tag list",
		"media list", "media get", "media positions", "media inspect tape", "media inspect volume", "media delete",
		"volume initialize", "volume register", "volume candidates",
		"tape device list",
		"job list", "job changes", "job get", "job progress", "job wait", "job log", "job log-lines", "job cancel", "job delete",
		"archive create", "archive files", "archive write volume", "archive write tape append", "archive write tape format",
		"archive creation", "restore creation", "scan creation",
		"restore create", "restore media", "restore files", "restore run volume", "restore run tape",
		"preview create",
		"scan create", "scan media", "scan progress", "scan results",
		"location create", "location list", "location get", "location update", "location delete",

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
