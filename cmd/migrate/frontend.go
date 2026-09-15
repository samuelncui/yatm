package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/samuelncui/yatm/config"
	"golang.org/x/net/html"
)

func checkFrontend(ctx context.Context, conf *config.Config, directory string) error {
	// Match the rendered entry document, including the server's configured API base.
	serverURL, err := localServerURL(conf.Listen)
	if err != nil {
		return err
	}
	expected, err := os.ReadFile(filepath.Join(directory, "index.html"))
	if err != nil {
		return err
	}
	expected = bytes.ReplaceAll(expected, []byte("%%API_BASE%%"), []byte(conf.Domain+"/services"))
	actual, err := readFrontend(ctx, serverURL+"/")
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("served frontend does not match this release and configuration")
	}

	// A served HTML shell alone is insufficient; verify each initial local script as well.
	scripts := 0
	tokens := html.NewTokenizer(bytes.NewReader(actual))
	for {
		if tokens.Next() == html.ErrorToken {
			if err := tokens.Err(); err != io.EOF {
				return err
			}
			break
		}
		token := tokens.Token()
		if token.Type != html.StartTagToken || token.Data != "script" {
			continue
		}
		for _, attribute := range token.Attr {
			if attribute.Key != "src" {
				continue
			}
			asset := attribute.Val
			if !strings.HasPrefix(asset, "/assets/") || path.Clean(asset) != asset || strings.ContainsAny(asset, "?#") {
				return fmt.Errorf("frontend entry contains an unsupported script reference")
			}
			expected, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(asset)))
			if err != nil {
				return err
			}
			actual, err := readFrontend(ctx, serverURL+asset)
			if err != nil {
				return err
			}
			if !bytes.Equal(actual, expected) {
				return fmt.Errorf("served frontend script does not match this release")
			}
			scripts++
		}
	}
	if scripts == 0 {
		return fmt.Errorf("frontend entry has no initial script")
	}
	return nil
}

func readFrontend(ctx context.Context, address string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("frontend readiness returned HTTP %d", response.StatusCode)
	}
	const limit = 32 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if len(data) > limit {
		return nil, fmt.Errorf("frontend readiness resource is too large")
	}
	return data, err
}
