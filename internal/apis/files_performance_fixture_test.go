package apis

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupFilesLocationBenchmark(t testing.TB, entries, depth int, ignoreText string) (*filesService, *entity.FileOperationRef, string) {
	t.Helper()
	service, directory, physical, _ := setupFilesLocationBenchmarkNames(t, entries, depth, ignoreText, func(index int) string {
		return fmt.Sprintf("file-%05d.txt", index)
	})
	return service, directory, physical
}

func setupFilesLocationBenchmarkNames(t testing.TB, entries, depth int, ignoreText string, name func(int) string) (*filesService, *entity.FileOperationRef, string, *gorm.DB) {
	t.Helper()

	// Register one nested live directory holding the enumerated entries.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	db.Logger = logger.Discard
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())

	// Preserve the directory depth and configured Ignore rules independently of its names.
	parts := make([]string, 0, depth)
	for index := 0; index < depth; index++ {
		parts = append(parts, fmt.Sprintf("d%02d", index))
	}
	relative := strings.Join(parts, "/")
	physical := filepath.Join(root, "originals", filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(physical, 0755))
	location := &library.Location{Name: "Live", ExecutorID: "local", RootPath: filepath.Join(root, "originals"), Config: &entity.LocationConfig{}}
	if ignoreText != "" {
		location.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: ignoreText}
	}
	require.NoError(t, lib.CreateLocation(context.Background(), location))

	// Write each final name once in the supplied creation order.
	for index := 0; index < entries; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(physical, name(index)), []byte("content"), 0644))
	}

	// Expose the same service and fixture connection for optional catalog seeding.
	exe := executor.New(db, lib, nil, executor.Paths{Access: []executor.AccessRange{{Root: root}}, Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID, Path: relative}}}
	return &filesService{api: New(lib, exe)}, directory, physical, db
}

// setupFilesListMixedBenchmark keeps the timed List fixture identical for both source revisions.
func setupFilesListMixedBenchmark(t testing.TB, count int, order string, depth int, ignoreText string) (*filesService, *entity.FileOperationRef) {
	t.Helper()

	// Register the same live directory and fixed-seed name order for every implementation.
	var names []int
	if order == "reverse" {
		names = make([]int, count)
		for i := range names {
			names[i] = count - i - 1
		}
	} else {
		names = rand.New(rand.NewSource(19)).Perm(count)
	}
	service, directory, physical, db := setupFilesLocationBenchmarkNames(t, count, depth, ignoreText, func(index int) string {
		return fmt.Sprintf("entry-%08d", names[index])
	})

	// Seed alternate lexical names in one fixture transaction; setup is never part of List timing.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		lib := library.New(tx)
		for i := 0; i < count; i += 2 {
			name := fmt.Sprintf("entry-%08d", i)
			file := &library.File{Name: name}
			if err := lib.SaveFile(context.Background(), file); err != nil {
				return err
			}
			info, err := os.Lstat(filepath.Join(physical, name))
			if err != nil {
				return err
			}
			if err := tx.Create(&library.FileLocation{FileID: file.ID, LocationID: directory.GetLocation().LocationId, Path: directory.GetLocation().Path + "/" + name,
				Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano()}).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	return service, directory
}

func filesPerformanceRules() string {
	// Include actual exclusions and negation after unrelated wildcard/path rules.
	var rules strings.Builder
	for index := range 50 {
		fmt.Fprintf(&rules, "cache-%02d/**\n*.temporary-%02d\n", index, index)
	}
	rules.WriteString("entry-*7\n!entry-*17\n")
	return rules.String()
}

func filesPerformanceRequest(directory *entity.FileOperationRef, batchSize int32) *entity.ListFilesRequest {
	return &entity.ListFilesRequest{Directory: directory, BatchSize: batchSize, Include: []entity.FilesInclude{
		entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS,
		entity.FilesInclude_FILES_INCLUDE_OPERATIONS, entity.FilesInclude_FILES_INCLUDE_NAVIGATION,
	}}
}

// filesStructuralRecorder validates the stream without retaining every projected row.
type filesStructuralRecorder struct {
	filesListRecorder
	count      int
	associated int
	batches    int
	maxBatch   int
	total      int64
	previous   string
}

func (r *filesStructuralRecorder) Send(batch *entity.ListFilesResponse) error {
	// The exact total belongs to the first reply, and every reply respects the requested bound.
	if len(batch.Entries) > r.maxBatch {
		return fmt.Errorf("batch has %d rows, limit %d", len(batch.Entries), r.maxBatch)
	}
	if r.batches == 0 {
		if batch.TotalEntryCount == nil {
			return fmt.Errorf("first batch lacks complete directory count")
		}
		r.total = *batch.TotalEntryCount
	} else if batch.TotalEntryCount != nil {
		return fmt.Errorf("later batch repeats directory count")
	}

	// Strict order proves uniqueness while requiring only one previous name in the receiver.
	for _, entry := range batch.Entries {
		if r.count > 0 && entry.Name <= r.previous {
			return fmt.Errorf("non-increasing name %q after %q", entry.Name, r.previous)
		}
		if entry.Kind != entity.EntryKind_ENTRY_KIND_FILE {
			return fmt.Errorf("unexpected entry kind %s", entry.Kind)
		}
		if entry.AssociatedFileId != nil {
			r.associated++
		}
		r.previous = entry.Name
		r.count++
	}
	r.batches++
	return nil
}
