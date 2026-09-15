package buildinfo

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfflineVersion(t *testing.T) {
	// Recognize only a standalone query, never hide additional requested actions.
	for _, args := range [][]string{{"--version"}, {"-version"}} {
		require.True(t, IsVersion(args))
	}
	for _, args := range [][]string{nil, {"version"}, {"--version", "status"}} {
		require.False(t, IsVersion(args))
	}

	// Return the same release identity in a parseable single-line response.
	var output bytes.Buffer
	require.NoError(t, Write(&output, "yatm-cli"))
	var value map[string]string
	require.NoError(t, json.Unmarshal(output.Bytes(), &value))
	require.Equal(t, map[string]string{"program": "yatm-cli", "version": Version, "commit": Commit}, value)
	output.Reset()
	require.NoError(t, Write(&output, "yatm-httpd"))
	require.Contains(t, output.String(), `"program":"yatm-httpd"`)
}
