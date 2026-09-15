package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/config"
	"github.com/stretchr/testify/require"
)

func TestFrontendReadinessChecksRenderedHTMLAndAssets(t *testing.T) {
	directory := t.TempDir()
	template := `<html><script>window.apiBase="%%API_BASE%%"</script><script type="module" src="/assets/main.js"></script></html>`
	require.NoError(t, os.WriteFile(filepath.Join(directory, "index.html"), []byte(template), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(directory, "assets"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "assets", "main.js"), []byte("release entry"), 0600))
	conf := &config.Config{Domain: "http://configured.example:8080"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/assets/main.js" {
			_, _ = io.WriteString(w, "release entry")
			return
		}
		_, _ = io.WriteString(w, strings.ReplaceAll(template, "%%API_BASE%%", conf.Domain+"/services"))
	}))
	defer server.Close()
	conf.Listen = server.URL
	require.NoError(t, checkFrontend(context.Background(), conf, directory))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "assets", "main.js"), []byte("another release"), 0600))
	require.ErrorContains(t, checkFrontend(context.Background(), conf, directory), "script does not match")
}
