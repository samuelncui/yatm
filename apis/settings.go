package apis

import (
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
	"github.com/samuelncui/yatm/executor"
	scanjob "github.com/samuelncui/yatm/executor/scan"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type settingsService struct {
	entity.UnimplementedSettingsServiceServer
	api *API
}

func (s *settingsService) GetLibrary(ctx context.Context, _ *entity.GetLibrarySettingsRequest) (*entity.LibrarySettings, error) {
	settings, err := s.api.lib.GetLibrarySettings(ctx)
	if err != nil {
		return nil, err
	}
	return settings.ToEntity(), nil
}

func (s *settingsService) UpdateLibrary(ctx context.Context, req *entity.UpdateLibrarySettingsRequest) (*entity.UpdateLibrarySettingsReply, error) {
	// Persist independent preferences before launching explicit off-to-on collection.
	if req.GetSettings() == nil {
		return nil, status.Error(codes.InvalidArgument, "Library settings are missing")
	}
	previous, err := s.api.lib.GetLibrarySettings(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.api.lib.UpdateFileSettings(ctx, &library.LibrarySettings{IncludeUnbackedFiles: req.Settings.IncludeUnbackedFiles,
		AutoCollectFiles: req.Settings.AutoCollectFiles, ConfirmPermanentDelete: req.Settings.ConfirmPermanentDelete, Revision: req.Settings.Revision})
	if err != nil {
		return nil, onlineError(err)
	}
	reply := &entity.UpdateLibrarySettingsReply{Settings: settings.ToEntity()}
	if previous.AutoCollectFiles || !settings.AutoCollectFiles {
		return reply, nil
	}
	for after := int64(0); ; {
		locations, more, err := s.api.lib.ListOnlineSources(ctx, after, 100, nil)
		if err != nil {
			reply.CollectionErrors = append(reply.CollectionErrors, err.Error())
			break
		}
		for _, location := range locations {
			after = location.ID
			if location.Binding != entity.OnlineBinding_CONFIRMED {
				continue
			}
			job, err := scanjob.Create(ctx, s.api.exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{LocationId: location.ID,
				SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS}})
			if err != nil {
				reply.CollectionErrors = append(reply.CollectionErrors, location.Name+": "+err.Error())
				continue
			}
			reply.CollectionJobIds = append(reply.CollectionJobIds, job.Job.Id)
		}
		if !more {
			break
		}
	}
	return reply, nil
}

func (s *settingsService) GetAccess(ctx context.Context, _ *entity.GetAccessRequest) (*entity.GetAccessReply, error) {
	return s.api.exe.AccessSettings(ctx)
}

func (s *settingsService) BrowsePaths(ctx context.Context, req *entity.BrowsePathsRequest) (*entity.BrowsePathsReply, error) {
	// Resolve the selected destination independently from editable original-index Ignore rules.
	limit, err := onlineLimit(req.GetLimit())
	if err != nil {
		return nil, err
	}
	root := req.GetPath()
	if req.GetLocationId() != 0 {
		location, err := s.api.lib.GetOnlineSource(ctx, req.LocationId)
		if err != nil {
			return nil, onlineError(err)
		}
		if location.Binding != entity.OnlineBinding_CONFIRMED {
			return nil, onlineError(library.ErrOnlineUnverified)
		}
		if _, err := s.api.exe.CheckOnlineSource(location); err != nil {
			return nil, onlineError(err)
		}
		if root != "" {
			if err := entity.ValidateRelativePath(root); err != nil {
				return nil, status.Error(codes.InvalidArgument, err.Error())
			}
		}
		root = filepath.Join(location.RootPath, filepath.FromSlash(root))
	}
	if root == "" {
		access, err := s.api.exe.AccessSettings(ctx)
		if err != nil {
			return nil, err
		}
		reply := &entity.BrowsePathsReply{}
		for _, item := range access.Ranges {
			reply.Directories = append(reply.Directories, &entity.SourceFile{Name: item.RootPath, Path: item.RootPath, Mode: int64(os.ModeDir)})
		}
		return reply, nil
	}
	root, err = s.api.exe.OnlineRoot(root)
	if err != nil {
		return nil, onlineError(err)
	}
	if _, err := s.api.exe.RequiredOnlineExclusions(root); err != nil {
		return nil, onlineError(err)
	}

	// A cursor belongs to exactly this root, never a client-supplied arbitrary filesystem offset.
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

	// Keep only the next bounded lexical page while reading the directory in fixed-size batches.
	names := make([]string, 0, limit+1)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := handle.ReadDir(256)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("browse directory failed, %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() <= after {
				continue
			}
			candidate := filepath.Join(root, entry.Name())
			if _, err := s.api.exe.OnlineRoot(candidate); err != nil {
				if errors.Is(err, executor.ErrAccessExcluded) {
					continue
				}
				return nil, fmt.Errorf("check directory access failed, %w", err)
			}
			if _, err := s.api.exe.RequiredOnlineExclusions(candidate); err != nil {
				if errors.Is(err, executor.ErrAccessExcluded) {
					continue
				}
				return nil, fmt.Errorf("check directory resources failed, %w", err)
			}
			index := sort.SearchStrings(names, entry.Name())
			if index > limit {
				continue
			}
			names = append(names, "")
			copy(names[index+1:], names[index:])
			names[index] = entry.Name()
			if len(names) > limit+1 {
				names = names[:limit+1]
			}
		}
		if err == io.EOF {
			break
		}
	}

	// Return live directory observations, not a claim of a filesystem snapshot.
	reply := &entity.BrowsePathsReply{Path: root}
	if len(names) > limit {
		names = names[:limit]
		reply.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(root + "\x00" + names[len(names)-1]))
	}
	for _, name := range names {
		reply.Directories = append(reply.Directories, &entity.SourceFile{Name: name, Path: filepath.Join(root, name), ParentPath: root, Mode: int64(os.ModeDir)})
	}
	return reply, nil
}
