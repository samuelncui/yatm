package library

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

// FileReadFacts is a page-bounded metadata projection, not a saved File state.
type FileReadFacts struct {
	Original                                         *FileLocation
	Tracking                                         []*FileTrackingKey
	Size                                             *int64
	MtimeNS                                          *int64
	HasVersions, HasCopies, HasArchive, HasBadCopies bool
	CurrentCopies, CurrentArchive, CurrentBadCopies  bool
	LatestUnavailable                                bool
}

// fileListPage bounds one page read. A nil page reads every matching row.
type fileListPage struct {
	cursor string
	limit  int
}

// ListAllFileRows enumerates every matching child in one read, so a caller that presents a
// directory as one list walks it once instead of once per page.
func (l *Library) ListAllFileRows(ctx context.Context, parent int64, scope entity.FileScope, recursive bool, query string) (*FilePage, error) {
	return l.listFiles(ctx, parent, scope, recursive, query, nil)
}

// listFiles runs the one listing query both a bounded page and a complete listing use.
func (l *Library) listFiles(ctx context.Context, parent int64, scope entity.FileScope, recursive bool, query string, page *fileListPage) (*FilePage, error) {
	compiled, err := l.CompileFilesQuery(query)
	if err != nil {
		return nil, err
	}
	return l.listFileQuery(ctx, parent, scope, recursive, compiled, page)
}

func (l *Library) listFileQuery(ctx context.Context, parent int64, scope entity.FileScope, recursive bool, query *FilesQuery, page *fileListPage) (*FilePage, error) {
	// Freeze the effective visibility and query in the continuation identity.
	scope, err := l.ResolveFileScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	expression := query.catalog
	input := fmt.Sprintf("rows/%d/%d/%t/%s", parent, scope, recursive, query.Text)
	after, afterID, present := "", int64(0), false
	if page != nil {
		after, afterID, present, err = decodePageCursor(page.cursor, fileListCursorKind, input)
		if err != nil {
			return nil, err
		}
	}

	// Recursive membership stays in the database; only the requested rows are materialized.
	db := filterFileScope(l.readDB().WithContext(ctx).Model(ModelFile), scope)
	if recursive {
		db = db.Where(`files.id IN (WITH RECURSIVE descendants(id) AS (
			SELECT id FROM files WHERE parent_id = ? UNION SELECT files.id FROM files JOIN descendants ON files.parent_id = descendants.id
		) SELECT id FROM descendants)`, parent)
	} else {
		db = db.Where("parent_id = ?", parent)
	}
	if expression != nil {
		db = db.Where(expression)
	}
	if present {
		db = db.Where("name > ? OR (name = ? AND id > ?)", after, after, afterID)
	}

	// Filter the complete predicate before the look-ahead row determines continuation.
	result := &FilePage{Scope: scope}
	rows := make([]*fileRow, 0)
	if page == nil {
		if err := db.Order("name, id").Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("list File rows failed, %w", err)
		}
		result.Files = fileViews(rows)
		return result, nil
	}
	if err := db.Order("name, id").Limit(page.limit + 1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list File rows failed, %w", err)
	}
	result.Files = fileViews(rows)
	page2 := result
	if len(page2.Files) > page.limit {
		page2.Files = page2.Files[:page.limit]
		last := page2.Files[len(page2.Files)-1]
		page2.NextCursor = encodePageCursor(fileListCursorKind, input, last.Name, last.ID)
	}
	return page2, nil
}

// ReadFileRows loads persisted identity and organization without presentation facts.
func (l *Library) ReadFileRows(ctx context.Context, ids []int64) (map[int64]*File, error) {
	result := make(map[int64]*File, len(ids))
	ids = uniqueFileIDs(ids)
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		var rows []*fileRow
		if err := l.readDB().WithContext(ctx).Where("id IN ?", ids[start:end]).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("read File rows failed, %w", err)
		}
		for _, row := range rows {
			result[row.ID] = row.file()
		}
	}
	return result, nil
}

// ListFileRows applies filters before pagination without loading omitted projections.
func (l *Library) ListFileRows(ctx context.Context, parent int64, scope entity.FileScope, recursive bool, query, cursor string, limit int64) (*FilePage, error) {
	pageLimit, err := normalizeFileSearchLimit(limit)
	if err != nil {
		return nil, err
	}
	return l.listFiles(ctx, parent, scope, recursive, query, &fileListPage{cursor: cursor, limit: pageLimit})
}

// ListFileQueryRows keeps candidate pagination identical while reusing the request's compilation.
func (l *Library) ListFileQueryRows(ctx context.Context, parent int64, scope entity.FileScope, recursive bool, query *FilesQuery, cursor string, limit int64) (*FilePage, error) {
	pageLimit, err := normalizeFileSearchLimit(limit)
	if err != nil {
		return nil, err
	}
	return l.listFileQuery(ctx, parent, scope, recursive, query, &fileListPage{cursor: cursor, limit: pageLimit})
}

// ReadFileOriginalsAt resolves one live page's optional associations with one query per batch.
func (l *Library) ReadFileOriginalsAt(ctx context.Context, locationID int64, paths []string) (map[string]*FileLocation, error) {
	result := make(map[string]*FileLocation, len(paths))
	for start := 0; start < len(paths); start += batchSize {
		var rows []*FileLocation
		if err := l.readDB().WithContext(ctx).Where("location_id = ? AND path IN ?", locationID, paths[start:min(start+batchSize, len(paths))]).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("read live associations failed, %w", err)
		}
		for _, row := range rows {
			result[row.Path] = row
		}
	}
	return result, nil
}

// ReadFileFacts fetches only requested attribute/status dependencies in bounded batches.
func (l *Library) ReadFileFacts(ctx context.Context, ids []int64, attributes, status bool) (map[int64]*FileReadFacts, error) {
	// Original associations serve attributes and live status; they never assert current existence.
	ids = uniqueFileIDs(ids)
	result := make(map[int64]*FileReadFacts, len(ids))
	for _, id := range ids {
		result[id] = &FileReadFacts{}
	}
	for start := 0; start < len(ids); start += batchSize {
		batch := ids[start:min(start+batchSize, len(ids))]
		var originals []*FileLocation
		if err := l.readDB().WithContext(ctx).Where("file_id IN ?", batch).Find(&originals).Error; err != nil {
			return nil, err
		}
		for _, original := range originals {
			facts := result[original.FileID]
			facts.Original = original
			if attributes {
				facts.Size, facts.MtimeNS = &original.Size, &original.MtimeNS
			}
		}
		if attributes {
			var versions []struct{ FileID, Size, MtimeNS int64 }
			if err := l.readDB().WithContext(ctx).Table("file_versions v").Select("v.file_id, v.size, v.mtime_ns").
				Where("v.file_id IN ? AND NOT EXISTS (SELECT 1 FROM file_locations o WHERE o.file_id = v.file_id)", batch).
				Where("v.id = (SELECT latest.id FROM file_versions latest WHERE latest.file_id = v.file_id ORDER BY latest.last_archived_at_ns DESC, latest.id DESC LIMIT 1)").Scan(&versions).Error; err != nil {
				return nil, err
			}
			for _, version := range versions {
				facts := result[version.FileID]
				facts.Size, facts.MtimeNS = &version.Size, &version.MtimeNS
			}
		}
		if !status {
			continue
		}

		// Existence queries stop at the first eligible copy instead of calculating display-only counts.
		// `has_archive` reads the file's own versions first and looks each signature up in positions:
		// written as one join, SQLite starts from positions through the health index, which matches
		// nearly every Position and costs seconds per batch. Every other EXISTS is already driven
		// by an indexed per-file lookup, which is the shape to preserve here.
		var rows []struct {
			FileID                                                             int64
			HasVersions, HasCopies, HasArchive, HasBadCopies                   bool
			CurrentCopies, CurrentArchive, CurrentBadCopies, LatestUnavailable bool
		}
		if err := l.readDB().WithContext(ctx).Table("files").Select(fmt.Sprintf(`files.id AS file_id,
			EXISTS (SELECT 1 FROM file_versions v WHERE v.file_id = files.id) AS has_versions,
			EXISTS (SELECT 1 FROM file_versions v JOIN positions p ON p.signature = v.signature WHERE v.file_id = files.id AND p.is_dir = @directory) AS has_copies,
			EXISTS (SELECT 1 FROM file_versions v WHERE v.file_id = files.id AND %[1]s AND EXISTS (SELECT 1 FROM positions p WHERE p.signature = v.signature AND p.is_dir = @directory AND p.health IN (@healthy,@unchecked))) AS has_archive,
			EXISTS (SELECT 1 FROM file_versions v JOIN positions p ON p.signature = v.signature WHERE v.file_id = files.id AND p.is_dir = @directory AND p.health NOT IN (@healthy,@unchecked)) AS has_bad_copies,
			EXISTS (SELECT 1 FROM positions p WHERE p.signature = file_locations.signature AND p.is_dir = @directory) AS current_copies,
			EXISTS (SELECT 1 FROM positions p WHERE p.signature = file_locations.signature AND p.is_dir = @directory AND p.health IN (@healthy,@unchecked) AND %[2]s) AS current_archive,
			EXISTS (SELECT 1 FROM positions p WHERE p.signature = file_locations.signature AND p.is_dir = @directory AND p.health NOT IN (@healthy,@unchecked)) AS current_bad_copies,
			EXISTS (SELECT 1 FROM file_versions v WHERE v.id = (SELECT latest.id FROM file_versions latest WHERE latest.file_id = files.id ORDER BY latest.last_archived_at_ns DESC,latest.id DESC LIMIT 1)
			AND NOT EXISTS (SELECT 1 FROM positions p WHERE p.signature = v.signature AND p.is_dir = @directory AND p.health IN (@healthy,@unchecked) AND %[1]s)) AS latest_unavailable`, consistentRestoreContentSQL("v"), consistentRestoreContentSQL("file_locations")),
			sql.Named("directory", false), sql.Named("healthy", entity.PositionHealth_POSITION_HEALTH_HEALTHY), sql.Named("unchecked", entity.PositionHealth_POSITION_HEALTH_UNKNOWN)).
			Joins("LEFT JOIN file_locations ON file_locations.file_id = files.id").Where("files.id IN ?", batch).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			facts := result[row.FileID]
			facts.HasVersions, facts.HasCopies, facts.HasArchive, facts.HasBadCopies = row.HasVersions, row.HasCopies, row.HasArchive, row.HasBadCopies
			facts.CurrentCopies, facts.CurrentArchive, facts.CurrentBadCopies, facts.LatestUnavailable = row.CurrentCopies, row.CurrentArchive, row.CurrentBadCopies, row.LatestUnavailable
		}
		var keys []*FileTrackingKey
		if err := l.readDB().WithContext(ctx).Where("file_id IN ?", batch).Find(&keys).Error; err != nil {
			return nil, err
		}
		for _, key := range keys {
			result[key.FileID].Tracking = append(result[key.FileID].Tracking, key)
		}
	}
	return result, nil
}

// ReadFileAncestors returns navigation metadata without loading any content projection.
func (l *Library) ReadFileAncestors(ctx context.Context, id int64) ([]*File, error) {
	return listFileRowParents(l.readDB().WithContext(ctx), id)
}

// ReadFilePaths resolves only page ancestry and never hydrates the parents' content.
func (l *Library) ReadFilePaths(ctx context.Context, files []*File) (map[int64]string, error) {
	return l.resolveFilePaths(ctx, files)
}

// FileRowTime uses the metadata timestamp only for logical directories.
func FileRowTime(file *File) int64 { return file.UpdatedAtNS }
