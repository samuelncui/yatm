package apis

import (
	"context"
	"os"
	"path"
	"strconv"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
)

type filesCursor struct{ Query, Page string }

func (s *filesService) locationDirectoryEntries(ctx context.Context, rows []executor.LocationDirectoryEntry,
	projection filesProjection, location *library.Location, infos map[string]os.FileInfo) ([]*entity.FilesEntry, error) {
	// Only observed children participate in association lookup and optional status projection.
	live := make([]*entity.LocationEntry, 0, len(rows))
	for _, row := range rows {
		if row.Error == nil {
			live = append(live, row.Entry)
		}
	}
	projected, _, _, err := s.locationEntriesObserved(ctx, live, projection, location, infos)
	if err != nil {
		return nil, err
	}

	// Keep lexical enumeration order and count, with display-only placeholders for failed children.
	entries := make([]*entity.FilesEntry, 0, len(rows))
	index := 0
	for _, row := range rows {
		if row.Error != nil {
			entries = append(entries, locationDirectoryError(row))
			continue
		}
		entries = append(entries, projected[index])
		index++
	}
	return entries, nil
}

func locationDirectoryError(row executor.LocationDirectoryEntry) *entity.FilesEntry {
	// Preserve legal names exactly; invalid UTF-8 needs escaped display text for the wire contract.
	reason := strconv.Quote(row.Error.Error())
	entry := &entity.FilesEntry{Name: path.Base(row.Path), Path: row.Path,
		Error: reason[1 : len(reason)-1]}
	if !utf8.ValidString(row.Path) {
		entry.Name, entry.Path = strconv.Quote(entry.Name), strconv.Quote(entry.Path)
	}

	// A zero directory-entry type can mean unknown, so failed stat must not invent a regular file.
	switch {
	case row.Type.IsDir():
		entry.Kind = entity.EntryKind_ENTRY_KIND_DIRECTORY
	case row.Type&os.ModeSymlink != 0:
		entry.Kind = entity.EntryKind_ENTRY_KIND_LINK
	case row.Type != 0:
		entry.Kind = entity.EntryKind_ENTRY_KIND_OTHER
	}
	return entry
}

func basicLocationEntry(live *entity.LocationEntry, projection filesProjection) *entity.FilesEntry {
	entry := &entity.FilesEntry{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: live.Reference}}, Name: path.Base(live.Path), Path: live.Path}
	mode := os.FileMode(live.Reference.GetFacts().GetMode())
	switch {
	case mode.IsDir():
		entry.Kind = entity.EntryKind_ENTRY_KIND_DIRECTORY
	case mode.IsRegular():
		entry.Kind = entity.EntryKind_ENTRY_KIND_FILE
	case mode&os.ModeSymlink != 0:
		entry.Kind = entity.EntryKind_ENTRY_KIND_LINK
	default:
		entry.Kind = entity.EntryKind_ENTRY_KIND_OTHER
	}
	if projection[entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES] {
		mtime := live.Reference.Facts.MtimeNs
		entry.MtimeNs = &mtime
		if mode.IsRegular() {
			size := live.Reference.Facts.SizeBytes
			entry.SizeBytes = &size
		}
	}
	if projection[entity.FilesInclude_FILES_INCLUDE_OPERATIONS] {
		entry.Operations = locationOperations(live.Path, entry.Kind)
	}
	if projection[entity.FilesInclude_FILES_INCLUDE_STATUS] && mode.IsRegular() {
		entry.Status = &entity.FilesStatus{Original: entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, Archive: entity.FilesArchive_FILES_ARCHIVE_UNSPECIFIED, Current: entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED}
	}
	return entry
}

func locationOperations(relative string, kind entity.EntryKind) []entity.FileOperationKind {
	if executor.IsLocationTrashPath(relative) {
		if executor.IsLocationTrashContent(relative) {
			return []entity.FileOperationKind{entity.FileOperationKind_FILE_OPERATION_KIND_MOVE}
		}
		return nil
	}
	var operations []entity.FileOperationKind
	if relative != "" {
		operations = append(operations, entity.FileOperationKind_FILE_OPERATION_KIND_MOVE, entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE)
	}
	if kind == entity.EntryKind_ENTRY_KIND_DIRECTORY {
		operations = append(operations, entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR)
	}
	if kind == entity.EntryKind_ENTRY_KIND_DIRECTORY || kind == entity.EntryKind_ENTRY_KIND_FILE {
		operations = append(operations, entity.FileOperationKind_FILE_OPERATION_KIND_SCAN, entity.FileOperationKind_FILE_OPERATION_KIND_ARCHIVE, entity.FileOperationKind_FILE_OPERATION_KIND_ADMIT)
	}
	if kind == entity.EntryKind_ENTRY_KIND_FILE {
		operations = append(operations, entity.FileOperationKind_FILE_OPERATION_KIND_UPDATE_METADATA)
	}
	return operations
}

func (s *filesService) locationEntries(ctx context.Context, live []*entity.LocationEntry, projection filesProjection) ([]*entity.FilesEntry, map[int64]*library.FileReadFacts, map[int64]*executor.FileReadObservation, error) {
	return s.locationEntriesObserved(ctx, live, projection, nil, nil)
}

func (s *filesService) locationEntriesObserved(ctx context.Context, live []*entity.LocationEntry, projection filesProjection, location *library.Location, infos map[string]os.FileInfo) ([]*entity.FilesEntry, map[int64]*library.FileReadFacts, map[int64]*executor.FileReadObservation, error) {
	// Physical rows always exist independently of optional Library associations.
	entries := make([]*entity.FilesEntry, 0, len(live))
	paths := make([]string, 0, len(live))
	for _, row := range live {
		entries = append(entries, basicLocationEntry(row, projection))
		paths = append(paths, row.Path)
	}
	if len(live) == 0 || !projection[entity.FilesInclude_FILES_INCLUDE_STATUS] && !projection[entity.FilesInclude_FILES_INCLUDE_OPERATIONS] {
		return entries, nil, nil, nil
	}
	originals, err := s.api.lib.ReadFileOriginalsAt(ctx, live[0].Reference.LocationId, paths)
	if err != nil {
		return nil, nil, nil, err
	}
	ids := make([]int64, 0, len(originals))
	for index, row := range live {
		if original := originals[row.Path]; original != nil {
			if projection[entity.FilesInclude_FILES_INCLUDE_OPERATIONS] {
				id := original.FileID
				entries[index].AssociatedFileId = &id
			}
			ids = append(ids, original.FileID)
		}
	}
	if !projection[entity.FilesInclude_FILES_INCLUDE_STATUS] {
		return entries, nil, nil, nil
	}
	facts, err := s.api.lib.ReadFileFacts(ctx, ids, false, true)
	if err != nil {
		return nil, nil, nil, err
	}
	observations, err := s.api.exe.ObserveListedFileRows(ctx, facts, location, infos)
	if err != nil {
		return nil, nil, nil, err
	}

	// Current physical facts win over the recorded association.
	for index, row := range live {
		original := originals[row.Path]
		if original == nil || entries[index].Kind != entity.EntryKind_ENTRY_KIND_FILE {
			continue
		}
		observation := observations[original.FileID]
		if observation == nil {
			observation = &executor.FileReadObservation{}
		}
		observation.Availability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT
		entries[index].Status = filesStatus(facts[original.FileID], observation)
	}
	return entries, facts, observations, nil
}

func (s *filesService) matchLocationRows(ctx context.Context, query *library.FilesQuery, live []*entity.LocationEntry, entries []*entity.FilesEntry, facts map[int64]*library.FileReadFacts) ([]int, error) {
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if id := entry.GetAssociatedFileId(); id != 0 {
			ids = append(ids, id)
		}
	}
	files, err := s.api.lib.ReadFileRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	rows := make([]library.LiveQueryRow, 0, len(entries))
	for index, entry := range entries {
		id := entry.GetAssociatedFileId()
		row := library.LiveQueryRow{FileID: id, LocationID: live[index].Reference.LocationId, Name: entry.Name, Kind: entry.Kind, Size: entry.GetSizeBytes(), MtimeNS: entry.GetMtimeNs(), HasArchive: entry.GetStatus().GetArchive() == entity.FilesArchive_FILES_ARCHIVE_AVAILABLE}
		if file := files[id]; file != nil {
			row.Note = file.Note
		}
		if fact := facts[id]; fact != nil && entry.GetStatus().GetCurrent() != entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED && fact.Original != nil {
			row.Signature = fact.Original.Signature
		}
		rows = append(rows, row)
	}
	return s.api.lib.MatchLiveQuery(ctx, query, rows)
}
