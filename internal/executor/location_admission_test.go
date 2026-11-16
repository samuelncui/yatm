package executor

import (
	"testing"

	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestAdmissionNativeMatchingStaysWithinOneScope(t *testing.T) {
	// Inode numbers are meaningful only within their scope, so another device cannot claim the recorded File.
	candidate := &library.FileTrackingKey{FileID: 1, Kind: library.TrackingNative, Scope: "local/fs/1", KeyValue: []byte("42")}
	observed := func(scope string) *library.ObservedEntry {
		return &library.ObservedEntry{TrackingKeys: []*library.FileTrackingKey{{Kind: library.TrackingNative, Scope: scope, KeyValue: []byte("42")}}}
	}
	require.True(t, admissionEvidenceMatches(library.TrackingNative, candidate, observed("local/fs/1")))
	require.False(t, admissionEvidenceMatches(library.TrackingNative, candidate, observed("local/fs/2")))
}
