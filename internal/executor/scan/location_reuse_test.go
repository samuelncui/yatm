package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestScanReusesRecordedHashOfRenamedOriginal(t *testing.T) {
	// A rename keeps the native identity, so the recorded hash is reused instead of reading the file.
	f := newInputFixture(t, 1, 1)
	ctx := context.Background()
	source := f.locations[0]
	var old library.FileLocation
	require.NoError(t, f.db.First(&old).Error)
	keys, err := f.exe.Lib().ReadFileTrackingKeys(ctx, []int64{old.FileID})
	require.NoError(t, err)
	if len(keys[old.FileID]) == 0 {
		t.Skip("temporary directory filesystem has no stable identity")
	}
	hash := make([]byte, 32)
	hash[0] = 7
	old.Hash, old.Signature = hash, []byte("opaque")
	require.NoError(t, f.db.Save(&old).Error)
	renamed := filepath.Join(source.RootPath, "selected", "renamed")
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, filepath.FromSlash(old.Path)), renamed))

	r := newProgressRunner(t)
	r.exe = f.exe
	stage := &locationStage{runner: r, source: source}
	info, err := executor.Lstat(renamed)
	require.NoError(t, err)
	row, err := stage.observation("selected/renamed", "", info, entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING)
	require.NoError(t, err)
	require.True(t, row.NeedsHash)
	require.NoError(t, stage.reuseRelocatedContent(ctx, []*Entry{row}))
	require.False(t, row.NeedsHash)
	require.Equal(t, hash, row.SHA256)
	require.Equal(t, []byte("opaque"), row.Signature)

	// Changed metadata keeps the read: the same object may hold other content now.
	row, err = stage.observation("selected/renamed", "", info, entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING)
	require.NoError(t, err)
	row.MtimeNS++
	require.NoError(t, stage.reuseRelocatedContent(ctx, []*Entry{row}))
	require.True(t, row.NeedsHash)
	require.Empty(t, row.SHA256)
}
