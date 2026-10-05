package library

import (
	"fmt"
	"strconv"

	"github.com/samuelncui/yatm/entity"
)

// Indexed row projections retain complete groups while omitting dot-name members from the visible view.
type identicalGroupIndex struct {
	Component    int64  `gorm:"primaryKey;autoIncrement:false;index:idx_identical_group_title,priority:2;index:idx_identical_visible_group_title,priority:2"`
	Name         string `gorm:"index:idx_identical_group_title,priority:1"`
	VisibleName  string `gorm:"index:idx_identical_visible_group_title,priority:1"`
	Count        int64
	VisibleCount int64
	Fingerprint  string
}

type identicalRowIndex struct {
	Projection       int   `gorm:"primaryKey;autoIncrement:false"`
	Position         int64 `gorm:"primaryKey;autoIncrement:false"`
	FileDescPosition int64
	NameAscPosition  int64
	NameDescPosition int64
	SizeAscPosition  int64
	SizeDescPosition int64
	Component        int64 `gorm:"index"`
	FileID           int64 `gorm:"index:idx_identical_file"`
	HeaderPosition   int64
	DisplayCount     int64
}

// IdenticalIndexedRow is one stable row in a retained result.
type IdenticalIndexedRow struct {
	Position       int64
	HeaderPosition int64
	DisplayCount   int64
	Group          IdenticalGroup
	FileID         int64 // Zero identifies the group header.
}

// IdenticalPosition addresses a member in both projections.
type IdenticalPosition struct {
	FileID                int64
	GroupID               string
	AllPosition           int64
	VisiblePosition       *int64
	AllHeaderPosition     int64
	VisibleHeaderPosition *int64
}

// materializeRows indexes complete group metadata and title-ordered dense row positions on disposable disk.
func (s *IdenticalSnapshot) materializeRows() error {
	// Store group summaries by component without retaining their complete membership in memory.
	if err := s.db.AutoMigrate(&identicalGroupIndex{}, &identicalRowIndex{}); err != nil {
		return fmt.Errorf("create identical row indexes failed, %w", err)
	}
	var after int64
	for {
		var groups []identicalGroupIndex
		err := s.db.Model(&identicalNode{}).
			Select("component, MIN(name) AS name, COALESCE(MIN(CASE WHEN substr(name,1,1) != '.' THEN name END), '') AS visible_name, COUNT(*) AS count, SUM(CASE WHEN substr(name,1,1) = '.' THEN 0 ELSE 1 END) AS visible_count").
			Where("component > ?", after).
			Group("component").Having("COUNT(*) > 1").Order("component").Limit(64).Scan(&groups).Error
		if err != nil {
			return fmt.Errorf("read identical groups failed, %w", err)
		}
		if len(groups) == 0 {
			break
		}
		for i := range groups {
			groups[i].Fingerprint, err = s.fingerprint(groups[i].Component)
			if err != nil {
				return err
			}
		}
		if err := s.db.Create(&groups).Error; err != nil {
			return fmt.Errorf("index identical groups failed, %w", err)
		}
		s.GroupCount += int64(len(groups))
		after = groups[len(groups)-1].Component
	}

	// Build each complete projection in one SQLite write to bound transaction cost.
	for _, includeHidden := range []bool{true, false} {
		if err := s.indexProjectionRows(includeHidden); err != nil {
			return err
		}
	}

	// Assign the four metadata orders once, then index all five alternate positions for direct page seeks.
	for _, order := range []struct {
		column string
		by     string
	}{
		{"name_asc_position", "node.name COLLATE NOCASE ASC, node.name COLLATE BINARY ASC, row.file_id ASC"},
		{"name_desc_position", "node.name COLLATE NOCASE DESC, node.name COLLATE BINARY DESC, row.file_id ASC"},
		{"size_asc_position", "node.size IS NULL ASC, node.size ASC, row.file_id ASC"},
		{"size_desc_position", "node.size IS NULL ASC, node.size DESC, row.file_id ASC"},
	} {
		query := `UPDATE identical_row_indices AS target SET ` + order.column + ` = ranked.sorted_position
			FROM (SELECT row.projection, row.position,
				row.header_position + ROW_NUMBER() OVER (
					PARTITION BY row.projection, row.component ORDER BY ` + order.by + `) AS sorted_position
				FROM identical_row_indices AS row
				JOIN identical_nodes AS node ON node.file_id = row.file_id WHERE row.file_id <> 0
			) AS ranked WHERE target.projection = ranked.projection AND target.position = ranked.position`
		if err := s.db.Exec(query).Error; err != nil {
			return fmt.Errorf("rank identical rows by %s failed, %w", order.column, err)
		}
	}
	for _, column := range []string{"file_desc_position", "name_asc_position", "name_desc_position", "size_asc_position", "size_desc_position"} {
		if err := s.db.Exec("CREATE INDEX idx_identical_" + column + " ON identical_row_indices (projection, " + column + ")").Error; err != nil {
			return fmt.Errorf("index identical %s failed, %w", column, err)
		}
	}
	return nil
}

func (s *IdenticalSnapshot) indexProjectionRows(includeHidden bool) error {
	// Only the displayed title, membership filter and count vary between the two retained projections.
	projection, title, count, groups, members := 0, "visible_name", "visible_count", "visible_count > 0",
		"substr(node.name,1,1) <> '.'"
	total := &s.VisibleRows
	if includeHidden {
		projection, title, count, groups, members, total = 1, "name", "count", "1=1", "1=1", &s.AllRows
	}

	// Prefix sums place headers; member ranks fill the following dense range without per-group traversal.
	// Insert in position order so retained pages read adjacent records.
	query := `WITH headers AS (
		SELECT component, ` + count + ` AS display_count,
			COALESCE(SUM(` + count + ` + 1) OVER (ORDER BY ` + title + `, component
				ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0) AS header_position
		FROM identical_group_indices WHERE ` + groups + `
	), members AS (
		SELECT header.component, header.header_position, header.display_count, node.file_id,
			ROW_NUMBER() OVER (PARTITION BY node.component ORDER BY node.file_id) AS member_position
		FROM headers AS header JOIN identical_nodes AS node ON node.component = header.component
		WHERE ` + members + `
	)
	INSERT INTO identical_row_indices (projection, position, file_desc_position,
		name_asc_position, name_desc_position, size_asc_position, size_desc_position,
		component, file_id, header_position, display_count)
	SELECT ?, header_position AS position, header_position, header_position, header_position, header_position, header_position,
		component, 0, header_position, display_count FROM headers
	UNION ALL
	SELECT ?, header_position + member_position, header_position + display_count + 1 - member_position,
		0, 0, 0, 0, component, file_id, header_position, display_count FROM members
	ORDER BY position`
	if err := s.db.Exec(query, projection, projection).Error; err != nil {
		return fmt.Errorf("index identical projection %d failed, %w", projection, err)
	}

	// Report the stored projection count without accumulating state while rows are being written.
	if err := s.db.Model(&identicalRowIndex{}).Where("projection = ?", projection).Count(total).Error; err != nil {
		return fmt.Errorf("count identical projection %d failed, %w", projection, err)
	}
	return nil
}

// Rows reads one bounded random-access page without traversing earlier rows.
func (s *IdenticalSnapshot) Rows(offset int64, limit int, includeHidden bool) ([]IdenticalIndexedRow, int64, error) {
	return s.SortedRows(offset, limit, includeHidden, entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID,
		entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED)
}

// SortedRows reads a bounded page in the requested per-group order.
func (s *IdenticalSnapshot) SortedRows(offset int64, limit int, includeHidden bool, key entity.IdenticalSortKey, order entity.IdenticalSortOrder) ([]IdenticalIndexedRow, int64, error) {
	// Validate the range and select the indexed position column before any read.
	if offset < 0 || limit < 1 || limit > 200 {
		return nil, 0, fmt.Errorf("invalid identical row range")
	}
	column, err := identicalSortColumn(key, order)
	if err != nil {
		return nil, 0, err
	}
	projection, total := 0, s.VisibleRows
	if includeHidden {
		projection, total = 1, s.AllRows
	}

	// Seek directly to the requested position without walking earlier group members.
	var indexed []identicalRowIndex
	if err := s.db.Where("projection = ? AND "+column+" >= ?", projection, offset).
		Order(column).Limit(limit).Find(&indexed).Error; err != nil {
		return nil, 0, err
	}
	if len(indexed) == 0 {
		return nil, total, nil
	}

	// Attach retained group metadata only to rows in this page.
	components := make([]int64, 0, len(indexed))
	for _, row := range indexed {
		components = append(components, row.Component)
	}
	var groups []identicalGroupIndex
	if err := s.db.Where("component IN ?", components).Find(&groups).Error; err != nil {
		return nil, 0, err
	}
	byID := make(map[int64]identicalGroupIndex, len(groups))
	for _, group := range groups {
		byID[group.Component] = group
	}
	result := make([]IdenticalIndexedRow, 0, len(indexed))
	for _, row := range indexed {
		group, ok := byID[row.Component]
		if !ok {
			return nil, 0, fmt.Errorf("identical group index is incomplete")
		}
		name := group.VisibleName
		if includeHidden {
			name = group.Name
		}
		result = append(result, IdenticalIndexedRow{Position: identicalRowPosition(row, column), HeaderPosition: row.HeaderPosition,
			DisplayCount: row.DisplayCount, FileID: row.FileID,
			Group: IdenticalGroup{ID: strconv.FormatInt(group.Component, 10), Name: name, Count: group.Count, Fingerprint: group.Fingerprint}})
	}
	return result, total, nil
}

func identicalSortColumn(key entity.IdenticalSortKey, order entity.IdenticalSortOrder) (string, error) {
	var descending bool
	switch order {
	case entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED:
		descending = key == entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE
	case entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC:
		descending = false
	case entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC:
		descending = true
	default:
		return "", fmt.Errorf("invalid identical sort order")
	}
	switch key {
	case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID:
		if descending {
			return "file_desc_position", nil
		}
		return "position", nil
	case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME:
		if descending {
			return "name_desc_position", nil
		}
		return "name_asc_position", nil
	case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE:
		if descending {
			return "size_desc_position", nil
		}
		return "size_asc_position", nil
	default:
		return "", fmt.Errorf("invalid identical sort key")
	}
}

func identicalRowPosition(row identicalRowIndex, column string) int64 {
	switch column {
	case "name_asc_position":
		return row.NameAscPosition
	case "name_desc_position":
		return row.NameDescPosition
	case "size_asc_position":
		return row.SizeAscPosition
	case "size_desc_position":
		return row.SizeDescPosition
	case "file_desc_position":
		return row.FileDescPosition
	default:
		return row.Position
	}
}

// LookupPositions finds selected members without scanning either projection.
func (s *IdenticalSnapshot) LookupPositions(ids []int64) ([]IdenticalPosition, error) {
	return s.LookupSortedPositions(ids, entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID,
		entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED)
}

// LookupSortedPositions locates members in both projections for one requested order.
func (s *IdenticalSnapshot) LookupSortedPositions(ids []int64, key entity.IdenticalSortKey, order entity.IdenticalSortOrder) ([]IdenticalPosition, error) {
	// Reject unsupported orders and oversized requests before querying the index.
	column, err := identicalSortColumn(key, order)
	if err != nil {
		return nil, err
	}
	if len(ids) > 1000 {
		return nil, fmt.Errorf("too many identical File IDs")
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Fetch only the selected File IDs from the retained row index.
	var rows []identicalRowIndex
	for start := 0; start < len(ids); start += identicalBatch {
		end := start + identicalBatch
		if end > len(ids) {
			end = len(ids)
		}
		var batch []identicalRowIndex
		if err := s.db.Where("file_id IN ?", ids[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("look up identical positions for File IDs %d-%d failed, %w", start, end, err)
		}
		rows = append(rows, batch...)
	}

	// Pair all-member and visible positions without scanning either projection.
	byID := make(map[int64]*IdenticalPosition, len(ids))
	for _, row := range rows {
		position := byID[row.FileID]
		if position == nil {
			position = &IdenticalPosition{FileID: row.FileID, GroupID: strconv.FormatInt(row.Component, 10)}
			byID[row.FileID] = position
		}
		if row.Projection == 1 {
			position.AllPosition, position.AllHeaderPosition = identicalRowPosition(row, column), row.HeaderPosition
		} else {
			visiblePosition, header := identicalRowPosition(row, column), row.HeaderPosition
			position.VisiblePosition, position.VisibleHeaderPosition = &visiblePosition, &header
		}
	}

	// Return each found File once in the caller's requested order.
	result := make([]IdenticalPosition, 0, len(byID))
	seen := make(map[int64]bool, len(byID))
	for _, id := range ids {
		if position := byID[id]; position != nil && !seen[id] {
			result = append(result, *position)
			seen[id] = true
		}
	}
	return result, nil
}
