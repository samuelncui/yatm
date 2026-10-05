package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type filesListQueryLog struct {
	logger.Interface
	queries []string
}

func (l *filesListQueryLog) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	query, _ := fc()
	l.queries = append(l.queries, query)
}

func TestFilesListReadCostIsBatchBounded(t *testing.T) {
	// Exercise the public listing against a moderately sized, nested Location directory.
	service, directory, db := setupFilesListBenchmark(t)
	trace := &filesListQueryLog{Interface: logger.Discard}
	db.Logger = trace
	include := []entity.FilesInclude{
		entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES,
		entity.FilesInclude_FILES_INCLUDE_STATUS,
		entity.FilesInclude_FILES_INCLUDE_OPERATIONS,
		entity.FilesInclude_FILES_INCLUDE_NAVIGATION,
	}
	ctx := context.Background()

	// A complete listing is one enumeration, so a basic one costs the same whatever the
	// batch size and never touches a projection table.
	batches := []int32{1, 50, 100, 200}
	basicQueries := 0
	for _, batchSize := range batches {
		trace.queries = nil
		entries, rows, err := drainListFirstBatch(ctx, service, &entity.ListFilesRequest{Directory: directory, BatchSize: batchSize})
		require.NoError(t, err)
		require.Len(t, entries, int(batchSize))
		require.NotZero(t, rows)
		if basicQueries == 0 {
			basicQueries = len(trace.queries)
		} else {
			require.Equal(t, basicQueries, len(trace.queries), "a basic listing is one enumeration, whatever the batch size")
		}
		for _, table := range []string{"file_locations", "file_versions", "positions", "file_tracking_keys"} {
			require.NotContains(t, strings.ToLower(strings.Join(trace.queries, "\n")), table)
		}
	}

	// A projected listing reads its metadata one batch at a time, so the rows reach the client
	// as each batch is read: the work is a bounded round of statements per batch, never one
	// query per row inside one.
	trace.queries = nil
	entries, _, err := drainListFirstBatch(ctx, service, &entity.ListFilesRequest{Directory: directory, BatchSize: 200, Include: include})
	require.NoError(t, err)
	require.Len(t, entries, 200)
	wholeBatchQueries := len(trace.queries)
	require.LessOrEqual(t, wholeBatchQueries, 20, "200 rows in one batch read metadata in a bounded number of rounds")
	trace.queries = nil
	entries, _, err = drainListFirstBatch(ctx, service, &entity.ListFilesRequest{Directory: directory, BatchSize: 1, Include: include})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Greater(t, len(trace.queries), wholeBatchQueries, "one row per batch pays one round of metadata reads per batch")
	require.NotContains(t, strings.ToUpper(strings.Join(trace.queries, "\n")), "COUNT(")
	t.Logf("Files List SQL statements for one 200-row listing: basic=%d, projected one batch=%d, projected one row per batch=%d", basicQueries, wholeBatchQueries, len(trace.queries))
}

// drainListFirstBatch drains the entire listing and returns its first batch.
// Use a send-time measurement when benchmarking first-batch latency.
func drainListFirstBatch(ctx context.Context, service *filesService, request *entity.ListFilesRequest) ([]*entity.FilesEntry, int, error) {
	server := &filesListRecorder{ctx: ctx}
	if err := service.List(request, server); err != nil {
		return nil, 0, err
	}
	total := 0
	if server.first != nil && server.first.TotalEntryCount != nil {
		total = int(*server.first.TotalEntryCount)
	}
	return server.first.GetEntries(), total, nil
}

func BenchmarkFilesListReadCost(b *testing.B) {
	// Measure repeated hot reads only; this intentionally does not claim a cold filesystem-cache result.
	service, directory, _ := setupFilesListBenchmark(b)
	include := []entity.FilesInclude{
		entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES,
		entity.FilesInclude_FILES_INCLUDE_STATUS,
		entity.FilesInclude_FILES_INCLUDE_OPERATIONS,
		entity.FilesInclude_FILES_INCLUDE_NAVIGATION,
	}
	ctx := context.Background()
	for _, projection := range []struct {
		name    string
		include []entity.FilesInclude
	}{
		{name: "basic"},
		{name: "projected", include: include},
	} {
		for _, batchSize := range []int32{1, 50, 100, 200} {
			b.Run(fmt.Sprintf("%s/%d", projection.name, batchSize), func(b *testing.B) {
				request := &entity.ListFilesRequest{Directory: directory, BatchSize: batchSize, Include: projection.include}
				b.ReportAllocs()
				b.ResetTimer()
				for index := 0; index < b.N; index++ {
					entries, _, err := drainListFirstBatch(ctx, service, request)
					if err != nil {
						b.Fatal(err)
					}
					if len(entries) != int(batchSize) {
						b.Fatalf("got %d entries, want %d", len(entries), batchSize)
					}
				}
			})
		}
	}
}

func setupFilesListBenchmark(t testing.TB) (*filesService, *entity.FileOperationRef, *gorm.DB) {
	t.Helper()

	// Initialize an isolated Library and database for one real API harness.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())

	// Register the nested directory that the visible Location page will enumerate.
	physical := filepath.Join(root, "originals", "archive", "2026", "september")
	require.NoError(t, os.MkdirAll(physical, 0755))
	location := &library.Location{Name: "Originals", ExecutorID: "local", RootPath: filepath.Join(root, "originals")}
	require.NoError(t, lib.CreateLocation(context.Background(), location))

	// Seed retained associations without admitting data through the read path under test.
	for index := 0; index < 200; index++ {
		name := fmt.Sprintf("file-%03d.txt", index)
		filename := filepath.Join(physical, name)
		require.NoError(t, os.WriteFile(filename, []byte("content"), 0644))
		info, err := os.Stat(filename)
		require.NoError(t, err)
		file := &library.File{Name: name}
		require.NoError(t, lib.SaveFile(context.Background(), file))
		require.NoError(t, db.Create(&library.FileLocation{
			FileID: file.ID, LocationID: location.ID, Path: filepath.Join("archive", "2026", "september", name),
			Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(),
		}).Error)
	}

	// Wire the public Files service around the same Library and physical Location.
	exe := executor.New(db, lib, nil, executor.Paths{Access: []executor.AccessRange{{Root: root}}, Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: location.ID, Path: "archive/2026/september"}}}
	return &filesService{api: New(lib, exe)}, directory, db
}

// benchmarkIgnoreRules is a realistic Location Ignore rule set with many rules.
var benchmarkIgnoreRules = func() string {
	var builder strings.Builder
	builder.WriteString("*.tmp\ncache/\n**/node_modules/\nthumbs.db\n*.bak\n.metadata/\n@eaDir/\n#recycle/\n")
	for index := 0; index < 42; index++ {
		builder.WriteString("ignore-pattern-" + string(rune('a'+index%26)) + "/\n")
	}
	return builder.String()
}()

// BenchmarkFilesListLocationComplete measures a complete listing, with 100 rows per batch.
func BenchmarkFilesListLocationComplete(b *testing.B) {
	include := []entity.FilesInclude{
		entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES,
		entity.FilesInclude_FILES_INCLUDE_STATUS,
		entity.FilesInclude_FILES_INCLUDE_OPERATIONS,
		entity.FilesInclude_FILES_INCLUDE_NAVIGATION,
	}
	for _, entries := range []int{2000, 20000} {
		for _, rules := range []struct{ name, text string }{{"no-rules", ""}, {"rules-50", benchmarkIgnoreRules}} {
			service, directory, _ := setupFilesLocationBenchmark(b, entries, 3, rules.text)
			request := &entity.ListFilesRequest{Directory: directory, BatchSize: 100, Include: include}
			ctx := context.Background()
			b.Run(fmt.Sprintf("%s/entries-%d", rules.name, entries), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for index := 0; index < b.N; index++ {
					batch, _, err := drainListFirstBatch(ctx, service, request)
					if err != nil {
						b.Fatal(err)
					}
					if len(batch) != 100 {
						b.Fatalf("got %d entries, want 100", len(batch))
					}
				}
			})
		}
	}
}

// BenchmarkFilesLocationEnumerate measures the production complete-directory collector.
func BenchmarkFilesLocationEnumerate(b *testing.B) {
	for _, count := range []int{2000, 20000} {
		b.Run(fmt.Sprintf("entries-%d", count), func(b *testing.B) {
			service, directory, _ := setupFilesLocationBenchmark(b, count, 3, "")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				read, err := service.api.exe.EnumerateLocationDirectory(context.Background(), directory.GetLocation().LocationId, directory.GetLocation().Path)
				if err != nil {
					b.Fatal(err)
				}
				if read.Len() != count {
					b.Fatalf("got %d entries, want %d", read.Len(), count)
				}
			}
		})
	}
}
