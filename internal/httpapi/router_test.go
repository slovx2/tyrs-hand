package httpapi

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWebhookRouterSeparation(t *testing.T) {
	server := &Server{}

	combined := routeSet(server.Router())
	require.Contains(t, combined, "POST /webhooks/github")
	require.Contains(t, combined, "GET /api/v1/setup/status")

	admin := routeSet(server.AdminRouter())
	require.NotContains(t, admin, "POST /webhooks/github")
	require.Contains(t, admin, "GET /api/v1/setup/status")
	require.Contains(t, admin, "POST /internal/v1/tools/call")

	webhook := routeSet(server.WebhookRouter())
	require.Contains(t, webhook, "POST /webhooks/github")
	require.Contains(t, webhook, "GET /healthz")
	require.NotContains(t, webhook, "GET /api/v1/setup/status")
	require.NotContains(t, webhook, "POST /internal/v1/tools/call")
}

func TestRateLimitPoliciesUseIndependentBuckets(t *testing.T) {
	tests := []struct {
		path   string
		bucket string
		limit  int64
	}{
		{path: "/api/v1/auth/login", bucket: "auth-login", limit: 10},
		{path: "/api/v1/setup/admin", bucket: "setup-admin", limit: 10},
		{path: "/webhooks/github", bucket: "github-webhook", limit: 300},
		{path: "/api/v1/setup/status", bucket: "default", limit: 600},
	}
	for _, test := range tests {
		bucket, limit := rateLimitPolicy(test.path)
		require.Equal(t, test.bucket, bucket)
		require.Equal(t, test.limit, limit)
	}
}

func routeSet(handler http.Handler) map[string]bool {
	engine := handler.(*gin.Engine)
	result := make(map[string]bool, len(engine.Routes()))
	for _, route := range engine.Routes() {
		result[route.Method+" "+route.Path] = true
	}
	return result
}
