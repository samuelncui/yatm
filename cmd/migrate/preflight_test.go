package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuiesceService(t *testing.T) {
	originalTransport := http.DefaultTransport
	requests := 0
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			require.Equal(t, http.MethodGet, request.Method)
			require.Equal(t, "/files/_upgrade/status", request.URL.Path)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"process_id":42,"running_job_ids":[]}`)),
				Request:    request,
			}, nil
		}
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/files/_upgrade/quiesce", request.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"running_job_ids":[7,9]}`)),
			Request:    request,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	ids, err := quiesceService(context.Background(), "http://localhost:8080", 42)
	require.NoError(t, err)
	require.Equal(t, []int64{7, 9}, ids)
	require.Equal(t, 2, requests)
}

func TestQuiesceServiceRejectsChangedProcess(t *testing.T) {
	originalTransport := http.DefaultTransport
	requests := 0
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		body := `{"process_id":42,"running_job_ids":[]}`
		if requests == 2 {
			body = `{"process_id":99,"running_job_ids":[]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	_, err := quiesceService(context.Background(), ":8080", 42)
	require.ErrorContains(t, err, "expected systemd process 42")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestUpgradeStatusURLUsesLoopbackForWildcardListen(t *testing.T) {
	endpoint, err := upgradeStatusURL(":8080")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8080/files/_upgrade/status", endpoint)
}

func TestUpgradeStatusDoesNotQuiesce(t *testing.T) {
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/files/_upgrade/status", request.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"process_id":42,"running_job_ids":[]}`)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	status, err := requestUpgradeStatus(context.Background(), ":8080", false, 42)
	require.NoError(t, err)
	require.Empty(t, status.RunningJobIDs)
}

func TestUpgradeStatusRequiresSystemdProcess(t *testing.T) {
	// A healthy response from another local process cannot satisfy installer preflight.
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"process_id":99,"running_job_ids":[]}`)),
			Header:     make(http.Header),
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	_, err := requestUpgradeStatus(context.Background(), ":8080", false, 42)
	require.ErrorContains(t, err, "expected systemd process 42")
	_, err = requestUpgradeStatus(context.Background(), ":8080", false, 0)
	require.ErrorContains(t, err, "process identity is required")
}

func TestUpgradeURLRejectsNonlocalAndRedirectSources(t *testing.T) {
	for _, address := range []string{"https://localhost:8080", "http://example.com:8080", "http://user@localhost:8080", "http://localhost:8080/?token=secret", "10.0.0.1:8080"} {
		t.Run(address, func(t *testing.T) {
			_, err := localServerURL(address)
			require.Error(t, err)
		})
	}
}
