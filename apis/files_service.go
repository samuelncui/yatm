package apis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type filesService struct {
	entity.UnimplementedFilesServiceServer
	api *API
}

func (s *filesService) List(ctx context.Context, req *entity.ListFilesRequest) (*entity.ListFilesReply, error) {
	// The common read boundary never performs admission, even for previously associated entries.
	if req.GetDirectory().GetTarget() == nil {
		return nil, status.Error(codes.InvalidArgument, "directory reference is required")
	}
	release, err := s.api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	limit, err := onlineLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.Query)
	if req.NameFilter != "" {
		name := fmt.Sprintf("name:%q", req.NameFilter)
		if query != "" {
			name = "(" + query + ") AND " + name
		}
		query = name
	}
	if err := s.api.lib.ValidateFilesQuery(query); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Provider differences stop at enumeration and observation, not client interaction logic.
	switch source := req.Directory.Target.(type) {
	case *entity.FileOperationRef_FileId:
		if source.FileId != 0 {
			file, err := s.api.lib.GetFile(ctx, source.FileId)
			if err != nil {
				return nil, onlineError(err)
			}
			if file.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
				return nil, status.Error(codes.InvalidArgument, "entry is not a directory")
			}
		}
		page, err := s.api.lib.ListFilesMatching(ctx, source.FileId, req.Scope, req.Cursor, int64(limit), query)
		if err != nil {
			return nil, onlineError(err)
		}
		if err := s.hydrate(ctx, page.Files); err != nil {
			return nil, err
		}
		reply := &entity.ListFilesReply{NextCursor: page.NextCursor, Scope: page.Scope}
		for _, file := range page.Files {
			entry := libraryEntry(file)
			if req.NeedSize && file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
				size, err := s.api.lib.FileTreeSize(ctx, file.ID, page.Scope)
				if err != nil {
					return nil, err
				}
				entry.Size = &size
			}
			reply.Entries = append(reply.Entries, entry)
		}
		return reply, nil
	case *entity.FileOperationRef_Location:
		if req.NeedSize {
			return nil, status.Error(codes.InvalidArgument, "recursive data usage is not supported by this source")
		}
		return s.listLocation(ctx, source.Location, req.Cursor, query, limit)
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported Files source")
	}
}

func (s *filesService) Get(ctx context.Context, req *entity.GetFilesEntryRequest) (*entity.FilesEntry, error) {
	if req.GetReference().GetTarget() == nil {
		return nil, status.Error(codes.InvalidArgument, "entry reference is required")
	}
	release, err := s.api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	switch source := req.Reference.Target.(type) {
	case *entity.FileOperationRef_FileId:
		if source.FileId == 0 {
			return &entity.FilesEntry{Reference: req.Reference, Name: "Library", Kind: entity.EntryKind_ENTRY_DIRECTORY,
				Operations: []entity.FileOperationKind{entity.FileOperationKind_MAKE_DIRECTORY}}, nil
		}
		file, err := s.api.lib.GetFile(ctx, source.FileId)
		if err != nil {
			return nil, onlineError(err)
		}
		if err := s.hydrate(ctx, []*library.File{file}); err != nil {
			return nil, err
		}
		entry := libraryEntry(file)
		if file.Kind == entity.FileKind_FILE_KIND_REGULAR {
			entry.CanRead = false
			original, err := s.api.lib.GetFileLocation(ctx, file.ID)
			if err != nil {
				return nil, err
			}
			if original != nil {
				live, err := s.api.exe.ObserveLocationEntry(ctx, original.LocationID, original.Path)
				if err == nil && live.Reference.BindingToken == original.ObservedBindingToken && os.FileMode(live.Reference.Facts.Mode).IsRegular() {
					entry.ContentReference = &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: live.Reference}}
					entry.CanRead = true
				}
			}
		}
		return entry, nil
	case *entity.FileOperationRef_Location:
		if source.Location == nil {
			return nil, status.Error(codes.InvalidArgument, "Location reference is required")
		}
		live, err := s.api.exe.ObserveLocationEntry(ctx, source.Location.LocationId, source.Location.Path)
		if err != nil {
			return nil, onlineError(err)
		}
		if source.Location.BindingToken != "" && source.Location.BindingToken != live.Reference.BindingToken {
			return nil, onlineError(library.ErrOnlineConflict)
		}
		return s.locationEntry(ctx, live)
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported Files source")
	}
}

func (s *filesService) Inspect(ctx context.Context, req *entity.InspectFilesRequest) (*entity.InspectFilesReply, error) {
	// Errors belong to individual observations; an unavailable source does not hide other rows.
	if len(req.GetReferences()) == 0 || len(req.References) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "inspect between 1 and 1000 entries")
	}
	release, err := s.api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	reply := &entity.InspectFilesReply{}
	for _, ref := range req.References {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		observation := &entity.FilesObservation{Reference: ref}
		entry, err := s.Get(ctx, &entity.GetFilesEntryRequest{Reference: ref})
		if err != nil {
			observation.Error = err.Error()
			observation.Summary = &entity.FileContentSummary{OriginalAvailability: entity.OriginalAvailability_ORIGINAL_UNAVAILABLE,
				ObservedAtMs: time.Now().UnixMilli()}
		} else {
			observation.Summary = entry.Summary
			if ref.GetLocation() == nil && entry.File != nil {
				if err := s.api.exe.ObserveFileContent(ctx, entry.File.Id, observation.Summary); err != nil {
					observation.Error = err.Error()
				}
			}
		}
		reply.Observations = append(reply.Observations, observation)
	}
	return reply, nil
}

func (s *filesService) Collect(ctx context.Context, req *entity.CollectFilesRequest) (*entity.CollectFilesReply, error) {
	// Explicit collection is separate from pure List/Get and honors automatic collection settings.
	if len(req.GetReferences()) == 0 || len(req.References) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "collect between 1 and 1000 entries")
	}
	release, err := s.api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	if req.Automatic {
		settings, err := s.api.lib.GetLibrarySettings(ctx)
		if err != nil {
			return nil, err
		}
		if !settings.AutoCollectFiles {
			return &entity.CollectFilesReply{}, nil
		}
	}
	groups := map[int64][]*entity.LocationEntryRef{}
	seen := map[string]bool{}
	for _, ref := range req.References {
		live := ref.GetLocation()
		if live == nil {
			continue
		}
		location, _, info, err := s.api.exe.ResolveLocationEntry(ctx, live)
		if err != nil {
			return nil, onlineError(err)
		}
		if !info.Mode().IsRegular() || req.Automatic && location.Excluded(live.Path, false) {
			continue
		}
		key := fmt.Sprintf("%d/%s", live.LocationId, live.Path)
		if !seen[key] {
			groups[live.LocationId] = append(groups[live.LocationId], live)
			seen[key] = true
		}
	}
	for _, refs := range groups {
		if _, err := s.api.exe.AdmitLocationEntries(ctx, refs); err != nil {
			return nil, onlineError(err)
		}
	}
	reply := &entity.CollectFilesReply{}
	for _, ref := range req.References {
		entry, err := s.Get(ctx, &entity.GetFilesEntryRequest{Reference: ref})
		if err != nil {
			return nil, err
		}
		reply.Entries = append(reply.Entries, entry)
	}
	return reply, nil
}

func (s *filesService) UpdateMetadata(ctx context.Context, req *entity.UpdateFilesMetadataRequest) (*entity.FilesEntry, error) {
	// Explicit annotation admits an uncollected ordinary entry before editing the shared File.
	ref := req.GetReference()
	if ref.GetTarget() == nil {
		return nil, status.Error(codes.InvalidArgument, "entry reference is required")
	}
	release, err := s.api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()
	fileID := ref.GetFileId()
	if live := ref.GetLocation(); live != nil {
		original, err := s.api.exe.AdmitLocationEntry(ctx, live)
		if err != nil {
			return nil, onlineError(err)
		}
		fileID = original.FileID
	}
	if err := s.api.lib.EditFileMetadata(ctx, []int64{fileID}, library.FileMetadataEdit{
		Note: req.Note, AddTags: req.AddTags, RemoveTags: req.RemoveTags}); err != nil {
		return nil, onlineError(err)
	}
	return s.Get(ctx, &entity.GetFilesEntryRequest{Reference: ref})
}

func (s *filesService) hydrate(ctx context.Context, files []*library.File) error {
	if err := s.api.hydrateFileTags(ctx, files...); err != nil {
		return err
	}
	return s.api.lib.HydrateFileContent(ctx, files...)
}

func libraryEntry(file *library.File) *entity.FilesEntry {
	entry := &entity.FilesEntry{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: file.ID}},
		Name: file.Name, Path: file.Name, File: convertFiles(file)[0], Summary: file.ContentSummary, MtimeNs: file.ModTime.UnixNano(),
		Operations: []entity.FileOperationKind{entity.FileOperationKind_MOVE, entity.FileOperationKind_DELETE}}
	if file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
		entry.Kind = entity.EntryKind_ENTRY_DIRECTORY
		entry.Operations = append(entry.Operations, entity.FileOperationKind_MAKE_DIRECTORY)
		return entry
	}
	if file.ContentSummary != nil && (file.ContentSummary.HasOriginal || file.ContentSummary.HasVersions) {
		size := file.Size
		entry.Size = &size
		entry.CanRead = file.ContentSummary.HasOriginal
	}
	return entry
}

func (s *filesService) locationEntry(ctx context.Context, live *entity.LocationEntry) (*entity.FilesEntry, error) {
	// Keep real rows visible even when there is no Library identity or content signature.
	ref := live.Reference
	entry := &entity.FilesEntry{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: ref}},
		Name: path.Base(live.Path), Path: live.Path, MtimeNs: ref.Facts.MtimeNs,
		Operations: []entity.FileOperationKind{entity.FileOperationKind_MOVE, entity.FileOperationKind_DELETE}}
	mode := os.FileMode(ref.Facts.Mode)
	switch {
	case mode.IsDir():
		entry.Kind = entity.EntryKind_ENTRY_DIRECTORY
		entry.Operations = append(entry.Operations, entity.FileOperationKind_MAKE_DIRECTORY)
	case mode.IsRegular():
		size := ref.Facts.Size
		entry.Size, entry.CanRead = &size, true
		entry.ContentReference = entry.Reference
		entry.Summary = &entity.FileContentSummary{OriginalAvailability: entity.OriginalAvailability_ORIGINAL_PRESENT,
			ObservedAtMs: time.Now().UnixMilli()}
	case mode&os.ModeSymlink != 0:
		entry.Kind = entity.EntryKind_ENTRY_LINK
	default:
		entry.Kind = entity.EntryKind_ENTRY_OTHER
	}
	if live.Path == "" {
		location, err := s.api.lib.GetOnlineSource(ctx, ref.LocationId)
		if err != nil {
			return nil, err
		}
		entry.Name = location.Name
		entry.Operations = []entity.FileOperationKind{entity.FileOperationKind_MAKE_DIRECTORY}
	}
	if !mode.IsRegular() {
		return entry, nil
	}
	original, err := s.api.lib.GetFileLocationAtPath(ctx, ref.LocationId, live.Path)
	if err != nil || original == nil {
		return entry, err
	}
	file, err := s.api.lib.GetFile(ctx, original.FileID)
	if err != nil {
		return nil, err
	}
	if err := s.hydrate(ctx, []*library.File{file}); err != nil {
		return nil, err
	}
	_ = s.api.exe.ObserveFileContent(ctx, file.ID, file.ContentSummary)
	entry.File, entry.Summary = convertFiles(file)[0], file.ContentSummary
	return entry, nil
}

type filesCursor struct {
	Query string
	Page  string
}

func (s *filesService) listLocation(ctx context.Context, directory *entity.LocationEntryRef, cursor, query string, limit int) (*entity.ListFilesReply, error) {
	// The underlying live cursor binds the actual directory and root; the wrapper binds its query.
	if directory == nil {
		return nil, status.Error(codes.InvalidArgument, "Location directory is required")
	}
	if directory.BindingToken != "" {
		location, err := s.api.lib.GetOnlineSource(ctx, directory.LocationId)
		if err != nil {
			return nil, err
		}
		if location.BindingToken != directory.BindingToken {
			return nil, onlineError(library.ErrOnlineConflict)
		}
	}
	pageCursor := ""
	if cursor != "" {
		var decoded filesCursor
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if len(cursor) > 32768 || err != nil || json.Unmarshal(data, &decoded) != nil || decoded.Query != query {
			return nil, status.Error(codes.InvalidArgument, "invalid Files cursor")
		}
		pageCursor = decoded.Page
	}
	var match func([]*entity.LocationEntry) ([]int, error)
	if query != "" {
		match = func(rows []*entity.LocationEntry) ([]int, error) { return s.matchLocationEntries(ctx, query, rows) }
	}
	page, err := s.api.exe.ListLocationEntriesMatching(ctx, &entity.ListLocationEntriesRequest{LocationId: directory.LocationId,
		ParentPath: directory.Path, Cursor: pageCursor, Limit: int32(limit)}, match)
	if err != nil {
		return nil, onlineError(err)
	}
	reply := &entity.ListFilesReply{Scope: entity.FileScope_FILE_SCOPE_ALL}
	for _, live := range page.Entries {
		entry, err := s.locationEntry(ctx, live)
		if err != nil {
			return nil, err
		}
		reply.Entries = append(reply.Entries, entry)
	}
	pageCursor = page.NextCursor
	if pageCursor != "" {
		data, err := json.Marshal(filesCursor{Query: query, Page: pageCursor})
		if err != nil {
			return nil, err
		}
		reply.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return reply, nil
}

func (s *filesService) matchLocationEntries(ctx context.Context, query string, entries []*entity.LocationEntry) ([]int, error) {
	rows := make([]library.LiveQueryRow, 0, len(entries))
	for _, live := range entries {
		entry, err := s.locationEntry(ctx, live)
		if err != nil {
			return nil, err
		}
		row := library.LiveQueryRow{FileID: entry.GetFile().GetId(), LocationID: live.Reference.LocationId, Name: entry.Name,
			Note: entry.GetFile().GetNote(), Kind: entry.Kind, Size: entry.GetSize(), MtimeNS: entry.MtimeNs,
			HasArchive: entry.GetSummary().GetArchivedCopies() > 0}
		if entry.GetSummary().GetCurrentObservationValid() && entry.GetSummary().GetSignatureKnown() {
			original, err := s.api.lib.GetFileLocation(ctx, row.FileID)
			if err != nil {
				return nil, err
			}
			if original != nil {
				row.Signature = original.Signature
			}
		}
		rows = append(rows, row)
	}
	return s.api.lib.MatchLiveFiles(ctx, query, rows)
}
