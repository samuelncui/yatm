package executor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

const (
	// DefaultJobResultPageSize is the page size used when a listing request omits one.
	DefaultJobResultPageSize = 100
	// MaxJobResultPageSize bounds every Job manifest result page.
	MaxJobResultPageSize = 1000
)

// JobResultPage is one validated page request shared by every Job manifest
// listing. Cursor is the transparent order key of a boundary row; Offset
// addresses a position in the filtered sequence and only anchors a jump.
type JobResultPage struct {
	Limit        int
	Cursor       string
	Order        entity.JobResultOrder
	Offset       int64
	IncludeTotal bool
}

// NewJobResultPage validates one bounded page request. All listings share these
// bounds so a page request means the same thing everywhere.
func NewJobResultPage(limit int32, cursor string, order entity.JobResultOrder, offset *int64, includeTotal bool) (JobResultPage, error) {
	if order == entity.JobResultOrder_JOB_RESULT_ORDER_UNSPECIFIED {
		order = entity.JobResultOrder_JOB_RESULT_ORDER_ASCENDING
	}
	page := JobResultPage{Cursor: cursor, Order: order, IncludeTotal: includeTotal}
	if limit == 0 {
		page.Limit = DefaultJobResultPageSize
	} else if limit < 0 || int(limit) > MaxJobResultPageSize {
		return JobResultPage{}, fmt.Errorf("Job result page limit must be between 1 and %d, limit=%d", MaxJobResultPageSize, limit)
	} else {
		page.Limit = int(limit)
	}
	if offset != nil {
		if *offset < 0 {
			return JobResultPage{}, fmt.Errorf("Job result page offset must not be negative, offset=%d", *offset)
		}
		page.Offset = *offset
	}
	if _, valid := entity.JobResultOrder_name[int32(order)]; !valid {
		return JobResultPage{}, fmt.Errorf("invalid Job result order, order=%d", order)
	}
	return page, nil
}

// Descending reports whether the page reads its order key backwards.
func (p JobResultPage) Descending() bool {
	return p.Order == entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING
}

// Direction is the SQL ordering keyword for the requested direction.
func (p JobResultPage) Direction() string {
	if p.Descending() {
		return "DESC"
	}
	return "ASC"
}

// Comparison is the SQL operator restricting an order key to rows beyond the cursor.
func (p JobResultPage) Comparison() string {
	if p.Descending() {
		return "<"
	}
	return ">"
}

// SentinelLimit reads one row beyond the page to report continuation.
func (p JobResultPage) SentinelLimit() int {
	return p.Limit + 1
}

// JobResultCursorID parses a numeric order key. An empty cursor means the caller
// starts at the beginning of the sequence.
func JobResultCursorID(cursor string) (int64, bool, error) {
	if cursor == "" {
		return 0, false, nil
	}
	value, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || value <= 0 {
		return 0, false, fmt.Errorf("Job result cursor %q is not a valid row key", cursor)
	}
	return value, true, nil
}

// JobResultCursorPair parses a composite "first:second" order key.
func JobResultCursorPair(cursor string) (int64, int64, bool, error) {
	if cursor == "" {
		return 0, 0, false, nil
	}
	first, second, found := strings.Cut(cursor, ":")
	if !found {
		return 0, 0, false, fmt.Errorf("Job result cursor %q is not a composite row key", cursor)
	}
	firstValue, err := strconv.ParseInt(first, 10, 64)
	if err != nil || firstValue <= 0 {
		return 0, 0, false, fmt.Errorf("Job result cursor %q is not a valid row key", cursor)
	}
	secondValue, err := strconv.ParseInt(second, 10, 64)
	if err != nil || secondValue <= 0 {
		return 0, 0, false, fmt.Errorf("Job result cursor %q is not a valid row key", cursor)
	}
	return firstValue, secondValue, true, nil
}

// JobResultPairValue renders a composite order key for a cursor.
func JobResultPairValue(first, second int64) string {
	return fmt.Sprintf("%d:%d", first, second)
}
