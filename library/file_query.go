package library

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	querystring "github.com/bytedance/go-querystring-parser"
	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const likeEscape = '!'

func fileQueryName(db *gorm.DB) string {
	if value, ok := db.Get("file_query_location"); ok && value == true {
		return "(SELECT path FROM file_locations WHERE file_locations.file_id = files.id)"
	}
	return "files.name"
}

// Live rows expose observed facts rather than falling back to catalog history.
func queryFactColumn(db *gorm.DB, name string) clause.Column {
	if live, _ := db.Get("file_query_live"); live == true {
		return clause.Column{Table: "files", Name: name}
	}
	return fileFactColumn(name)
}

func compileFileQuery(db *gorm.DB, condition querystring.Condition) (clause.Expression, error) {
	switch value := condition.(type) {
	case *querystring.AndCondition:
		left, err := compileFileQuery(db, value.Left)
		if err != nil {
			return nil, err
		}
		right, err := compileFileQuery(db, value.Right)
		if err != nil {
			return nil, err
		}
		return clause.And(left, right), nil
	case *querystring.OrCondition:
		left, err := compileFileQuery(db, value.Left)
		if err != nil {
			return nil, err
		}
		right, err := compileFileQuery(db, value.Right)
		if err != nil {
			return nil, err
		}
		return clause.Or(left, right), nil
	case *querystring.NotCondition:
		expression, err := compileFileQuery(db, value.Condition)
		if err != nil {
			return nil, err
		}
		return clause.Not(expression), nil
	case *querystring.MatchCondition:
		return compileFileMatch(db, value.Field, value.Value)
	case *querystring.WildcardCondition:
		return compileFileWildcard(db, value.Field, value.Value)
	case *querystring.NumberRangeCondition:
		return compileFileNumberRange(db, value)
	case *querystring.TimeRangeCondition:
		return compileFileTimeRange(db, value)
	case *querystring.RegexpCondition:
		return nil, fmt.Errorf("regular expressions are not supported")
	default:
		return nil, fmt.Errorf("unsupported query condition %T", condition)
	}
}

func compileFileMatch(db *gorm.DB, field, value string) (clause.Expression, error) {
	// Normalize the field and reject empty operands before typed dispatch.
	field = strings.ToLower(strings.TrimSpace(field))
	if value == "" {
		return nil, fmt.Errorf("query value must not be empty")
	}
	// Each typed predicate uses bound values and the authoritative published metadata tables.
	switch field {
	case "":
		return clause.Or(
			containsExpression(fileQueryName(db), value),
			containsExpression("files.note", value),
			fileTagExpression(db, exactTagExpression(value)),
		), nil
	case "name":
		return containsExpression(fileQueryName(db), value), nil
	case "note":
		return containsExpression("files.note", value), nil
	case "tag":
		return fileTagExpression(db, exactTagExpression(value)), nil
	case "type":
		return fileTypeExpression(value)
	case "location":
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("location requires a positive Location ID")
		}
		if live, _ := db.Get("file_query_live"); live == true {
			return clause.Eq{Column: "files.location_id", Value: id}, nil
		}
		query := db.Model(&FileLocation{}).Select("1").Where("file_locations.file_id = files.id AND location_id = ?", id)
		return clause.Expr{SQL: "EXISTS (?)", Vars: []any{query}}, nil
	case "has":
		if live, _ := db.Get("file_query_live"); live == true {
			switch strings.ToLower(value) {
			case "online", "archive", "unknown":
				return clause.Eq{Column: "files.has_" + strings.ToLower(value), Value: true}, nil
			case "duplicates":
				return clause.Expr{SQL: "files.signature IS NOT NULL AND EXISTS (SELECT 1 FROM file_locations duplicate WHERE duplicate.signature = files.signature AND duplicate.file_id <> files.id)"}, nil
			default:
				return nil, fmt.Errorf("has supports online, archive, unknown or duplicates")
			}
		}
		switch strings.ToLower(value) {
		case "online":
			return clause.Expr{SQL: "EXISTS (?)", Vars: []any{db.Model(&FileLocation{}).Select("1").Where("file_locations.file_id = files.id")}}, nil
		case "archive":
			return clause.Expr{SQL: "EXISTS (?)", Vars: []any{db.Model(ModelPosition).Select("1").Where("positions.signature = ? AND positions.is_dir = ?", fileFactColumn("signature"), false)}}, nil
		case "unknown":
			return clause.Expr{SQL: "EXISTS (?)", Vars: []any{db.Model(&FileLocation{}).Select("1").Where("file_locations.file_id = files.id AND signature IS NULL")}}, nil
		case "duplicates":
			other := db.Table("file_locations AS duplicate").Select("1").
				Where("duplicate.signature = file_locations.signature AND duplicate.file_id <> file_locations.file_id")
			original := db.Model(&FileLocation{}).Select("1").
				Where("file_locations.file_id = files.id AND signature IS NOT NULL").Where("EXISTS (?)", other)
			return clause.Expr{SQL: "EXISTS (?)", Vars: []any{original}}, nil
		default:
			return nil, fmt.Errorf("has supports online, archive, unknown or duplicates")
		}
	case "size":
		size, err := parseFileSize(value)
		if err != nil {
			return nil, err
		}
		return clause.Eq{Column: queryFactColumn(db, "size"), Value: size}, nil
	case "mtime":
		modified, err := parseFileTime(value)
		if err != nil {
			return nil, err
		}
		return clause.Eq{Column: queryFactColumn(db, "mtime_ns"), Value: modified.UnixNano()}, nil
	default:
		return nil, fmt.Errorf("unknown query field %q", field)
	}
}

func compileFileWildcard(db *gorm.DB, field, value string) (clause.Expression, error) {
	field = strings.ToLower(strings.TrimSpace(field))
	if value == "" {
		return nil, fmt.Errorf("query wildcard must not be empty")
	}
	switch field {
	case "":
		return clause.Or(
			wildcardExpression(fileQueryName(db), value),
			wildcardExpression("files.note", value),
			fileTagExpression(db, wildcardTagExpression(value)),
		), nil
	case "name":
		return wildcardExpression(fileQueryName(db), value), nil
	case "note":
		return wildcardExpression("files.note", value), nil
	case "tag":
		return fileTagExpression(db, wildcardTagExpression(value)), nil
	case "type", "size", "mtime":
		return nil, fmt.Errorf("query field %q does not support wildcards", field)
	default:
		return nil, fmt.Errorf("unknown query field %q", field)
	}
}

func compileFileNumberRange(db *gorm.DB, value *querystring.NumberRangeCondition) (clause.Expression, error) {
	field := strings.ToLower(strings.TrimSpace(value.Field))
	if field != "size" {
		if exact, ok := exactRangeValue(value.Start, value.End, value.IncludeStart, value.IncludeEnd); ok {
			return compileFileMatch(db, field, exact)
		}
		if field == "" {
			return nil, fmt.Errorf("bare numeric ranges are not supported")
		}
		return nil, fmt.Errorf("query field %q does not support numeric ranges", field)
	}
	return fileSizeRangeExpression(db, value)
}

func compileFileTimeRange(db *gorm.DB, value *querystring.TimeRangeCondition) (clause.Expression, error) {
	field := strings.ToLower(strings.TrimSpace(value.Field))
	if field != "mtime" {
		if field == "" {
			return nil, fmt.Errorf("bare time ranges are not supported")
		}
		return nil, fmt.Errorf("query field %q does not support time ranges", field)
	}

	// Parse optional bounds and build each comparison explicitly.
	column := queryFactColumn(db, "mtime_ns")
	expressions := make([]clause.Expression, 0, 2)
	if value.Start != nil {
		start, err := parseFileTime(*value.Start)
		if err != nil {
			return nil, err
		}
		if value.IncludeStart {
			expressions = append(expressions, clause.Gte{Column: column, Value: start.UnixNano()})
		} else {
			expressions = append(expressions, clause.Gt{Column: column, Value: start.UnixNano()})
		}
	}
	if value.End != nil {
		end, err := parseFileTime(*value.End)
		if err != nil {
			return nil, err
		}
		if value.IncludeEnd {
			expressions = append(expressions, clause.Lte{Column: column, Value: end.UnixNano()})
		} else {
			expressions = append(expressions, clause.Lt{Column: column, Value: end.UnixNano()})
		}
	}
	if len(expressions) == 0 {
		return nil, fmt.Errorf("mtime range requires a bound")
	}
	return clause.And(expressions...), nil
}

func fileSizeRangeExpression(db *gorm.DB, value *querystring.NumberRangeCondition) (clause.Expression, error) {
	// Parse optional bounds and build each comparison explicitly.
	column := queryFactColumn(db, "size")
	expressions := make([]clause.Expression, 0, 2)
	if value.Start != nil {
		start, err := parseFileSize(*value.Start)
		if err != nil {
			return nil, err
		}
		if value.IncludeStart {
			expressions = append(expressions, clause.Gte{Column: column, Value: start})
		} else {
			expressions = append(expressions, clause.Gt{Column: column, Value: start})
		}
	}
	if value.End != nil {
		end, err := parseFileSize(*value.End)
		if err != nil {
			return nil, err
		}
		if value.IncludeEnd {
			expressions = append(expressions, clause.Lte{Column: column, Value: end})
		} else {
			expressions = append(expressions, clause.Lt{Column: column, Value: end})
		}
	}
	if len(expressions) == 0 {
		return nil, fmt.Errorf("size range requires a bound")
	}
	return clause.And(expressions...), nil
}

func containsExpression(column, value string) clause.Expression {
	return clause.Expr{
		SQL:  fmt.Sprintf("LOWER(%s) LIKE ? ESCAPE '%c'", column, likeEscape),
		Vars: []any{"%" + escapeLike(strings.ToLower(value), false) + "%"},
	}
}

func wildcardExpression(column, value string) clause.Expression {
	return clause.Expr{
		SQL:  fmt.Sprintf("LOWER(%s) LIKE ? ESCAPE '%c'", column, likeEscape),
		Vars: []any{escapeLike(strings.ToLower(value), true)},
	}
}

func exactTagExpression(value string) clause.Expression {
	return clause.Eq{Column: clause.Column{Table: "file_tags", Name: "tag"}, Value: strings.ToLower(strings.TrimSpace(value))}
}

func wildcardTagExpression(value string) clause.Expression {
	return clause.Expr{
		SQL:  fmt.Sprintf("file_tags.tag LIKE ? ESCAPE '%c'", likeEscape),
		Vars: []any{escapeLike(strings.ToLower(strings.TrimSpace(value)), true)},
	}
}

func fileTagExpression(db *gorm.DB, condition clause.Expression) clause.Expression {
	subquery := db.Session(&gorm.Session{NewDB: true}).
		Model(ModelFileTag).
		Select("1").
		Where("file_tags.file_id = files.id").
		Where(condition)
	return clause.Expr{SQL: "EXISTS (?)", Vars: []any{subquery}}
}

func fileTypeExpression(value string) (clause.Expression, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "file":
		return clause.Eq{Column: "files.kind", Value: entity.FileKind_FILE_KIND_REGULAR}, nil
	case "dir":
		return clause.Eq{Column: "files.kind", Value: entity.FileKind_FILE_KIND_DIRECTORY}, nil
	default:
		return nil, fmt.Errorf("unexpected File type %q", value)
	}
}

func parseFileSize(value string) (int64, error) {
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse File size %q failed, %w", value, err)
	}
	if size < 0 {
		return 0, fmt.Errorf("File size must not be negative, size=%d", size)
	}
	return size, nil
}

func parseFileTime(value string) (time.Time, error) {
	modified, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse File mtime %q failed, %w", value, err)
	}
	return modified, nil
}

func exactRangeValue(start, end *string, includeStart, includeEnd bool) (string, bool) {
	if start == nil || end == nil {
		return "", false
	}
	if !includeStart || !includeEnd || *start != *end {
		return "", false
	}
	return *start, true
}

func escapeLike(value string, wildcard bool) string {
	var escaped strings.Builder
	escaped.Grow(len(value))
	for _, current := range value {
		switch current {
		case likeEscape:
			escaped.WriteRune(likeEscape)
			escaped.WriteRune(likeEscape)
		case '%', '_':
			escaped.WriteRune(likeEscape)
			escaped.WriteRune(current)
		case '*':
			if wildcard {
				escaped.WriteByte('%')
				continue
			}
			escaped.WriteRune(current)
		case '?':
			if wildcard {
				escaped.WriteByte('_')
				continue
			}
			escaped.WriteRune(current)
		default:
			escaped.WriteRune(current)
		}
	}
	return escaped.String()
}

// Only code-owned column names reach this expression. A current unsigned original never falls back to history.
func fileFactColumn(name string) clause.Column {
	expression := fmt.Sprintf("(CASE WHEN EXISTS (SELECT 1 FROM file_locations WHERE file_id = files.id) THEN (SELECT %s FROM file_locations WHERE file_id = files.id) ELSE (SELECT %s FROM file_versions WHERE file_id = files.id ORDER BY last_archived_at DESC, id DESC LIMIT 1) END)", name, name)
	switch name {
	case "size":
		expression = "(CASE WHEN files.kind = 1 THEN 0 ELSE " + expression + " END)"
	case "mtime_ns":
		expression = "(CASE WHEN files.kind = 1 THEN files.updated_at * 1000000 ELSE " + expression + " END)"
	}
	return clause.Column{Raw: true, Name: expression}
}
