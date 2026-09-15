package library

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileSignatureEncoding(t *testing.T) {
	hash := sha256.Sum256([]byte("content"))
	signature, err := NewFileSignature(hash[:], 123)
	require.NoError(t, err)
	require.NoError(t, ValidateFileSignature(signature))
	require.Equal(t, SignatureV1Header[0], signature[0])
	require.Equal(t, hash[:], signature[1:1+sha256.Size])
	require.Equal(t, uint64(123), binary.BigEndian.Uint64(signature[1+sha256.Size:]))
}

func TestFileSignatureRejectsInvalidInput(t *testing.T) {
	_, err := NewFileSignature(make([]byte, sha256.Size-1), 1)
	require.ErrorContains(t, err, "SHA-256 length")
	_, err = NewFileSignature(make([]byte, sha256.Size), -1)
	require.ErrorContains(t, err, "file size")
	require.Error(t, ValidateFileSignature([]byte{1}))
	invalidVersion := make([]byte, 1+sha256.Size+8)
	invalidVersion[0] = 2
	require.ErrorContains(t, ValidateFileSignature(invalidVersion), "version")
}
