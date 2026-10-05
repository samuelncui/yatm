package library

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type identicalPerfMember struct {
	id   int64
	name string
	size int64
}

type identicalPerfGroup struct {
	id      string
	members []identicalPerfMember
}

type identicalPerfRow struct {
	id, header, count, display int64
	group, title               string
}

// TestIdenticalLargeCatalogPerformance is opt-in so normal unit tests do not build a 100k catalog.
// Run with YATM_IDENTICAL_PERF=1 go test ./internal/library -run '^TestIdenticalLargeCatalogPerformance$' -count=1 -v -timeout 30m.
// Find includes staging, connection, fingerprints and every row index. Pages are warm retained reads;
// Go allocations exclude fixture/oracle construction and do not measure native SQLite memory or peak RSS.
func TestIdenticalLargeCatalogPerformance(t *testing.T) {
	if os.Getenv("YATM_IDENTICAL_PERF") != "1" {
		t.Skip("set YATM_IDENTICAL_PERF=1 to measure the isolated 10k/100k catalogs")
	}
	for _, count := range []int{10_000, 100_000} {
		t.Run(fmt.Sprintf("files-%d", count), func(t *testing.T) {
			// Release fixture-building allocations before measuring the complete explicit Find.
			lib, groups := newIdenticalPerfCatalog(t, count)
			runtime.GC()
			var before, after, retained runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), IdenticalScope{Source: IdenticalLibrary})
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, snapshot.Close()) })
			runtime.GC()
			runtime.ReadMemStats(&retained)
			require.Equal(t, int64(len(groups)), snapshot.GroupCount)
			var members int64
			for _, group := range groups {
				members += int64(len(group.members))
			}
			require.Equal(t, members+int64(len(groups)), snapshot.AllRows)
			t.Logf("Find files=%d pairs=%d popular=%d chain=129 groups=%d allRows=%d visibleRows=%d "+
				"duration=%s ns/op=%d B/op=%d allocs/op=%d retainedGoHeapDelta=%d tempDBbytes=%d",
				count, count*4/10, count/10, snapshot.GroupCount, snapshot.AllRows, snapshot.VisibleRows,
				elapsed, elapsed.Nanoseconds(), after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs,
				int64(retained.HeapAlloc)-int64(before.HeapAlloc), identicalPerfDBBytes(t, snapshot.directory))
			var nodes, edges, indexedGroups, indexedRows int64
			require.NoError(t, snapshot.db.Model(&identicalNode{}).Count(&nodes).Error)
			require.NoError(t, snapshot.db.Model(&identicalEdge{}).Count(&edges).Error)
			require.NoError(t, snapshot.db.Model(&identicalGroupIndex{}).Count(&indexedGroups).Error)
			require.NoError(t, snapshot.db.Model(&identicalRowIndex{}).Count(&indexedRows).Error)
			require.Equal(t, int64(count), nodes)
			require.Equal(t, snapshot.GroupCount, indexedGroups)
			require.Equal(t, snapshot.AllRows+snapshot.VisibleRows, indexedRows)
			t.Logf("SnapshotDB nodes=%d edges=%d groups=%d rows=%d", nodes, edges, indexedGroups, indexedRows)

			// Observe every catalog read after Find, including the timed retained reads.
			catalogTrace := &fileReadSQL{Interface: logger.Discard}
			lib.db.Logger = catalogTrace
			for _, order := range []struct {
				name string
				key  entity.IdenticalSortKey
			}{
				{"FileID", entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID},
				{"Name", entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME},
				{"Size", entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE},
			} {
				for _, direction := range []entity.IdenticalSortOrder{
					entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC,
					entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC,
				} {
					// Derive both dense projections from fixture facts, independently of SQLite ranking.
					all := identicalPerfOracle(groups, true, order.key, direction)
					visible := identicalPerfOracle(groups, false, order.key, direction)
					require.Equal(t, int64(len(visible)), snapshot.VisibleRows)
					name := order.name + "/" + strings.TrimPrefix(direction.String(), "IDENTICAL_SORT_ORDER_")
					for _, includeHidden := range []bool{true, false} {
						want := all
						if !includeHidden {
							want = visible
						}
						// Shuffle complete 200-row windows, then add arbitrary offsets and range boundaries.
						offsets := make([]int64, 0, len(want)/200+36)
						for offset := 0; offset < len(want); offset += 200 {
							offsets = append(offsets, int64(offset))
						}
						random := rand.New(rand.NewSource(42))
						for range 32 {
							offsets = append(offsets, int64(random.Intn(len(want))))
						}
						offsets = append(offsets, int64(len(want)-1), int64(len(want)), int64(len(want)+1))
						random.Shuffle(len(offsets), func(i, j int) { offsets[i], offsets[j] = offsets[j], offsets[i] })
						checkIdenticalPerfPages(t, snapshot, want, offsets, includeHidden, order.key, direction)
						measureIdenticalPerfPages(t, snapshot, len(want), offsets, includeHidden, order.key, direction, name)
					}
					measureIdenticalPerfPositions(t, snapshot, count, all, visible, order.key, direction, name)
				}
			}
			require.Empty(t, catalogTrace.queries, "retained reads must never recollect the catalog")
		})
	}
}

func newIdenticalPerfCatalog(t *testing.T, count int) (*Library, []identicalPerfGroup) {
	t.Helper()
	// Use normal catalog models and hooks, with no query-only synthetic staging tables.
	root := t.TempDir()
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.sqlite"))
	require.NoError(t, err)
	db.Logger = logger.Discard
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	lib := New(db)
	require.NoError(t, lib.AutoMigrate())
	release, err := lib.PrepareIdenticalTemp(filepath.Join(root, "work"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, release()) })
	locations := make([]Location, 4)
	for i := range locations {
		locations[i] = Location{ID: int64(i + 1), Name: fmt.Sprintf("Location %d", i), ExecutorID: "fixture",
			RootPath: filepath.Join(root, fmt.Sprintf("location-%d", i)), Config: &entity.LocationConfig{}}
	}
	require.NoError(t, db.Create(&locations).Error)

	// Eighty percent form pairs, ten percent share popular content, and one bounded chain is transitive.
	const chain = 129
	pairs, popular := count*4/10, count/10
	groups := make([]identicalPerfGroup, pairs+2)
	files := make([]fileRow, 0, count+32)
	originals := make([]FileLocation, 0, count)
	versions := make([]FileVersion, 0, count+chain)
	for i := range 32 {
		files = append(files, fileRow{ID: int64(count + i + 1), Name: fmt.Sprintf("folder-%02d", i),
			Kind: entity.FileKind_FILE_KIND_DIRECTORY})
	}
	for i := range count {
		id := int64(i + 1)
		name := fmt.Sprintf("Photo-%06d.JPG", (i*7919)%count)
		if i%3 == 0 {
			name = strings.ToLower(name)
		}
		if i%17 == 0 || (i < pairs*2 && i/2%101 == 0) {
			name = "." + name
		}
		files = append(files, fileRow{ID: id, ParentID: int64(count + i%32 + 1), Name: name,
			Kind: entity.FileKind_FILE_KIND_REGULAR})
		current := ""
		unsigned := false
		var saved []string
		group := -1
		switch {
		case i < pairs*2:
			group = i / 2
			shared := fmt.Sprintf("pair-%d", group)
			switch group % 3 {
			case 0:
				current, saved = shared, []string{fmt.Sprintf("older-%d", i)}
			case 1:
				saved = []string{shared}
				if i%2 == 0 {
					current = fmt.Sprintf("changed-%d", i)
				}
			case 2:
				if i%2 == 0 {
					current, saved = shared, []string{fmt.Sprintf("older-%d", i)}
				} else {
					saved, current, unsigned = []string{shared}, fmt.Sprintf("unsigned-%d", i), true
				}
			}
		case i < pairs*2+popular:
			group, saved = pairs, []string{"popular"}
			switch i % 4 {
			case 0:
				current = "popular"
			case 1:
				current = fmt.Sprintf("changed-%d", i)
			case 2:
				current, unsigned = fmt.Sprintf("unsigned-%d", i), true
			}
		case i < pairs*2+popular+chain:
			group = pairs + 1
			index := i - pairs*2 - popular
			if index > 0 {
				saved = append(saved, fmt.Sprintf("chain-edge-%d", index-1))
			}
			if index+1 < chain {
				saved = append(saved, fmt.Sprintf("chain-edge-%d", index))
			}
			if i%4 == 0 {
				current = fmt.Sprintf("changed-%d", i)
			}
		default:
			switch i % 4 {
			case 0:
				current = fmt.Sprintf("unique-%d", i)
			case 1:
				saved = []string{fmt.Sprintf("unique-%d", i)}
			case 2:
				current, unsigned = fmt.Sprintf("unsigned-%d", i), true
			}
		}

		// Equal signatures carry equal content facts; originals override the latest saved size.
		var size int64
		for index, label := range saved {
			hash, signature, bytes := identicalPerfContent(t, label)
			archived := int64(1_700_000_000_000+index) * int64(time.Millisecond)
			versions = append(versions, FileVersion{FileID: id, Hash: hash, Signature: signature, Size: bytes,
				Mode: 0o644, MtimeNS: 1_700_000_000_000_000_000, FirstArchivedAtNS: &archived, LastArchivedAtNS: &archived})
			size = bytes
		}
		if current != "" {
			hash, signature, bytes := identicalPerfContent(t, current)
			if unsigned {
				hash, signature = nil, nil
			}
			originals = append(originals, FileLocation{FileID: id, LocationID: int64(i%4 + 1),
				Path: fmt.Sprintf("photos/%02d/%s", i%32, name), Hash: hash, Signature: signature,
				Size: bytes, Mode: 0o644, MtimeNS: 1_700_000_000_000_000_000})
			size = bytes
		}
		if group >= 0 {
			if groups[group].id == "" {
				groups[group].id = strconv.FormatInt(id, 10)
			}
			groups[group].members = append(groups[group].members, identicalPerfMember{id: id, name: name, size: size})
		}
	}

	// Batch legitimate fixture publication in one transaction, outside all query measurements.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.CreateInBatches(&files, identicalBatch).Error; err != nil {
			return err
		}
		if err := tx.CreateInBatches(&originals, identicalBatch).Error; err != nil {
			return err
		}
		return tx.CreateInBatches(&versions, identicalBatch).Error
	}))
	t.Logf("Fixture regularFiles=%d directories=32 originals=%d savedVersions=%d uniqueFiles=%d catalogDBbytes=%d",
		count, len(originals), len(versions), count-pairs*2-popular-chain, identicalPerfDBBytes(t, root))
	return lib, groups
}

func identicalPerfContent(t *testing.T, label string) ([]byte, []byte, int64) {
	t.Helper()
	hash := sha256.Sum256([]byte(label))
	size := int64(binary.BigEndian.Uint32(hash[:4])%65)*1024*1024 + int64(hash[4])*4096
	signature, err := NewFileSignature(hash[:], size)
	require.NoError(t, err)
	return hash[:], signature, size
}

func identicalPerfOracle(groups []identicalPerfGroup, includeHidden bool,
	key entity.IdenticalSortKey, order entity.IdenticalSortOrder) []identicalPerfRow {
	// Select and title each shown group before sorting its members by independent fixture facts.
	type shownGroup struct {
		identicalPerfGroup
		title string
	}
	shown := make([]shownGroup, 0, len(groups))
	for _, group := range groups {
		members := make([]identicalPerfMember, 0, len(group.members))
		for _, member := range group.members {
			if includeHidden || !strings.HasPrefix(member.name, ".") {
				members = append(members, member)
			}
		}
		if len(members) == 0 {
			continue
		}
		title := members[0].name
		for _, member := range members[1:] {
			if member.name < title {
				title = member.name
			}
		}
		sort.Slice(members, func(i, j int) bool {
			left, right := members[i], members[j]
			desc := order == entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC
			switch key {
			case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID:
				return (left.id < right.id) != desc
			case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME:
				if a, b := strings.ToLower(left.name), strings.ToLower(right.name); a != b {
					return (a < b) != desc
				}
				if left.name != right.name {
					return (left.name < right.name) != desc
				}
			case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE:
				if left.size != right.size {
					return (left.size < right.size) != desc
				}
			}
			return left.id < right.id
		})
		shown = append(shown, shownGroup{identicalPerfGroup{group.id, members}, title})
	}
	sort.Slice(shown, func(i, j int) bool { return shown[i].title < shown[j].title })

	// Retain original group counts when a dot filter leaves only one visible member.
	counts := make(map[string]int64, len(groups))
	for _, group := range groups {
		counts[group.id] = int64(len(group.members))
	}
	var rows []identicalPerfRow
	for _, group := range shown {
		header := int64(len(rows))
		row := identicalPerfRow{header: header, count: counts[group.id], display: int64(len(group.members)),
			group: group.id, title: group.title}
		rows = append(rows, row)
		for _, member := range group.members {
			row.id = member.id
			rows = append(rows, row)
		}
	}
	return rows
}

func checkIdenticalPerfPages(t *testing.T, snapshot *IdenticalSnapshot, want []identicalPerfRow, offsets []int64,
	hidden bool, key entity.IdenticalSortKey, order entity.IdenticalSortOrder) {
	t.Helper()
	// Check all positions with the existing query logger, outside the uninstrumented timings.
	trace := &fileReadSQL{Interface: logger.Discard}
	snapshot.db.Logger = trace
	defer func() { snapshot.db.Logger = logger.Discard }()
	for _, offset := range offsets {
		rows, total, err := snapshot.SortedRows(offset, 200, hidden, key, order)
		require.NoError(t, err)
		require.Equal(t, int64(len(want)), total)
		require.Len(t, rows, max(0, min(200, len(want)-int(offset))))
		for i, row := range rows {
			expected := want[int(offset)+i]
			if row.Position != offset+int64(i) || row.FileID != expected.id || row.HeaderPosition != expected.header ||
				row.Group.ID != expected.group || row.Group.Name != expected.title || row.Group.Count != expected.count ||
				row.DisplayCount != expected.display || len(row.Group.Fingerprint) != 64 {
				t.Fatalf("row %d: got %+v, want %+v", offset+int64(i), row, expected)
			}
		}
		for _, query := range trace.queries {
			require.True(t, strings.HasPrefix(query, "SELECT"), "retained pages must not write or reconnect: %s", query)
			require.True(t, strings.Contains(query, "identical_row_indices") ||
				strings.Contains(query, "identical_group_indices"),
				"retained pages must use only materialized rows and groups: %s", query)
		}
		trace.queries = trace.queries[:0]
	}
}

func measureIdenticalPerfPages(t *testing.T, snapshot *IdenticalSnapshot, total int, offsets []int64,
	hidden bool, key entity.IdenticalSortKey, order entity.IdenticalSortOrder, name string) {
	t.Helper()
	// The identical shuffled windows measure seeks throughout the result, including the last row.
	samples := make([]time.Duration, len(offsets))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var elapsed time.Duration
	for i, offset := range offsets {
		start := time.Now()
		rows, got, err := snapshot.SortedRows(offset, 200, hidden, key, order)
		samples[i] = time.Since(start)
		elapsed += samples[i]
		if err != nil || got != int64(total) || len(rows) != max(0, min(200, total-int(offset))) {
			t.Fatalf("timed page %d: rows=%d total=%d error=%v", offset, len(rows), got, err)
		}
	}
	runtime.ReadMemStats(&after)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("Rows hideDots=%t order=%s pages=%d limit=200 p50=%s p95=%s ns/op=%d B/op=%d allocs/op=%d",
		!hidden, name, len(samples), samples[(len(samples)-1)/2], samples[(len(samples)*95-1)/100],
		elapsed.Nanoseconds()/int64(len(samples)), (after.TotalAlloc-before.TotalAlloc)/uint64(len(samples)),
		(after.Mallocs-before.Mallocs)/uint64(len(samples)))
}

func measureIdenticalPerfPositions(t *testing.T, snapshot *IdenticalSnapshot, count int,
	all, visible []identicalPerfRow, key entity.IdenticalSortKey, order entity.IdenticalSortOrder, name string) {
	t.Helper()
	// Spread the 200 requested File IDs over pairs, the popular group, both chain endpoints and singletons.
	ids := make([]int64, 200)
	for i := range ids {
		ids[i] = 1 + int64(i*(count-1)/(len(ids)-1))
	}
	ids[1], ids[2], ids[3] = int64(count*9/10+1), int64(count*9/10+129), int64(count*8/10+1)
	allPositions, visiblePositions := make(map[int64]int64, len(all)), make(map[int64]int64, len(visible))
	for i, row := range all {
		if row.id != 0 {
			allPositions[row.id] = int64(i)
		}
	}
	for i, row := range visible {
		if row.id != 0 {
			visiblePositions[row.id] = int64(i)
		}
	}
	trace := &fileReadSQL{Interface: logger.Discard}
	snapshot.db.Logger = trace
	positions, err := snapshot.LookupSortedPositions(ids, key, order)
	require.NoError(t, err)
	var index int
	for _, id := range ids {
		position, exists := allPositions[id]
		if !exists {
			continue
		}
		require.Less(t, index, len(positions))
		got := positions[index]
		require.Equal(t, id, got.FileID)
		require.Equal(t, position, got.AllPosition)
		require.Equal(t, all[position].header, got.AllHeaderPosition)
		require.Equal(t, all[position].group, got.GroupID)
		if position, exists := visiblePositions[id]; exists {
			require.NotNil(t, got.VisiblePosition)
			require.Equal(t, position, *got.VisiblePosition)
			require.NotNil(t, got.VisibleHeaderPosition)
			require.Equal(t, visible[position].header, *got.VisibleHeaderPosition)
		} else {
			require.Nil(t, got.VisiblePosition)
			require.Nil(t, got.VisibleHeaderPosition)
		}
		index++
	}
	require.Len(t, positions, index)
	for _, query := range trace.queries {
		require.True(t, strings.HasPrefix(query, "SELECT") && strings.Contains(query, "identical_row_indices"),
			"position lookups must not reconnect: %s", query)
	}
	snapshot.db.Logger = logger.Discard

	// Repeated retained lookups measure allocation and latency without query-log rendering.
	samples := make([]time.Duration, 128)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var elapsed time.Duration
	for i := range samples {
		start := time.Now()
		got, err := snapshot.LookupSortedPositions(ids, key, order)
		samples[i] = time.Since(start)
		elapsed += samples[i]
		if err != nil || len(got) != len(positions) {
			t.Fatalf("timed position lookup: count=%d error=%v", len(got), err)
		}
	}
	runtime.ReadMemStats(&after)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("Lookup order=%s requested=%d found=%d samples=%d p50=%s p95=%s ns/op=%d B/op=%d allocs/op=%d",
		name, len(ids), len(positions), len(samples), samples[(len(samples)-1)/2], samples[(len(samples)*95-1)/100],
		elapsed.Nanoseconds()/int64(len(samples)), (after.TotalAlloc-before.TotalAlloc)/uint64(len(samples)),
		(after.Mallocs-before.Mallocs)/uint64(len(samples)))
}

func identicalPerfDBBytes(t *testing.T, root string) int64 {
	t.Helper()
	// Count SQLite files and any journal/WAL sidecars, rather than only logical row payloads.
	var bytes int64
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.Contains(entry.Name(), ".sqlite") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		bytes += info.Size()
		return nil
	}))
	return bytes
}
