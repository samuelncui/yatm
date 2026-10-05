package library

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestIdenticalLongTransitiveChain(t *testing.T) {
	// Adjacent saved signatures connect the endpoints without a popular shared signature.
	db, lib := newTestLibrary(t)
	const length = 129
	files := make([]*File, length)
	for i := range files {
		files[i] = &File{Name: fmt.Sprintf("chain-%03d", i), Kind: entity.FileKind_FILE_KIND_REGULAR}
	}
	createFileRows(t, db, files...)
	versions := make([]FileVersion, 0, 2*length-2)
	for i, file := range files {
		if i > 0 {
			versions = append(versions, FileVersion{FileID: file.ID, Signature: []byte(fmt.Sprintf("edge-%03d", i-1))})
		}
		if i+1 < length {
			versions = append(versions, FileVersion{FileID: file.ID, Signature: []byte(fmt.Sprintf("edge-%03d", i))})
		}
	}
	require.NoError(t, db.CreateInBatches(&versions, identicalBatch).Error)

	// Full Find and targeted validation must agree on the complete chain and its last row.
	scope := IdenticalScope{Source: IdenticalLibrary}
	full, err := lib.OpenIdenticalSnapshot(context.Background(), scope)
	require.NoError(t, err)
	defer full.Close()
	require.Equal(t, int64(1), full.GroupCount)
	require.Equal(t, int64(length+1), full.AllRows)
	groups, err := full.Groups("", 1)
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	require.Equal(t, int64(length), groups.Groups[0].Count)
	rows, _, err := full.Rows(length, 1, true)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, files[length-1].ID, rows[0].FileID)
	targeted, err := lib.OpenIdenticalComponent(context.Background(), scope, files[length-1].ID)
	require.NoError(t, err)
	defer targeted.Close()
	group, err := targeted.Group(groups.Groups[0].ID)
	require.NoError(t, err)
	require.Equal(t, groups.Groups[0], *group)
}

func TestIdenticalDisconnectedGraphOracle(t *testing.T) {
	// Two unresolved components have branches and cycles; a third settles in one reduction.
	db, lib := newTestLibrary(t)
	files := make([]*File, 17)
	for i := range files {
		files[i] = &File{Name: fmt.Sprintf("graph-%02d", i), Kind: entity.FileKind_FILE_KIND_REGULAR}
	}
	createFileRows(t, db, files...)
	links := [][2]int{
		{0, 1}, {1, 2}, {2, 3}, {3, 4}, {4, 5}, {5, 6}, {1, 4}, {2, 7},
		{8, 9}, {9, 10}, {10, 11}, {11, 12}, {12, 13}, {9, 12},
		{14, 15},
	}
	adjacent := make(map[int64][]int64, len(files))
	versions := make([]FileVersion, 0, 2*len(links))
	for i, link := range links {
		left, right := files[link[0]].ID, files[link[1]].ID
		signature := []byte(fmt.Sprintf("link-%02d", i))
		versions = append(versions, FileVersion{FileID: left, Signature: signature}, FileVersion{FileID: right, Signature: signature})
		adjacent[left] = append(adjacent[left], right)
		adjacent[right] = append(adjacent[right], left)
	}
	require.NoError(t, db.CreateInBatches(&versions, identicalBatch).Error)

	// Independently walk each graph component to obtain its minimum File ID.
	want := make(map[int64]int64, len(files))
	for _, file := range files {
		if _, ok := want[file.ID]; ok {
			continue
		}
		members := []int64{file.ID}
		seen := map[int64]bool{file.ID: true}
		minimum := file.ID
		for i := 0; i < len(members); i++ {
			for _, peer := range adjacent[members[i]] {
				if seen[peer] {
					continue
				}
				seen[peer] = true
				members = append(members, peer)
				if peer < minimum {
					minimum = peer
				}
			}
		}
		for _, id := range members {
			want[id] = minimum
		}
	}

	// Compare every staged File, including the isolated one, with the oracle.
	snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	require.Equal(t, int64(3), snapshot.GroupCount)
	var nodes []identicalNode
	require.NoError(t, snapshot.db.Order("file_id").Find(&nodes).Error)
	require.Len(t, nodes, len(files))
	for _, node := range nodes {
		require.Equal(t, want[node.FileID], node.Component, "File ID %d", node.FileID)
		if node.FileID <= files[13].ID {
			require.True(t, node.Expanded, "long component File ID %d", node.FileID)
		} else {
			require.False(t, node.Expanded, "settled or isolated File ID %d", node.FileID)
		}
	}
}

func BenchmarkIdenticalLongChainConnect(b *testing.B) {
	// Time only graph connection; staging cost is the same for every grouping algorithm.
	for _, length := range []int{16, 64, 128, 512} {
		b.Run(fmt.Sprintf("files-%d", length), func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				db, err := resource.OpenSQLite(filepath.Join(b.TempDir(), "chain.sqlite"))
				if err != nil {
					b.Fatal(err)
				}
				if err := db.AutoMigrate(&identicalNode{}, &identicalEdge{}, &identicalMinimum{}, &identicalComponentSignature{}); err != nil {
					b.Fatal(err)
				}
				nodes := make([]identicalNode, 0, length)
				edges := make([]identicalEdge, 0, 2*length-1)
				for i := 0; i < length; i++ {
					id := int64(i + 1)
					nodes = append(nodes, identicalNode{FileID: id, Component: id, Kind: entity.FileKind_FILE_KIND_REGULAR})
					edges = append(edges, identicalEdge{FileID: id, Signature: []byte(fmt.Sprintf("edge-%03d", i))})
					if i > 0 {
						edges = append(edges, identicalEdge{FileID: id, VersionID: 1, Signature: []byte(fmt.Sprintf("edge-%03d", i-1))})
					}
				}
				if err := db.CreateInBatches(&nodes, identicalBatch).Error; err != nil {
					b.Fatal(err)
				}
				if err := db.CreateInBatches(&edges, identicalBatch).Error; err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				snapshot := &IdenticalSnapshot{db: db}
				if err := snapshot.connect(); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				var last identicalNode
				if err := db.Where("file_id = ?", length).First(&last).Error; err != nil || last.Component != 1 {
					b.Fatalf("last chain member has component %d: %v", last.Component, err)
				}
				sqlDB, err := db.DB()
				if err != nil {
					b.Fatal(err)
				}
				if err := sqlDB.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkIdenticalManySmallGroupsConnect(b *testing.B) {
	// A chain must not turn unrelated small groups into per-component database work.
	for _, chainLength := range []int{0, 128} {
		b.Run(fmt.Sprintf("chain-%d", chainLength), func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				const length = 512
				db, err := resource.OpenSQLite(filepath.Join(b.TempDir(), "pairs.sqlite"))
				if err != nil {
					b.Fatal(err)
				}
				if err := db.AutoMigrate(&identicalNode{}, &identicalEdge{}, &identicalMinimum{}, &identicalComponentSignature{}); err != nil {
					b.Fatal(err)
				}
				nodes := make([]identicalNode, 0, length)
				edges := make([]identicalEdge, 0, length+chainLength)
				for i := range length {
					id := int64(i + 1)
					nodes = append(nodes, identicalNode{FileID: id, Component: id, Kind: entity.FileKind_FILE_KIND_REGULAR})
					if i < chainLength {
						edges = append(edges, identicalEdge{FileID: id, Signature: []byte(fmt.Sprintf("edge-%03d", i))})
						if i > 0 {
							edges = append(edges, identicalEdge{FileID: id, VersionID: 1, Signature: []byte(fmt.Sprintf("edge-%03d", i-1))})
						}
					} else {
						edges = append(edges, identicalEdge{FileID: id, Signature: []byte(fmt.Sprintf("pair-%03d", i/2))})
					}
				}
				if err := db.CreateInBatches(&nodes, identicalBatch).Error; err != nil {
					b.Fatal(err)
				}
				if err := db.CreateInBatches(&edges, identicalBatch).Error; err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := (&IdenticalSnapshot{db: db}).connect(); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				var count int64
				if err := db.Model(&identicalNode{}).Where("component = ?", int64(chainLength+1)).Count(&count).Error; err != nil || count != 2 {
					b.Fatalf("first independent pair has %d members: %v", count, err)
				}
				if chainLength > 0 {
					if err := db.Model(&identicalNode{}).Where("component = ?", 1).Count(&count).Error; err != nil || count != int64(chainLength) {
						b.Fatalf("chain has %d members: %v", count, err)
					}
				}
				sqlDB, err := db.DB()
				if err != nil {
					b.Fatal(err)
				}
				if err := sqlDB.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
