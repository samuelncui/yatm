package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
)

// BenchmarkScanInputManifest uses the same public Job creation, completion and result queries on
// both source revisions. Run in an exclusively reserved window with -benchtime=1x -count=1;
// fixture construction and Job deletion are outside the measured interval.
func BenchmarkScanInputManifest(b *testing.B) {
	for _, count := range []int{10000, 100000} {
		for _, locations := range []int{1, 3, 9} {
			b.Run(fmt.Sprintf("logical/%d/%d", locations, count), func(b *testing.B) {
				f := newInputFixture(b, count, locations)
				benchmarkScanInput(b, f, f.spec(), count)
			})
		}
		b.Run(fmt.Sprintf("partial/1/%d", count), func(b *testing.B) {
			// A small explicit selection still publishes against a large pre-existing Location.
			f := newInputFixture(b, count, 1)
			spec := f.spec()
			spec.Selections = nil
			for i := 0; i < 16; i++ {
				spec.Selections = append(spec.Selections, &entity.FileSelection{Target: &entity.FileSelection_Location{
					Location: &entity.LocationSelection{LocationId: f.locations[0].ID, Path: fmt.Sprintf("selected/file-%08d", i)}}})
			}
			spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS
			benchmarkScanInput(b, f, spec, 16)
		})
		b.Run(fmt.Sprintf("relocation/3/%d", count), func(b *testing.B) {
			// Renamed originals exercise the compact candidate IDs and all three global matching rounds.
			f := newInputFixture(b, count, 3)
			for i := 0; i < count; i += 32 {
				directory := filepath.Join(f.locations[i%3].RootPath, "selected")
				require.NoError(b, os.Rename(filepath.Join(directory, fmt.Sprintf("file-%08d", i)),
					filepath.Join(directory, fmt.Sprintf("moved-%08d", i))))
			}
			spec := f.spec()
			spec.Selections = nil
			for _, source := range f.locations {
				spec.Selections = append(spec.Selections, &entity.FileSelection{Target: &entity.FileSelection_Location{
					Location: &entity.LocationSelection{LocationId: source.ID, Path: "selected"}}})
			}
			spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS
			benchmarkScanInput(b, f, spec, count+(count+31)/32)
		})
	}
}

func benchmarkScanInput(b *testing.B, f *inputFixture, spec *entity.ScanJobSpec, count int) {
	// Separate API return, completed execution and first result page while retaining allocation totals.
	ctx := context.Background()
	var createTime, completeTime, pageTime time.Duration
	iterations := 0
	b.ReportAllocs()
	for b.Loop() {
		if iterations != 0 {
			b.Fatal("fresh-catalog Scan measurements require -benchtime=1x")
		}
		iterations++
		started := time.Now()
		created, err := Create(ctx, f.exe, &entity.CreateScanJobRequest{Spec: spec})
		if err != nil {
			b.Fatal(err)
		}
		createTime += time.Since(started)
		job := waitInputJob(b, f.exe, created.Job.Id)
		completeTime += time.Since(started)
		if job.Status != entity.JobStatus_JOB_STATUS_COMPLETED {
			b.Fatalf("Scan failed: %s", job.Error)
		}
		pageStarted := time.Now()
		page, err := (&service{exe: f.exe}).ListEntries(ctx, &entity.ListScanJobEntriesRequest{
			Id: job.ID, Limit: 100, IncludeTotal: true})
		pageTime += time.Since(pageStarted)
		if err != nil || len(page.GetEntries()) != min(count, 100) || page.GetTotalEntryCount() != int64(count) {
			b.Fatalf("incomplete Scan result: entries=%d total=%d error=%v", len(page.GetEntries()), page.GetTotalEntryCount(), err)
		}

		// Cleanup belongs to the harness, never the operation's measured result or its next iteration.
		b.StopTimer()
		if _, err := f.exe.DeleteJobs(ctx, false, job.ID); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
	b.ReportMetric(float64(createTime.Nanoseconds())/float64(b.N), "create-ns/op")
	b.ReportMetric(float64(completeTime.Nanoseconds())/float64(b.N), "complete-ns/op")
	b.ReportMetric(float64(pageTime.Nanoseconds())/float64(b.N), "page-ns/op")
}

func waitInputJob(t testing.TB, exe *executor.Executor, id int64) *executor.Job {
	// Poll only active state; do not add catalog/progress reads to the measured execution path.
	t.Helper()
	deadline := time.Now().Add(30 * time.Minute)
	for exe.IsRunning(id) {
		if time.Now().After(deadline) {
			_ = exe.Cancel(id)
			t.Fatal("Scan did not finish within 30 minutes")
		}
		time.Sleep(time.Millisecond)
	}
	job, err := exe.GetJob(context.Background(), id)
	require.NoError(t, err)
	return job
}

func TestScanInputMillionStructural(t *testing.T) {
	if os.Getenv("YATM_SCAN_PERF_MILLION") != "1" {
		t.Skip("set YATM_SCAN_PERF_MILLION=1 in the reserved performance window")
	}

	// One large logical selection proves manifest completeness through bounded public result pages.
	const count = 1000000
	f := newInputFixture(t, count, 9)
	ctx := context.Background()
	created, err := Create(ctx, f.exe, &entity.CreateScanJobRequest{Spec: f.spec()})
	require.NoError(t, err)
	job := waitInputJob(t, f.exe, created.Job.Id)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	service := &service{exe: f.exe}
	var after int64
	total := 0
	for {
		request := &entity.ListScanJobEntriesRequest{Id: job.ID, Limit: 1000, IncludeTotal: total == 0}
		if after > 0 {
			request.Cursor = strconv.FormatInt(after, 10)
		}
		page, err := service.ListEntries(ctx, request)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Entries), 1000)
		if total == 0 {
			require.EqualValues(t, count, page.GetTotalEntryCount())
		}
		for _, row := range page.Entries {
			require.Greater(t, row.Id, after)
			require.NoError(t, entity.ValidateRelativePath(row.Path))
			after = row.Id
			total++
		}
		if !page.HasMore {
			break
		}
		require.NotEmpty(t, page.Entries)
	}
	require.Equal(t, count, total)
	_, err = f.exe.DeleteJobs(ctx, false, job.ID)
	require.NoError(t, err)
}
