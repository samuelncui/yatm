package scan

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestEntryMatchStoreMapsOwnedSchemaAndScope(t *testing.T) {
	// Both consumers must preserve their own table and Location scope.
	ctx := context.Background()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Entry{}))
	evidence := observation.Evidence{
		NativeScope: "executor/filesystem", NativeKey: []byte{1}, BirthNS: 7, Generation: 2,
	}
	require.NoError(t, db.Create([]*Entry{
		{LocationID: 1, Path: "b.txt", Signature: []byte("signature"), Evidence: evidence, Size: 3, SHA256: []byte("hash")},
		{LocationID: 2, Path: "a.txt", Signature: []byte("signature"), Evidence: evidence},
	}).Error)
	store := observation.NewMatchStore(db.Model(&Entry{}).Where("location_id = ?", 1))

	// Every shared rule sees only the admitted Location.
	criteria := []observation.MatchCriteria{
		{Kind: observation.MatchPath, Path: "b.txt"},
		{Kind: observation.MatchSignature, Signature: []byte("signature")},
		{Kind: observation.MatchNative, Evidence: evidence},
	}
	for _, value := range criteria {
		candidate, err := store.FindCandidate(ctx, value)
		require.NoError(t, err)
		require.NotNil(t, candidate)
		require.Equal(t, "b.txt", candidate.Path)
	}

	// Assignment updates matching evidence without touching a different Location.
	candidate, err := store.FindCandidate(ctx, criteria[0])
	require.NoError(t, err)
	require.NoError(t, store.Assign(ctx, candidate.ID, 41, []byte("carried")))
	claimed, err := store.FileClaimed(ctx, 41)
	require.NoError(t, err)
	require.True(t, claimed)

	var inside, outside Entry
	require.NoError(t, db.Where("location_id = ?", 1).First(&inside).Error)
	require.NoError(t, db.Where("location_id = ?", 2).First(&outside).Error)
	require.Equal(t, int64(41), inside.FileID)
	require.Equal(t, []byte("carried"), inside.Signature)
	require.Zero(t, outside.FileID)
}
