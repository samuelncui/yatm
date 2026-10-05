package apis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/files"
	"github.com/samuelncui/yatm/internal/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// librarySource lists logical Library directories. It owns what listing means for the
// catalog; the Files service only binds the request and transports the result.
type librarySource struct {
	service *filesService
	query   *library.FilesQuery
}

// List reads one Library directory completely and hands it back in batches. The directory
// is validated and enumerated once; each batch is projected as it is emitted, so rows reach
// the client while the remaining ones are still being read. Nothing continues a listing.
func (s *librarySource) List(ctx context.Context, req files.ListRequest, emit func(files.ListReply) error) error {
	projection, err := parseFilesProjection(req.Include)
	if err != nil {
		return err
	}
	page, directory, breadcrumbs, err := s.enumerate(ctx, req, projection)
	if err != nil {
		return err
	}
	total := int64(len(page.Files))
	reader := s.service.api.exe.NewFileRowReader()
	return emitListBatches(page.Files, req.BatchSize,
		func(batch []*library.File) ([]*entity.FilesEntry, error) {
			entries, _, _, err := s.service.libraryEntriesObserved(ctx, batch, projection, false, reader)
			return entries, err
		},
		func(entries []*entity.FilesEntry, first bool) error {
			reply := files.ListReply{Entries: entries, Scope: page.Scope}
			if first {
				reply.Directory, reply.Breadcrumbs, reply.Total = directory, breadcrumbs, &total
			}
			return emit(reply)
		})
}

// enumerate validates one Library directory and reads every row it holds, without projecting
// any of them: a listing projects one batch at a time, as it emits.
func (s *librarySource) enumerate(ctx context.Context, req files.ListRequest, projection filesProjection) (*library.FilePage, *entity.FilesEntry, []*entity.FilesEntry, error) {
	parentID := req.Directory.ID
	// Logical parent validation bypasses physical-fact hydration, including for basic ls.
	if parentID < library.TrashFileID {
		return nil, nil, nil, status.Error(codes.InvalidArgument, "invalid Library directory")
	}
	if parentID != 0 {
		file, err := s.service.readFile(ctx, parentID)
		if err != nil {
			return nil, nil, nil, err
		}
		if file.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
			return nil, nil, nil, status.Error(codes.InvalidArgument, "entry is not a directory")
		}
	}
	page, err := s.service.api.lib.ListAllFileRows(ctx, parentID, req.Scope, false, "")
	if err != nil {
		return nil, nil, nil, apiError(err)
	}
	directory, breadcrumbs, err := s.navigation(ctx, parentID, projection)
	if err != nil {
		return nil, nil, nil, err
	}
	return page, directory, breadcrumbs, nil
}

// Search answers one bounded page of a Library query over logical organization.
func (s *librarySource) Search(ctx context.Context, req files.SearchRequest) (*files.SearchReply, error) {
	entries, directory, breadcrumbs, scope, next, err := s.readDirectory(ctx, req.ListRequest(), req.Cursor)
	if err != nil {
		return nil, err
	}
	return &files.SearchReply{Entries: entries, Scope: scope, Directory: directory, Breadcrumbs: breadcrumbs, NextCursor: next}, nil
}

// readDirectory reads one bounded Library query page.
func (s *librarySource) readDirectory(ctx context.Context, req files.ListRequest, cursor string) ([]*entity.FilesEntry, *entity.FilesEntry, []*entity.FilesEntry, entity.FileScope, string, error) {
	// Validate requested display groups independently of predicate evaluation.
	projection, err := parseFilesProjection(req.Include)
	if err != nil {
		return nil, nil, nil, 0, "", err
	}

	// Logical parent validation bypasses physical-fact hydration, including for basic ls.
	parentID := req.Directory.ID
	if parentID < library.TrashFileID {
		return nil, nil, nil, 0, "", status.Error(codes.InvalidArgument, "invalid Library directory")
	}
	if parentID != 0 {
		file, err := s.service.readFile(ctx, parentID)
		if err != nil {
			return nil, nil, nil, 0, "", err
		}
		if file.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
			return nil, nil, nil, 0, "", status.Error(codes.InvalidArgument, "entry is not a directory")
		}
	}

	// Catalog facts determine membership before pagination; observations only project returned rows.
	page, err := s.service.api.lib.ListFileQueryRows(ctx, parentID, req.Scope, req.Recursive, s.query, cursor, int64(req.BatchSize))
	if err != nil {
		return nil, nil, nil, 0, "", apiError(err)
	}
	entries, _, _, err := s.service.libraryEntries(ctx, page.Files, projection, req.Recursive)
	if err != nil {
		return nil, nil, nil, 0, "", err
	}

	// Navigation depends on the requested projection, never on query fields.
	directory, breadcrumbs, err := s.navigation(ctx, parentID, projection)
	if err != nil {
		return nil, nil, nil, 0, "", err
	}
	return entries, directory, breadcrumbs, page.Scope, page.NextCursor, nil
}

// navigation resolves only this directory's bounded ancestry, and only when asked for.
func (s *librarySource) navigation(ctx context.Context, parentID int64, projection filesProjection) (*entity.FilesEntry, []*entity.FilesEntry, error) {
	if !projection[entity.FilesInclude_FILES_INCLUDE_NAVIGATION] {
		return nil, nil, nil
	}
	ancestors, err := s.service.api.lib.ReadFileAncestors(ctx, parentID)
	if err != nil {
		return nil, nil, err
	}
	breadcrumbs := []*entity.FilesEntry{libraryRootEntry(projection)}
	logicalPath := ""
	for _, ancestor := range ancestors {
		logicalPath = path.Join(logicalPath, ancestor.Name)
		entry := basicLibraryEntry(ancestor)
		entry.Path = logicalPath
		if projection[entity.FilesInclude_FILES_INCLUDE_OPERATIONS] {
			entry.Operations = libraryOperations(ancestor, logicalPath)
		}
		breadcrumbs = append(breadcrumbs, entry)
	}
	return breadcrumbs[len(breadcrumbs)-1], breadcrumbs, nil
}

// emitListBatches hands one enumerated directory to the consumer in bounded batches,
// projecting a batch only once the previous one has left. The first rows therefore reach
// the client after one batch of work instead of after the whole directory, and the server
// never holds a projected listing in memory.
func emitListBatches[T any](rows []T, batchSize int, project func([]T) ([]*entity.FilesEntry, error), emit func([]*entity.FilesEntry, bool) error) error {
	if batchSize <= 0 {
		batchSize = files.DefaultBatchSize
	}
	if len(rows) == 0 {
		return emit(nil, true)
	}
	for start := 0; start < len(rows); start += batchSize {
		end := min(start+batchSize, len(rows))
		entries, err := project(rows[start:end])
		if err != nil {
			return err
		}
		if err := emit(entries, start == 0); err != nil {
			return err
		}
	}
	return nil
}

// Get resolves one Library entry's detail, including its original and content reference.
func (s *librarySource) Get(ctx context.Context, entry files.Entry) (*files.Detail, error) {
	projection := filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true, entity.FilesInclude_FILES_INCLUDE_STATUS: true, entity.FilesInclude_FILES_INCLUDE_OPERATIONS: true, entity.FilesInclude_FILES_INCLUDE_NAVIGATION: true}
	if entry.ID == 0 {
		return &files.Detail{Entry: libraryRootEntry(projection)}, nil
	}
	file, err := s.service.readFile(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	entries, observations, facts, err := s.service.libraryEntries(ctx, []*library.File{file}, projection, true)
	if err != nil {
		return nil, err
	}
	detail := &files.Detail{Entry: entries[0], FileID: entry.ID}
	observation := observations[entry.ID]
	if observation == nil {
		return detail, nil
	}
	original := facts[entry.ID].Original
	if original != nil {
		name := ""
		if observation.Location != nil {
			name = observation.Location.Name
		}
		detail.Original = &entity.FilesOriginal{SourceName: name, Path: original.Path,
			Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: original.LocationID, Path: original.Path}}}}
	}
	if observation.Entry != nil {
		detail.ContentReference = &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: observation.Entry.Reference}}
		if detail.Original != nil {
			detail.Original.Reference = detail.ContentReference
		}
	}
	detail.ContentSignature = knownContentSignature(facts[entry.ID], observation)
	return detail, nil
}

// locationSource lists a registered Location's live directory. It owns what listing means
// on the filesystem; the Files service only binds the request and transports the result.
type locationSource struct {
	service *filesService
	query   *library.FilesQuery
}

// List reads one live directory completely and hands it back in batches. The directory is
// enumerated once; each batch is projected as it is emitted, so rows reach the client while
// the remaining ones are still being read. Nothing continues a listing.
func (s *locationSource) List(ctx context.Context, req files.ListRequest, emit func(files.ListReply) error) error {
	// Reject unsupported projection groups before opening the directory.
	projection, err := parseFilesProjection(req.Include)
	if err != nil {
		return err
	}

	// Enumerate once for the exact total, then observe and project only the batch being sent.
	read, err := s.service.api.exe.EnumerateLocationDirectory(ctx, req.Directory.ID, req.Directory.Path)
	if err != nil {
		return apiError(err)
	}
	directory, breadcrumbs := locationNavigation(read.Location, read.Parent, projection)
	total := int64(read.Len())
	size := req.BatchSize
	if size <= 0 {
		size = files.DefaultBatchSize
	}
	for start := 0; start < read.Len() || start == 0; start += size {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, infos, err := read.Read(ctx, start, min(start+size, read.Len()))
		if err != nil {
			return apiError(err)
		}
		entries, err := s.service.locationDirectoryEntries(ctx, rows, projection, read.Location, infos)
		if err != nil {
			return err
		}
		reply := files.ListReply{Entries: entries, Scope: entity.FileScope_FILE_SCOPE_ALL}
		if start == 0 {
			reply.Directory, reply.Breadcrumbs, reply.Total = directory, breadcrumbs, &total
		}
		if err := emit(reply); err != nil {
			return err
		}
		if read.Len() == 0 {
			break
		}
	}
	return nil
}

// Search answers one bounded page of a live query, keeping its cursor for continuation.
func (s *locationSource) Search(ctx context.Context, req files.SearchRequest) (*files.SearchReply, error) {
	// Compile ownership is the public request; the provider binds its existing physical cursor.
	provider, err := s.decodeCursor(req.Cursor, req.Query)
	if err != nil {
		return nil, err
	}
	requested, err := parseFilesProjection(req.Include)
	if err != nil {
		return nil, err
	}
	query := s.query
	cached := make(map[string]*entity.FilesEntry)
	var match func(*library.Location, []*entity.LocationEntry, map[string]os.FileInfo) ([]int, error)
	if req.Query != "" {
		projection := filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true, entity.FilesInclude_FILES_INCLUDE_OPERATIONS: query.Catalog, entity.FilesInclude_FILES_INCLUDE_STATUS: query.Content}
		match = func(location *library.Location, live []*entity.LocationEntry, infos map[string]os.FileInfo) ([]int, error) {
			entries, facts, _, err := s.service.locationEntriesObserved(ctx, live, projection, location, infos)
			if err != nil {
				return nil, err
			}
			indexes, err := s.service.matchLocationRows(ctx, query, live, entries, facts)
			if err != nil {
				return nil, err
			}

			// Content predicates already loaded every row fact; keep only the bounded lexical page.
			if query.Content {
				for _, index := range indexes {
					cached[live[index].Path] = entries[index]
				}
				if len(cached) > req.Limit+1 {
					paths := make([]string, 0, len(cached))
					for value := range cached {
						paths = append(paths, value)
					}
					sort.Strings(paths)
					for _, value := range paths[req.Limit+1:] {
						delete(cached, value)
					}
				}
			}
			return indexes, nil
		}
	}
	read, err := s.service.api.exe.ReadLocationEntriesMatching(ctx, &entity.LocationEntriesQuery{LocationId: req.Directory.ID, ParentPath: req.Directory.Path, Limit: int32(req.Limit)}, provider, match)
	if err != nil {
		return nil, apiError(err)
	}

	// Reuse matching observations and content projections; omitted groups remain absent on the wire.
	entries := make([]*entity.FilesEntry, 0, len(read.Page.Entries))
	if query.Content {
		for _, live := range read.Page.Entries {
			entry := cached[live.Path]
			if !requested[entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES] {
				entry.SizeBytes, entry.MtimeNs = nil, nil
			}
			if !requested[entity.FilesInclude_FILES_INCLUDE_STATUS] {
				entry.Status = nil
			}
			if !requested[entity.FilesInclude_FILES_INCLUDE_OPERATIONS] {
				entry.Operations, entry.AssociatedFileId = nil, nil
			}
			entries = append(entries, entry)
		}
	} else {
		entries, _, _, err = s.service.locationEntriesObserved(ctx, read.Page.Entries, requested, read.Location, read.Infos)
		if err != nil {
			return nil, err
		}
	}
	directory, breadcrumbs := locationNavigation(read.Location, read.Parent, requested)
	reply := &files.SearchReply{Entries: entries, Scope: entity.FileScope_FILE_SCOPE_ALL, Directory: directory, Breadcrumbs: breadcrumbs}
	if read.Page.NextCursor != "" {
		data, err := json.Marshal(filesCursor{Query: req.Query, Page: read.Page.NextCursor})
		if err != nil {
			return nil, err
		}
		reply.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return reply, nil
}

// decodeCursor binds a continuation to the query that produced it.
func (s *locationSource) decodeCursor(cursor, query string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	var decoded filesCursor
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if len(cursor) > 32768 || err != nil || json.Unmarshal(data, &decoded) != nil || decoded.Query != query {
		return "", status.Error(codes.InvalidArgument, "invalid Files cursor")
	}
	return decoded.Page, nil
}

func locationNavigation(location *library.Location, observed *entity.LocationEntry, projection filesProjection) (*entity.FilesEntry, []*entity.FilesEntry) {
	if !projection[entity.FilesInclude_FILES_INCLUDE_NAVIGATION] {
		return nil, nil
	}
	// Ancestor references are derived from this authorized path without additional filesystem reads.
	root := &entity.FilesEntry{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID}}}, Name: location.Name, Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY}
	breadcrumbs := []*entity.FilesEntry{root}
	current := ""
	for _, part := range strings.Split(observed.Path, "/") {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		breadcrumbs = append(breadcrumbs, &entity.FilesEntry{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID, Path: current}}}, Name: part, Path: current, Kind: entity.EntryKind_ENTRY_KIND_DIRECTORY})
	}
	directoryEntry := basicLocationEntry(observed, projection)
	if observed.Path == "" {
		directoryEntry.Name = location.Name
	}
	breadcrumbs[len(breadcrumbs)-1] = directoryEntry
	return directoryEntry, breadcrumbs
}

// Get resolves one live Location entry's detail and the Library row it is associated with.
func (s *locationSource) Get(ctx context.Context, entry files.Entry) (*files.Detail, error) {
	projection := filesProjection{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES: true, entity.FilesInclude_FILES_INCLUDE_STATUS: true, entity.FilesInclude_FILES_INCLUDE_OPERATIONS: true, entity.FilesInclude_FILES_INCLUDE_NAVIGATION: true}
	live, err := s.service.api.exe.ObserveLocationEntry(ctx, entry.ID, entry.Path)
	if err != nil {
		return nil, apiError(err)
	}
	entries, facts, observations, err := s.service.locationEntries(ctx, []*entity.LocationEntry{live}, projection)
	if err != nil {
		return nil, err
	}
	location, err := s.service.api.lib.GetLocation(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	detail := &files.Detail{Entry: entries[0], FileID: entries[0].GetAssociatedFileId()}
	if live.Path == "" {
		detail.Entry.Name = location.Name
	}
	detail.Original = &entity.FilesOriginal{SourceName: location.Name, Path: live.Path, Reference: detail.Entry.Reference}
	if detail.Entry.Kind == entity.EntryKind_ENTRY_KIND_FILE {
		detail.ContentReference = detail.Entry.Reference
		detail.ContentSignature = knownContentSignature(facts[detail.FileID], observations[detail.FileID])
	}
	return detail, nil
}
