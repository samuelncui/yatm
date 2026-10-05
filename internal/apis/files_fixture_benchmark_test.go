package apis

import (
	"strconv"
	"testing"
)

// Fixture creation and catalog seeding are measured separately from List/Search execution.
func BenchmarkFilesFixtureSetup(b *testing.B) {
	for _, count := range []int{10000, 100000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			// Each iteration owns fresh mutable data; filesystem cleanup follows the timed work.
			for b.Loop() {
				setupFilesListMixedBenchmark(b, count, "random", 16, filesPerformanceRules())
			}
		})
	}
}
