package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/internal/buildinfo"
	"github.com/stretchr/testify/require"
)

func TestVersionDoesNotPrepareRemoteConnection(t *testing.T) {
	// Invalid connection settings must have no effect on local binary inspection.
	t.Setenv("YATM_SERVER", "not a valid server address")
	t.Setenv("YATM_BASIC_PASSWORD_FILE", "/nonexistent/credentials")
	for _, option := range []string{"--version", "-version"} {
		var stdout, stderr bytes.Buffer
		require.Equal(t, exitSuccess, run([]string{option}, strings.NewReader(""), &stdout, &stderr))
		require.Empty(t, stderr.String())
		var value map[string]string
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &value))
		require.Equal(t, "yatm-cli", value["program"])
		require.Equal(t, buildinfo.Version, value["version"])
		require.Equal(t, buildinfo.Commit, value["commit"])
	}
}
