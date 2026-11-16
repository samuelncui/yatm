package executor

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func writeObservedFile(t *testing.T, name string, content []byte) (string, os.FileInfo) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(filename, content, 0o644))
	info, err := os.Lstat(filename)
	require.NoError(t, err)
	return filename, info
}

func TestContentVerificationReportsTheObservedFileAndRefreshesItsCache(t *testing.T) {
	ctx := context.Background()
	content := []byte("observed content")
	contentHash := sha256.Sum256(content)
	filename, observed := writeObservedFile(t, "file.txt", content)

	// The file is read and hashed, and the result becomes reusable cache state.
	facts, err := VerifyObservedContent(ctx, filename, observed)
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), facts.SizeBytes)
	require.Equal(t, contentHash[:], facts.Sha256)
	require.Equal(t, uint32(observed.Mode()), facts.Mode)
	require.Equal(t, observed.ModTime().UnixNano(), facts.MtimeNs)
	signature, err := library.NewFileSignature(facts.Sha256, facts.SizeBytes)
	require.NoError(t, err)
	require.Equal(t, signature, facts.Signature)
	cached, valid, err := acp.ReadCachedSignature(filename)
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, facts.Sha256, cached.SHA256[:])
}

func TestContentVerificationReadsContentInsteadOfTrustingTheCache(t *testing.T) {
	ctx := context.Background()
	filename, observed := writeObservedFile(t, "file.txt", []byte("saved"))
	_, err := VerifyObservedContent(ctx, filename, observed)
	require.NoError(t, err)

	// Equal metadata with different bytes must still be observed as the bytes on disk.
	require.NoError(t, os.WriteFile(filename, []byte("other"), 0o644))
	require.NoError(t, os.Chtimes(filename, observed.ModTime(), observed.ModTime()))
	require.NoError(t, os.Chmod(filename, observed.Mode()))
	current, err := os.Lstat(filename)
	require.NoError(t, err)
	require.Equal(t, observed.Size(), current.Size())

	otherHash := sha256.Sum256([]byte("other"))
	verified, err := VerifyObservedContent(ctx, filename, current)
	require.NoError(t, err)
	require.Equal(t, otherHash[:], verified.Sha256)
}

func TestContentObservationKeepsRetryableSourceErrors(t *testing.T) {
	ctx := context.Background()
	filename, observed := writeObservedFile(t, "file.txt", []byte("saved"))

	// A source ACP cannot describe stays an error with its original identity, never a silent gap.
	require.NoError(t, os.Remove(filename))
	_, err := VerifyObservedContent(ctx, filename, observed)
	require.ErrorIs(t, err, fs.ErrNotExist)
}
