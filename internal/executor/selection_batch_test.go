package executor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestSelectionStageAndCaptureReuseBatchFacts(t *testing.T) {
	// Seed known original/native facts so the measured path does not need content hashing.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	require.NoError(t, exe.lib.AutoMigrate())
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	exe = New(exe.db, exe.lib, nil, Paths{Access: []AccessRange{{Root: root}}}, Scripts{}, nil)
	location := &library.Location{Name: "batch", RootPath: root, ExecutorID: "local"}
	require.NoError(t, exe.lib.CreateLocation(ctx, location))
	var ids []int64
	var items []*observation.Item
	hash := sha256.Sum256([]byte("content"))
	for i := 0; i < 201; i++ {
		file := &library.File{Name: fmt.Sprintf("file-%03d", i)}
		require.NoError(t, exe.lib.SaveFile(ctx, file))
		name := filepath.Join(root, file.Name)
		require.NoError(t, os.WriteFile(name, []byte("content"), 0644))
		info, err := os.Stat(name)
		require.NoError(t, err)
		original := &library.FileLocation{FileID: file.ID, LocationID: location.ID, Path: file.Name, Mode: uint32(info.Mode()), Size: info.Size(), MtimeNS: info.ModTime().UnixNano(), Hash: hash[:], Signature: []byte("opaque")}
		require.NoError(t, exe.db.Create(original).Error)
		keys := ObserveTracking(location, info)
		for _, key := range keys {
			key.FileID, key.LocationID = file.ID, location.ID
			require.NoError(t, exe.db.Create(key).Error)
		}
		entry, err := locationEntry(location.ID, file.Name, info)
		require.NoError(t, err)
		item, err := selectedFileObservation(location, entry.Reference, info)
		require.NoError(t, err)
		items, ids = append(items, item), append(ids, file.ID)
	}
	queries := 0
	require.NoError(t, exe.db.Callback().Query().After("gorm:query").Register("selection-batch-cost", func(*gorm.DB) { queries++ }))
	staged, err := resource.OpenTemporaryDB(t.TempDir(), "stage-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, staged.Close()) })
	require.NoError(t, staged.DB.AutoMigrate(&observation.Item{}))
	require.NoError(t, exe.stageSelectedFiles(ctx, staged.DB, location, items))
	require.LessOrEqual(t, queries, 6, "association and tracking reads are bounded batches")
	for _, item := range items {
		require.Equal(t, []byte("opaque"), item.Signature)
		require.Equal(t, hash[:], item.Hash)
	}

	// Capture uses the same observation-matching rule and preserves order and opaque signatures.
	queries = 0
	var captured []int64
	require.NoError(t, exe.CaptureOriginals(ctx, append(ids, ids[len(ids)-1]+1), func(id int64, name string, expected *entity.ExpectedFile) error {
		require.Equal(t, filepath.Join(root, fmt.Sprintf("file-%03d", len(captured))), name)
		require.Equal(t, []byte("opaque"), expected.Signature)
		require.Equal(t, hash[:], expected.Sha256)
		captured = append(captured, id)
		return nil
	}))
	require.Equal(t, ids, captured)
	require.LessOrEqual(t, queries, 12, "unchanged admission cannot issue per-row catalog queries")

	// Full physical preparation freezes the same identities and emits only identity and logical paths.
	queries = 0
	require.NoError(t, exe.stageLiveSelections(ctx, staged.DB, location.ID, []*entity.LocationSelection{{LocationId: location.ID}}))
	var selected []int64
	require.NoError(t, exe.yieldStagedSelections(ctx, staged.DB, func(file *library.File, target string) error {
		require.Equal(t, file.Name, target)
		require.Zero(t, file.Size)
		require.Nil(t, file.ContentSummary)
		selected = append(selected, file.ID)
		return nil
	}))
	require.Equal(t, ids, selected)
	require.LessOrEqual(t, queries, 90, "publication and final identity/path projection also batch their reads")

	// Duplicate complete roots must not multiply observations before staging deduplication.
	observed := 0
	require.NoError(t, staged.DB.Callback().Create().Before("gorm:create").Register("selection-observation-count", func(tx *gorm.DB) {
		conflict, _ := tx.Statement.Clauses["ON CONFLICT"].Expression.(clause.OnConflict)
		rows, ok := tx.Statement.Dest.(*[]*observation.Item)
		if ok && conflict.DoNothing {
			observed += len(*rows)
			require.LessOrEqual(t, len(*rows), observation.BatchSize)
		}
	}))
	var roots []*entity.LocationSelection
	for i := 0; i < 40; i++ {
		roots = append(roots, &entity.LocationSelection{LocationId: location.ID})
	}
	roots = append(roots, &entity.LocationSelection{LocationId: location.ID, Path: "file-000"})
	require.NoError(t, exe.stageLiveSelections(ctx, staged.DB, location.ID, roots))
	require.Equal(t, len(ids)+1, observed, "one observation per File plus the root directory")

	// Changed admissions also share transaction-local validation facts; actual row writes remain atomic.
	matches := make([]*library.ObservationAdmission, 0, len(items))
	for i, item := range items {
		position := item.Position()
		position.FileID, position.Size = ids[i], position.Size+1
		matches = append(matches, &library.ObservationAdmission{Observation: position})
	}
	queries = 0
	admitted, err := exe.lib.AdmitObservations(ctx, location.ID, matches)
	require.NoError(t, err)
	require.Len(t, admitted, len(items))
	require.LessOrEqual(t, queries, 25, "changed admission cannot reload original/identity/native facts per File")
	for _, original := range admitted {
		require.EqualValues(t, 8, original.Size)
	}

	// Single-file missing identity remains typed, and cancellation cannot emit partial captures.
	_, _, err = exe.CaptureOriginal(ctx, 0)
	require.ErrorIs(t, err, library.ErrFileNotFound)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err = exe.CaptureOriginals(cancelled, ids, func(int64, string, *entity.ExpectedFile) error {
		t.Fatal("cancelled capture emitted a row")
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}
