package readplan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// cacheBytes bounds each decoded working set independently of the selection size.
const cacheBytes = 8 << 20

// statements owns the planner's finite query shapes for one serial connection or transaction.
type statements struct {
	gorm.ConnPool
	prepared map[string]*sql.Stmt
}

func newStatements(pool gorm.ConnPool) *statements {
	return &statements{ConnPool: pool, prepared: make(map[string]*sql.Stmt)}
}

func (s *statements) statement(ctx context.Context, query string) (*sql.Stmt, error) {
	if statement := s.prepared[query]; statement != nil {
		return statement, nil
	}
	statement, err := s.ConnPool.PrepareContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("prepare read planning statement failed, %w", err)
	}
	s.prepared[query] = statement
	return statement, nil
}

func (s *statements) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	statement, err := s.statement(ctx, query)
	if err != nil {
		return nil, err
	}
	return statement.ExecContext(ctx, args...)
}

func (s *statements) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	statement, err := s.statement(ctx, query)
	if err != nil {
		return nil, err
	}
	return statement.QueryContext(ctx, args...)
}

func (s *statements) Close() error {
	var result error
	for query, statement := range s.prepared {
		result = errors.Join(result, statement.Close())
		delete(s.prepared, query)
	}
	return result
}

type item struct {
	span      Span
	canonical State
	bytes     int
}

type edgeCost struct {
	cost  time.Duration
	known bool
}

// cache belongs to one search with one frozen provider and origin. Each map is cleared when its
// byte allowance is reached; immutable extents always remain available in SQLite.
type cache struct {
	statements *statements
	items      map[int64]*item
	itemBytes  int
	probes     map[int64][]int64
	probeBytes int
	runProbes  map[int64][]int64
	runBytes   int
	generation uint64
	edges      map[[2]int64]edgeCost
	probeQuery [4][4]string
}

func newCache(statements *statements) *cache {
	return &cache{statements: statements, items: make(map[int64]*item), probes: make(map[int64][]int64),
		runProbes: make(map[int64][]int64), edges: make(map[[2]int64]edgeCost)}
}

func (c *cache) get(ctx context.Context, rank int64) (_ *item, rerr error) {
	if value := c.items[rank]; value != nil {
		return value, nil
	}
	// Decode only a bounded working set; the complete immutable extents remain in SQLite.
	rows, err := c.statements.QueryContext(ctx, "SELECT copy_id, ranges, canonical FROM read_order WHERE rank = ?", rank)
	if err != nil {
		return nil, err
	}
	defer func() { rerr = errors.Join(rerr, rows.Close()) }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("read execution rank is missing, rank=%d", rank)
	}
	var stored row
	if err := rows.Scan(&stored.CopyID, &stored.Ranges, &stored.Canonical); err != nil {
		return nil, err
	}
	value := &item{span: Span{ID: stored.CopyID}}
	if err := json.Unmarshal(stored.Ranges, &value.span.Ranges); err != nil {
		return nil, fmt.Errorf("decode read ranges failed, rank=%d, %w", rank, err)
	}
	if err := json.Unmarshal(stored.Canonical, &value.canonical); err != nil {
		return nil, fmt.Errorf("decode canonical read state failed, rank=%d, %w", rank, err)
	}
	if stored.CopyID <= 0 || !validRanges(value.span.Ranges) || value.canonical.Position == nil {
		return nil, fmt.Errorf("read planning row is invalid, rank=%d", rank)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The allowance counts encoded rows plus decoded positions and bookkeeping.
	value.bytes = len(stored.Ranges) + len(stored.Canonical) + 128 +
		len(value.span.Ranges)*int(unsafe.Sizeof(Range{})+2*unsafe.Sizeof(entity.TapeBytePosition{}))
	if c.itemBytes+value.bytes > cacheBytes {
		clear(c.items)
		c.itemBytes = 0
	}
	c.items[rank] = value
	c.itemBytes += value.bytes
	return value, nil
}

func (c *cache) putEdge(key [2]int64, value edgeCost) {
	// Reserve 128 bytes per scalar entry, including map overhead, under the same allowance.
	if len(c.edges) >= cacheBytes/128 {
		clear(c.edges)
	}
	c.edges[key] = value
}

func (s *Sequence) firstSpan(ctx context.Context) (_ Span, rerr error) {
	statements := newStatements(s.stage.DB.Statement.ConnPool)
	defer func() { rerr = errors.Join(rerr, statements.Close()) }()
	value, err := newCache(statements).get(ctx, 1)
	if err != nil {
		return Span{}, err
	}
	return value.span, nil
}

// insertionTargets merges a bounded route neighbourhood with spatial entry/exit probes.
func (s *Sequence) insertionTargets(ctx context.Context, r route, first, last int64, c *cache,
	targets []int64,
) ([]int64, error) {
	targets = targets[:0]
	add := func(rank int64) {
		for _, existing := range targets {
			if existing == rank {
				return
			}
		}
		targets = append(targets, rank)
	}
	add(0)
	add(r[0].previous)

	// Route neighbours expose short exchanges that spatially nearer files can crowd out.
	const routeNeighbors = 8
	for direction, start := range [2]int64{last, first} {
		rank := start
		for range routeNeighbors {
			if direction == 0 {
				rank = r[rank].next
			} else {
				rank = r[rank].previous
			}
			if rank == 0 {
				break
			}
			add(rank)
		}
	}

	// Spatial probes retain independent opportunities beyond the current route neighbourhood.
	a, err := c.get(ctx, first)
	if err != nil {
		return nil, err
	}
	b, err := c.get(ctx, last)
	if err != nil {
		return nil, err
	}
	for side, origin := range [2]*entity.TapeBytePosition{b.canonical.Position, a.span.Ranges[0].Start} {
		rank := last
		if side == 1 {
			rank = first
		}
		nearby, err := s.nearby(ctx, rank*2+int64(side), uint8(side), origin, c, c.probes, &c.probeBytes)
		if err != nil {
			return nil, err
		}
		for _, rank := range nearby {
			if rank < 1 || rank >= int64(len(r)) {
				return nil, errors.New("read spatial index contains an invalid rank")
			}
			// An exit neighbour precedes the moved segment's entry; an entry neighbour follows its exit.
			if side == 0 {
				rank = r[rank].previous
			}
			add(rank)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
	return targets, nil
}

// nearby probes one endpoint's spatial groups. Results are cached under the supplied byte allowance.
func (s *Sequence) nearby(ctx context.Context, key int64, side uint8, origin *entity.TapeBytePosition, c *cache,
	probes map[int64][]int64, retained *int,
) ([]int64, error) {
	if values, ok := probes[key]; ok {
		return values, nil
	}
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	index, known := s.spatialIndex(origin)
	if !known {
		return nil, errors.New("read spatial index changed for an indexed endpoint")
	}
	args := make([]any, 0, len(index.Groups)*2)
	for _, group := range index.Groups {
		args = append(args, group, index.Position)
	}
	query := &c.probeQuery[side][len(index.Groups)]
	if *query == "" {
		*query = probeQuery(side, len(index.Groups))
	}
	rows, err := c.statements.QueryContext(ctx, *query, args...)
	if err != nil {
		return nil, err
	}
	var values []int64
	err = func() (rerr error) {
		defer func() { rerr = errors.Join(rerr, rows.Close()) }()
		// One scalar result avoids a driver/cancellation handoff for every individual rank.
		for rows.Next() {
			var encoded sql.NullString
			if err := rows.Scan(&encoded); err != nil {
				return err
			}
			if !encoded.Valid {
				continue
			}
			for part := range strings.SplitSeq(encoded.String, ",") {
				rank, err := strconv.ParseInt(part, 10, 64)
				if err != nil {
					return fmt.Errorf("decode read candidate rank failed, %w", err)
				}
				values = append(values, rank)
			}
		}
		return rows.Err()
	}()
	if err != nil {
		return nil, err
	}

	bytes := cap(values)*int(unsafe.Sizeof(int64(0))) + 64
	if *retained+bytes > cacheBytes {
		clear(probes)
		*retained = 0
	}
	probes[key] = values
	*retained += bytes
	return values, nil
}

// probeQuery returns the nearest three ranks on each side of the position and the lowest rank of
// every group; numbered parameters share each group's pair without correlated re-evaluation.
func probeQuery(side uint8, groups int) string {
	var choices []string
	for group := range groups {
		key, position := group*2+1, group*2+2
		for _, operator := range []string{"<=", ">"} {
			direction := "ASC"
			if operator == "<=" {
				direction = "DESC"
			}
			choices = append(choices, fmt.Sprintf("SELECT rank FROM (SELECT rank FROM read_candidates "+
				"WHERE side = %d AND group_key = ?%d AND position %s ?%d ORDER BY position %s, rank LIMIT 3)",
				side, key, operator, position, direction))
		}
		choices = append(choices, fmt.Sprintf("SELECT rank FROM (SELECT rank FROM read_candidates "+
			"WHERE side = %d AND group_key = ?%d ORDER BY rank LIMIT 1)", side, key))
	}
	return "SELECT group_concat(rank, ',') FROM (SELECT DISTINCT rank FROM (" +
		strings.Join(choices, " UNION ALL ") + ") ORDER BY rank)"
}

func (s *Sequence) commit(ctx context.Context, r route) error {
	// Every bounded update joins one transaction; a failed tail cannot replace the prior complete checkpoint.
	tx := s.stage.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	err := func() (rerr error) {
		statements := newStatements(tx.Statement.ConnPool)
		defer func() { rerr = errors.Join(rerr, statements.Close()) }()
		position := int64(0)
		var ranks [256]int64
		args := make([]any, 0, len(ranks)*3)
		for rank := r[0].next; rank != 0; {
			// Three bindings per member keep each statement below SQLite's historical 999-variable limit.
			if err := s.check(ctx); err != nil {
				return err
			}
			args = args[:0]
			count := 0
			for rank != 0 && count < len(ranks) {
				position++
				ranks[count] = rank
				args = append(args, rank, position)
				count++
				rank = r[rank].next
			}
			for _, rank := range ranks[:count] {
				args = append(args, rank)
			}
			query := "UPDATE read_order SET planned = CASE rank " + strings.Repeat("WHEN ? THEN ? ", count) +
				"END WHERE rank IN (" + strings.Repeat("?,", count-1) + "?)"
			result, err := statements.ExecContext(ctx, query, args...)
			if err != nil {
				return err
			}
			if affected, err := result.RowsAffected(); err != nil {
				return err
			} else if affected != int64(count) {
				return errors.New("read execution rank disappeared before commit")
			}
		}
		return s.check(ctx)
	}()
	if err != nil {
		return errors.Join(err, tx.Rollback().Error)
	}
	return tx.Commit().Error
}
