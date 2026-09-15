package library

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	querystring "github.com/bytedance/go-querystring-parser"
	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DuplicateMember pairs independent organization with its indexed physical original.
type DuplicateMember struct {
	File          *File
	LibraryPath   string
	Original      *FileLocation
	LocationName  string
	MatchesFilter bool
}

type DuplicateMemberPage struct {
	Group         *entity.DuplicateGroup
	Members       []*DuplicateMember
	NextCursor    string
	IndexRevision string
}

type duplicateSummary struct {
	Signature      []byte
	Name           string
	OriginalCount  int64
	LocationCount  int64
	ArchivedCopies int64
	MatchingCount  int64
	MinSize        int64
	MaxSize        int64
}

func (g *duplicateSummary) entity() *entity.DuplicateGroup {
	value := &entity.DuplicateGroup{Signature: g.Signature, Name: g.Name, OriginalCount: g.OriginalCount,
		LocationCount: g.LocationCount, ArchivedCopies: g.ArchivedCopies, MatchingCount: g.MatchingCount}
	if g.MinSize == g.MaxSize {
		size := g.MinSize
		value.Size = &size
	}
	return value
}

// ListDuplicateGroups pages complete content groups rather than grouping a File search page.
func (l *Library) ListDuplicateGroups(
	ctx context.Context, query, cursor string, limit int64,
) (*entity.ListDuplicateGroupsReply, error) {
	// Validate the query-bound signature cursor before starting the consistent metadata read.
	query = strings.TrimSpace(query)
	filter, pageLimit, err := l.duplicateFilter(query, limit)
	if err != nil {
		return nil, err
	}
	key, _, hasCursor, err := decodePageCursor(cursor, duplicateGroupCursorKind, query)
	if err != nil {
		return nil, err
	}
	after, err := hex.DecodeString(key)
	if err != nil || (hasCursor && len(after) == 0) {
		return nil, fmt.Errorf("invalid duplicate group cursor")
	}

	// Group globally, then retain groups with a matching member; hydrate only one summary page.
	page := &entity.ListDuplicateGroupsReply{}
	err = l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		request := duplicateSummaries(tx, filter).Having("COUNT(*) > 1 AND matching_count > 0")
		if hasCursor {
			request = request.Where("file_locations.signature > ?", after)
		}
		var rows []duplicateSummary
		if err := request.Order("file_locations.signature").Limit(pageLimit + 1).Scan(&rows).Error; err != nil {
			return fmt.Errorf("list duplicate groups failed, %w", err)
		}
		more := len(rows) > pageLimit
		if more {
			rows = rows[:pageLimit]
		}
		for _, row := range rows {
			page.Groups = append(page.Groups, row.entity())
		}
		if more {
			page.NextCursor = encodePageCursor(duplicateGroupCursorKind, query,
				hex.EncodeToString(rows[len(rows)-1].Signature), 0)
		}

		// The Location revision fingerprint lets clients discard pages after a Sync or rebind.
		var err error
		page.IndexRevision, err = duplicateIndexRevision(tx)
		return err
	})
	return page, err
}

// ListDuplicateMembers returns all group members, identifying those outside the current filters.
func (l *Library) ListDuplicateMembers(
	ctx context.Context, signature []byte, query, cursor string, limit int64,
) (*DuplicateMemberPage, error) {
	// Membership paging has a separate cursor bound to both the query and exact opaque content.
	if len(signature) == 0 {
		return nil, fmt.Errorf("duplicate signature must not be empty")
	}
	query = strings.TrimSpace(query)
	filter, pageLimit, err := l.duplicateFilter(query, limit)
	if err != nil {
		return nil, err
	}
	input := query + "\x00" + hex.EncodeToString(signature)
	_, after, _, err := decodePageCursor(cursor, duplicateMemberCursorKind, input)
	if err != nil {
		return nil, err
	}
	if after < 0 {
		return nil, fmt.Errorf("invalid duplicate member cursor")
	}

	// Counts and member facts must describe the same read view, including an obsolete group.
	page := &DuplicateMemberPage{}
	err = l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var summaries []duplicateSummary
		if err := duplicateSummaries(tx, filter).Where("file_locations.signature = ?", signature).
			Scan(&summaries).Error; err != nil {
			return fmt.Errorf("read duplicate summary failed, %w", err)
		}
		var err error
		page.IndexRevision, err = duplicateIndexRevision(tx)
		if err != nil {
			return err
		}
		if len(summaries) == 0 {
			return nil
		}
		page.Group = summaries[0].entity()
		if page.Group.OriginalCount < 2 || page.Group.MatchingCount == 0 {
			return nil
		}

		// Resolve only a bounded member page, never all Files sharing a popular signature.
		var originals []*FileLocation
		if err := tx.Where("signature = ? AND file_id > ?", signature, after).
			Order("file_id").Limit(pageLimit + 1).Find(&originals).Error; err != nil {
			return fmt.Errorf("list duplicate members failed, %w", err)
		}
		more := len(originals) > pageLimit
		if more {
			originals = originals[:pageLimit]
		}
		page.Members, err = duplicateMembers(tx, filter, originals)
		if err != nil {
			return err
		}
		if more {
			page.NextCursor = encodePageCursor(duplicateMemberCursorKind, input, "", originals[len(originals)-1].FileID)
		}
		return nil
	})
	return page, err
}

func (l *Library) duplicateFilter(query string, limit int64) (clause.Expression, int, error) {
	// Keep grouped filters identical to ordinary File search, allowing no additional filter.
	pageLimit, err := normalizeFileSearchLimit(limit)
	if err != nil {
		return nil, 0, err
	}
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > maxFileSearchRunes {
		return nil, 0, fmt.Errorf("invalid duplicate search query")
	}
	if query == "" {
		return clause.Expr{SQL: "1 = 1"}, pageLimit, nil
	}
	condition, err := querystring.Parse(query)
	if err != nil {
		return nil, 0, fmt.Errorf("parse duplicate search query failed, %w", err)
	}
	filter, err := compileFileQuery(l.db, condition)
	if err != nil {
		return nil, 0, fmt.Errorf("compile duplicate search query failed, %w", err)
	}
	return filter, pageLimit, nil
}

func duplicateSummaries(tx *gorm.DB, filter clause.Expression) *gorm.DB {
	matching := tx.Model(ModelFile).Select("files.id").Where(filter)
	return tx.Model(&FileLocation{}).Joins("JOIN files ON files.id = file_locations.file_id").
		Where("file_locations.signature IS NOT NULL AND length(file_locations.signature) > 0").
		Select(`file_locations.signature, MIN(files.name) AS name, COUNT(*) AS original_count,
			COUNT(DISTINCT location_id) AS location_count, MIN(file_locations.size) AS min_size,
			MAX(file_locations.size) AS max_size,
			SUM(CASE WHEN files.id IN (?) THEN 1 ELSE 0 END) AS matching_count,
			(SELECT COUNT(*) FROM positions WHERE positions.signature = file_locations.signature
			AND positions.is_dir = ?) AS archived_copies`, matching, false).
		Group("file_locations.signature")
}

func duplicateMembers(tx *gorm.DB, filter clause.Expression, originals []*FileLocation) ([]*DuplicateMember, error) {
	// Collect the page's identities once and batch-load organization, annotations and location labels.
	ids, locationIDs := make([]int64, 0, len(originals)), make([]int64, 0, len(originals))
	for _, original := range originals {
		ids = append(ids, original.FileID)
		locationIDs = append(locationIDs, original.LocationID)
	}
	l := &Library{db: tx}
	ctx := tx.Statement.Context
	files, err := l.mGetFile(ctx, tx, ids...)
	if err != nil {
		return nil, err
	}
	tags, err := l.mGetFileTags(ctx, tx, ids...)
	if err != nil {
		return nil, err
	}
	var locations []struct {
		ID   int64
		Name string
	}
	if err := tx.Model(&Location{}).Select("id, name").Where("id IN ?", uniqueFileIDs(locationIDs)).
		Scan(&locations).Error; err != nil {
		return nil, fmt.Errorf("read duplicate locations failed, %w", err)
	}
	names := make(map[int64]string, len(locations))
	for _, location := range locations {
		names[location.ID] = location.Name
	}
	var matches []int64
	if err := tx.Model(ModelFile).Where("files.id IN ?", ids).Where(filter).Pluck("id", &matches).Error; err != nil {
		return nil, fmt.Errorf("match duplicate members failed, %w", err)
	}
	matched := make(map[int64]struct{}, len(matches))
	for _, id := range matches {
		matched[id] = struct{}{}
	}

	// Reuse the standard bounded path and status projections without probing original files.
	ordered := make([]*File, 0, len(originals))
	for _, original := range originals {
		file := files[original.FileID]
		if file == nil {
			return nil, fmt.Errorf("duplicate File %d is missing", original.FileID)
		}
		file.Tags = tags[file.ID]
		ordered = append(ordered, file)
	}
	paths, err := l.resolveFilePaths(ctx, ordered)
	if err != nil {
		return nil, err
	}
	if err := l.HydrateFileContent(ctx, ordered...); err != nil {
		return nil, err
	}
	result := make([]*DuplicateMember, 0, len(originals))
	for index, original := range originals {
		_, matches := matched[original.FileID]
		result = append(result, &DuplicateMember{File: ordered[index], Original: original,
			LibraryPath: paths[original.FileID], LocationName: names[original.LocationID], MatchesFilter: matches})
	}
	return result, nil
}

func duplicateIndexRevision(tx *gorm.DB) (string, error) {
	// Stream Location revisions, not the potentially huge original index, into a bounded fingerprint.
	rows, err := tx.Model(&Location{}).Select("id, revision").Order("id").Rows()
	if err != nil {
		return "", fmt.Errorf("read duplicate index revision failed, %w", err)
	}
	defer rows.Close()
	hash := sha256.New()
	var data [16]byte
	for rows.Next() {
		var id, revision int64
		if err := rows.Scan(&id, &revision); err != nil {
			return "", fmt.Errorf("read Location revision failed, %w", err)
		}
		binary.BigEndian.PutUint64(data[:8], uint64(id))
		binary.BigEndian.PutUint64(data[8:], uint64(revision))
		hash.Write(data[:])
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("stream Location revisions failed, %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
