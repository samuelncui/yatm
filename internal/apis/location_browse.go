package apis

import (
	"container/heap"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// nameHeap is a max-heap of directory names, so its root is the largest retained name.
type nameHeap []string

func (h nameHeap) Len() int           { return len(h) }
func (h nameHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h nameHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nameHeap) Push(v any)        { *h = append(*h, v.(string)) }
func (h *nameHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

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
		return nil, apiError(err)
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
	handle, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	// Keep only the next bounded lexical page while reading the directory in fixed-size batches:
	// a max-heap retains the smallest limit+1 names.
	names := make(nameHeap, 0, limit+1)
	read := func(n int) ([]os.DirEntry, error) {
		entries, err := handle.ReadDir(n)
		if err != nil && err != io.EOF {
			err = fmt.Errorf("browse directory failed, %w", err)
		}
		return entries, err
	}
	if err := executor.ReadLocationEntries(ctx, read, func(entries []os.DirEntry) error {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() <= after {
				continue
			}
			candidate := filepath.Join(root, entry.Name())
			if _, err := s.api.exe.LocationRoot(candidate); err != nil {
				if errors.Is(err, executor.ErrAccessExcluded) {
					continue
				}
				return fmt.Errorf("check directory access failed, %w", err)
			}
			if len(names) <= limit {
				heap.Push(&names, entry.Name())
				continue
			}
			if entry.Name() < names[0] {
				names[0] = entry.Name()
				heap.Fix(&names, 0)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(names)

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
