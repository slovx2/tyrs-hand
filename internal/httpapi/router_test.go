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

func routeSet(handler http.Handler) map[string]bool {
	engine := handler.(*gin.Engine)
	result := make(map[string]bool, len(engine.Routes()))
	for _, route := range engine.Routes() {
		result[route.Method+" "+route.Path] = true
	}
	return result
}
