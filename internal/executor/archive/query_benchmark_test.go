package archive

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

const manifestPageSize = 200

type manifestPageCase struct {
	name    string
	request *entity.ListArchiveJobFilesRequest
	first   int
	step    int
}

func manifestPath(index, count int) string {
	return fmt.Sprintf("folder-%04d/file-%08d.bin", index*min(5155, count/20)/count, index)
}

func manifestPageCases(count int) []manifestPageCase {
	return []manifestPageCase{
		{"first_total", &entity.ListArchiveJobFilesRequest{
			Limit: manifestPageSize, IncludeTotal: true}, 0, 1},
		{"forward_cursor", &entity.ListArchiveJobFilesRequest{
			Limit: manifestPageSize, Cursor: manifestPath(count/2-1, count)}, count / 2, 1},
		{"reverse_cursor", &entity.ListArchiveJobFilesRequest{
			Limit: manifestPageSize, Cursor: manifestPath(count/2, count),
			Order: entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING}, count/2 - 1, -1},
		{"far_offset", &entity.ListArchiveJobFilesRequest{
			Limit: manifestPageSize, Offset: proto.Int64(int64(count - 2*manifestPageSize))},
			count - 2*manifestPageSize, 1},
	}
}

func newManifestFixture(t testing.TB, count int) *jobArchiveRunner {
	// Use the real on-disk SQLite connection and Item schema without physical source files.
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "archive.sqlite"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&Item{}))

	// Keep seed memory bounded; preparation and writes are outside the listing timer.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		for start := 0; start < count; start += batchSize {
			rows := make([]*Item, 0, batchSize)
			for index := start; index < min(start+batchSize, count); index++ {
				path := manifestPath(index, count)
				rows = append(rows, &Item{
					ID: int64(index + 1), Status: entity.CopyStatus_COPY_STATUS_SUBMITTED,
					Size: 4096, TargetPath: path, MediaPath: path, MediaID: proto.Int64(1),
					Data: &entity.ArchiveManifestFile{
						SourcePath: "/synthetic/" + path,
						Expected: &entity.ExpectedFile{
							FileId: int64(index + 1), SizeBytes: 4096, Mode: 0644,
							Signature:          []byte("0123456789abcdef0123456789abcdef"),
							OriginalLocationId: 1,
						},
					},
				})
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	return &jobArchiveRunner{db: db}
}

func checkManifestPage(t testing.TB, page *entity.ListArchiveJobFilesResponse, c manifestPageCase, count int) {
	// Check every returned row, including decoded content facts, before accepting a timing result.
	t.Helper()
	require.Len(t, page.Items, int(c.request.Limit))
	last := c.first + (len(page.Items)-1)*c.step
	require.Equal(t, last+c.step >= 0 && last+c.step < count, page.HasMore)
	if c.request.IncludeTotal {
		require.EqualValues(t, count, page.GetTotalFileCount())
	} else {
		require.Nil(t, page.TotalFileCount)
	}
	for index, row := range page.Items {
		position := c.first + index*c.step
		path := manifestPath(position, count)
		require.EqualValues(t, position+1, row.Id)
		require.Equal(t, entity.CopyStatus_COPY_STATUS_SUBMITTED, row.Status)
		require.EqualValues(t, 4096, row.SizeBytes)
		require.EqualValues(t, 1, row.GetMediaId())
		require.Equal(t, path, row.File.TargetPath)
		require.Equal(t, path, row.File.MediaPath)
		require.Equal(t, "/synthetic/"+path, row.File.SourcePath)
		require.EqualValues(t, position+1, row.File.Expected.FileId)
		require.EqualValues(t, 4096, row.File.Expected.SizeBytes)
		require.Equal(t, []byte("0123456789abcdef0123456789abcdef"), row.File.Expected.Signature)
	}
}

// BenchmarkArchiveManifestPages measures warm manifest SQL reads and public response construction.
// It excludes creation, file copying, RPC transport/serialization and browser rendering;
// protobuf-B/op is the encoded response size, measured outside the timer.
func BenchmarkArchiveManifestPages(b *testing.B) {
	// Retain one real manifest for all page cases at each reported file count.
	for _, count := range []int{1000, 10000, 100000, 1000000} {
		b.Run(fmt.Sprintf("%d", count), func(b *testing.B) {
			runner := newManifestFixture(b, count)
			ctx := context.Background()
			for _, c := range manifestPageCases(count) {
				b.Run(c.name, func(b *testing.B) {
					// Warm and verify the full window before measuring repeated queries.
					page, err := runner.queryFiles(ctx, c.request)
					require.NoError(b, err)
					checkManifestPage(b, page, c, count)

					// Time the existing query path without correctness assertions in the loop.
					b.ReportAllocs()
					for b.Loop() {
						page, err = runner.queryFiles(ctx, c.request)
						if err != nil {
							b.Fatal(err)
						}
					}

					// Validate the final result and expose the bounded response size.
					checkManifestPage(b, page, c, count)
					b.ReportMetric(float64(len(page.Items)), "rows/op")
					b.ReportMetric(float64(proto.Size(page)), "protobuf-B/op")
				})
			}
		})
	}
}

func TestArchiveManifestPaths(t *testing.T) {
	// Every scale retains populated directories and lexical order, including the cheap fixture.
	for _, c := range []struct {
		count int
		last  string
	}{
		{800, "folder-0039/file-00000799.bin"},
		{1000, "folder-0049/file-00000999.bin"},
		{10000, "folder-0499/file-00009999.bin"},
		{100000, "folder-4999/file-00099999.bin"},
		{1000000, "folder-5154/file-00999999.bin"},
	} {
		t.Run(fmt.Sprintf("%d", c.count), func(t *testing.T) {
			require.Equal(t, "folder-0000/file-00000000.bin", manifestPath(0, c.count))
			require.Equal(t, c.last, manifestPath(c.count-1, c.count))
			require.Less(t, manifestPath(c.count/2-1, c.count), manifestPath(c.count/2, c.count))
		})
	}
}

func TestArchiveListFilesBenchmarkFixture(t *testing.T) {
	// Exercise every measured page case without seeding a large manifest in the regular suite.
	const count = 800
	runner := newManifestFixture(t, count)
	ctx := context.Background()
	for _, c := range manifestPageCases(count) {
		t.Run(c.name, func(t *testing.T) {
			page, err := runner.queryFiles(ctx, c.request)
			require.NoError(t, err)
			checkManifestPage(t, page, c, count)
		})
	}

	// The final window and empty continuation establish sentinel behavior at the boundary.
	last := manifestPageCase{"last", &entity.ListArchiveJobFilesRequest{
		Limit: manifestPageSize, Offset: proto.Int64(count - manifestPageSize)}, count - manifestPageSize, 1}
	page, err := runner.queryFiles(ctx, last.request)
	require.NoError(t, err)
	checkManifestPage(t, page, last, count)
	page, err = runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{
		Limit: manifestPageSize, Cursor: manifestPath(count-1, count),
	})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.False(t, page.HasMore)
}
