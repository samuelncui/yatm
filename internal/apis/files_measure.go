package apis

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/fileops"
	"github.com/samuelncui/yatm/internal/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *filesService) Measure(req *entity.MeasureFilesRequest, stream entity.FilesService_MeasureServer) error {
	// Validate selection before opening the pure-read lifetime; no Job or admission is created.
	ctx := stream.Context()
	if req.GetDirectory().GetTarget() == nil {
		return status.Error(codes.InvalidArgument, "directory reference is required")
	}
	query := strings.TrimSpace(req.Query)
	compiled, err := s.api.lib.CompileFilesQuery(query)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	scope, err := s.api.lib.ResolveFileScope(ctx, req.Scope)
	if err != nil {
		return apiError(err)
	}
	send := func(item *entity.FilesMeasurement) error {
		return stream.Send(&entity.MeasureFilesResponse{Result: &entity.MeasureFilesResponse_Item{Item: item}})
	}

	// Each provider walks once; selected ancestor contents are accumulated only once in the summary.
	var summary *entity.FilesMeasurement
	switch source := req.Directory.Target.(type) {
	case *entity.FileOperationRef_FileId:
		if source.FileId < library.TrashFileID {
			return status.Error(codes.InvalidArgument, "invalid Library directory")
		}
		if source.FileId != 0 {
			file, readErr := s.readFile(ctx, source.FileId)
			if readErr != nil {
				return readErr
			}
			if file.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
				return status.Error(codes.InvalidArgument, "entry is not a directory")
			}
		}
		summary, err = s.measureLibraryDirectory(ctx, source.FileId, scope, compiled, req.Recursive, false, 0, send)
	case *entity.FileOperationRef_Location:
		if source.Location == nil || req.Recursive {
			return status.Error(codes.InvalidArgument, "Location measurement requires a single directory scope")
		}
		location, loadErr := s.api.lib.GetLocation(ctx, source.Location.LocationId)
		if loadErr != nil {
			return apiError(loadErr)
		}
		_, info, checkErr := s.api.exe.CheckLocationPath(location, source.Location.Path)
		if checkErr != nil {
			return apiError(checkErr)
		}
		if !info.IsDir() {
			return apiError(library.ErrLocationConflict)
		}
		// Location Ignore excludes this Location's content, so it also excludes a measured root.
		if location.Excluded(source.Location.Path, true) {
			return status.Error(codes.FailedPrecondition, "Location Ignore excludes the measured path")
		}
		boundaries, loadErr := s.api.lib.ReadLocationBoundaries(ctx, location)
		if loadErr != nil {
			return loadErr
		}
		summary, err = s.measureLocationDirectory(ctx, location, boundaries, source.Location.Path, compiled, false, 0, send)
	default:
		return status.Error(codes.InvalidArgument, "unsupported Files source")
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return stream.Send(&entity.MeasureFilesResponse{Result: &entity.MeasureFilesResponse_Summary{Summary: summary}})
}

func mergeMeasurement(total, item *entity.FilesMeasurement) {
	if item.KnownBytes < 0 || total.KnownBytes > math.MaxInt64-item.KnownBytes {
		total.Complete, total.Error = false, "size exceeds supported range"
		return
	}
	total.KnownBytes += item.KnownBytes
	if !item.Complete {
		total.Complete = false
		if total.Error == "" {
			total.Error = item.Error
		}
	}
}

func (s *filesService) measureLibraryDirectory(ctx context.Context, parent int64, scope entity.FileScope, query *library.FilesQuery,
	recursive, included bool, depth int, send func(*entity.FilesMeasurement) error) (*entity.FilesMeasurement, error) {
	// Bound ancestry memory independently of imported catalog shape.
	total := &entity.FilesMeasurement{Complete: true}
	if depth > 512 {
		total.Complete, total.Error = false, "directory depth exceeds measurement limit"
		return total, nil
	}

	// Walk Catalog rows without making selection depend on the original's current accessibility.
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := s.api.lib.ListFileRows(ctx, parent, entity.FileScope_FILE_SCOPE_ALL, false, "", cursor, 256)
		if err != nil {
			total.Complete, total.Error = false, err.Error()
			return total, nil
		}

		// Match and read attributes in bounded batches, never through per-file details or status hydration.
		ids := make([]int64, 0, len(page.Files))
		for _, file := range page.Files {
			ids = append(ids, file.ID)
		}
		matches := map[int64]bool{}
		if depth == 0 || recursive {
			matches, err = s.api.lib.MatchFileQueryRows(ctx, ids, scope, query)
			if err != nil {
				return nil, err
			}
		}
		projection := filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true}
		entries, _, facts, err := s.libraryEntries(ctx, page.Files, projection, false)
		if err != nil {
			return nil, err
		}

		// Postorder yields directory totals while overlap is naturally counted once in its parent's total.
		for index, file := range page.Files {
			selected := matches[file.ID]
			if !selected && !included && (!recursive || file.Kind != entity.FileKind_FILE_KIND_DIRECTORY) {
				continue
			}
			item := &entity.FilesMeasurement{Reference: entries[index].Reference, Complete: true}
			if file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
				nested, err := s.measureLibraryDirectory(ctx, file.ID, scope, query, recursive, included || selected, depth+1, send)
				if err != nil {
					return nil, err
				}
				mergeMeasurement(item, nested)
			} else if facts[file.ID] == nil || facts[file.ID].Size == nil {
				item.Complete, item.Error = false, "size is unknown"
			} else {
				item.KnownBytes = *facts[file.ID].Size
			}
			mergeMeasurement(total, item)
			if selected {
				if err := send(item); err != nil {
					return nil, err
				}
			}
		}
		if page.NextCursor == "" {
			return total, nil
		}
		cursor = page.NextCursor
	}
}

func (s *filesService) measureLocationDirectory(ctx context.Context, location *library.Location, boundaries []string, relative string, query *library.FilesQuery,
	included bool, depth int, send func(*entity.FilesMeasurement) error) (*entity.FilesMeasurement, error) {
	// Preserve mount and registration ownership without following symbolic links.
	total := &entity.FilesMeasurement{Complete: true}
	if depth > 512 {
		total.Complete, total.Error = false, "directory depth exceeds measurement limit"
		return total, nil
	}
	if err := measureLocationBoundary(location, boundaries, relative); err != nil {
		total.Complete, total.Error = false, err.Error()
		return total, nil
	}
	var sendErr error
	err := s.api.exe.ReadObservedLocationDirectory(ctx, location, relative, func(rows []*entity.LocationEntry, infos map[string]os.FileInfo) error {
		// Only selection roots use the query; every accessible descendant contributes to selected folders.
		matches := make(map[int]bool, len(rows))
		if !included {
			projection := filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true, entity.FilesInclude_FILES_INCLUDE_OPERATIONS: query.Catalog, entity.FilesInclude_FILES_INCLUDE_STATUS: query.Content}
			entries, facts, _, err := s.locationEntriesObserved(ctx, rows, projection, location, infos)
			if err != nil {
				return err
			}
			indexes, err := s.matchLocationRows(ctx, query, rows, entries, facts)
			if err != nil {
				return err
			}
			for _, index := range indexes {
				matches[index] = true
			}
		}
		for index, row := range rows {
			if !included && !matches[index] {
				continue
			}
			entry := basicLocationEntry(row, filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true})
			item := &entity.FilesMeasurement{Reference: entry.Reference, Complete: true}
			if row.Directory {
				nested, err := s.measureLocationDirectory(ctx, location, boundaries, row.Path, query, true, depth+1, send)
				if err != nil {
					return err
				}
				mergeMeasurement(item, nested)
			} else if entry.SizeBytes != nil {
				item.KnownBytes = *entry.SizeBytes
			}
			mergeMeasurement(total, item)
			if !included {
				if err := send(item); err != nil {
					sendErr = err
					return err
				}
			}
		}
		return nil
	})
	if sendErr != nil {
		return nil, sendErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		total.Complete, total.Error = false, err.Error()
	}
	return total, nil
}

func measureLocationBoundary(location *library.Location, boundaries []string, relative string) error {
	// Reject nested mounts, including bind mounts, before enumerating their contents.
	full := filepath.Join(location.RootPath, filepath.FromSlash(relative))
	same, err := fileops.SameMount(location.RootPath, full)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("measurement cannot cross a mount boundary: %q", relative)
	}

	// Registration boundaries remain provider-owned even when directories are explicitly selected.
	for _, root := range boundaries {
		if full == root || strings.HasPrefix(full, root+string(filepath.Separator)) {
			return fmt.Errorf("measurement crosses another registered Location at %q", root)
		}
	}
	return nil
}
