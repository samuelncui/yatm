package library

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	querystring "github.com/bytedance/go-querystring-parser"
	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
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

// FilesQueryDependencies separates physical predicates from optional catalog observations.
func FilesQueryDependencies(query string) (catalog, content bool, err error) {
	condition, err := parseFilesQuery(query)
	if err != nil {
		return false, false, err
	}
	catalog, content = queryDependencies(condition)
	return
}

func queryDependencies(condition querystring.Condition) (catalog, content bool) {
	// Location attributes are always observed; only annotations and content need extra Catalog facts.
	var visit func(querystring.Condition)
	field := func(name string) {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "", "note", "tag":
			catalog = true
		case "has":
			catalog, content = true, true
		}
	}
	visit = func(value querystring.Condition) {
		switch value := value.(type) {
		case *querystring.AndCondition:
			visit(value.Left)
			visit(value.Right)
		case *querystring.OrCondition:
			visit(value.Left)
			visit(value.Right)
		case *querystring.NotCondition:
			visit(value.Condition)
		case *querystring.MatchCondition:
			field(value.Field)
		case *querystring.WildcardCondition:
			field(value.Field)
		case *querystring.NumberRangeCondition:
			field(value.Field)
		case *querystring.TimeRangeCondition:
			field(value.Field)
		}
	}
	visit(condition)
	return catalog, content
}

// FilesQuery is a request-owned compilation shared by candidate reads and observed matching.
// It retains no filesystem or catalog result and does not extend a continuation's lifetime.
type FilesQuery struct {
	Text             string
	Catalog, Content bool
	catalog, live    clause.Expression
}

func parseFilesQuery(query string) (querystring.Condition, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > maxFileSearchRunes {
		return nil, fmt.Errorf("query must be valid UTF-8 and at most %d characters", maxFileSearchRunes)
	}
	return querystring.Parse(query)
}

// CompileFilesQuery validates once, including for an empty source, and freezes predicate dependencies.
func (l *Library) CompileFilesQuery(query string) (*FilesQuery, error) {
	// Freeze Location projection dependencies from the same parsed expression used by both sources.
	condition, err := parseFilesQuery(query)
	if err != nil {
		return nil, err
	}
	result := &FilesQuery{Text: strings.TrimSpace(query)}
	result.Catalog, result.Content = queryDependencies(condition)
	if condition == nil {
		return result, nil
	}

	// Clone each predicate's subquery from the configured statement so Boolean branches cannot alias it.
	result.live, err = compileFileQuery(l.readDB().Set("file_query_live", true).Session(&gorm.Session{}), condition)
	if err != nil {
		return nil, err
	}
	result.catalog, err = compileFileQuery(l.readDB().Set("file_query_live", false).Session(&gorm.Session{}), condition)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ValidateFilesQuery shares the predicate compiler with every Files read.
func (l *Library) ValidateFilesQuery(query string) error {
	_, err := l.CompileFilesQuery(query)
	return err
}

func (l *Library) filesQuery(query string, live bool) (clause.Expression, error) {
	compiled, err := l.CompileFilesQuery(query)
	if err != nil {
		return nil, err
	}
	if live {
		return compiled.live, nil
	}
	return compiled.catalog, nil
}

// MatchLiveFiles evaluates the ordinary query compiler over bound observations, not a cached tree.
// It returns matching input indexes without persisting the rows or reading the filesystem.
func (l *Library) MatchLiveFiles(ctx context.Context, query string, rows []LiveQueryRow) ([]int, error) {
	compiled, err := l.CompileFilesQuery(query)
	if err != nil {
		return nil, err
	}
	return l.MatchLiveQuery(ctx, compiled, rows)
}

// MatchLiveQuery evaluates one compiled predicate over a bounded batch of observations.
func (l *Library) MatchLiveQuery(ctx context.Context, query *FilesQuery, rows []LiveQueryRow) ([]int, error) {
	// An empty predicate accepts the observed batch without an extra Catalog read.
	expression := query.live
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
			selects = append(selects, "SELECT ? AS row_index, ? AS id, ? AS location_id, ? AS name, ? AS note, ? AS kind, ? AS size, ? AS mtime_ns, ? AS signature, ? AS has_original, ? AS has_archive, ? AS has_unknown")
			kind := row.Kind
			if kind == entity.EntryKind_ENTRY_KIND_UNSPECIFIED {
				kind = entity.EntryKind_ENTRY_KIND_FILE
			}
			signature := row.Signature
			if len(signature) == 0 {
				signature = nil
			}
			var size any = row.Size
			if kind != entity.EntryKind_ENTRY_KIND_FILE {
				size = nil
			}
			values = append(values, index, row.FileID, row.LocationID, row.Name, row.Note, kind, size, row.MtimeNS,
				signature, kind == entity.EntryKind_ENTRY_KIND_FILE, row.HasArchive,
				kind == entity.EntryKind_ENTRY_KIND_FILE && len(signature) == 0)
		}
		var indexes []int
		projection := l.readDB().Raw(strings.Join(selects, " UNION ALL "), values...)
		if err := l.readDB().WithContext(ctx).Table("(?) AS files", projection).Select("files.row_index").Where(expression).
			Order("files.row_index").Scan(&indexes).Error; err != nil {
			return nil, err
		}
		matched = append(matched, indexes...)
	}
	return matched, nil
}
