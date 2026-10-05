package apis

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/files"
	"github.com/samuelncui/yatm/internal/library"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type filesService struct {
	entity.UnimplementedFilesServiceServer
	api *API
}

type filesProjection map[entity.FilesInclude]bool

func parseFilesProjection(include []entity.FilesInclude) (filesProjection, error) {
	projection := filesProjection{}
	for _, value := range include {
		if value < entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES || value > entity.FilesInclude_FILES_INCLUDE_NAVIGATION {
			return nil, status.Error(codes.InvalidArgument, "invalid Files include")
		}
		projection[value] = true
	}
	return projection, nil
}

// List streams one complete directory. The source enumerates once before the first batch
// to determine the total, then projects bounded batches without a continuation cursor.
func (s *filesService) List(req *entity.ListFilesRequest, stream entity.FilesService_ListServer) error {
	ctx := stream.Context()
	if req.GetDirectory().GetTarget() == nil {
		return status.Error(codes.InvalidArgument, "directory reference is required")
	}
	if _, err := parseFilesProjection(req.Include); err != nil {
		return err
	}
	source, request, err := s.resolveListSource(req)
	if err != nil {
		return err
	}
	return source.List(ctx, request, func(batch files.ListReply) error {
		return stream.Send(&entity.ListFilesResponse{
			Entries:         batch.Entries,
			Scope:           batch.Scope,
			Directory:       batch.Directory,
			Breadcrumbs:     batch.Breadcrumbs,
			TotalEntryCount: batch.Total,
		})
	})
}

// Search answers one bounded query page with a continuation cursor. Recursive queries
// are supported by the Library source; a Location query reads one physical directory.
func (s *filesService) Search(ctx context.Context, req *entity.SearchFilesRequest) (*entity.SearchFilesResponse, error) {
	if req.GetDirectory().GetTarget() == nil {
		return nil, status.Error(codes.InvalidArgument, "directory reference is required")
	}
	if _, err := parseFilesProjection(req.Include); err != nil {
		return nil, err
	}
	limit, err := pageLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.Query)
	compiled, err := s.api.lib.CompileFilesQuery(query)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	source, request, err := s.resolveSearchSource(req, compiled, limit)
	if err != nil {
		return nil, err
	}
	reply, err := source.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	return &entity.SearchFilesResponse{
		Entries:         reply.Entries,
		NextCursor:      reply.NextCursor,
		Scope:           reply.Scope,
		Directory:       reply.Directory,
		Breadcrumbs:     reply.Breadcrumbs,
		TotalEntryCount: reply.Total,
	}, nil
}

// resolveListSource binds one listing request to the source that owns the directory.
func (s *filesService) resolveListSource(req *entity.ListFilesRequest) (files.Source, files.ListRequest, error) {
	directory, err := filesDirectory(req.Directory)
	if err != nil {
		return nil, files.ListRequest{}, err
	}
	request := files.ListRequest{Directory: directory, Scope: req.Scope, Include: req.Include, BatchSize: int(req.BatchSize)}
	switch directory.Kind {
	case files.KindLibrary:
		return &librarySource{service: s}, request, nil
	default:
		return &locationSource{service: s}, request, nil
	}
}

// resolveSearchSource binds one query request to the source that owns the directory.
func (s *filesService) resolveSearchSource(req *entity.SearchFilesRequest, query *library.FilesQuery, limit int) (files.Source, files.SearchRequest, error) {
	directory, err := filesDirectory(req.Directory)
	if err != nil {
		return nil, files.SearchRequest{}, err
	}
	if directory.Kind == files.KindLocation && req.Recursive {
		return nil, files.SearchRequest{}, status.Error(codes.InvalidArgument, "Location browsing supports one directory at a time")
	}
	request := files.SearchRequest{Directory: directory, Scope: req.Scope, Query: query.Text, Cursor: req.Cursor,
		Limit: limit, Recursive: req.Recursive, Include: req.Include}
	if directory.Kind == files.KindLibrary {
		return &librarySource{service: s, query: query}, request, nil
	}
	return &locationSource{service: s, query: query}, request, nil
}

// filesDirectory reads the addressed directory out of a public reference.
func filesDirectory(reference *entity.FileOperationRef) (files.Directory, error) {
	switch directory := reference.GetTarget().(type) {
	case *entity.FileOperationRef_FileId:
		return files.Directory{Kind: files.KindLibrary, ID: directory.FileId}, nil
	case *entity.FileOperationRef_Location:
		if directory.Location == nil {
			return files.Directory{}, status.Error(codes.InvalidArgument, "Location directory is required")
		}
		return files.Directory{Kind: files.KindLocation, ID: directory.Location.LocationId, Path: directory.Location.Path}, nil
	default:
		return files.Directory{}, status.Error(codes.InvalidArgument, "unsupported Files source")
	}
}

func (s *filesService) Get(ctx context.Context, req *entity.GetFileRequest) (*entity.GetFileResponse, error) {
	// Details load one identity and its organization; histories and previews have separate APIs.
	if req.GetReference().GetTarget() == nil {
		return nil, status.Error(codes.InvalidArgument, "entry reference is required")
	}
	source, entry, err := s.resolveDetailSource(req.Reference)
	if err != nil {
		return nil, err
	}
	detail, err := source.Get(ctx, entry)
	if err != nil {
		return nil, err
	}
	reply := &entity.FilesDetail{Entry: detail.Entry, Original: detail.Original, ContentReference: detail.ContentReference, ContentSignature: detail.ContentSignature}

	// Organization is a separate small read, performed only for an explicitly opened detail.
	if detail.FileID != 0 {
		file, err := s.readFile(ctx, detail.FileID)
		if err != nil {
			return nil, err
		}
		paths, err := s.api.lib.ReadFilePaths(ctx, []*library.File{file})
		if err != nil {
			return nil, err
		}
		tags, err := s.api.lib.MGetFileTags(ctx, detail.FileID)
		if err != nil {
			return nil, err
		}
		reply.Organization = &entity.FilesOrganization{Parent: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: file.ParentID}}, Path: strings.TrimPrefix(paths[detail.FileID], "/"), Note: file.Note, Tags: tags[detail.FileID]}
	}
	return &entity.GetFileResponse{Detail: reply}, nil
}

// resolveDetailSource binds one detail request to the source that owns the reference.
func (s *filesService) resolveDetailSource(reference *entity.FileOperationRef) (files.Source, files.Entry, error) {
	switch target := reference.Target.(type) {
	case *entity.FileOperationRef_FileId:
		if target.FileId < library.TrashFileID {
			return nil, files.Entry{}, status.Error(codes.InvalidArgument, "invalid Library reference")
		}
		return &librarySource{service: s}, files.Entry{Kind: files.KindLibrary, ID: target.FileId}, nil
	case *entity.FileOperationRef_Location:
		if target.Location == nil {
			return nil, files.Entry{}, status.Error(codes.InvalidArgument, "Location reference is required")
		}
		return &locationSource{service: s}, files.Entry{Kind: files.KindLocation, ID: target.Location.LocationId, Path: target.Location.Path}, nil
	default:
		return nil, files.Entry{}, status.Error(codes.InvalidArgument, "unsupported Files source")
	}
}

// readFile loads one admitted File or reports the same not-found identity every caller uses.
func (s *filesService) readFile(ctx context.Context, fileID int64) (*library.File, error) {
	rows, err := s.api.lib.ReadFileRows(ctx, []int64{fileID})
	if err != nil {
		return nil, err
	}
	file := rows[fileID]
	if file == nil {
		return nil, apiError(library.ErrFileNotFound)
	}
	return file, nil
}

func (s *filesService) UpdateMetadata(ctx context.Context, req *entity.UpdateFilesMetadataRequest) (*entity.UpdateFilesMetadataResponse, error) {
	// Explicit annotation is the only read-adjacent path that admits an uncollected original.
	if len(req.GetReferences()) == 0 || len(req.References) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "select between 1 and 1000 entries")
	}
	ids := make([]int64, 0, len(req.References))
	groups := map[int64][]*entity.LocationEntryRef{}
	for _, ref := range req.References {
		if ref.GetTarget() == nil {
			return nil, status.Error(codes.InvalidArgument, "entry reference is required")
		}
		if live := ref.GetLocation(); live != nil {
			if executor.IsLocationTrashPath(live.Path) {
				return nil, status.Error(codes.FailedPrecondition, "trash content cannot be annotated")
			}
			groups[live.LocationId] = append(groups[live.LocationId], live)
		} else {
			ids = append(ids, ref.GetFileId())
		}
	}
	// Enforce the same logical-trash capability before any explicit admission is published.
	stored, err := s.api.lib.ReadFileRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	selected := make([]*library.File, 0, len(ids))
	for _, id := range ids {
		file := stored[id]
		if id <= 0 || file == nil {
			return nil, status.Error(codes.InvalidArgument, "select existing Library files")
		}
		selected = append(selected, file)
	}
	paths, err := s.api.lib.ReadFilePaths(ctx, selected)
	if err != nil {
		return nil, err
	}
	for _, logicalPath := range paths {
		if strings.HasPrefix(logicalPath, "/.Trash/") || logicalPath == "/.Trash" {
			return nil, status.Error(codes.FailedPrecondition, "trash content cannot be annotated")
		}
	}

	// Admit selected originals in bounded Location groups before the shared metadata mutation.
	for _, refs := range groups {
		originals, err := s.api.exe.AdmitLocationEntries(ctx, refs)
		if err != nil {
			return nil, apiError(err)
		}
		for _, original := range originals {
			ids = append(ids, original.FileID)
		}
	}
	if err := s.api.lib.EditFileMetadata(ctx, ids, library.FileMetadataEdit{Note: req.Note, AddTags: req.AddTags, RemoveTags: req.RemoveTags}); err != nil {
		return nil, apiError(err)
	}

	// Return settled details for this explicit write; ordinary list reads never enter this path.
	reply := &entity.UpdateFilesMetadataResponse{}
	for _, ref := range req.References {
		entry, err := s.Get(ctx, &entity.GetFileRequest{Reference: ref})
		if err != nil {
			return nil, fmt.Errorf("metadata saved; refresh details failed: %w", err)
		}
		reply.Entries = append(reply.Entries, entry.Detail)
	}
	return reply, nil
}
