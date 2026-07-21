package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
)

func TestWorkerSourceIPOnlyTrustsForwardedHeaderFromTrustedProxy(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("10.0.0.0/8")}
	request := httptest.NewRequest("GET", "/worker/v1/heartbeat", nil)
	request.RemoteAddr = "127.0.0.1:9000"
	request.Header.Set("X-Forwarded-For", "203.0.113.8, 10.1.2.3")
	address, err := workerSourceIP(request, trusted)
	require.NoError(t, err)
	require.Equal(t, "203.0.113.8", address.String())

	request.RemoteAddr = "198.51.100.5:9000"
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	address, err = workerSourceIP(request, trusted)
	require.NoError(t, err)
	require.Equal(t, "198.51.100.5", address.String())
}

func TestWorkerIPAllowlistSupportsSingleAddressAndCIDR(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("203.0.113.8/32"),
		netip.MustParsePrefix("2001:db8:1234::/48")}
	require.True(t, prefixesContain(prefixes, netip.MustParseAddr("203.0.113.8")))
	require.True(t, prefixesContain(prefixes, netip.MustParseAddr("2001:db8:1234::20")))
	require.False(t, prefixesContain(prefixes, netip.MustParseAddr("203.0.113.9")))
}

func TestWorkerIPMiddlewareCoversDirectAndTrustedProxyRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := &Server{cfg: config.Config{
		WorkerAPIAllowlist: []netip.Prefix{netip.MustParsePrefix("203.0.113.8/32"),
			netip.MustParsePrefix("2001:db8:1234::/48")},
		WorkerAPITrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}}
	router := gin.New()
	router.Use(server.requireWorkerIP())
	router.GET("/worker/v1/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodGet, "/worker/v1/test", nil)
	request.RemoteAddr = "203.0.113.8:443"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code)

	request = httptest.NewRequest(http.MethodGet, "/worker/v1/test", nil)
	request.RemoteAddr = "203.0.113.9:443"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)

	request = httptest.NewRequest(http.MethodGet, "/worker/v1/test", nil)
	request.RemoteAddr = "10.10.0.2:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code)

	request = httptest.NewRequest(http.MethodGet, "/worker/v1/test", nil)
	request.RemoteAddr = "198.51.100.10:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code, "非可信来源不能伪造 X-Forwarded-For")
}
