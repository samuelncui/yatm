//go:build !linux && !darwin

package media

import "testing"

func reportReadSequenceRSS(b *testing.B) {
	b.Helper()
	b.Skip("Restore resource benchmarks require Linux or Darwin process RSS accounting")
}
