package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
)

type fakeOAuthApp struct{}

func (fakeOAuthApp) Credentials(context.Context) (string, string, error) {
	return "client-id", "client-secret", nil
}

type fakeBindingStore struct {
	mu      sync.Mutex
	states  map[string]OAuthState
	binding Binding
	unbound bool
}

func (s *fakeBindingStore) SaveOAuthState(_ context.Context, hash string, state OAuthState, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[hash] = state
	return nil
}
func (s *fakeBindingStore) ConsumeOAuthState(_ context.Context, hash string, _ time.Time) (OAuthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[hash]
	if !ok {
		return OAuthState{}, sql.ErrNoRows
	}
	delete(s.states, hash)
	return state, nil
}
func (s *fakeBindingStore) Bind(_ context.Context, binding Binding) (Binding, error) {
	binding.Version = 1
	s.binding = binding
	return binding, nil
}
func (s *fakeBindingStore) Unbind(context.Context, string, string) error {
	s.unbound = true
	return nil
}
func (s *fakeBindingStore) CurrentBinding(context.Context, string, string) (Binding, error) {
	return s.binding, nil
}

func TestGitHubBindingUsesOneTimeStatePKCEAndTemporaryToken(t *testing.T) {
	var verifier string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/login/oauth/access_token":
			require.NoError(t, request.ParseForm())
			require.Equal(t, "client-id", request.Form.Get("client_id"))
			require.Equal(t, "client-secret", request.Form.Get("client_secret"))
			require.Equal(t, "oauth-code", request.Form.Get("code"))
			verifier = request.Form.Get("code_verifier")
			_ = json.NewEncoder(response).Encode(map[string]string{"access_token": "temporary-user-token"})
		case "/user":
			require.Equal(t, "Bearer temporary-user-token", request.Header.Get("Authorization"))
			_ = json.NewEncoder(response).Encode(map[string]any{"id": 42, "login": "octocat"})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	store := &fakeBindingStore{states: make(map[string]OAuthState)}
	service := NewBindingService(store, box, fakeOAuthApp{}, "https://tyrs.example", server.URL)
	service.authorizeURL = server.URL + "/login/oauth/authorize"
	service.tokenURL = server.URL + "/login/oauth/access_token"
	service.httpClient = server.Client()

	authorize, err := service.Start(context.Background(), "guild-1", "discord-1")
	require.NoError(t, err)
	parsed, err := url.Parse(authorize)
	require.NoError(t, err)
	require.Equal(t, "S256", parsed.Query().Get("code_challenge_method"))
	require.Equal(t, "read:user", parsed.Query().Get("scope"))
	require.NotEmpty(t, parsed.Query().Get("state"))

	binding, err := service.Callback(context.Background(), parsed.Query().Get("state"), "oauth-code")
	require.NoError(t, err)
	require.Equal(t, int64(42), binding.GitHubUserID)
	require.Equal(t, "octocat", binding.GitHubLogin)
	require.Equal(t, "discord-1", binding.DiscordUserID)
	require.Equal(t, parsed.Query().Get("code_challenge"), pkceChallenge(verifier))
	require.Equal(t, Binding{GuildID: "guild-1", DiscordUserID: "discord-1", GitHubUserID: 42, GitHubLogin: "octocat", Version: 1}, store.binding)

	_, err = service.Callback(context.Background(), parsed.Query().Get("state"), "oauth-code")
	require.ErrorContains(t, err, "已使用")
}

func TestGitHubUnbindRequiresConfirmation(t *testing.T) {
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	store := &fakeBindingStore{states: make(map[string]OAuthState)}
	service := NewBindingService(store, box, fakeOAuthApp{}, "https://tyrs.example", "https://api.github.com")
	require.Error(t, service.Unbind(context.Background(), "g", "u", false))
	require.False(t, store.unbound)
	require.NoError(t, service.Unbind(context.Background(), "g", "u", true))
	require.True(t, store.unbound)
}
