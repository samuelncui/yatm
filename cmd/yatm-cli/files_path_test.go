package main

import (
	"context"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestParseDirectoryPath(t *testing.T) {
	for _, test := range []struct {
		input, location, path string
	}{
		{input: "/"},
		{input: "."},
		{input: "./"},
		{input: "/Photos/2026", path: "Photos/2026"},
		{input: "Photos/2026", path: "Photos/2026"},
		{input: "./Photos//2026/", path: "Photos/2026"},
		{input: "Photos/../Videos/./", path: "Videos"},
		{input: "library:2026", path: "library:2026"},
		{input: "/library:2026", path: "library:2026"},
		{input: "./library:2026", path: "library:2026"},
		{input: "location:NAS", path: "location:NAS"},
		{input: "library:/Photos", path: "library:/Photos"},
		{input: "location:/NAS", path: "location:/NAS"},
		{input: "library:///"},
		{input: "library:///Photos/2026", path: "Photos/2026"},
		{input: "LIBRARY:///Photos", path: "Photos"},
		{input: `/照片/100% ready?# "OR" *`, path: `照片/100% ready?# "OR" *`},
		{input: "library:///照片/100%25%20ready%3F%23", path: "照片/100% ready?#"},
		{input: "/%2e%2e", path: "%2e%2e"},
		{input: "library:///%252e%252e", path: "%2e%2e"},
		{input: "location://NAS", location: "NAS"},
		{input: "location://NAS/", location: "NAS"},
		{input: "location://NAS/DCIM", location: "NAS", path: "DCIM"},
		{input: "location://Home NAS/DCIM", location: "Home NAS", path: "DCIM"},
		{input: "location://Home%20NAS/DCIM/../照片", location: "Home NAS", path: "照片"},
		{input: "location://NAS%25%3F%23/%252e%252e", location: "NAS%?#", path: "%2e%2e"},
		{input: `Photos\2026`, path: `Photos\2026`},
		{input: " leading/trailing / \t\n", path: " leading/trailing / \t\n"},
		{input: "library:///back%5Cslash/%20%09%0A", path: "back\\slash/ \t\n"},
		{input: "location://NAS/back%5Cslash/%20%09%0A", location: "NAS", path: "back\\slash/ \t\n"},
	} {
		t.Run(test.input, func(t *testing.T) {
			location, path, err := parseDirectoryPath(test.input)
			require.NoError(t, err)
			require.Equal(t, test.location, location)
			require.Equal(t, test.path, path)
		})
	}
}

func TestLSPathRejectsInvalidInputBeforeNetwork(t *testing.T) {
	// Invalid operands and conflicting selectors must fail as usage, before a path lookup.
	for _, input := range []string{
		"", "..", "/../Photos", "Photos/../../Videos", "location://NAS/../etc",
		"location://NAS/%2e%2e/etc", "location://NAS/DCIM/../../etc", "library:///%2F../etc",
		"library://host/Photos", "location:///DCIM",
		"location://.", "location://..", "location://%20/", "location://NAS%2Fother/DCIM",
		"location://NAS%5Cother/DCIM", "location://NAS%00/DCIM", "location://%FF/DCIM",
		"library:///bad%", "location://bad%/", "library:///bad%00", "library:///bad%FF",
		"library:///a?query", "location://NAS/a#fragment", "hdfs://host/Photos",
		"Photos\x00", "Photos\xff", strings.Repeat("dir/", 257),
	} {
		t.Run(input, func(t *testing.T) {
			exit, stdout, stderr := executeTestCLI("http://127.0.0.1:1", "", "ls", input)
			require.Equal(t, exitUsage, exit, stderr)
			require.Empty(t, stdout)
		})
	}

	// Parsing still consumes exactly one operand and retains the existing listing/query distinction.
	for _, args := range [][]string{
		{"ls", "/Photos", "--file-id", "17"},
		{"ls", "--location-id", "4", "location://NAS"},
		{"ls", "/Photos", "--path", "DCIM"},
		{"ls", "/Photos", "/Videos"},
		{"ls", "/Photos", "--limit", "1"},
		{"ls", "/Photos", "--cursor", "next"},
		{"ls", "/Photos", "--query", "type:file", "--limit", "501"},
		{"ls", "/Photos", "--query", "type:file", "--limit", "0"},
		{"ls", "--path", "DCIM"},
		{"du", "/Photos"},
	} {
		exit, stdout, stderr := executeTestCLI("http://127.0.0.1:1", "", args...)
		require.Equal(t, exitUsage, exit, stderr)
		require.Empty(t, stdout)
	}
}

type pathFilesServer struct {
	filesCommandServer
	searchPage func(*entity.SearchFilesRequest) (*entity.SearchFilesResponse, error)
}

func (s *pathFilesServer) Search(_ context.Context, req *entity.SearchFilesRequest) (*entity.SearchFilesResponse, error) {
	return s.searchPage(req)
}

type pathLocationServer struct {
	entity.UnimplementedLocationServiceServer
	listPage func(*entity.ListLocationsRequest) (*entity.ListLocationsResponse, error)
}

func (s *pathLocationServer) List(_ context.Context, req *entity.ListLocationsRequest) (*entity.ListLocationsResponse, error) {
	return s.listPage(req)
}

func TestLSLibraryPathPagesAndPreservesReadOptions(t *testing.T) {
	// A similar name on the first page must not shadow the exact directory on the second.
	name := `2026 照片 "OR" *?#%`
	files := &pathFilesServer{searchPage: func(req *entity.SearchFilesRequest) (*entity.SearchFilesResponse, error) {
		if req.Directory.GetFileId() == 29 {
			require.Equal(t, "tag:keep", req.Query)
			require.True(t, req.Recursive)
			require.Equal(t, entity.FileScope_FILE_SCOPE_DEFAULT, req.Scope)
			require.EqualValues(t, 2, req.Limit)
			require.Equal(t, "query-first", req.Cursor)
			return &entity.SearchFilesResponse{Scope: req.Scope, NextCursor: "query-next"}, nil
		}
		require.Equal(t, "type:dir", req.Query)
		require.Equal(t, entity.FileScope_FILE_SCOPE_ALL, req.Scope)
		require.False(t, req.Recursive)
		require.Empty(t, req.Include)
		require.Greater(t, req.Limit, int32(0))
		require.LessOrEqual(t, req.Limit, int32(500))
		switch {
		case req.Directory.GetFileId() == 0 && req.Cursor == "":
			return &entity.SearchFilesResponse{NextCursor: "directories-next", Entries: []*entity.FilesEntry{
				{Reference: fileReference(1), Name: "photos", Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY},
				{Reference: fileReference(2), Name: "Photos extra", Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY},
			}}, nil
		case req.Directory.GetFileId() == 0 && req.Cursor == "directories-next":
			return &entity.SearchFilesResponse{Entries: []*entity.FilesEntry{
				{Reference: fileReference(17), Name: "Photos", Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY},
			}}, nil
		case req.Directory.GetFileId() == 17 && req.Cursor == "":
			return &entity.SearchFilesResponse{Entries: []*entity.FilesEntry{
				{Reference: fileReference(29), Name: name, Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY},
			}}, nil
		default:
			return nil, status.Error(codes.Internal, "unexpected path resolution request")
		}
	}}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, files)
	}, nil, nil)

	// Resolution sees all ancestors, while the final streaming read retains the requested projections and scope.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", "/Photos/"+name,
		"--scope", "saved", "-l", "--status", "--include", "navigation")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "FILE_SCOPE_SAVED")
	want := &entity.ListFilesRequest{Directory: fileReference(29), Scope: entity.FileScope_FILE_SCOPE_SAVED,
		Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES,
			entity.FilesInclude_FILES_INCLUDE_STATUS, entity.FilesInclude_FILES_INCLUDE_NAVIGATION}}
	require.True(t, proto.Equal(want, files.list.Load()), "%v", files.list.Load())
	require.Equal(t, 3, recorder.count(entity.FilesService_Search_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.FilesService_List_FullMethodName))
	require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))

	// Search flags belong to the final query, and do not contaminate directory resolution.
	exit, stdout, stderr = executeTestCLI(server.URL, "", "ls", "/Photos/"+name,
		"--query", "tag:keep", "--recursive", "--scope", "default", "--limit", "2", "--cursor", "query-first")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "query-next")
	require.Equal(t, 7, recorder.count(entity.FilesService_Search_FullMethodName))
	require.Equal(t, 1, recorder.count(entity.FilesService_List_FullMethodName))
}

func TestLSRecursivePathWithoutQuery(t *testing.T) {
	// Resolve a named directory before sending the recursive search's own cursor and page size.
	files := &pathFilesServer{}
	files.searchPage = func(req *entity.SearchFilesRequest) (*entity.SearchFilesResponse, error) {
		if req.Directory.GetFileId() == 0 {
			return &entity.SearchFilesResponse{Entries: []*entity.FilesEntry{
				{Name: "Photos", Reference: fileReference(17), Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY},
			}}, nil
		}
		files.search.Store(req)
		return &entity.SearchFilesResponse{NextCursor: "next"}, nil
	}
	server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterFilesServiceServer(server, files)
	}, nil, nil)

	// Recursive search does not require a query expression and never becomes a complete List.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", "/Photos",
		"--recursive", "--cursor", "first", "--limit", "2")
	require.Equal(t, exitSuccess, exit, stderr)
	require.Contains(t, stdout, "next")
	want := &entity.SearchFilesRequest{Directory: fileReference(17), Recursive: true,
		Scope: entity.FileScope_FILE_SCOPE_ALL, Cursor: "first", Limit: 2}
	require.True(t, proto.Equal(want, files.search.Load()), "%v", files.search.Load())
	require.Equal(t, 2, recorder.count(entity.FilesService_Search_FullMethodName))
	require.Zero(t, recorder.count(entity.FilesService_List_FullMethodName))
}

func TestLSPathLookupFailures(t *testing.T) {
	for _, test := range []struct {
		name, code string
		page       *entity.SearchFilesResponse
		err        error
	}{
		{name: "missing", code: "not_found", page: &entity.SearchFilesResponse{}},
		{name: "file is not a directory", code: "not_found", page: &entity.SearchFilesResponse{
			Entries: []*entity.FilesEntry{{Name: "Photos", Reference: fileReference(17)}}}},
		{name: "no exact match", code: "not_found", page: &entity.SearchFilesResponse{
			Entries: []*entity.FilesEntry{{Name: "photos", Reference: fileReference(17),
				Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY}}}},
		{name: "nonadvancing cursor", code: "internal", page: &entity.SearchFilesResponse{NextCursor: "same"}},
		{name: "wrong reference", code: "internal", page: &entity.SearchFilesResponse{
			Entries: []*entity.FilesEntry{{Name: "Photos", Reference: locationReference(4, "Photos"),
				Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY}}}},
		{name: "access denied", code: "permission_denied", err: status.Error(codes.PermissionDenied, "denied")},
		{name: "unavailable", code: "unavailable", err: status.Error(codes.Unavailable, "offline")},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A failed lookup must never fall through to listing another source or partial output.
			files := &pathFilesServer{searchPage: func(*entity.SearchFilesRequest) (*entity.SearchFilesResponse, error) {
				return test.page, test.err
			}}
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterFilesServiceServer(server, files)
			}, nil, nil)
			exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", "/Photos")
			require.Equal(t, exitFailure, exit, stderr)
			require.Contains(t, stderr, `"code":"`+test.code+`"`)
			require.Empty(t, stdout)
			require.Zero(t, recorder.count(entity.FilesService_List_FullMethodName))
		})
	}
}

func TestLSLocationNameResolution(t *testing.T) {
	for _, test := range []struct {
		name, code string
		exit       int
		second     *entity.ListLocationsResponse
		err        error
	}{
		{name: "exact name across pages", second: &entity.ListLocationsResponse{}},
		{name: "duplicate on later page", exit: exitUsage, code: "usage", second: &entity.ListLocationsResponse{
			Locations: []*entity.Location{{Id: 19, Name: "Home NAS"}}}},
		{name: "nonadvancing page", exit: exitFailure, code: "internal", second: &entity.ListLocationsResponse{
			HasMore: true, Locations: []*entity.Location{{Id: 9, Name: "Home NAS old"}}}},
		{name: "empty continuation", exit: exitFailure, code: "internal", second: &entity.ListLocationsResponse{HasMore: true}},
		{name: "RPC failure", exit: exitFailure, code: "permission_denied", err: status.Error(codes.PermissionDenied, "denied")},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A match does not end Location paging: an exact duplicate can occur on a later page.
			locations := &pathLocationServer{listPage: func(req *entity.ListLocationsRequest) (*entity.ListLocationsResponse, error) {
				require.Greater(t, req.Limit, int32(0))
				require.LessOrEqual(t, req.Limit, int32(500))
				if req.AfterId == 0 {
					return &entity.ListLocationsResponse{HasMore: true, Locations: []*entity.Location{
						{Id: 4, Name: "home nas"}, {Id: 7, Name: "Home NAS"}, {Id: 9, Name: "Home NAS old"},
					}}, nil
				}
				require.EqualValues(t, 9, req.AfterId)
				return test.second, test.err
			}}
			files := &filesCommandServer{}
			server, recorder := newGRPCWebTestServer(t, func(server *grpc.Server) {
				entity.RegisterLocationServiceServer(server, locations)
				entity.RegisterFilesServiceServer(server, files)
			}, nil, nil)

			// The resolved Location reference remains root-relative and never becomes a Library lookup.
			exit, stdout, stderr := executeTestCLI(server.URL, "", "ls", "location://Home%20NAS/DCIM/./2026")
			require.Equal(t, test.exit, exit, stderr)
			require.Equal(t, 2, recorder.count(entity.LocationService_List_FullMethodName))
			require.Zero(t, recorder.count(entity.FilesService_Search_FullMethodName))
			require.Zero(t, recorder.count(entity.FilesService_Get_FullMethodName))
			if test.exit != exitSuccess {
				require.Empty(t, stdout)
				require.Contains(t, stderr, `"code":"`+test.code+`"`)
				require.Zero(t, recorder.count(entity.FilesService_List_FullMethodName))
				return
			}
			require.True(t, proto.Equal(locationReference(7, "DCIM/2026"), files.list.Load().Directory))
			require.Equal(t, 1, recorder.count(entity.FilesService_List_FullMethodName))
		})
	}
}
