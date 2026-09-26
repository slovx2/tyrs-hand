//go:build integration

package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// 只改变真实 HTTP 的到达顺序，404 必须由真实 Control 返回。
func newConfirmationRegistrationGate(t *testing.T, target string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	upstream, err := url.Parse(target)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	confirmedBeforeRegistration := make(chan struct{})
	var once sync.Once
	var registrationWaiting atomic.Bool
	rejected := &atomic.Int64{}
	proxy.ModifyResponse = func(response *http.Response) error {
		path := response.Request.URL.Path
		if strings.HasPrefix(path, "/worker/v1/runs/") && strings.HasSuffix(path, "/confirm") &&
			response.StatusCode == http.StatusNotFound && registrationWaiting.Load() {
			rejected.Add(1)
			once.Do(func() { close(confirmedBeforeRegistration) })
		}
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/worker/v1/inputs/decide" {
			registrationWaiting.Store(true)
			select {
			case <-confirmedBeforeRegistration:
			case <-request.Context().Done():
				return
			}
		}
		proxy.ServeHTTP(w, request)
	}))
	t.Cleanup(server.Close)
	return server, rejected
}
