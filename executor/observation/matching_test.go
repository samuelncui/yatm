package observation

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/resource"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestMatchingUsesCompleteOrderedRounds(t *testing.T) {
	// Deliberately disagreeing evidence distinguishes priority from identity proof.
	native := Evidence{NativeScope: "executor/filesystem", NativeKey: []byte{1}, BirthNS: 7, Generation: 2}
	uuid := Evidence{UUIDScope: "executor", UUID: []byte("copied-uuid")}
	cases := []struct {
		name  string
		old   []Original
		items []Item
		want  map[string]int64
	}{
		{"all paths before signatures", []Original{{FileID: 1, Path: "gone", Signature: []byte("a")}, {FileID: 2, Path: "stay"}},
			[]Item{{Path: "stay", Signature: []byte("a")}, {Path: "copy", Signature: []byte("a")}}, map[string]int64{"stay": 2, "copy": 1}},
		{"path replacement ignores lower evidence", []Original{{FileID: 1, Path: "stay", Evidence: native}},
			[]Item{{Path: "stay"}, {Path: "moved", Evidence: native}}, map[string]int64{"stay": 1, "moved": 0}},
		{"swapped paths remain organization", []Original{{FileID: 1, Path: "a", Signature: []byte("a")}, {FileID: 2, Path: "b", Signature: []byte("b")}},
			[]Item{{Path: "a", Signature: []byte("b")}, {Path: "b", Signature: []byte("a")}}, map[string]int64{"a": 1, "b": 2}},
		{"observed signature before native", []Original{{FileID: 1, Path: "gone", Signature: []byte("a"), Evidence: native}},
			[]Item{{Path: "by-native", Evidence: native}, {Path: "by-content", Signature: []byte("a")}}, map[string]int64{"by-native": 0, "by-content": 1}},
		{"unknown signature skips to native", []Original{{FileID: 1, Path: "gone", Evidence: native}},
			[]Item{{Path: "a"}, {Path: "b", Evidence: native}}, map[string]int64{"a": 0, "b": 1}},
		{"UUID candidates use path order", []Original{{FileID: 9, Evidence: uuid}, {FileID: 2, Evidence: uuid}},
			[]Item{{Path: "z", Evidence: uuid}, {Path: "a", Evidence: uuid}, {Path: "b", Evidence: uuid}}, map[string]int64{"a": 2, "b": 9, "z": 0}},
		{"native namespace birth and generation are checked", []Original{{FileID: 1, Evidence: native}},
			[]Item{{Path: "scope", Evidence: Evidence{NativeScope: "other", NativeKey: native.NativeKey, BirthNS: 7, Generation: 2}},
				{Path: "birth", Evidence: Evidence{NativeScope: native.NativeScope, NativeKey: native.NativeKey, BirthNS: 8, Generation: 2}},
				{Path: "generation", Evidence: Evidence{NativeScope: native.NativeScope, NativeKey: native.NativeKey, BirthNS: 7, Generation: 3}}},
			map[string]int64{"scope": 0, "birth": 0, "generation": 0}},
		{"removed entries cannot be claimed", []Original{{FileID: 1, Path: "gone", Signature: []byte("a")}},
			[]Item{{Path: "gone", Change: entity.ScanChange_SCAN_CHANGE_REMOVED, Signature: []byte("a")}}, map[string]int64{"gone": 0}},
		{"known copy never inherits another object", []Original{{FileID: 1, Path: "copy", Signature: []byte("a"), Evidence: uuid}},
			[]Item{{Path: "copy", Signature: []byte("a"), Evidence: uuid, Independent: true}}, map[string]int64{"copy": 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Stage the full observation before running any matching rule.
			db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&Original{}, &Item{}))
			require.NoError(t, db.Create(&tc.old).Error)
			require.NoError(t, db.Create(&tc.items).Error)
			require.NoError(t, Match(context.Background(), db, logrus.New()))

			// Check the complete one-to-one assignment, including observations left for new Files.
			var items []Item
			require.NoError(t, db.Find(&items).Error)
			actual := make(map[string]int64, len(items))
			for _, item := range items {
				actual[item.Path] = item.FileID
			}
			require.Equal(t, tc.want, actual)
		})
	}
}

func TestMatchingPagesKeepFileIDAndPathOrder(t *testing.T) {
	// More than two pages of identical candidates must remain deterministic and one-to-one.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Original{}, &Item{}))
	for index := BatchSize * 2; index > 0; index-- {
		require.NoError(t, db.Create(&Original{FileID: int64(index), Signature: []byte("same")}).Error)
		require.NoError(t, db.Create(&Item{Path: fmt.Sprintf("%04d", index), Signature: []byte("same")}).Error)
	}
	require.NoError(t, Match(context.Background(), db, logrus.New()))

	// First eligible File and first eligible normalized path win independently of insertion order.
	var rows []Item
	require.NoError(t, db.Order("path").Find(&rows).Error)
	for index, row := range rows {
		require.EqualValues(t, index+1, row.FileID)
	}
}
