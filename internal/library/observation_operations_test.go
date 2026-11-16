package library

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObservationHashEnrichmentPreservesOnlyUnchangedOpaqueContent(t *testing.T) {
	for _, publisher := range []string{"single", "batch", "selected"} {
		for _, change := range []string{"unchanged", "metadata", "native", "remount", "known-hash", "explicit-signature"} {
			t.Run(publisher+"/"+change, func(t *testing.T) {
				// A recorded content identity can be valid without retaining its producer's hash.
				ctx := context.Background()
				_, lib := newTestLibrary(t)
				location := locationTestSource(t, lib)
				opaque := []byte{2, 0, 255, 17}
				before := observedTestEntry("file", "content")
				before.Signature, before.Hash = opaque, nil
				before.TrackingKeys = []*FileTrackingKey{{Kind: TrackingNative, Scope: "filesystem", KeyValue: []byte("first")}}
				if change == "known-hash" {
					hash := sha256.Sum256([]byte("different content"))
					before.Hash = hash[:]
				}
				original, err := admitOne(ctx, lib, location.ID, before)
				require.NoError(t, err)

				// Metadata/native contradictions and explicitly different opaque signatures still change content.
				after := observedTestEntry("file", "content")
				after.FileID = original.FileID
				after.TrackingKeys = []*FileTrackingKey{{Kind: TrackingNative, Scope: "filesystem", KeyValue: []byte("first")}}
				derived, err := NewFileSignature(after.Hash, after.Size)
				require.NoError(t, err)
				after.Signature = derived
				want := derived
				switch change {
				case "unchanged":
					want = opaque
				case "metadata":
					after.MtimeNS++
				case "native":
					after.TrackingKeys[0].KeyValue = []byte("replacement")
				case "remount":
					// Another scope proves nothing about the recorded inode, so unchanged metadata decides.
					after.TrackingKeys[0].Scope, after.TrackingKeys[0].KeyValue = "remounted", []byte("other inode")
					want = opaque
				case "explicit-signature":
					after.Signature = []byte("another producer identity")
					want = after.Signature
				}

				// All mutation entry points use the same content decision and retain the learned hash.
				switch publisher {
				case "single":
					_, err = admitOne(ctx, lib, location.ID, after)
				case "batch":
					_, err = lib.AdmitObservations(ctx, location.ID, []*ObservedEntry{after})
				case "selected":
					_, err = lib.PublishSelectedObservations(ctx, location.ID, observedTestManifest(after))
				}
				require.NoError(t, err)
				stored, err := lib.GetFileLocation(ctx, original.FileID)
				require.NoError(t, err)
				require.Equal(t, want, stored.Signature)
				require.Equal(t, after.Hash, stored.Hash)
			})
		}
	}
}

func TestNativeReplacedComparesOnlyWithinOneScope(t *testing.T) {
	recorded := []*FileTrackingKey{{Kind: TrackingNative, Scope: "local/fs/1", KeyValue: []byte("42"), Details: TrackingDetails{BirthNS: 7}}}
	observed := func(scope, key string, birth int64) []*FileTrackingKey {
		return []*FileTrackingKey{{Kind: TrackingNative, Scope: scope, KeyValue: []byte(key), Details: TrackingDetails{BirthNS: birth}}}
	}
	require.False(t, NativeReplaced(recorded, observed("local/fs/1", "42", 7)))
	require.True(t, NativeReplaced(recorded, observed("local/fs/1", "43", 7)))
	require.True(t, NativeReplaced(recorded, observed("local/fs/1", "42", 8)))
	require.False(t, NativeReplaced(recorded, observed("local/fs/2", "43", 8)))
	require.False(t, NativeReplaced(recorded, observed("local/fs/2", "42", 7)))
	require.False(t, NativeReplaced(recorded, nil))
	require.False(t, NativeReplaced(nil, observed("local/fs/1", "43", 7)))
}

func TestSingleAndBatchObservationPreserveTrackingDecisions(t *testing.T) {
	for _, single := range []bool{true, false} {
		name := "batch"
		if single {
			name = "single"
		}
		t.Run(name, func(t *testing.T) {
			// Both entry points publish the same observation and preserve the caller's evidence values.
			ctx := context.Background()
			_, lib := newTestLibrary(t)
			location := locationTestSource(t, lib)
			key := &FileTrackingKey{Kind: TrackingNative, Scope: "filesystem", KeyValue: []byte("native")}
			entry := observedTestEntry("file", "content")
			entry.TrackingKeys = []*FileTrackingKey{key}
			admit := func() (*FileLocation, error) {
				if single {
					return admitOne(ctx, lib, location.ID, entry)
				}
				rows, err := lib.AdmitObservations(ctx, location.ID, []*ObservedEntry{entry})
				if err != nil {
					return nil, err
				}
				return rows[0], nil
			}
			original, err := admit()
			require.NoError(t, err)
			require.Zero(t, key.FileID)
			require.Zero(t, key.LocationID)
			require.Zero(t, key.ObservedAtNS)
			before, err := lib.GetLocation(ctx, location.ID)
			require.NoError(t, err)

			// Missing tracking input retains evidence and an unchanged observation does not publish a revision.
			entry.TrackingKeys = nil
			_, err = admit()
			require.NoError(t, err)
			tracking, err := lib.ReadFileTrackingKeys(ctx, []int64{original.FileID})
			require.NoError(t, err)
			keys := tracking[original.FileID]
			require.Len(t, keys, 1)
			after, err := lib.GetLocation(ctx, location.ID)
			require.NoError(t, err)
			require.Equal(t, before.Revision, after.Revision)

			// An explicit empty replacement clears evidence while keeping the same original and File.
			entry.TrackingKeys = []*FileTrackingKey{}
			cleared, err := admit()
			require.NoError(t, err)
			require.Equal(t, original.FileID, cleared.FileID)
			tracking, err = lib.ReadFileTrackingKeys(ctx, []int64{original.FileID})
			require.NoError(t, err)
			keys = tracking[original.FileID]
			require.Empty(t, keys)
		})
	}
}

func admitOne(ctx context.Context, lib *Library, locationID int64, p *ObservedEntry) (*FileLocation, error) {
	originals, err := lib.AdmitObservations(ctx, locationID, []*ObservedEntry{p})
	if err != nil {
		return nil, err
	}
	return originals[0], nil
}
