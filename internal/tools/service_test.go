package tools

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/stretchr/testify/require"
)

func TestToolArgumentBoundaries(t *testing.T) {
	auth := authorization{Owner: "Owner", Repository: "Repo", Number: 1, AllowedNumbers: []int{1, 8}}
	require.NoError(t, validateArguments(json.RawMessage(`{"owner":"owner","repo":"repo","issueNumber":1}`), auth))
	require.NoError(t, validateArguments(json.RawMessage(`{"owner":"Owner","repo":"Repo","pullNumber":8}`), auth))
	require.NoError(t, validateArguments(json.RawMessage(`{"owner":"Owner","repo":"Repo","issue_number":"8"}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"owner":"other"}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"repo":"other"}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"pullNumber":9}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"pull_request_number":"invalid"}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"issue_number":1.5}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"issue_number":true}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`[]`), auth))
	require.True(t, contains([]string{"a", "b"}, "b"))
	require.False(t, contains([]string{"a", "b"}, "c"))
	require.True(t, containsNumber([]int{1, 2}, 2))
	require.False(t, containsNumber([]int{1, 2}, 3))
	number, valid := argumentNumber("42")
	require.True(t, valid)
	require.Equal(t, 42, number)
	_, valid = argumentNumber(struct{}{})
	require.False(t, valid)
}

func TestDiscordContributorPermissionUsesLiveIntersection(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	permissions := map[string]string{"alice": "write", "bob": "read"}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/app/installations/42/access_tokens":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"token": "installation", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case strings.HasPrefix(request.URL.Path, "/repos/owner/repo/collaborators/"):
			parts := strings.Split(request.URL.Path, "/")
			login := parts[len(parts)-2]
			mu.Lock()
			permission := permissions[login]
			mu.Unlock()
			if permission == "" {
				http.NotFound(response, request)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]string{"permission": permission})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	app, err := ghadapter.NewAppClient(123, privateKey, server.URL+"/")
	require.NoError(t, err)
	service := &Service{app: app}
	auth := authorization{
		SourceType: "discord_conversation", InstallationID: 42, Owner: "owner", Repository: "repo",
		Contributors: []string{"alice", "bob"},
	}
	require.NoError(t, service.requireDiscordPermission(context.Background(), auth, false))
	require.ErrorContains(t, service.requireDiscordPermission(context.Background(), auth, true), "bob")

	mu.Lock()
	permissions["bob"] = ""
	mu.Unlock()
	require.ErrorContains(t, service.requireDiscordPermission(context.Background(), auth, false), "bob")

	auth.HasUnboundContributor = true
	require.ErrorContains(t, service.requireDiscordPermission(context.Background(), auth, false), "未绑定")
}

func TestGitHubPermissionRank(t *testing.T) {
	require.Greater(t, githubPermissionRank("admin"), githubPermissionRank("write"))
	require.Greater(t, githubPermissionRank("triage"), githubPermissionRank("read"))
	require.Zero(t, githubPermissionRank("none"))
}

func TestPullRequestNumberExtraction(t *testing.T) {
	require.Equal(t, 42, pullRequestNumber(codex.TextToolResult(`{"pull_request":{"number":42}}`, true)))
	require.Equal(t, 19, pullRequestNumber(codex.TextToolResult("created https://github.com/o/r/pull/19", true)))
	require.Zero(t, pullRequestNumber(codex.TextToolResult("created", true)))
	require.Equal(t, 7, findNumber([]any{map[string]any{"number": float64(7)}}))
	require.Equal(t, 8, findNumber(map[string]any{"nested": map[string]any{"number": float64(8)}}))
	require.Zero(t, findNumber("none"))
}
