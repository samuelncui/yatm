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
			entry.Status = filesStatus(facts[file.ID], observations[file.ID])
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
	if logicalPath == ".Trash" || strings.HasPrefix(logicalPath, ".Trash/") {
		return operations
	}
	operations = append(operations, entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, entity.FileOperationKind_FILE_OPERATION_KIND_SCAN, entity.FileOperationKind_FILE_OPERATION_KIND_ARCHIVE, entity.FileOperationKind_FILE_OPERATION_KIND_UPDATE_METADATA)
	if file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
		operations = append(operations, entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR)
	}
	return operations
}

// knownContentSignature is the recorded identity of content whose live observation still agrees
// with its record: the one identity a content-addressed read may name.
func knownContentSignature(facts *library.FileReadFacts, observation *executor.FileReadObservation) []byte {
	if facts == nil || facts.Original == nil || observation == nil || !observation.Valid {
		return nil
	}
	return facts.Original.Signature
}

func filesStatus(facts *library.FileReadFacts, observation *executor.FileReadObservation) *entity.FilesStatus {
	// A retained association and saved content are independent of current filesystem availability.
	result := &entity.FilesStatus{Original: entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNLINKED, Archive: entity.FilesArchive_FILES_ARCHIVE_NONE, Current: entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE}
	if facts == nil {
		return result
	}
	if observation != nil {
		result.Original = observation.Availability
	}
	if facts.Original != nil && result.Original != entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING {
		result.Current = entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED
	}
	currentKnown := len(knownContentSignature(facts, observation)) > 0
	if currentKnown {
		result.Current = entity.FilesCoverage_FILES_COVERAGE_UNCOVERED
		if facts.CurrentArchive {
			result.Current = entity.FilesCoverage_FILES_COVERAGE_COVERED
		}
	}
	hasArchive := facts.HasArchive || currentKnown && facts.CurrentArchive
	if hasArchive {
		result.Archive = entity.FilesArchive_FILES_ARCHIVE_AVAILABLE
	} else if facts.Original != nil && !currentKnown && result.Original != entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING {
		result.Archive = entity.FilesArchive_FILES_ARCHIVE_UNSPECIFIED
	}

	// Keep all applicable issues without deriving a health expiry or publishing new facts.
	bad := facts.HasBadCopies || currentKnown && facts.CurrentBadCopies
	if hasArchive && bad {
		result.Issues = append(result.Issues, entity.FilesIssue_FILES_ISSUE_PARTIAL_COPIES_UNAVAILABLE)
	}
	if !hasArchive && (facts.HasVersions || facts.HasCopies || currentKnown && facts.CurrentCopies) {
		result.Issues = append(result.Issues, entity.FilesIssue_FILES_ISSUE_ALL_COPIES_UNAVAILABLE)
	}
	if facts.LatestUnavailable {
		result.Issues = append(result.Issues, entity.FilesIssue_FILES_ISSUE_LATEST_VERSION_UNAVAILABLE)
	}
	return result
}
