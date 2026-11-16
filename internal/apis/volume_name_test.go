package apis

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVolumeNameUsesThePathComponentRule(t *testing.T) {
	// Names keep literal backslashes and only trim surrounding display whitespace.
	name, err := validateVolumeName(` Archive\2026 `)
	require.NoError(t, err)
	require.Equal(t, `Archive\2026`, name)

	for _, value := range []string{"", " ", ".", "..", "a/b", "a\x00b", "bad\xff"} {
		_, err := validateVolumeName(value)
		require.Error(t, err, "%q", value)
	}
}
