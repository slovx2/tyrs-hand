package github

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	githubapi "github.com/google/go-github/v89/github"
	"github.com/slovx2/tyrs-hand/internal/domain"
)

type cachedToken struct {
	value     string
	expiresAt time.Time
}

type AppClient struct {
	appID      int64
	privateKey *rsa.PrivateKey
	apiURL     *url.URL

	mu     sync.Mutex
	tokens map[int64]cachedToken
}

func NewAppClient(appID int64, privateKeyPEM []byte, apiBaseURL string) (*AppClient, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("解析 GitHub App Private Key: %w", err)
	}
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com/"
	}
	parsed, err := url.Parse(apiBaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("配置的 GitHub API Base URL 无效")
	}
	return &AppClient{appID: appID, privateKey: key, apiURL: parsed, tokens: make(map[int64]cachedToken)}, nil
}

func (a *AppClient) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	a.mu.Lock()
	if token, ok := a.tokens[installationID]; ok && time.Until(token.expiresAt) > 5*time.Minute {
		a.mu.Unlock()
		return token.value, nil
	}
	a.mu.Unlock()

	appJWT, err := a.signJWT()
	if err != nil {
		return "", err
	}
	baseURL := a.apiURL.String()
	client, err := githubapi.NewClient(
		githubapi.WithAuthToken(appJWT),
		githubapi.WithURLs(&baseURL, &baseURL),
		githubapi.WithTimeout(30*time.Second),
	)
	if err != nil {
		return "", fmt.Errorf("创建 GitHub 客户端: %w", err)
	}
	token, _, err := client.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return "", fmt.Errorf("创建 Installation Token: %w", err)
	}
	if token.Token == nil || token.ExpiresAt == nil {
		return "", fmt.Errorf("上游 GitHub 没有返回有效 Installation Token")
	}
	a.mu.Lock()
	a.tokens[installationID] = cachedToken{value: *token.Token, expiresAt: token.ExpiresAt.Time}
	a.mu.Unlock()
	return *token.Token, nil
}

func (a *AppClient) RESTClient(ctx context.Context, installationID int64) (*githubapi.Client, error) {
	token, err := a.InstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	baseURL := a.apiURL.String()
	client, err := githubapi.NewClient(
		githubapi.WithAuthToken(token),
		githubapi.WithURLs(&baseURL, &baseURL),
		githubapi.WithTimeout(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 GitHub 客户端: %w", err)
	}
	return client, nil
}

func (a *AppClient) Permission(ctx context.Context, installationID int64, owner, repository, actor string) (string, error) {
	client, err := a.RESTClient(ctx, installationID)
	if err != nil {
		return "", err
	}
	permission, _, err := client.Repositories.GetPermissionLevel(ctx, owner, repository, actor)
	if err != nil {
		return "", err
	}
	return permission.GetPermission(), nil
}

func (a *AppClient) Repository(ctx context.Context, installationID int64, owner, repository string) (domain.SCMRepository, error) {
	client, err := a.RESTClient(ctx, installationID)
	if err != nil {
		return domain.SCMRepository{}, err
	}
	value, _, err := client.Repositories.Get(ctx, owner, repository)
	if err != nil {
		return domain.SCMRepository{}, err
	}
	return domain.SCMRepository{
		ExternalID:    value.GetID(),
		Owner:         value.GetOwner().GetLogin(),
		Name:          value.GetName(),
		DefaultBranch: value.GetDefaultBranch(),
		CloneURL:      value.GetCloneURL(),
	}, nil
}

func (a *AppClient) signJWT() (string, error) {
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(a.appID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(a.privateKey)
}

type Provider struct {
	webhookSecret []byte
	app           *AppClient
}

func NewProvider(webhookSecret []byte, app *AppClient) *Provider {
	return &Provider{webhookSecret: append([]byte(nil), webhookSecret...), app: app}
}

func (p *Provider) Name() string { return "github" }
func (p *Provider) VerifyWebhook(signature string, payload []byte) bool {
	return VerifySignature(p.webhookSecret, signature, payload)
}
func (p *Provider) NormalizeWebhook(deliveryID, eventName string, payload []byte) (domain.NormalizedEvent, error) {
	return NormalizeWebhook(deliveryID, eventName, payload)
}
func (p *Provider) Repository(ctx context.Context, installationID int64, owner, repository string) (domain.SCMRepository, error) {
	return p.app.Repository(ctx, installationID, owner, repository)
}
func (p *Provider) Permission(ctx context.Context, installationID int64, owner, repository, actor string) (string, error) {
	return p.app.Permission(ctx, installationID, owner, repository, actor)
}
