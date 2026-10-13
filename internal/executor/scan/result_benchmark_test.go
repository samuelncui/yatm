package scan

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	previewpkg "github.com/samuelncui/yatm/internal/preview"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type resultPreviewer struct {
	*previewpkg.Manager
	calls int64
}

func (p *resultPreviewer) Exists(signature []byte) (bool, error) {
	p.calls++
	return p.Manager.Exists(signature)
}

type resultPageCase struct {
	name    string
	request *entity.ListScanJobEntriesRequest
	first   int64
	step    int64
}

func resultPageCases(id int64, count int) []resultPageCase {
	return []resultPageCase{
		{"first_total", &entity.ListScanJobEntriesRequest{Id: id, Limit: 100, IncludeTotal: true}, 1, 1},
		{"forward_cursor", &entity.ListScanJobEntriesRequest{
			Id: id, Limit: 100, Cursor: strconv.Itoa(count / 2)}, int64(count/2 + 1), 1},
		{"reverse_cursor", &entity.ListScanJobEntriesRequest{
			Id: id, Limit: 100, Cursor: strconv.Itoa(count/2 + 1),
			Order: entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING}, int64(count / 2), -1},
		{"far_offset", &entity.ListScanJobEntriesRequest{
			Id: id, Limit: 100, Offset: proto.Int64(int64(count - 200))}, int64(count - 199), 1},
	}
}

func newResultFixture(t testing.TB, count int) (*inputFixture, *runner, *resultPreviewer) {
	// Reuse catalog and Location setup without creating a filesystem-sized source tree.
	t.Helper()
	f := newInputFixture(t, 0, 1)
	ctx := context.Background()
	created, err := Create(ctx, f.exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{
		Selections:      scanLocationSelections(f.locations[0].ID),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
	}})
	require.NoError(t, err)
	job := waitInputJob(t, f.exe, created.Job.Id)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	value, err := f.exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	r := value.(*runner)
	t.Cleanup(func() {
		_, err := f.exe.DeleteJobs(ctx, false, job.ID)
		require.NoError(t, err)
	})

	// Populate a completed synthetic manifest in bounded batches, outside every timed listing.
	require.NoError(t, r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM entries").Error; err != nil {
			return err
		}
		for start := 0; start < count; start += batchSize {
			rows := make([]*Entry, 0, batchSize)
			for index := start; index < min(start+batchSize, count); index++ {
				name := fmt.Sprintf("folder/result-%08d.jpg", index)
				hash := sha256.Sum256([]byte(name))
				rows = append(rows, &Entry{ID: int64(index + 1), LocationID: f.locations[0].ID,
					Path: name, Size: 4096, Mode: 0644, SHA256: hash[:],
					After: &entity.ObservedEntry{Path: name, SizeBytes: 4096, Sha256: hash[:]},
				})
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		return nil
	}))

	// Real negative Exists lookups include signature validation, path construction and os.Stat.
	manager, err := previewpkg.New(previewpkg.Config{Root: t.TempDir()}, "")
	require.NoError(t, err)
	return f, r, &resultPreviewer{Manager: manager}
}

func newResultService(f *inputFixture, previews *resultPreviewer) *service {
	return &service{exe: executor.New(f.db, f.exe.Lib(), nil, f.exe.Paths(), executor.Scripts{}, previews)}
}

func closeResultService(t testing.TB, s *service, id int64) {
	// Each disposable executor owns only its opened runner, not the shared fixture catalog.
	t.Helper()
	r, err := s.exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, r.Close())
}

func checkResultPage(t testing.TB, page *entity.ListScanJobEntriesResponse, c resultPageCase,
	count int, preview bool,
) {
	// Verify the full requested window so a faster but incomplete result cannot pass the harness.
	t.Helper()
	require.Len(t, page.Entries, int(c.request.Limit))
	require.True(t, page.HasMore)
	if c.request.IncludeTotal {
		require.EqualValues(t, count, page.GetTotalEntryCount())
	} else {
		require.Nil(t, page.TotalEntryCount)
	}
	outcome := entity.ScanPreviewOutcome_SCAN_PREVIEW_OUTCOME_NOT_REQUESTED
	if preview {
		outcome = entity.ScanPreviewOutcome_SCAN_PREVIEW_OUTCOME_SKIPPED
	}
	for index, row := range page.Entries {
		require.Equal(t, c.first+int64(index)*c.step, row.Id)
		require.Equal(t, outcome, row.Preview)
	}
}

// BenchmarkScanListEntries measures the public service handler, including Job hydration and
// optional real Preview Exists calls, but not RPC serialization, fixture creation or cleanup.
// Reopened means an empty executor runner cache, not a cold filesystem or process cache.
func BenchmarkScanListEntries(b *testing.B) {
	// Build the 100k manifest once; subbenchmarks never time Scan creation or content processing.
	const count = 100000
	f, r, previews := newResultFixture(b, count)
	ctx := context.Background()
	for _, preview := range []bool{false, true} {
		b.Run(fmt.Sprintf("preview_%t", preview), func(b *testing.B) {
			// Change only synthetic fixture policy; generation is never invoked by a result read.
			var config Config
			require.NoError(b, r.db.First(&config, 1).Error)
			config.Spec.PreviewPolicy = entity.PreviewPolicy_PREVIEW_POLICY_NONE
			if preview {
				config.Spec.PreviewPolicy = entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY
			}
			require.NoError(b, r.db.Save(&config).Error)
			for _, reopened := range []bool{false, true} {
				for _, c := range resultPageCases(r.job.ID, count) {
					b.Run(fmt.Sprintf("reopened_%t/%s", reopened, c.name), func(b *testing.B) {
						// Warm and validate a complete public request before measuring repeated reads.
						s := newResultService(f, previews)
						page, err := s.ListEntries(ctx, c.request)
						require.NoError(b, err)
						checkResultPage(b, page, c, count, preview)
						if reopened {
							closeResultService(b, s, r.job.ID)
						} else {
							defer closeResultService(b, s, r.job.ID)
						}

						// Include opening the runner inside ListEntries; exclude executor allocation and teardown.
						previews.calls = 0
						b.ReportAllocs()
						for b.Loop() {
							if reopened {
								b.StopTimer()
								s = newResultService(f, previews)
								b.StartTimer()
							}
							page, err = s.ListEntries(ctx, c.request)
							if err != nil {
								b.Fatal(err)
							}
							if reopened {
								b.StopTimer()
								closeResultService(b, s, r.job.ID)
								b.StartTimer()
							}
						}

						// Check results and report observed enrichment calls outside the measured interval.
						checkResultPage(b, page, c, count, preview)
						b.ReportMetric(float64(len(page.Entries)), "rows/op")
						b.ReportMetric(float64(previews.calls)/float64(b.N), "preview-exists/op")
					})
				}
			}
		})
	}
}

func TestScanListEntriesBenchmarkFixture(t *testing.T) {
	// Exercise the benchmark fixtures and page assertions cheaply, without running a timing loop.
	const count = 400
	f, r, previews := newResultFixture(t, count)
	var config Config
	require.NoError(t, r.db.First(&config, 1).Error)
	config.Spec.PreviewPolicy = entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY
	require.NoError(t, r.db.Save(&config).Error)
	for _, c := range resultPageCases(r.job.ID, count) {
		t.Run(c.name, func(t *testing.T) {
			// Two reads cover first runner construction and reuse against the same retained manifest.
			s := newResultService(f, previews)
			defer closeResultService(t, s, r.job.ID)
			previews.calls = 0
			for range 2 {
				page, err := s.ListEntries(context.Background(), c.request)
				require.NoError(t, err)
				checkResultPage(t, page, c, count, true)
			}
			require.EqualValues(t, 2*c.request.Limit, previews.calls)
		})
	}
}
