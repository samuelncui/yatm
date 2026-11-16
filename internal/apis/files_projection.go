package apis

import (
	"context"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
)

func (s *filesService) libraryEntries(ctx context.Context, files []*library.File, projection filesProjection, fullPaths bool) ([]*entity.FilesEntry, map[int64]*executor.FileReadObservation, map[int64]*library.FileReadFacts, error) {
	return s.libraryEntriesObserved(ctx, files, projection, fullPaths, s.api.exe.NewFileRowReader())
}

func (s *filesService) libraryEntriesObserved(ctx context.Context, files []*library.File, projection filesProjection, fullPaths bool, reader *executor.FileRowReader) ([]*entity.FilesEntry, map[int64]*executor.FileReadObservation, map[int64]*library.FileReadFacts, error) {
	// Load exactly the requested data groups in bounded metadata batches.
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		if file.Kind == entity.FileKind_FILE_KIND_REGULAR {
			ids = append(ids, file.ID)
		}
	}
	facts := map[int64]*library.FileReadFacts{}
	var err error
	if projection[entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES] || projection[entity.FilesInclude_FILES_INCLUDE_STATUS] {
		facts, err = s.api.lib.ReadFileFacts(ctx, ids, projection[entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES], projection[entity.FilesInclude_FILES_INCLUDE_STATUS])
		if err != nil {
			return nil, nil, nil, err
		}
	}
	observations := map[int64]*executor.FileReadObservation{}
	if projection[entity.FilesInclude_FILES_INCLUDE_STATUS] {
		observations, err = reader.Read(ctx, facts)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	paths := map[int64]string{}
	if fullPaths || projection[entity.FilesInclude_FILES_INCLUDE_OPERATIONS] || projection[entity.FilesInclude_FILES_INCLUDE_NAVIGATION] {
		paths, err = s.api.lib.ReadFilePaths(ctx, files)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	// Assemble a coherent page without nested File, version, Preview or copy-count projections.
	entries := make([]*entity.FilesEntry, 0, len(files))
	for _, file := range files {
		entry := basicLibraryEntry(file)
		if value, ok := paths[file.ID]; ok {
			entry.Path = strings.TrimPrefix(value, "/")
		}
		if projection[entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES] {
			if row := facts[file.ID]; row != nil {
				entry.SizeBytes, entry.MtimeNs = row.Size, row.MtimeNS
			}
			if file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
				value := library.FileRowTime(file)
				entry.MtimeNs = &value
			}
			if observation := observations[file.ID]; observation != nil && observation.Entry != nil {
				actual := observation.Entry.Reference.Facts
				entry.SizeBytes, entry.MtimeNs = &actual.SizeBytes, &actual.MtimeNs
			}
		}
		if projection[entity.FilesInclude_FILES_INCLUDE_STATUS] && file.Kind == entity.FileKind_FILE_KIND_REGULAR {
			entry.Status = observations[file.ID].Status(facts[file.ID])
		}
		if projection[entity.FilesInclude_FILES_INCLUDE_OPERATIONS] {
			entry.Operations = libraryOperations(file, entry.Path)
		}
		entries = append(entries, entry)
	}
	return entries, observations, facts, nil
}

func basicLibraryEntry(file *library.File) *entity.FilesEntry {
	entry := &entity.FilesEntry{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: file.ID}}, Name: file.Name, Path: file.Name}
	switch file.Kind {
	case entity.FileKind_FILE_KIND_REGULAR:
		entry.Kind = entity.EntryKind_ENTRY_KIND_FILE
	case entity.FileKind_FILE_KIND_DIRECTORY:
		entry.Kind = entity.EntryKind_ENTRY_KIND_DIRECTORY
	default:
		entry.Kind = entity.EntryKind_ENTRY_KIND_OTHER
	}
	return entry
}

func libraryRootEntry(projection filesProjection) *entity.FilesEntry {
	entry := basicLibraryEntry(library.Root)
	entry.Name = "Library"
	if projection[entity.FilesInclude_FILES_INCLUDE_OPERATIONS] {
		entry.Operations = []entity.FileOperationKind{entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR, entity.FileOperationKind_FILE_OPERATION_KIND_SCAN, entity.FileOperationKind_FILE_OPERATION_KIND_ARCHIVE}
	}
	return entry
}

func libraryOperations(file *library.File, logicalPath string) []entity.FileOperationKind {
	if file.ID <= 0 {
		return nil
	}
	operations := []entity.FileOperationKind{entity.FileOperationKind_FILE_OPERATION_KIND_MOVE}
	if library.IsTrashPath(logicalPath) {
		return operations
	}
	operations = append(operations, entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, entity.FileOperationKind_FILE_OPERATION_KIND_SCAN, entity.FileOperationKind_FILE_OPERATION_KIND_ARCHIVE, entity.FileOperationKind_FILE_OPERATION_KIND_UPDATE_METADATA)
	if file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
		operations = append(operations, entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR)
	}
	return operations
}
