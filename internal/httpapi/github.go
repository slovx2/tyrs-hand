package httpapi

func githubAppManifest(publicURL, appName string) map[string]any {
	return map[string]any{
		"name": appName, "url": publicURL,
		"hook_attributes": map[string]any{"url": publicURL + "/webhooks/github", "active": true},
		"redirect_url":    publicURL + "/api/v1/github/app/manifest/callback",
		"public":          false,
		"default_permissions": map[string]string{
			"metadata": "read", "contents": "write", "issues": "write", "pull_requests": "write", "actions": "read", "checks": "read", "members": "read",
		},
		"default_events": []string{"repository", "member", "membership", "organization", "team", "team_add",
			"issues", "issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment", "push"},
	}
}
