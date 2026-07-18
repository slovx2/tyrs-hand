//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/domain"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	"github.com/slovx2/tyrs-hand/internal/orchestrator"
	"github.com/slovx2/tyrs-hand/internal/queue"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	toolservice "github.com/slovx2/tyrs-hand/internal/tools"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPostgresMigrationsAndLeaseEpoch(t *testing.T) {
	db := postgresDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	require.NoError(t, database.CheckMigrations(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "setup-token-value", "http://localhost")
	required, err := authService.SetupRequired(ctx)
	require.NoError(t, err)
	require.True(t, required)
	_, err = authService.Setup(ctx, "wrong", "admin", "integration-password")
	require.ErrorIs(t, err, auth.ErrInvalidSetupToken)
	setup, err := authService.Setup(ctx, "setup-token-value", "admin", "integration-password")
	require.NoError(t, err)
	required, err = authService.SetupRequired(ctx)
	require.NoError(t, err)
	require.False(t, required)
	_, err = authService.Setup(ctx, "setup-token-value", "second", "integration-password")
	require.ErrorIs(t, err, auth.ErrSetupComplete)
	code, err := totp.GenerateCode(setup.TOTPSecret, time.Now())
	require.NoError(t, err)
	_, err = authService.Login(ctx, "admin", "wrong-password", code)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
	session, err := authService.Login(ctx, "admin", "integration-password", code)
	require.NoError(t, err)
	_, err = authService.Authenticate(ctx, session.Token)
	require.NoError(t, err)
	require.True(t, authService.ValidateCSRF(ctx, session.Token, session.CSRFToken))
	require.False(t, authService.ValidateCSRF(ctx, session.Token, "invalid"))
	_, err = authService.Authenticate(ctx, "")
	require.ErrorIs(t, err, auth.ErrSessionInvalid)
	require.NoError(t, authService.Logout(ctx, session.Token))
	_, err = authService.Authenticate(ctx, session.Token)
	require.ErrorIs(t, err, auth.ErrSessionInvalid)
	settingsService := platformsettings.NewService(db, secrets.NewStore(db, box))
	provider, err := settingsService.AgentProvider(ctx)
	require.NoError(t, err)
	require.Equal(t, "device-code", provider.ProviderType)
	require.Error(t, settingsService.SaveAgentProvider(ctx, platformsettings.AgentProviderInput{ProviderType: "unknown"}))
	require.Error(t, settingsService.SaveAgentProvider(ctx, platformsettings.AgentProviderInput{ProviderType: "api-key"}))
	require.Error(t, settingsService.SaveAgentProvider(ctx, platformsettings.AgentProviderInput{ProviderType: "device-code", ProxyURL: "relative"}))
	require.NoError(t, settingsService.SaveAgentProvider(ctx, platformsettings.AgentProviderInput{
		ProviderType: "api-key", BaseURL: "https://api.example.com/v1", APIKey: "test-provider-key", Model: "test-model",
	}))
	apiKey, err := settingsService.APIKey(ctx)
	require.NoError(t, err)
	require.Equal(t, "test-provider-key", string(apiKey))
	provider, environment, err := settingsService.PrepareCodexHome(ctx, t.TempDir(), t.TempDir())
	require.NoError(t, err)
	require.True(t, provider.Configured)
	require.Contains(t, environment, "OPENAI_BASE_URL=https://api.example.com/v1")
	sharedHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sharedHome, "auth.json"), []byte(`{"tokens":{}}`), 0o600))
	require.NoError(t, settingsService.SaveAgentProvider(ctx, platformsettings.AgentProviderInput{
		ProviderType: "device-code", ProxyURL: "https://proxy.example.com",
	}))
	deviceHome := t.TempDir()
	_, _, err = settingsService.PrepareCodexHome(ctx, deviceHome, t.TempDir())
	require.Error(t, err)
	provider, environment, err = settingsService.PrepareCodexHome(ctx, deviceHome, sharedHome)
	require.NoError(t, err)
	require.Equal(t, "device-code", provider.ProviderType)
	require.Contains(t, environment, "HTTP_PROXY=https://proxy.example.com")
	require.FileExists(t, filepath.Join(deviceHome, "auth.json"))

	repositoryID, workItemID, profileID := seedQueue(t, db)
	redisClient, redisContainer := redisInstance(t)
	require.NoError(t, redisClient.Ping(ctx).Err())
	require.NoError(t, testcontainers.TerminateContainer(redisContainer))
	require.Error(t, redisClient.Ping(ctx).Err())
	for index := 0; index < 2; index++ {
		_, err := db.ExecContext(ctx, `
			INSERT INTO job_intents(work_item_id, repository_id, agent_profile_id, idempotency_key, instruction, allowed_tools, actor_login, actor_permission)
			VALUES ($1,$2,$3,$4,'test','["issue_read","create_pull_request"]'::jsonb,'alice','write')`, workItemID, repositoryID, profileID, "job-"+string(rune('a'+index)))
		require.NoError(t, err)
	}

	repository := queue.NewRepository(db, 2*time.Second)
	var claims [2]*queue.ClaimedJob
	var claimErrors [2]error
	var group sync.WaitGroup
	for index := range claims {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			claims[index], claimErrors[index] = repository.Claim(ctx, "worker-"+string(rune('a'+index)))
		}(index)
	}
	group.Wait()
	require.NoError(t, claimErrors[0])
	require.NoError(t, claimErrors[1])
	claimed := claims[0]
	if claimed == nil {
		claimed = claims[1]
	}
	require.NotNil(t, claimed)
	require.True(t, claims[0] == nil || claims[1] == nil, "同一 Work Item 只能有一个活动租约")

	_, err = db.ExecContext(ctx, "UPDATE job_intents SET lease_expires_at = now() - interval '1 second' WHERE id = $1", claimed.ID)
	require.NoError(t, err)
	count, err := repository.RequeueExpired(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	_, err = db.ExecContext(ctx, "UPDATE job_intents SET available_at = now() WHERE id = $1", claimed.ID)
	require.NoError(t, err)
	newClaim, err := repository.Claim(ctx, "worker-new")
	require.NoError(t, err)
	require.NotNil(t, newClaim)
	require.Equal(t, claimed.ID, newClaim.ID)
	require.Greater(t, newClaim.LeaseEpoch, claimed.LeaseEpoch)
	require.Equal(t, "alice", newClaim.ActorLogin)
	require.Equal(t, "write", newClaim.ActorPermission)
	require.ErrorIs(t, repository.Complete(ctx, claimed.ID, claimed.LeaseToken, claimed.LeaseEpoch), queue.ErrLeaseLost)
	require.NoError(t, repository.Heartbeat(ctx, newClaim.ID, newClaim.LeaseToken, newClaim.LeaseEpoch))
	require.ErrorIs(t, repository.Heartbeat(ctx, newClaim.ID, newClaim.LeaseToken, claimed.LeaseEpoch), queue.ErrLeaseLost)
	testOfficialGitHubTool(t, db, newClaim.Capability)
	require.NoError(t, repository.Complete(ctx, newClaim.ID, newClaim.LeaseToken, newClaim.LeaseEpoch))
	failedClaim, err := repository.Claim(ctx, "worker-failure")
	require.NoError(t, err)
	require.NotNil(t, failedClaim)
	require.NoError(t, repository.Fail(ctx, failedClaim.ID, failedClaim.LeaseToken, failedClaim.LeaseEpoch, errors.New("integration failure")))
	emptyClaim, err := repository.Claim(ctx, "worker-empty")
	require.NoError(t, err)
	require.Nil(t, emptyClaim)
	testWebhookOrchestration(t, db, repositoryID)
}

type fakeSCMProvider struct {
	event      domain.NormalizedEvent
	permission string
	valid      bool
}

func (p fakeSCMProvider) Name() string                      { return "github" }
func (p fakeSCMProvider) VerifyWebhook(string, []byte) bool { return p.valid }
func (p fakeSCMProvider) NormalizeWebhook(string, string, []byte) (domain.NormalizedEvent, error) {
	return p.event, nil
}
func (p fakeSCMProvider) Permission(context.Context, int64, string, string, string) (string, error) {
	return p.permission, nil
}

func testWebhookOrchestration(t *testing.T, db *sql.DB, repositoryID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, orchestrator.SeedRepositoryRules(ctx, tx, repositoryID))
	require.NoError(t, tx.Commit())
	event := domain.NormalizedEvent{
		Provider: "github", DeliveryID: "delivery-1", EventName: "issue_comment", Action: "created",
		InstallationID: 1, RepositoryID: 2, Owner: "owner", Repository: "repo",
		Number: 2, Kind: domain.WorkItemIssue, Title: "issue", Actor: "alice",
		Body: "@tyrs-hand inspect", Raw: json.RawMessage(`{"test":true}`), ReceivedAt: time.Now(),
	}
	service := orchestrator.NewWebhookService(db, fakeSCMProvider{event: event, permission: "write", valid: true}, "tyrs-hand[bot]")
	result, err := service.Process(ctx, "signature", "delivery-1", "issue_comment", []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, 1, result.Jobs)
	duplicate, err := service.Process(ctx, "signature", "delivery-1", "issue_comment", []byte(`{}`))
	require.NoError(t, err)
	require.True(t, duplicate.Duplicate)

	invalid := orchestrator.NewWebhookService(db, fakeSCMProvider{event: event}, "tyrs-hand")
	_, err = invalid.Process(ctx, "signature", "invalid", "issue_comment", nil)
	require.Error(t, err)

	installationEvent := event
	installationEvent.DeliveryID = "installation-created"
	installationEvent.EventName = "installation"
	installationEvent.Action = "created"
	installationEvent.Number = 0
	installationEvent.RepositoryID = 0
	installationEvent.InstallationID = 99
	installationEvent.Installation = domain.SCMInstallationEvent{
		AccountLogin: "example-org",
		Repositories: []domain.SCMRepository{{
			ExternalID: 99, Owner: "example-org", Name: "installed", DefaultBranch: "main",
			CloneURL: "https://example.invalid/example-org/installed.git",
		}},
	}
	result = processWebhook(t, db, installationEvent, "write")
	require.Zero(t, result.Jobs)

	noRule := event
	noRule.DeliveryID = "issues-opened"
	noRule.EventName = "issues"
	noRule.Action = "opened"
	result = processWebhook(t, db, noRule, "write")
	require.Zero(t, result.Jobs)

	withoutMention := event
	withoutMention.DeliveryID = "missing-mention"
	withoutMention.Body = "please inspect"
	result = processWebhook(t, db, withoutMention, "write")
	require.Zero(t, result.Jobs)

	lowPermission := event
	lowPermission.DeliveryID = "low-permission"
	result = processWebhook(t, db, lowPermission, "read")
	require.Zero(t, result.Jobs)

	botEvent := event
	botEvent.DeliveryID = "bot-reconciliation"
	botEvent.Actor = "tyrs-hand[bot]"
	result = processWebhook(t, db, botEvent, "write")
	require.Zero(t, result.Jobs)

	closed := event
	closed.DeliveryID = "issue-closed"
	closed.EventName = "issues"
	closed.Action = "closed"
	closed.Body = ""
	result = processWebhook(t, db, closed, "write")
	require.Zero(t, result.Jobs)
	var state string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state FROM work_items WHERE repository_id = $1 AND external_number = 2", repositoryID).Scan(&state))
	require.Equal(t, "closed", state)

	reopened := closed
	reopened.DeliveryID = "issue-reopened"
	reopened.Action = "reopened"
	_ = processWebhook(t, db, reopened, "write")
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state FROM work_items WHERE repository_id = $1 AND external_number = 2", repositoryID).Scan(&state))
	require.Equal(t, "open", state)

	installationEvent.DeliveryID = "installation-deleted"
	installationEvent.Action = "deleted"
	installationEvent.Installation.Repositories = nil
	_ = processWebhook(t, db, installationEvent, "write")
	var enabled bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT enabled FROM repositories WHERE provider = 'github' AND external_id = 99").Scan(&enabled))
	require.False(t, enabled)
}

func processWebhook(t *testing.T, db *sql.DB, event domain.NormalizedEvent, permission string) orchestrator.WebhookResult {
	t.Helper()
	service := orchestrator.NewWebhookService(db, fakeSCMProvider{event: event, permission: permission, valid: true}, "tyrs-hand")
	result, err := service.Process(context.Background(), "signature", event.DeliveryID, event.EventName, []byte(`{}`))
	require.NoError(t, err)
	return result
}

func testOfficialGitHubTool(t *testing.T, db *sql.DB, capability string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/installations/1/access_tokens":
			_ = json.NewEncoder(response).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case "/repos/owner/repo/collaborators/alice/permission":
			_ = json.NewEncoder(response).Encode(map[string]any{"permission": "write"})
		case "/repos/owner/repo/issues/1":
			_ = json.NewEncoder(response).Encode(map[string]any{"id": 10, "number": 1, "title": "test", "state": "open", "user": map[string]any{"login": "alice"}})
		case "/repos/owner/repo/pulls":
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"id": 20, "number": 42, "title": "Agent PR", "state": "open",
				"html_url": "https://github.com/owner/repo/pull/42",
				"head":     map[string]any{"ref": "tyrs-hand/issue-1-test", "sha": "abc"},
				"base":     map[string]any{"ref": "main", "sha": "def"},
				"user":     map[string]any{"login": "tyrs-hand[bot]"},
			})
		case "/graphql":
			_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{}}})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	app, err := ghadapter.NewAppClient(123, privateKey, server.URL+"/")
	require.NoError(t, err)
	catalog, err := githubtools.NewCatalog(githubtools.RegisteredTools)
	require.NoError(t, err)
	service := toolservice.NewService(db, app, catalog)
	_, err = service.Call(context.Background(), toolservice.CallRequest{Namespace: "git"})
	require.Error(t, err)
	_, err = service.Call(context.Background(), toolservice.CallRequest{Capability: "invalid", Namespace: "github"})
	require.Error(t, err)
	_, err = service.GitCredential(context.Background(), capability, "invalid")
	require.Error(t, err)
	fetchToken, err := service.GitCredential(context.Background(), capability, "fetch")
	require.NoError(t, err)
	require.Equal(t, "installation-token", fetchToken)
	pushToken, err := service.GitCredential(context.Background(), capability, "push")
	require.NoError(t, err)
	require.Equal(t, fetchToken, pushToken)
	_, err = service.Call(context.Background(), toolservice.CallRequest{
		Capability: capability, ThreadID: "thread", TurnID: "turn", CallID: "boundary",
		Namespace: "github", Tool: "issue_read",
		Arguments: json.RawMessage(`{"method":"get","owner":"owner","repo":"repo","issue_number":999}`),
	})
	require.Error(t, err)
	_, err = service.Call(context.Background(), toolservice.CallRequest{
		Capability: capability, ThreadID: "thread", TurnID: "turn", CallID: "not-allowed",
		Namespace: "github", Tool: "merge_pull_request", Arguments: json.RawMessage(`{}`),
	})
	require.Error(t, err)
	result, err := service.Call(context.Background(), toolservice.CallRequest{
		Capability: capability, ThreadID: "thread", TurnID: "turn", CallID: "call",
		Namespace: "github", Tool: "issue_read",
		Arguments: json.RawMessage(`{"method":"get","owner":"owner","repo":"repo","issue_number":1}`),
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotEmpty(t, result.ContentItems)
	previous, err := service.Call(context.Background(), toolservice.CallRequest{
		Capability: capability, ThreadID: "thread", TurnID: "turn", CallID: "call",
		Namespace: "github", Tool: "issue_read",
		Arguments: json.RawMessage(`{"method":"get","owner":"owner","repo":"repo","issue_number":1}`),
	})
	require.NoError(t, err)
	require.Equal(t, result, previous)
	createRequest := toolservice.CallRequest{
		Capability: capability, ThreadID: "thread", TurnID: "turn", CallID: "create-pr",
		Namespace: "github", Tool: "create_pull_request",
		Arguments: json.RawMessage(`{"owner":"owner","repo":"repo","title":"Agent PR","head":"tyrs-hand/issue-1-test","base":"main"}`),
	}
	created, err := service.Call(context.Background(), createRequest)
	require.NoError(t, err)
	require.True(t, created.Success)
	createdAgain, err := service.Call(context.Background(), createRequest)
	require.NoError(t, err)
	require.Equal(t, created, createdAgain)
	var channelCount int
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT count(*) FROM work_item_channels WHERE channel_type = 'pull_request' AND external_number = 42`).Scan(&channelCount))
	require.Equal(t, 1, channelCount)
}

func redisInstance(t *testing.T) (*redis.Client, testcontainers.Container) {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:8.4.0-bookworm@sha256:c22af04bb576503bf16b3e34a1fd2fd82de0f765afd866d2e380145e0af30d78",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections").WithStartupTimeout(60 * time.Second),
		}, Started: true,
	})
	require.NoError(t, err)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{Addr: host + ":" + port.Port(), DialTimeout: 500 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close(); _ = testcontainers.TerminateContainer(container) })
	return client, container
}

func postgresDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba",
			Env:          map[string]string{"POSTGRES_DB": "tyrs_hand", "POSTGRES_USER": "tyrs_hand", "POSTGRES_PASSWORD": "test-password"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		}, Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	db, err := database.Open(ctx, "postgres://tyrs_hand:test-password@"+host+":"+port.Port()+"/tyrs_hand?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func seedQueue(t *testing.T, db *sql.DB) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var installationID, repositoryID, workItemID, profileID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scm_installations(provider,external_id,account_login,account_type) VALUES ('github',1,'test','Organization') RETURNING id`).Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO repositories(installation_id,provider,external_id,owner,name,default_branch,clone_url) VALUES ($1,'github',2,'owner','repo','main','https://example.invalid/repo.git') RETURNING id`, installationID).Scan(&repositoryID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name = 'Default'`).Scan(&profileID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO work_items(repository_id,kind,external_number,title) VALUES ($1,'issue',1,'test') RETURNING id`, repositoryID).Scan(&workItemID))
	return repositoryID, workItemID, profileID
}
