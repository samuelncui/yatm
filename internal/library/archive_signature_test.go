package library

import (
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestArchiveKeepsRecordedSignatureOnlyForTheSameContent(t *testing.T) {
	hash, other := make([]byte, 32), make([]byte, 32)
	other[0] = 1
	signature := []byte{2, 0, 255, 17}
	for _, test := range []struct {
		name     string
		expected *entity.ExpectedFile
		keep     bool
	}{
		{name: "unknown content", expected: &entity.ExpectedFile{SizeBytes: 5}},
		{name: "same hash", expected: &entity.ExpectedFile{Signature: signature, Sha256: hash, SizeBytes: 5}, keep: true},
		{name: "other hash", expected: &entity.ExpectedFile{Signature: signature, Sha256: other, SizeBytes: 5}},
		{name: "opaque without hash", expected: &entity.ExpectedFile{Signature: signature, SizeBytes: 5}, keep: true},
		{name: "other size", expected: &entity.ExpectedFile{Signature: signature, SizeBytes: 6}},
	} {
		require.Equal(t, test.keep, KeepsSignature(test.expected, 5, hash), test.name)
	}
}
