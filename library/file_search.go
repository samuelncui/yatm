package library

import (
	"context"
	"fmt"
	"github.com/samuelncui/yatm/entity"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	querystring "github.com/bytedance/go-querystring-parser"
)

const (
	defaultFileSearchLimit = 100
	maxFileSearchLimit     = 500
	maxFileSearchRunes     = 4096
	maxFilePathDepth       = 256
)

type FileSearchResult struct {
	File *File
	Path string
}

type FileSearchPage struct {
	Results    []*FileSearchResult
	NextCursor string
}

type Tag struct {
	Name      string
	FileCount int64
}

type TagPage struct {
	Tags       []*Tag
	NextCursor string
}

type filePathState struct {
	parentID int64
	parts    []string
	seen     map[int64]struct{}
}

func (l *Library) SearchFiles(ctx context.Context, query, cursor string, limit int64) (*FileSearchPage, error) {
	return l.SearchFilesIn(ctx, query, cursor, limit, entity.FileScope_FILE_SCOPE_ALL, 0, 0)
}

func (l *Library) SearchFilesIn(ctx context.Context, query, cursor string, limit int64, scope entity.FileScope, locationID, revision int64) (*FileSearchPage, error) {
	// Physical searches never inherit the Library visibility preference.
	db := l.db
	if locationID != 0 {
		location, err := l.GetOnlineSource(ctx, locationID)
		if err != nil {
			return nil, err
		}
		if location.Revision != revision {
			return nil, ErrOnlineConflict
		}
		scope = entity.FileScope_FILE_SCOPE_ALL
		db = db.Set("file_query_location", true)
	}
	scope, err := l.ResolveFileScope(ctx, scope)
	if err != nil {
		return nil, err
	}

	// Validate and parse the complete user query before touching the database.
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("File search query must not be empty")
	}
	if utf8.RuneCountInString(query) > maxFileSearchRunes {
		return nil, fmt.Errorf("File search query exceeds %d characters", maxFileSearchRunes)
	}
	pageLimit, err := normalizeFileSearchLimit(limit)
	if err != nil {
		return nil, err
	}
	condition, err := querystring.Parse(query)
	if err != nil {
		return nil, fmt.Errorf("parse File search query failed, %w", err)
	}
	if condition == nil {
		return nil, fmt.Errorf("File search query must not be empty")
	}
	expression, err := compileFileQuery(db, condition)
	if err != nil {
		return nil, fmt.Errorf("compile File search query failed, %w", err)
	}
	cursorInput := fmt.Sprintf("%s/%d/%d/%d", query, scope, locationID, revision)
	afterName, afterID, hasCursor, err := decodePageCursor(cursor, searchCursorKind, cursorInput)
	if err != nil {
		return nil, fmt.Errorf("validate File search cursor failed, %w", err)
	}

	// Query one deterministic page with a look-ahead row for the next cursor.
	files := make([]*File, 0, pageLimit+1)
	request := filterFileScope(l.db.WithContext(ctx).Model(ModelFile).Where(expression), scope)
	if locationID != 0 {
		request = request.Where("EXISTS (SELECT 1 FROM file_locations WHERE file_locations.file_id = files.id AND location_id = ?)", locationID)
	}
	if hasCursor {
		request = request.Where("name > ? OR (name = ? AND id > ?)", afterName, afterName, afterID)
	}
	result := request.Order("name ASC").Order("id ASC").Limit(pageLimit + 1).Find(&files)
	if result.Error != nil {
		return nil, fmt.Errorf("query Library Files failed, %w", result.Error)
	}
	hasMore := len(files) > pageLimit
	if hasMore {
		files = files[:pageLimit]
	}

	// Resolve paths for only the returned page and preserve its database order.
	paths, err := l.resolveFilePaths(ctx, files)
	if err != nil {
		return nil, err
	}
	if locationID != 0 {
		ids := make([]int64, 0, len(files))
		for _, file := range files {
			ids = append(ids, file.ID)
		}
		var originals []*FileLocation
		if len(ids) > 0 {
			if err := l.db.WithContext(ctx).Where("file_id IN ? AND location_id = ?", ids, locationID).Find(&originals).Error; err != nil {
				return nil, err
			}
		}
		for _, original := range originals {
			paths[original.FileID] = original.Path
		}
		location, err := l.GetOnlineSource(ctx, locationID)
		if err != nil {
			return nil, err
		}
		if location.Revision != revision {
			return nil, ErrOnlineConflict
		}
	}
	page := &FileSearchPage{Results: make([]*FileSearchResult, 0, len(files))}
	for _, file := range files {
		page.Results = append(page.Results, &FileSearchResult{File: file, Path: paths[file.ID]})
	}
	if hasMore && len(files) > 0 {
		last := files[len(files)-1]
		page.NextCursor = encodePageCursor(searchCursorKind, cursorInput, last.Name, last.ID)
	}
	return page, nil
}

func (l *Library) ListTags(ctx context.Context, prefix, cursor string, limit int64) (*TagPage, error) {
	// Canonicalize the prefix and validate the opaque page cursor.
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if !utf8.ValidString(prefix) {
		return nil, fmt.Errorf("Tag prefix is not valid UTF-8")
	}
	if utf8.RuneCountInString(prefix) > maxTagRunes {
		return nil, fmt.Errorf("Tag prefix exceeds %d characters", maxTagRunes)
	}
	pageLimit, err := normalizeFileSearchLimit(limit)
	if err != nil {
		return nil, err
	}
	afterTag, _, hasCursor, err := decodePageCursor(cursor, tagCursorKind, prefix)
	if err != nil {
		return nil, fmt.Errorf("validate Tag cursor failed, %w", err)
	}

	// Aggregate only referenced Tags and fetch one look-ahead row.
	tags := make([]*Tag, 0, pageLimit+1)
	request := l.db.WithContext(ctx).
		Model(ModelFileTag).
		Select("tag AS name, COUNT(*) AS file_count").
		Where(fmt.Sprintf("tag LIKE ? ESCAPE '%c'", likeEscape), escapeLike(prefix, false)+"%")
	if hasCursor {
		request = request.Where("tag > ?", afterTag)
	}
	result := request.Group("tag").Order("tag ASC").Limit(pageLimit + 1).Scan(&tags)
	if result.Error != nil {
		return nil, fmt.Errorf("list Tags failed, %w", result.Error)
	}
	hasMore := len(tags) > pageLimit
	if hasMore {
		tags = tags[:pageLimit]
	}

	// Return the next opaque cursor only when another page exists.
	page := &TagPage{Tags: tags}
	if hasMore && len(tags) > 0 {
		page.NextCursor = encodePageCursor(tagCursorKind, prefix, tags[len(tags)-1].Name, 0)
	}
	return page, nil
}

func normalizeFileSearchLimit(limit int64) (int, error) {
	if limit == 0 {
		return defaultFileSearchLimit, nil
	}
	if limit < 0 || limit > maxFileSearchLimit {
		return 0, fmt.Errorf("page limit must be between 1 and %d, limit=%d", maxFileSearchLimit, limit)
	}
	return int(limit), nil
}

func (l *Library) resolveFilePaths(ctx context.Context, files []*File) (map[int64]string, error) {
	// Track each result's ancestor chain while sharing parent reads across the page.
	states := make(map[int64]*filePathState, len(files))
	for _, file := range files {
		states[file.ID] = &filePathState{
			parentID: file.ParentID,
			parts:    []string{file.Name},
			seen:     map[int64]struct{}{file.ID: {}},
		}
	}

	// Resolve one shared ancestor level per query until every path reaches the root.
	for depth := 0; depth < maxFilePathDepth; depth++ {
		want := make(map[int64]struct{}, len(states))
		for _, current := range states {
			if current.parentID != 0 {
				want[current.parentID] = struct{}{}
			}
		}
		if len(want) == 0 {
			return buildResolvedFilePaths(states), nil
		}
		ids := make([]int64, 0, len(want))
		for id := range want {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		parents, err := l.mGetFile(ctx, l.db.WithContext(ctx), ids...)
		if err != nil {
			return nil, fmt.Errorf("resolve File path parents failed, %w", err)
		}
		for fileID, current := range states {
			if current.parentID == 0 {
				continue
			}
			parent := parents[current.parentID]
			if parent == nil {
				return nil, fmt.Errorf("resolve File %d path failed, parent_id=%d not found", fileID, current.parentID)
			}
			if _, ok := current.seen[parent.ID]; ok {
				return nil, fmt.Errorf("resolve File %d path failed, cycle at parent_id=%d", fileID, parent.ID)
			}
			current.seen[parent.ID] = struct{}{}
			current.parts = append(current.parts, parent.Name)
			current.parentID = parent.ParentID
		}
	}
	return nil, fmt.Errorf("resolve File paths exceeded depth %d", maxFilePathDepth)
}

func buildResolvedFilePaths(states map[int64]*filePathState) map[int64]string {
	paths := make(map[int64]string, len(states))
	for id, current := range states {
		for left, right := 0, len(current.parts)-1; left < right; left, right = left+1, right-1 {
			current.parts[left], current.parts[right] = current.parts[right], current.parts[left]
		}
		paths[id] = "/" + path.Join(current.parts...)
	}
	return paths
}
