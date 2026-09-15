package apis_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/executor"
	"github.com/stretchr/testify/require"
)

func TestUpgradeEndpointsRequireDirectLoopbackAccess(t *testing.T) {
	// Bind upgrade endpoints to the ordinary API router without a storage installation.
	api := apis.New(nil, executor.New(nil, nil, nil, executor.Paths{}, executor.Scripts{}, nil))
	handler := api.Uploader()

	// Direct local traffic can inspect the upgrade state.
	request := httptest.NewRequest(http.MethodGet, "/_upgrade/status", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)

	// Forwarded requests are not treated as trusted loopback callers.
	request = httptest.NewRequest(http.MethodGet, "/_upgrade/status", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)

	// Remote callers cannot request quiescing either.
	request = httptest.NewRequest(http.MethodPost, "/_upgrade/quiesce", nil)
	request.RemoteAddr = "192.0.2.1:12345"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
}
