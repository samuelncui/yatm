package library

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIdenticalIndexedRowsAndPositions(t *testing.T) {
	// Two signature links make a transitive group; dot-name filtering retains its complete metadata.
	db, lib := newTestLibrary(t)
	files := []*File{
		{Name: "a", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: ".b", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "c", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: ".d", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: ".e", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "f", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: ".g", Kind: entity.FileKind_FILE_KIND_REGULAR},
	}
	createFileRows(t, db, files...)
	for _, edge := range []struct{ file, signature string }{
		{"a", "x"}, {".b", "x"}, {".b", "y"}, {"c", "y"},
		{".d", "z"}, {".e", "z"}, {"f", "w"}, {".g", "w"},
	} {
		for _, file := range files {
			if file.Name == edge.file {
				require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte(edge.signature)}).Error)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	snapshot, err := lib.OpenIdenticalSnapshot(ctx, IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	cancel()
	require.Equal(t, int64(3), snapshot.GroupCount)
	require.Equal(t, int64(10), snapshot.AllRows)
	require.Equal(t, int64(5), snapshot.VisibleRows)

	// Random offsets and one-row pages address the same dense sequence after build cancellation.
	for _, projection := range []struct {
		hidden bool
		total  int64
		ids    []int64
	}{{true, 10, []int64{0, files[0].ID, files[1].ID, files[2].ID, 0, files[3].ID, files[4].ID, 0, files[5].ID, files[6].ID}},
		{false, 5, []int64{0, files[0].ID, files[2].ID, 0, files[5].ID}}} {
		for offset := int64(0); offset < projection.total+2; offset++ {
			rows, total, err := snapshot.Rows(offset, 1, projection.hidden)
			require.NoError(t, err)
			require.Equal(t, projection.total, total)
			if offset >= projection.total {
				require.Empty(t, rows)
				continue
			}
			require.Len(t, rows, 1)
			require.Equal(t, offset, rows[0].Position)
			require.Equal(t, projection.ids[offset], rows[0].FileID)
			require.NotEmpty(t, rows[0].Group.Fingerprint)
		}
	}
	positions, err := snapshot.LookupPositions([]int64{files[2].ID, files[1].ID, files[3].ID, files[5].ID, 999999})
	require.NoError(t, err)
	require.Len(t, positions, 4)
	require.Equal(t, int64(3), positions[0].AllPosition)
	require.Equal(t, int64(2), *positions[0].VisiblePosition)
	require.Nil(t, positions[1].VisiblePosition)
	require.Nil(t, positions[2].VisibleHeaderPosition)
	require.Equal(t, int64(3), *positions[3].VisibleHeaderPosition)
	ids := make([]int64, 1000)
	for i := range ids {
		ids[i] = int64(100000 + i)
	}
	ids[999] = files[5].ID
	positions, err = snapshot.LookupPositions(ids)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, files[5].ID, positions[0].FileID)
}

func TestIdenticalRowsOrderByGroupTitleThenComponent(t *testing.T) {
	// Component IDs deliberately oppose titles, and two Alpha groups require the ID tie-breaker.
	db, lib := newTestLibrary(t)
	files := []*File{{Name: "Zulu", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "Zzz", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "Alpha", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "Beta", Kind: entity.FileKind_FILE_KIND_REGULAR}}
	createFileRows(t, db, files...)
	bucket := File{Name: "bucket", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	createFileRows(t, db, &bucket)
	nested := []*File{{ParentID: bucket.ID, Name: "Alpha", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{ParentID: bucket.ID, Name: "Gamma", Kind: entity.FileKind_FILE_KIND_REGULAR}}
	createFileRows(t, db, nested...)
	files = append(files, nested...)
	for i, file := range files {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte(fmt.Sprintf("group-%d", i/2))}).Error)
	}
	snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	require.Equal(t, int64(9), snapshot.AllRows)
	rows, _, err := snapshot.Rows(0, 9, true)
	require.NoError(t, err)
	require.Equal(t, []string{strconv.FormatInt(files[2].ID, 10), strconv.FormatInt(files[4].ID, 10), strconv.FormatInt(files[0].ID, 10)},
		[]string{rows[0].Group.ID, rows[3].Group.ID, rows[6].Group.ID})
	require.Equal(t, []string{"Alpha", "Alpha", "Zulu"}, []string{rows[0].Group.Name, rows[3].Group.Name, rows[6].Group.Name})
	sorted, _, err := snapshot.SortedRows(0, 9, true, entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME,
		entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC)
	require.NoError(t, err)
	require.Equal(t, []string{rows[0].Group.ID, rows[3].Group.ID, rows[6].Group.ID},
		[]string{sorted[0].Group.ID, sorted[3].Group.ID, sorted[6].Group.ID})
	for _, offset := range []int{0, 3, 6} {
		require.Zero(t, sorted[offset].FileID)
		require.Equal(t, int64(offset), sorted[offset].HeaderPosition)
	}
	members, err := snapshot.MembersByIDs([]int64{nested[0].ID})
	require.NoError(t, err)
	require.Equal(t, "bucket/Alpha", members[0].Path)
	positions, err := snapshot.LookupPositions([]int64{files[0].ID, nested[0].ID})
	require.NoError(t, err)
	require.Equal(t, int64(7), positions[0].AllPosition)
	require.Equal(t, int64(6), positions[0].AllHeaderPosition)
	require.Equal(t, int64(4), positions[1].AllPosition)
	require.Equal(t, int64(3), positions[1].AllHeaderPosition)
}

func TestIdenticalSortedRowsLargeGroup(t *testing.T) {
	// Build one group across staging batches with repeated names, sizes, and hidden members.
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	const count = 300
	files := make([]*File, count)
	parents := make([]*File, (count+22)/23)
	for i := range parents {
		parents[i] = &File{Name: fmt.Sprintf("bucket-%02d", i), Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	}
	createFileRows(t, db, parents...)
	versions := make([]FileVersion, 0, count+2)
	type expectedMember struct {
		id     int64
		name   string
		size   int64
		known  bool
		hidden bool
	}
	want := make([]expectedMember, 0, count)
	for i := range files {
		name := fmt.Sprintf("name-%02d", (i*37)%23)
		if i%11 == 0 {
			name = "." + name
		}
		if i%13 == 0 {
			name = strings.ToUpper(name)
		}
		files[i] = &File{ParentID: parents[i/23].ID, Name: name, Kind: entity.FileKind_FILE_KIND_REGULAR}
	}
	createFileRows(t, db, files...)
	for i, file := range files {
		size := int64(i % 7)
		if i%5 == 0 {
			size = 50 + size
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID,
				Path: fmt.Sprintf("dir/%03d", i), Signature: []byte("shared"), Size: size}).Error)
		}
		versions = append(versions, FileVersion{FileID: file.ID, Signature: []byte("shared"), Size: int64(i % 7)})
		want = append(want, expectedMember{id: file.ID, name: file.Name, size: size, known: true,
			hidden: strings.HasPrefix(file.Name, ".")})
	}
	require.NoError(t, db.CreateInBatches(&versions, identicalBatch).Error)
	require.NoError(t, db.Exec("UPDATE file_versions SET size = NULL WHERE file_id = ?", files[2].ID).Error)
	want[2].known = false
	recent := int64(123)
	require.NoError(t, db.Create(&FileVersion{FileID: files[0].ID, Signature: []byte("extra-original"), Size: 888, LastArchivedAtNS: &recent}).Error)
	require.NoError(t, db.Create(&FileVersion{FileID: files[1].ID, Signature: []byte("extra-saved"), Size: 999, LastArchivedAtNS: &recent}).Error)
	want[1].size = 999

	// Every projection and direction has dense positions independent of page offsets.
	snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	require.Equal(t, int64(1), snapshot.GroupCount)
	for _, column := range []string{"file_desc_position", "name_asc_position", "name_desc_position", "size_asc_position", "size_desc_position"} {
		var plan []struct{ Detail string }
		require.NoError(t, snapshot.db.Raw("EXPLAIN QUERY PLAN SELECT * FROM identical_row_indices WHERE projection = ? AND "+column+" >= ? ORDER BY "+column+" LIMIT 2", 1, 255).Scan(&plan).Error)
		require.Contains(t, fmt.Sprint(plan), "idx_identical_"+column)
	}
	for _, hidden := range []bool{false, true} {
		members := make([]expectedMember, 0, count)
		for _, member := range want {
			if hidden || !member.hidden {
				members = append(members, member)
			}
		}
		for _, order := range []struct {
			key        entity.IdenticalSortKey
			sortOrder  entity.IdenticalSortOrder
			descending bool
		}{
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED, false},
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC, false},
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC, true},
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED, false},
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC, false},
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC, true},
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED, true},
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_ASC, false},
			{entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE, entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_DESC, true},
		} {
			ordered := append([]expectedMember(nil), members...)
			sort.Slice(ordered, func(i, j int) bool {
				left, right := ordered[i], ordered[j]
				switch order.key {
				case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_NAME:
					leftFolded, rightFolded := strings.ToLower(left.name), strings.ToLower(right.name)
					if leftFolded != rightFolded {
						return (leftFolded < rightFolded) != order.descending
					}
					if left.name != right.name {
						return (left.name < right.name) != order.descending
					}
				case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE:
					if left.known != right.known {
						return left.known
					}
					if left.size != right.size {
						return (left.size < right.size) != order.descending
					}
				case entity.IdenticalSortKey_IDENTICAL_SORT_KEY_FILE_ID:
					return (left.id < right.id) != order.descending
				}
				return left.id < right.id
			})
			offsets := []int64{0, 1, 127, 255, int64(len(ordered)), int64(len(ordered) + 1)}
			random := rand.New(rand.NewSource(7))
			for range 30 {
				offsets = append(offsets, int64(random.Intn(len(ordered)+2)))
			}
			for _, offset := range offsets {
				rows, total, err := snapshot.SortedRows(offset, 2, hidden, order.key, order.sortOrder)
				require.NoError(t, err)
				require.Equal(t, int64(len(ordered)+1), total)
				for i, row := range rows {
					position := int(offset) + i
					require.Equal(t, int64(position), row.Position)
					require.Zero(t, row.HeaderPosition)
					require.Equal(t, int64(len(ordered)), row.DisplayCount)
					if position == 0 {
						require.Zero(t, row.FileID)
					} else {
						require.Equal(t, ordered[position-1].id, row.FileID)
					}
				}
			}
			var actual []int64
			for offset := int64(0); offset < int64(len(ordered)+1); offset += 200 {
				rows, _, err := snapshot.SortedRows(offset, 200, hidden, order.key, order.sortOrder)
				require.NoError(t, err)
				for _, row := range rows {
					actual = append(actual, row.FileID)
				}
			}
			expected := []int64{0}
			for _, member := range ordered {
				expected = append(expected, member.id)
			}
			require.Equal(t, expected, actual)
			ids := []int64{files[0].ID, files[1].ID, files[57].ID, files[299].ID}
			positions, err := snapshot.LookupSortedPositions(ids, order.key, order.sortOrder)
			require.NoError(t, err)
			require.Len(t, positions, len(ids))
			for i, position := range positions {
				require.Equal(t, ids[i], position.FileID)
				for rank, member := range ordered {
					if member.id == position.FileID {
						if hidden {
							require.Equal(t, int64(rank+1), position.AllPosition)
						} else {
							require.Equal(t, int64(rank+1), *position.VisiblePosition)
						}
					}
				}
				if !hidden && want[0].id == position.FileID {
					require.Nil(t, position.VisiblePosition)
				}
			}
		}
	}
}

func TestIdenticalVisibleRowsUseVisibleGroupTitleAndOrder(t *testing.T) {
	// A hidden title must not appear in the visible projection or determine its order.
	db, lib := newTestLibrary(t)
	files := []*File{
		{Name: ".a-hidden", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "Zulu", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "Beta", Kind: entity.FileKind_FILE_KIND_REGULAR},
		{Name: "Gamma", Kind: entity.FileKind_FILE_KIND_REGULAR},
	}
	createFileRows(t, db, files...)
	for i, file := range files {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID,
			Signature: []byte(fmt.Sprintf("group-%d", i/2))}).Error)
	}

	// Both projections keep the same groups, but each sorts and labels them by its shown title.
	snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	all, _, err := snapshot.Rows(0, 6, true)
	require.NoError(t, err)
	visible, _, err := snapshot.Rows(0, 5, false)
	require.NoError(t, err)
	require.Equal(t, []string{".a-hidden", "Beta"}, []string{all[0].Group.Name, all[3].Group.Name})
	require.Equal(t, []string{"Beta", "Zulu"}, []string{visible[0].Group.Name, visible[3].Group.Name})
	require.Equal(t, files[2].ID, visible[1].FileID)
	require.Equal(t, files[1].ID, visible[4].FileID)
	sorted, _, err := snapshot.SortedRows(0, 5, false, entity.IdenticalSortKey_IDENTICAL_SORT_KEY_SIZE,
		entity.IdenticalSortOrder_IDENTICAL_SORT_ORDER_UNSPECIFIED)
	require.NoError(t, err)
	require.Equal(t, []string{"Beta", "Zulu"}, []string{sorted[0].Group.Name, sorted[3].Group.Name})
	require.Zero(t, sorted[0].FileID)
	require.Zero(t, sorted[3].FileID)
}

func TestIdenticalRowsTitleOrderingCrossesMetadataBatch(t *testing.T) {
	// A title page boundary must not reset or reorder global ordinals.
	db, lib := newTestLibrary(t)
	for i := 69; i >= 0; i-- {
		files := []*File{{Name: fmt.Sprintf("title-%03d-a", i), Kind: entity.FileKind_FILE_KIND_REGULAR},
			{Name: fmt.Sprintf("title-%03d-b", i), Kind: entity.FileKind_FILE_KIND_REGULAR}}
		createFileRows(t, db, files...)
		for _, file := range files {
			require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte(fmt.Sprintf("group-%03d", i))}).Error)
		}
	}
	snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	require.Equal(t, int64(70), snapshot.GroupCount)
	for _, group := range []int64{0, 1, 63, 64, 69} {
		rows, _, err := snapshot.Rows(group*3, 1, true)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, fmt.Sprintf("title-%03d-a", group), rows[0].Group.Name)
		require.Zero(t, rows[0].FileID)
	}
}

func TestIdenticalRetainedPagesDoNotCollectAgain(t *testing.T) {
	// Only opening a snapshot may issue the source collection query.
	db, lib := newTestLibrary(t)
	files := []*File{{Name: "a", Kind: entity.FileKind_FILE_KIND_REGULAR}, {Name: "b", Kind: entity.FileKind_FILE_KIND_REGULAR}}
	createFileRows(t, db, files...)
	for _, file := range files {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("shared")}).Error)
	}
	collects := 0
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("identical_collect_count", func(tx *gorm.DB) {
		if tx.Statement.Table == "file_versions" && tx.Statement.Selects != nil {
			collects++
		}
	}))
	snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	afterFind := collects
	require.Positive(t, afterFind)
	for offset := int64(0); offset < snapshot.AllRows; offset++ {
		_, _, err := snapshot.Rows(offset, 1, true)
		require.NoError(t, err)
	}
	groups, err := snapshot.Groups("", 10)
	require.NoError(t, err)
	_, err = snapshot.Members(groups.Groups[0].ID, "", 1)
	require.NoError(t, err)
	require.Equal(t, afterFind, collects)
}

func TestIdenticalRowIndexBuildBatchesSmallGroups(t *testing.T) {
	// Settled pairs span a metadata batch; sixteen groups contain only hidden members.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "groups.sqlite"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&identicalNode{}, &identicalEdge{}))
	var nodes []identicalNode
	var edges []identicalEdge
	for group := range 80 {
		for member := range 2 {
			id := int64(group*2 + member + 1)
			name := fmt.Sprintf("member-%03d", id)
			if group%5 == 0 {
				name = "." + name
			}
			nodes = append(nodes, identicalNode{FileID: id, Component: int64(group*2 + 1), Name: name,
				Kind: entity.FileKind_FILE_KIND_REGULAR})
			edges = append(edges, identicalEdge{FileID: id, Signature: []byte(fmt.Sprintf("group-%03d", group))})
		}
	}
	require.NoError(t, db.Create(&nodes).Error)
	require.NoError(t, db.Create(&edges).Error)

	// One write per projection and one per group batch replace per-group commits.
	trace := &fileReadSQL{Interface: db.Logger}
	db.Logger = trace
	snapshot := &IdenticalSnapshot{db: db, scope: "fixture"}
	require.NoError(t, snapshot.materializeRows())
	require.Equal(t, int64(80), snapshot.GroupCount)
	require.Equal(t, int64(240), snapshot.AllRows)
	require.Equal(t, int64(192), snapshot.VisibleRows)
	var rowWrites, groupWrites int
	for _, query := range trace.queries {
		if strings.Contains(query, "INSERT INTO identical_row_indices ") {
			rowWrites++
		}
		if strings.HasPrefix(query, "INSERT INTO `identical_group_indices`") {
			groupWrites++
		}
	}
	require.Equal(t, 2, rowWrites, "complete projections must not commit per group or File")
	require.Equal(t, 2, groupWrites, "group metadata must be published in bounded batches")

	// Dense page positions retain their physical insertion locality within each projection.
	for _, projection := range []int{0, 1} {
		var positions []int64
		require.NoError(t, db.Model(&identicalRowIndex{}).Where("projection = ?", projection).
			Order("rowid").Pluck("position", &positions).Error)
		for i, position := range positions {
			require.Equal(t, int64(i), position)
		}
	}

	// Every batched summary retains exactly the authoritative component fingerprint.
	var groups []identicalGroupIndex
	require.NoError(t, db.Order("component").Find(&groups).Error)
	require.Len(t, groups, 80)
	for _, group := range groups {
		fingerprint, err := snapshot.Fingerprint(strconv.FormatInt(group.Component, 10))
		require.NoError(t, err)
		require.Equal(t, fingerprint, group.Fingerprint)
	}
}
