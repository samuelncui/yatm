//go:build linux || darwin

package media

import (
	"runtime"
	"syscall"
	"testing"
)

func reportReadSequenceRSS(b *testing.B) {
	b.Helper()
	// This is the process high-water mark, including preceding cases and native SQLite allocations.
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		b.Fatal(err)
	}
	bytes := uint64(usage.Maxrss)
	if runtime.GOOS == "linux" {
		bytes *= 1024
	}
	b.ReportMetric(float64(bytes), "process-highwater-RSS-B")
}
