package apis

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *locationService) GetAccess(ctx context.Context, _ *entity.GetLocationAccessRequest) (*entity.GetLocationAccessResponse, error) {
	return s.api.exe.AccessSettings(ctx)
}

func (s *locationService) BrowsePaths(ctx context.Context, req *entity.BrowsePathsRequest) (*entity.BrowsePathsResponse, error) {
	// Discover administrator-authorized paths before they are registered as Locations.
	limit, err := pageLimit(req.GetLimit())
	if err != nil {
		return nil, err
	}
	root := req.GetPath()
	if root == "" {
		access, err := s.api.exe.AccessSettings(ctx)
		if err != nil {
			return nil, err
		}
		reply := &entity.BrowsePathsResponse{}
		for _, item := range access.Ranges {
			reply.Directories = append(reply.Directories, &entity.SourceFile{Name: item.RootPath, Path: item.RootPath, Mode: int64(os.ModeDir)})
		}
		return reply, nil
	}
	root, err = s.api.exe.LocationRoot(root)
	if err != nil {
		return nil, err
	}

	// Decode a cursor bound to exactly this authorized directory.
	after := ""
	if req.GetCursor() != "" {
		if len(req.Cursor) > 16384 {
			return nil, status.Error(codes.InvalidArgument, "directory cursor is too long")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid directory cursor")
		}
		bound, name, ok := strings.Cut(string(decoded), "\x00")
		if !ok || bound != root {
			return nil, status.Error(codes.InvalidArgument, "directory cursor belongs to another path")
		}
		after = name
	}
	names, err := s.api.exe.BrowseDirectories(ctx, root, after, limit)
	if err != nil {
		return nil, err
	}

	// Return live directory observations, not a claim of a filesystem snapshot.
	reply := &entity.BrowsePathsResponse{Path: root}
	if len(names) > limit {
		names = names[:limit]
		reply.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(root + "\x00" + names[len(names)-1]))
	}
	for _, name := range names {
		reply.Directories = append(reply.Directories, &entity.SourceFile{
			Name: name, Path: filepath.Join(root, name), ParentPath: root, Mode: int64(os.ModeDir),
		})
	}
	return reply, nil
}
