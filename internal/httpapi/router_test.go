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
	require.Contains(t, combined, "GET /api/v1/ssh/credentials")
	require.Contains(t, combined, "PUT /api/v1/ssh/credentials/:id")
	require.Contains(t, combined, "GET /api/v1/ssh/hosts")
	require.Contains(t, combined, "PUT /api/v1/settings/global-agents")
	require.Contains(t, combined, "GET /worker/v1/ssh-configuration")

	admin := routeSet(server.AdminRouter())
	require.NotContains(t, admin, "POST /webhooks/github")
	require.Contains(t, admin, "GET /api/v1/setup/status")
	require.Contains(t, admin, "POST /internal/v1/tools/call")
	require.Contains(t, admin, "DELETE /api/v1/ssh/hosts/:id")
	require.Contains(t, admin, "GET /api/v1/settings/global-agents")

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
		{path: "/worker/v1/runs/run-id/events", bucket: "worker-api", limit: 10000},
		{path: "/worker/v1/heartbeat", bucket: "worker-api", limit: 10000},
		{path: "/api/v1/setup/status", bucket: "default", limit: 600},
	}
	for _, test := range tests {
		bucket, limit := rateLimitPolicy(test.path)
		require.Equal(t, test.bucket, bucket)
		require.Equal(t, test.limit, limit)
	}
}

func TestValidateTriggerRule(t *testing.T) {
	tests := []struct {
		name    string
		request triggerRuleRequest
		valid   bool
		action  string
	}{
		{name: "event", request: triggerRuleRequest{TriggerKind: "event", EventName: "push"}, valid: true},
		{name: "slash command", request: triggerRuleRequest{TriggerKind: "slash_command", TriggerValue: "tyrs-hand", EventName: "issue_comment"}, valid: true, action: "created"},
		{name: "mention command", request: triggerRuleRequest{TriggerKind: "mention_command", EventName: "issue_comment"}, valid: true, action: "created"},
		{name: "label", request: triggerRuleRequest{TriggerKind: "label", TriggerValue: "tyrs-hand", EventName: "issues"}, valid: true, action: "labeled"},
		{name: "legacy mention", request: triggerRuleRequest{TriggerKind: "legacy_mention", EventName: "pull_request_review_comment"}, valid: true, action: "created"},
		{name: "slash missing value", request: triggerRuleRequest{TriggerKind: "slash_command", EventName: "issue_comment"}},
		{name: "slash with slash", request: triggerRuleRequest{TriggerKind: "slash_command", TriggerValue: "/tyrs-hand", EventName: "issue_comment"}},
		{name: "mention with value", request: triggerRuleRequest{TriggerKind: "mention_command", TriggerValue: "tyrshand", EventName: "issue_comment"}},
		{name: "mention wrong event", request: triggerRuleRequest{TriggerKind: "mention_command", EventName: "pull_request_review_comment"}},
		{name: "mention edited", request: triggerRuleRequest{TriggerKind: "mention_command", EventName: "issue_comment", Action: "edited"}},
		{name: "label wrong event", request: triggerRuleRequest{TriggerKind: "label", TriggerValue: "tyrs-hand", EventName: "issue_comment"}},
		{name: "event with value", request: triggerRuleRequest{TriggerKind: "event", TriggerValue: "unexpected", EventName: "push"}},
		{name: "unknown", request: triggerRuleRequest{TriggerKind: "unknown", EventName: "issues"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTriggerRule(&test.request)
			if test.valid {
				require.NoError(t, err)
				require.Equal(t, test.action, test.request.Action)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestLegacyMentionIsDisabledByDefault(t *testing.T) {
	require.False(t, triggerRuleEnabled(triggerRuleRequest{TriggerKind: "legacy_mention"}))
	require.True(t, triggerRuleEnabled(triggerRuleRequest{TriggerKind: "slash_command"}))
	require.True(t, triggerRuleEnabled(triggerRuleRequest{TriggerKind: "mention_command"}))
	enabled := true
	require.True(t, triggerRuleEnabled(triggerRuleRequest{TriggerKind: "legacy_mention", Enabled: &enabled}))
}

func routeSet(handler http.Handler) map[string]bool {
	engine := handler.(*gin.Engine)
	result := make(map[string]bool, len(engine.Routes()))
	for _, route := range engine.Routes() {
		result[route.Method+" "+route.Path] = true
	}
	return result
}
