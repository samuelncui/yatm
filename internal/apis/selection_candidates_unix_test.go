//go:build linux || darwin

package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSingleLiveSelectionDoesNotInspectUnrelatedOriginals(t *testing.T) {
	// A narrow selection must not expand to every registered original before matching.
	api, service, root, id := admissionLocation(t)
	for index := 0; index < 40; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("unrelated-%02d.txt", index)), []byte("other"), 0644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "wanted.txt"), []byte("selected"), 0644))
	entries := admittedPage(t, service, id)
	require.Len(t, entries, 41)
	wanted := entries["wanted.txt"].Entry.GetAssociatedFileId()

	// Observe the real candidate facts consumed before any eligibility check can stat their paths.
	db, err := resource.OpenSQLite(filepath.Join(filepath.Dir(root), "library.db"))
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	defer connection.Close()
	api.lib = library.New(db)
	api.exe = executor.New(db, api.lib, nil, api.exe.Paths(), executor.Scripts{}, nil)
	var unrelated []string
	reads := 0
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:selected_candidates_only", func(tx *gorm.DB) {
		rows, ok := tx.Statement.Dest.(*[]*library.FileLocation)
		if !ok {
			return
		}
		reads++
		for _, row := range *rows {
			if row.FileID != wanted {
				unrelated = append(unrelated, row.Path)
			}
		}
	}))
	var selected []int64
	require.NoError(t, api.exe.WalkLiveSelections(context.Background(), selectionStageDB(t),
		[]*entity.FileSelection{physicalSelection(id, "wanted.txt")}, func(file *library.File, _ string) error {
			selected = append(selected, file.ID)
			return nil
		}))
	require.Positive(t, reads, "the real matching projection must be exercised")
	require.Empty(t, unrelated, "unrelated originals must never reach path eligibility checks")
	require.Equal(t, []int64{wanted}, selected)
}
