package discordintegration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/slovx2/tyrs-hand/internal/security"
)

type OAuthState struct {
	GuildID            string
	DiscordUserID      string
	VerifierNonce      []byte
	VerifierCiphertext []byte
}

type Binding struct {
	GuildID       string `json:"guildId"`
	DiscordUserID string `json:"discordUserId"`
	GitHubUserID  int64  `json:"githubUserId"`
	GitHubLogin   string `json:"githubLogin"`
	Version       int64  `json:"version"`
}

type BindingStore interface {
	SaveOAuthState(context.Context, string, OAuthState, time.Time) error
	ConsumeOAuthState(context.Context, string, time.Time) (OAuthState, error)
	Bind(context.Context, Binding) (Binding, error)
	Unbind(context.Context, string, string) error
	CurrentBinding(context.Context, string, string) (Binding, error)
}

type OAuthApp interface {
	Credentials(context.Context) (string, string, error)
}

type GitHubOAuthApp struct{ manager *ghadapter.Manager }

func NewGitHubOAuthApp(manager *ghadapter.Manager) *GitHubOAuthApp {
	return &GitHubOAuthApp{manager: manager}
}

func (a *GitHubOAuthApp) Credentials(ctx context.Context) (string, string, error) {
	settings, _, _, ok := a.manager.Current()
	if !ok || settings.ClientID == "" {
		return "", "", errors.New("github App Client ID 尚未配置")
	}
	secret, err := a.manager.ClientSecret(ctx)
	return settings.ClientID, secret, err
}

type BindingService struct {
	store        BindingStore
	box          *security.SecretBox
	app          OAuthApp
	httpClient   *http.Client
	authorizeURL string
	tokenURL     string
	apiURL       string
	callbackURL  string
	now          func() time.Time
}

func NewBindingService(store BindingStore, box *security.SecretBox, app OAuthApp, publicURL, githubAPIURL string) *BindingService {
	return &BindingService{
		store: store, box: box, app: app, httpClient: &http.Client{Timeout: 30 * time.Second},
		authorizeURL: "https://github.com/login/oauth/authorize",
		tokenURL:     "https://github.com/login/oauth/access_token",
		apiURL:       strings.TrimRight(githubAPIURL, "/"),
		callbackURL:  strings.TrimRight(publicURL, "/") + "/api/v1/discord/github/bind/callback",
		now:          time.Now,
	}
}

func (s *BindingService) Start(ctx context.Context, guildID, discordUserID string) (string, error) {
	clientID, _, err := s.app.Credentials(ctx)
	if err != nil {
		return "", err
	}
	state, err := security.RandomToken(32)
	if err != nil {
		return "", err
	}
	verifier, err := security.RandomToken(48)
	if err != nil {
		return "", err
	}
	stateHash := security.Digest(state)
	nonce, ciphertext, err := s.box.Encrypt([]byte(verifier), stateHash)
	if err != nil {
		return "", err
	}
	err = s.store.SaveOAuthState(ctx, stateHash, OAuthState{
		GuildID: guildID, DiscordUserID: discordUserID,
		VerifierNonce: nonce, VerifierCiphertext: ciphertext,
	}, s.now().Add(10*time.Minute))
	if err != nil {
		return "", err
	}
	query := url.Values{
		"client_id": {clientID}, "redirect_uri": {s.callbackURL}, "state": {state},
		"code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"},
		"scope": {"read:user"},
	}
	return s.authorizeURL + "?" + query.Encode(), nil
}

func (s *BindingService) Callback(ctx context.Context, state, code string) (Binding, error) {
	if state == "" || code == "" {
		return Binding{}, errors.New("github 绑定回调缺少 state 或 code")
	}
	hash := security.Digest(state)
	session, err := s.store.ConsumeOAuthState(ctx, hash, s.now())
	if err != nil {
		return Binding{}, errors.New("github 绑定链接无效、已使用或已过期")
	}
	verifier, err := s.box.Decrypt(session.VerifierNonce, session.VerifierCiphertext, hash)
	if err != nil {
		return Binding{}, err
	}
	token, err := s.exchange(ctx, code, string(verifier))
	if err != nil {
		return Binding{}, err
	}
	userID, login, err := s.user(ctx, token)
	if err != nil {
		return Binding{}, err
	}
	return s.store.Bind(ctx, Binding{
		GuildID: session.GuildID, DiscordUserID: session.DiscordUserID,
		GitHubUserID: userID, GitHubLogin: login,
	})
}

func (s *BindingService) Unbind(ctx context.Context, guildID, discordUserID string, confirmed bool) error {
	if !confirmed {
		return errors.New("解绑 GitHub 身份需要二次确认")
	}
	return s.store.Unbind(ctx, guildID, discordUserID)
}

func (s *BindingService) exchange(ctx context.Context, code, verifier string) (string, error) {
	clientID, clientSecret, err := s.app.Credentials(ctx)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"client_id": {clientID}, "client_secret": {clientSecret}, "code": {code},
		"redirect_uri": {s.callbackURL}, "code_verifier": {verifier},
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("交换 GitHub User Access Token: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github OAuth 返回 %s", response.Status)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.AccessToken == "" {
		return "", errors.New("github OAuth 没有返回有效的临时 Token")
	}
	return payload.AccessToken, nil
}

func (s *BindingService) user(ctx context.Context, token string) (int64, string, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.apiURL+"/user", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return 0, "", fmt.Errorf("读取 GitHub 用户: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&user) != nil || user.ID <= 0 || user.Login == "" {
		return 0, "", errors.New("github 没有返回有效用户身份")
	}
	return user.ID, user.Login, nil
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
