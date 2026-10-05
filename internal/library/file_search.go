package library

import (
	"context"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	querystring "github.com/bytedance/go-querystring-parser"
	"github.com/samuelncui/yatm/entity"
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
	return l.SearchFilesIn(ctx, query, cursor, limit, entity.FileScope_FILE_SCOPE_ALL)
}

func (l *Library) SearchFilesIn(ctx context.Context, query, cursor string, limit int64, scope entity.FileScope) (*FileSearchPage, error) {
	// Use the configured reader, retaining transaction-scoped Library views.
	db := l.readDB()
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
	cursorInput := fmt.Sprintf("%s/%d", query, scope)
	afterName, afterID, hasCursor, err := decodePageCursor(cursor, searchCursorKind, cursorInput)
	if err != nil {
		return nil, fmt.Errorf("validate File search cursor failed, %w", err)
	}

	// Query one deterministic page with a look-ahead row for the next cursor.
	rows := make([]*fileRow, 0, pageLimit+1)
	request := filterFileScope(l.readDB().WithContext(ctx).Model(ModelFile).Where(expression), scope)
	if hasCursor {
		request = request.Where("name > ? OR (name = ? AND id > ?)", afterName, afterName, afterID)
	}
	result := request.Order("name ASC").Order("id ASC").Limit(pageLimit + 1).Find(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("query Library Files failed, %w", result.Error)
	}
	files := fileViews(rows)
	hasMore := len(files) > pageLimit
	if hasMore {
		files = files[:pageLimit]
	}

	// Attach the compatibility projection and resolve paths for only the returned page.
	if err := hydrateFileViews(l.readDB().WithContext(ctx), files...); err != nil {
		return nil, fmt.Errorf("hydrate Library search results failed, %w", err)
	}
	paths, err := l.resolveFilePaths(ctx, files)
	if err != nil {
		return nil, err
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

	// Bound the indexed Tag scan to the requested prefix and fetch one look-ahead row.
	tags := make([]*Tag, 0, pageLimit+1)
	request := l.readDB().WithContext(ctx).
		Model(ModelFileTag).
		Select("tag AS name, COUNT(*) AS file_count")
	if prefix != "" {
		request = request.Where("tag >= ?", prefix)
		if end := tagPrefixEnd(prefix); end != "" {
			request = request.Where("tag < ?", end)
		}
	}
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

// tagPrefixEnd keeps the range boundary valid UTF-8, including at the surrogate gap.
func tagPrefixEnd(prefix string) string {
	runes := []rune(prefix)
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == utf8.MaxRune {
			continue
		}
		next := runes[i] + 1
		if next >= 0xd800 && next <= 0xdfff {
			next = 0xe000
		}
		return string(append(runes[:i], next))
	}
	return ""
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
	states, err := l.resolveFilePathStates(ctx, files)
	if err != nil {
		return nil, err
	}
	return buildResolvedFilePaths(states), nil
}

func (l *Library) resolveFilePathStates(ctx context.Context, files []*File) (map[int64]*filePathState, error) {
	// Explicit roots and page siblings share ancestry, including ancestors at different depths.
	states := make(map[int64]*filePathState, len(files))
	parents := make(map[int64]*File, len(files))
	for _, file := range files {
		parents[file.ID] = file
		states[file.ID] = &filePathState{
			parentID: file.ParentID,
			parts:    []string{file.Name},
			seen:     map[int64]struct{}{file.ID: {}},
		}
	}

	// Read each missing ancestor once in bounded batches; cycles remain per-path errors.
	for depth := 0; depth < maxFilePathDepth; depth++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var ids []int64
		complete := true
		for _, current := range states {
			if current.parentID != 0 {
				complete = false
				if parents[current.parentID] == nil {
					ids = append(ids, current.parentID)
				}
			}
		}
		if complete {
			return states, nil
		}
		rows, err := l.ReadFileRows(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("resolve File path parents failed, %w", err)
		}
		for id, parent := range rows {
			parents[id] = parent
		}
		for fileID, current := range states {
			if current.parentID == 0 {
				continue
			}
			parent := parents[current.parentID]
			if parent == nil {
				return nil, fmt.Errorf("resolve File %d path failed, parent_id=%d: %w", fileID, current.parentID, ErrFileNotFound)
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
