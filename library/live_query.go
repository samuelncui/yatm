package library

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	querystring "github.com/bytedance/go-querystring-parser"
	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm/clause"
)

// LiveQueryRow is a bounded, read-only projection of an observed directory entry.
// An absent catalog ID is zero; it never creates a synthetic Library File.
type LiveQueryRow struct {
	FileID, LocationID int64
	Name, Note         string
	Kind               entity.EntryKind
	Size, MtimeNS      int64
	Signature          []byte
	HasArchive         bool
}

// ValidateFilesQuery shares the Library grammar and predicate compiler with live browsing.
func (l *Library) ValidateFilesQuery(query string) error {
	_, err := l.filesQuery(query, true)
	return err
}

func (l *Library) filesQuery(query string, live bool) (clause.Expression, error) {
	// Parse once before reading a page; invalid expressions also fail on empty directories.
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > maxFileSearchRunes {
		return nil, fmt.Errorf("query must be valid UTF-8 and at most %d characters", maxFileSearchRunes)
	}
	condition, err := querystring.Parse(query)
	if err != nil {
		return nil, err
	}
	return compileFileQuery(l.db.Set("file_query_live", live), condition)
}

// MatchLiveFiles evaluates the ordinary query compiler over bound observations, not a cached tree.
// It returns matching input indexes without persisting the rows or reading the filesystem.
func (l *Library) MatchLiveFiles(ctx context.Context, query string, rows []LiveQueryRow) ([]int, error) {
	expression, err := l.filesQuery(query, true)
	if err != nil {
		return nil, err
	}
	matched := make([]int, 0, len(rows))
	if expression == nil {
		for index := range rows {
			matched = append(matched, index)
		}
		return matched, nil
	}

	// A small derived table keeps SQLite parameter counts bounded on either driver.
	const pageSize = 64
	for start := 0; start < len(rows); start += pageSize {
		end := start + pageSize
		if end > len(rows) {
			end = len(rows)
		}
		selects := make([]string, 0, end-start)
		values := make([]any, 0, (end-start)*12)
		for index := start; index < end; index++ {
			row := rows[index]
			selects = append(selects, "SELECT ? AS row_index, ? AS id, ? AS location_id, ? AS name, ? AS note, ? AS kind, ? AS size, ? AS mtime_ns, ? AS signature, ? AS has_online, ? AS has_archive, ? AS has_unknown")
			signature := row.Signature
			if len(signature) == 0 {
				signature = nil
			}
			values = append(values, index, row.FileID, row.LocationID, row.Name, row.Note, row.Kind, row.Size, row.MtimeNS,
				signature, row.Kind == entity.EntryKind_ENTRY_FILE, row.HasArchive, row.Kind == entity.EntryKind_ENTRY_FILE && len(signature) == 0)
		}
		var indexes []int
		projection := l.db.Raw(strings.Join(selects, " UNION ALL "), values...)
		if err := l.db.WithContext(ctx).Table("(?) AS files", projection).Select("files.row_index").Where(expression).
			Order("files.row_index").Scan(&indexes).Error; err != nil {
			return nil, err
		}
		matched = append(matched, indexes...)
	}
	return matched, nil
}
