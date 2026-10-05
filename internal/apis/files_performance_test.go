package apis

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

// BenchmarkFilesListComplete measures first-send and completion separately on the public path.
func BenchmarkFilesListComplete(b *testing.B) {
	// Keep fixture creation outside the measured operation, including mixed catalog associations.
	for _, count := range []int{1000, 10000, 100000} {
		for _, order := range []string{"random", "reverse"} {
			b.Run(fmt.Sprintf("mixed/%s/%d", order, count), func(b *testing.B) {
				service, directory := setupFilesListMixedBenchmark(b, count, order, 4, "*.tmp\ncache/\n")
				benchmarkFilesList(b, service, directory, count)
			})
		}
	}
}

func BenchmarkFilesListRules(b *testing.B) {
	// Deep ancestors and 102 Ignore rules exercise the same complete List contract.
	for _, count := range []int{10000, 100000} {
		b.Run(fmt.Sprintf("deep/random/%d", count), func(b *testing.B) {
			service, directory := setupFilesListMixedBenchmark(b, count, "random", 16, filesPerformanceRules())
			benchmarkFilesList(b, service, directory, count-count/10+count/100)
		})
	}
}

func benchmarkFilesList(b *testing.B, service *filesService, directory *entity.FileOperationRef, count int) {
	b.Helper()
	request := filesPerformanceRequest(directory, 256)
	var first time.Duration
	b.ReportAllocs()
	for b.Loop() {
		start := time.Now()
		stream := &filesListRecorder{ctx: context.Background(), afterSend: func(batch int) {
			if batch == 0 {
				first += time.Since(start)
			}
		}}
		if err := service.List(request, stream); err != nil {
			b.Fatal(err)
		}
		if len(stream.reply.Entries) != count || stream.reply.GetTotalEntryCount() != int64(count) {
			b.Fatalf("incomplete listing: %d", len(stream.reply.Entries))
		}
	}
	b.ReportMetric(float64(first.Nanoseconds())/float64(b.N), "first-ns/op")
}

const filesPerformanceQuery = "(name:entry-*0 OR name:entry-*1) AND size:7 AND NOT name:entry-00000000"

func BenchmarkFilesSearchPage(b *testing.B) {
	// Query pages remain distinct from complete List; measure the first and next request separately.
	for _, count := range []int{10000, 100000} {
		for _, page := range []string{"first", "next"} {
			b.Run(fmt.Sprintf("%s/random/%d", page, count), func(b *testing.B) {
				service, directory := setupFilesListMixedBenchmark(b, count, "random", 16, filesPerformanceRules())
				request := &entity.SearchFilesRequest{Directory: directory, Query: filesPerformanceQuery, Limit: 100,
					Include: filesPerformanceRequest(directory, 256).Include}
				if page == "next" {
					first, err := service.Search(context.Background(), request)
					if err != nil || len(first.GetEntries()) != 100 || first.GetNextCursor() == "" {
						b.Fatalf("initial query page: %v", err)
					}
					request.Cursor = first.NextCursor
				}

				// One unary response is also the request's first response, unlike the List stream.
				var response time.Duration
				b.ReportAllocs()
				for b.Loop() {
					start := time.Now()
					reply, err := service.Search(context.Background(), request)
					response += time.Since(start)
					if err != nil || len(reply.GetEntries()) != 100 {
						b.Fatalf("query page: %v", err)
					}
				}
				b.ReportMetric(float64(response.Nanoseconds())/float64(b.N), "first-ns/op")
			})
		}
	}
}

func TestFilesListCompleteBeyondFormerCap(t *testing.T) {
	// Exercise the public stream above the old silent truncation boundary.
	service, directory, _ := setupFilesLocationBenchmark(t, 100001, 1, "")
	stream := &filesListRecorder{ctx: context.Background()}
	require.NoError(t, service.List(&entity.ListFilesRequest{Directory: directory, BatchSize: 256}, stream))
	require.EqualValues(t, 100001, stream.reply.GetTotalEntryCount())
	require.Len(t, stream.reply.Entries, 100001)
	names := make([]string, 100001)
	for index := range names {
		names[index] = fmt.Sprintf("file-%05d.txt", index)
	}
	sort.Strings(names)
	for index, entry := range stream.reply.Entries {
		require.Equal(t, names[index], entry.Name)
	}
}

func TestFilesPerformanceHarness(t *testing.T) {
	// Keep the shared fixture and non-retaining structural receiver executable in ordinary tests.
	for _, order := range []string{"random", "reverse"} {
		t.Run(order, func(t *testing.T) {
			service, directory := setupFilesListMixedBenchmark(t, 32, order, 4, "*.tmp\ncache/\n")
			stream := &filesStructuralRecorder{filesListRecorder: filesListRecorder{ctx: context.Background()}, maxBatch: 8}
			require.NoError(t, service.List(filesPerformanceRequest(directory, 8), stream))
			require.Equal(t, 32, stream.count)
			require.EqualValues(t, 32, stream.total)
			require.Equal(t, 16, stream.associated)
			require.Equal(t, 4, stream.batches)
		})
	}

	// Assert Ignore negation and the boolean query against fixture arithmetic, not the implementation's matcher.
	service, directory := setupFilesListMixedBenchmark(t, 32, "random", 16, filesPerformanceRules())
	stream := &filesStructuralRecorder{filesListRecorder: filesListRecorder{ctx: context.Background()}, maxBatch: 8}
	require.NoError(t, service.List(filesPerformanceRequest(directory, 8), stream))
	require.Equal(t, 30, stream.count)
	require.EqualValues(t, 30, stream.total)
	require.Equal(t, 16, stream.associated)
	request := &entity.SearchFilesRequest{Directory: directory, Query: filesPerformanceQuery, Limit: 2,
		Include: filesPerformanceRequest(directory, 8).Include}
	var names []string
	for {
		page, err := service.Search(context.Background(), request)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Entries), 2)
		for _, entry := range page.Entries {
			names = append(names, entry.Name)
		}
		if page.NextCursor == "" {
			break
		}
		require.NotEqual(t, request.Cursor, page.NextCursor)
		request.Cursor = page.NextCursor
	}
	require.Equal(t, []string{"entry-00000001", "entry-00000010", "entry-00000011", "entry-00000020", "entry-00000021", "entry-00000030", "entry-00000031"}, names)
}

func TestFilesListMillionStructural(t *testing.T) {
	// Release acceptance opts into real filesystem scale; regular correctness tests stay bounded.
	if os.Getenv("YATM_PERF_MILLION") != "1" {
		t.Skip("set YATM_PERF_MILLION=1 for the one-million-entry structural tier")
	}
	const count, size = 1000000, 256
	service, directory, _ := setupFilesLocationBenchmark(t, count, 4, "*.tmp\ncache/\n")
	stream := &filesStructuralRecorder{filesListRecorder: filesListRecorder{ctx: context.Background()}, maxBatch: size}
	require.NoError(t, service.List(&entity.ListFilesRequest{Directory: directory, BatchSize: size}, stream))
	require.Equal(t, count, stream.count)
	require.EqualValues(t, count, stream.total)
	require.Equal(t, (count+size-1)/size, stream.batches)
	require.Zero(t, stream.associated)
	t.Logf("complete directory: entries=%d batches=%d maximum_batch=%d; receiver retains only the previous name", stream.count, stream.batches, size)
}
