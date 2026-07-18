package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestVerifySignature(t *testing.T) {
	payload := []byte(`{"zen":"test"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(payload)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	require.True(t, VerifySignature([]byte("secret"), signature, payload))
	require.False(t, VerifySignature([]byte("wrong"), signature, payload))
}

func TestNormalizeIssueComment(t *testing.T) {
	event, err := NormalizeWebhook("delivery", "issue_comment", []byte(`{
		"action":"created","installation":{"id":9},
		"repository":{"id":10,"name":"repo","owner":{"login":"owner"}},
		"sender":{"id":11,"login":"alice","type":"User"},
		"issue":{"number":12,"title":"Bug","body":"body"},
		"comment":{"body":"@tyrs-hand fix this"}
	}`))
	require.NoError(t, err)
	require.Equal(t, domain.WorkItemIssue, event.Kind)
	require.Equal(t, 12, event.Number)
	require.Equal(t, "@tyrs-hand fix this", event.Body)
}

func TestNormalizePullRequestAndInstallationRepositories(t *testing.T) {
	pull, err := NormalizeWebhook("delivery-pr", "pull_request", []byte(`{
		"action":"review_requested","installation":{"id":9},
		"repository":{"id":10,"name":"repo","full_name":"owner/repo"},
		"sender":{"id":11,"login":"alice"},
		"pull_request":{"number":15,"title":"Change","body":"details","head":{"sha":"abc123"}}
	}`))
	require.NoError(t, err)
	require.Equal(t, domain.WorkItemPullRequest, pull.Kind)
	require.Equal(t, "owner", pull.Owner)
	require.Equal(t, "abc123", pull.HeadSHA)

	installation, err := NormalizeWebhook("delivery-install", "installation_repositories", []byte(`{
		"action":"added","installation":{"id":9,"account":{"login":"org","type":"Organization"}},
		"repositories_added":[{"id":20,"name":"repo","full_name":"org/repo","default_branch":"main","clone_url":"https://github.com/org/repo.git"}],
		"repositories_removed":[{"id":21,"name":"old"}],"sender":{"login":"admin"}
	}`))
	require.NoError(t, err)
	require.Equal(t, "org", installation.Installation.AccountLogin)
	require.Len(t, installation.Installation.Repositories, 1)
	require.Equal(t, int64(21), installation.Installation.RemovedRepositoryIDs[0])
}

func TestNormalizeWebhookRejectsInvalidInput(t *testing.T) {
	_, err := NormalizeWebhook("", "issues", []byte(`{}`))
	require.Error(t, err)
	_, err = NormalizeWebhook("delivery", "issues", []byte(`{`))
	require.Error(t, err)
	require.False(t, VerifySignature(nil, "sha256=00", nil))
	require.False(t, VerifySignature([]byte("secret"), "sha256=not-hex", nil))
}
