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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/slovx2/tyrs-hand/internal/executionnode"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	"github.com/slovx2/tyrs-hand/internal/orchestrator"
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

	repositoryID, workItemID, profileID, nodeID := seedQueue(t, db)
	redisClient, redisContainer := redisInstance(t)
	require.NoError(t, redisClient.Ping(ctx).Err())
	require.NoError(t, testcontainers.TerminateContainer(redisContainer))
	require.Error(t, redisClient.Ping(ctx).Err())
	repository := codexcontrol.NewRepository(db, 2*time.Second)
	for index := 0; index < 2; index++ {
		tx, beginErr := db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		_, inserted, enqueueErr := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceGitHub, WorkItemID: workItemID, RepositoryID: repositoryID,
			AgentProfileID: profileID, ContextVersion: 1,
			IdempotencyKey: "intent-" + string(rune('a'+index)), Instruction: "test",
			AllowedTools: []string{"issue_read", "create_pull_request"}, ActorLogin: "alice",
			ActorPermission: "write", ReplyPolicy: "required",
		})
		require.NoError(t, enqueueErr)
		require.True(t, inserted)
		require.NoError(t, tx.Commit())
	}

	var claims [2]*codexcontrol.ClaimedControl
	var claimErrors [2]error
	var group sync.WaitGroup
	for index := range claims {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			claims[index], claimErrors[index] = repository.ClaimNode(ctx,
				"worker-"+string(rune('a'+index)), codexcontrol.SourceGitHub, nodeID)
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
	require.NotEqual(t, uuid.Nil, claimed.RunID)
	require.True(t, claims[0] == nil || claims[1] == nil, "同一 Work Item 只能有一个活动租约")

	_, err = db.ExecContext(ctx, "UPDATE codex_thread_controls SET lease_expires_at = now() - interval '1 second' WHERE id = $1", claimed.ControlID)
	require.NoError(t, err)
	count, err := repository.RequeueExpired(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, repository.Heartbeat(ctx, claimed), "原节点应当使用同一 Lease 恢复 Run")
	var activeSlot int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT active_slot FROM codex_turn_runs WHERE id = $1`,
		claimed.RunID).Scan(&activeSlot))
	require.Equal(t, 1, activeSlot, "恢复时必须保留原 Run 的活动槽位")
	require.Equal(t, "alice", claimed.ActorLogin)
	require.Equal(t, "write", claimed.ActorPermission)
	stale := *claimed
	stale.LeaseEpoch--
	require.ErrorIs(t, repository.Heartbeat(ctx, &stale), codexcontrol.ErrLeaseLost)
	require.NoError(t, repository.SetThread(ctx, claimed, "thread", "/tmp/codex-home", "signature"))
	require.NoError(t, repository.RecordSubmission(ctx, claimed, "turn"))
	require.NoError(t, repository.ConfirmTurn(ctx, claimed, "turn"))
	satisfied, err := repository.ReplySatisfied(ctx, claimed)
	require.NoError(t, err)
	require.False(t, satisfied)
	silentClaim := *claimed
	silentClaim.ReplyPolicy = "silent"
	satisfied, err = repository.ReplySatisfied(ctx, &silentClaim)
	require.NoError(t, err)
	require.True(t, satisfied)
	testOfficialGitHubTool(t, db, claimed.Capability)
	require.NoError(t, repository.Complete(ctx, claimed, codexcontrol.TurnResult{
		TurnID: "turn", FinalAnswer: "succeeded, but command exited 127",
	}))
	failedClaim, err := repository.ClaimNode(ctx, "worker-failure", codexcontrol.SourceGitHub, nodeID)
	require.NoError(t, err)
	require.NotNil(t, failedClaim)
	_, err = db.ExecContext(ctx, "UPDATE codex_turn_intents SET reply_status = 'delivered' WHERE id = $1", failedClaim.ID)
	require.NoError(t, err)
	require.NoError(t, repository.Complete(ctx, failedClaim, codexcontrol.TurnResult{
		TurnID: "turn-2", FinalAnswer: "blocked: command exited 127 and needs attention",
	}))
	var completedCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents
		WHERE control_id = $1 AND status = 'completed'`, failedClaim.ControlID).Scan(&completedCount))
	require.Equal(t, 2, completedCount, "自然回复中的状态词不能改变平台终态")
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, inserted, err := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceGitHub, WorkItemID: workItemID, RepositoryID: repositoryID,
		AgentProfileID: profileID, ContextVersion: 1, IdempotencyKey: "intent-reconcile",
		Instruction: "reconcile", ReplyPolicy: "silent",
	})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	reconcileClaim, err := repository.ClaimNode(ctx, "worker-reconcile", codexcontrol.SourceGitHub, nodeID)
	require.NoError(t, err)
	require.NotNil(t, reconcileClaim)
	require.NoError(t, repository.Reconcile(ctx, reconcileClaim, "network_lost", errors.New("offline")))
	var reconciledStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_turn_intents WHERE id = $1`,
		reconcileClaim.ID).Scan(&reconciledStatus))
	require.Equal(t, "retry_wait", reconciledStatus)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET available_at = now() WHERE id = $1`,
		reconcileClaim.ID)
	require.NoError(t, err)
	reconcileClaim, err = repository.ClaimNode(ctx, "worker-reconcile", codexcontrol.SourceGitHub, nodeID)
	require.NoError(t, err)
	require.NotNil(t, reconcileClaim)
	require.Equal(t, 2, reconcileClaim.Attempt)
	require.NoError(t, repository.Cancel(ctx, reconcileClaim, "canceled", "test complete"))
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, inserted, err = repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceGitHub, WorkItemID: workItemID, RepositoryID: repositoryID,
		AgentProfileID: profileID, ContextVersion: 1, IdempotencyKey: "intent-control-failure",
		Instruction: "test failure", ReplyPolicy: "required",
	})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	failedClaim, err = repository.ClaimNode(ctx, "worker-failure", codexcontrol.SourceGitHub, nodeID)
	require.NoError(t, err)
	require.NotNil(t, failedClaim)
	require.NoError(t, repository.Fail(ctx, failedClaim, "integration_failure", errors.New("integration failure")))
	emptyClaim, err := repository.ClaimNode(ctx, "worker-empty", codexcontrol.SourceGitHub, nodeID)
	require.NoError(t, err)
	require.Nil(t, emptyClaim)
	testWebhookOrchestration(t, db, repositoryID)
}

func TestTriggerKindMigrationUpgradesExistingRules(t *testing.T) {
	db := postgresDatabase(t)
	ctx := context.Background()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrationDirectory := filepath.Join(filepath.Dir(sourceFile), "..", "..", "internal", "database", "migrations")
	paths, err := filepath.Glob(filepath.Join(migrationDirectory, "*.sql"))
	require.NoError(t, err)
	for _, path := range paths {
		if filepath.Base(path) >= "007_trigger_kinds.sql" {
			continue
		}
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, string(content))
		require.NoError(t, err, filepath.Base(path))
	}

	var installationID, repositoryID, profileID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `
		INSERT INTO scm_installations(provider, external_id, account_login, account_type)
		VALUES ('github', 1, 'owner', 'Organization') RETURNING id`).Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, `
		INSERT INTO repositories(installation_id, provider, external_id, owner, name, default_branch, clone_url)
		VALUES ($1, 'github', 2, 'owner', 'repo', 'main', 'https://example.invalid/repo.git') RETURNING id`, installationID).Scan(&repositoryID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name = 'Default'`).Scan(&profileID))
	_, err = db.ExecContext(ctx, `
		INSERT INTO trigger_rules(repository_id, agent_profile_id, name, event_name, action, mention_required, instruction_template)
		VALUES
			($1, $2, 'mention', 'issue_comment', 'created', true, '{{body}}'),
			($1, $2, 'custom-mention', 'issue_comment', 'created', true, '{{body}}')`, repositoryID, profileID)
	require.NoError(t, err)

	upgrade, err := os.ReadFile(filepath.Join(migrationDirectory, "007_trigger_kinds.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(upgrade))
	require.NoError(t, err)

	var kind, value string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT trigger_kind, trigger_value FROM trigger_rules
		WHERE repository_id = $1 AND name = 'command'`, repositoryID).Scan(&kind, &value))
	require.Equal(t, "slash_command", kind)
	require.Equal(t, "tyrs-hand", value)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT trigger_kind FROM trigger_rules
		WHERE repository_id = $1 AND name = 'custom-mention'`, repositoryID).Scan(&kind))
	require.Equal(t, "legacy_mention", kind)

	var labelRules int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM trigger_rules
		WHERE repository_id = $1 AND trigger_kind = 'label' AND trigger_value = 'tyrs-hand'`, repositoryID).Scan(&labelRules))
	require.Equal(t, 2, labelRules)

	var oldColumnCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'trigger_rules' AND column_name = 'mention_required'`).Scan(&oldColumnCount))
	require.Zero(t, oldColumnCount)

	mentionUpgrade, err := os.ReadFile(filepath.Join(migrationDirectory, "010_mention_command.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(mentionUpgrade))
	require.NoError(t, err)
	var enabled bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT trigger_kind, enabled, actor_min_permission FROM trigger_rules
		WHERE repository_id = $1 AND name = 'mention-command'`, repositoryID).Scan(&kind, &enabled, &value))
	require.Equal(t, "mention_command", kind)
	require.True(t, enabled)
	require.Equal(t, "triage", value)
}

type fakeSCMProvider struct {
	event      domain.NormalizedEvent
	permission string
	valid      bool
	pull       domain.PullRequest
	pullErr    error
}

func (p fakeSCMProvider) Name() string                      { return "github" }
func (p fakeSCMProvider) VerifyWebhook(string, []byte) bool { return p.valid }
func (p fakeSCMProvider) NormalizeWebhook(string, string, []byte) (domain.NormalizedEvent, error) {
	return p.event, nil
}
func (p fakeSCMProvider) Repository(context.Context, int64, string, string) (domain.SCMRepository, error) {
	return domain.SCMRepository{
		ExternalID: 99, Owner: "example-org", Name: "installed", DefaultBranch: "main",
		CloneURL: "https://example.invalid/example-org/installed.git",
	}, nil
}
func (p fakeSCMProvider) PullRequest(context.Context, int64, string, string, int) (domain.PullRequest, error) {
	if p.pullErr != nil {
		return domain.PullRequest{}, p.pullErr
	}
	return p.pull, nil
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
	var ruleCount int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM trigger_rules WHERE repository_id = $1`, repositoryID).Scan(&ruleCount))
	require.Equal(t, 4, ruleCount)
	event := domain.NormalizedEvent{
		Provider: "github", DeliveryID: "delivery-1", EventName: "issue_comment", Action: "created",
		InstallationID: 1, RepositoryID: 2, Owner: "owner", Repository: "repo",
		Number: 2, Kind: domain.WorkItemIssue, Title: "issue", Actor: "alice",
		Body: "/tyrs-hand inspect\nadditional context", Raw: json.RawMessage(`{"test":true}`), ReceivedAt: time.Now(),
	}
	service := orchestrator.NewWebhookService(db, fakeSCMProvider{event: event, permission: "write", valid: true}, "tyrs-hand[bot]")
	result, err := service.Process(ctx, "signature", "delivery-1", "issue_comment", []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, 1, result.Jobs)
	var instruction string
	var evidence []byte
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT j.instruction, j.trigger_evidence FROM codex_turn_intents j
		JOIN webhook_deliveries d ON d.id = j.webhook_delivery_id
		WHERE d.delivery_id = 'delivery-1'`).Scan(&instruction, &evidence))
	require.Contains(t, instruction, "inspect\nadditional context")
	require.NotContains(t, instruction, "/tyrs-hand")
	var decodedEvidence map[string]any
	require.NoError(t, json.Unmarshal(evidence, &decodedEvidence))
	require.Equal(t, "slash_command", decodedEvidence["kind"])
	require.Equal(t, "comment_first_line", decodedEvidence["source"])
	duplicate, err := service.Process(ctx, "signature", "delivery-1", "issue_comment", []byte(`{}`))
	require.NoError(t, err)
	require.True(t, duplicate.Duplicate)

	prEvent := event
	prEvent.DeliveryID = "delivery-pr-comment"
	prEvent.Kind = domain.WorkItemPullRequest
	prEvent.Number = 3
	prEvent.Title = "pull request"
	prEvent.HTMLURL = "https://github.com/owner/repo/pull/3"
	pull := domain.PullRequest{Number: 3, URL: prEvent.HTMLURL, HeadSHA: "abc123",
		HeadRef: "feature", HeadRepository: "fork/repo", BaseSHA: "def456", BaseRef: "main"}
	prService := orchestrator.NewWebhookService(db,
		fakeSCMProvider{event: prEvent, permission: "write", valid: true, pull: pull}, "tyrs-hand[bot]")
	prResult, err := prService.Process(ctx, "signature", prEvent.DeliveryID, "issue_comment", []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, 1, prResult.Jobs)
	var headRef, headRepository, baseSHA, baseRef, htmlURL string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT head_ref, head_repository, base_sha, base_ref, html_url
		FROM work_items WHERE repository_id = $1 AND kind = 'pull_request' AND external_number = 3`, repositoryID).
		Scan(&headRef, &headRepository, &baseSHA, &baseRef, &htmlURL))
	require.Equal(t, "feature", headRef)
	require.Equal(t, "fork/repo", headRepository)
	require.Equal(t, "def456", baseSHA)
	require.Equal(t, "main", baseRef)
	require.Equal(t, prEvent.HTMLURL, htmlURL)

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
			ExternalID: 99, Owner: "example-org", Name: "installed",
		}},
	}
	result = processWebhook(t, db, installationEvent, "write")
	require.Zero(t, result.Jobs)
	var defaultBranch, cloneURL string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT default_branch, clone_url FROM repositories
		WHERE provider = 'github' AND external_id = 99`).Scan(&defaultBranch, &cloneURL))
	require.Equal(t, "main", defaultBranch)
	require.Equal(t, "https://example.invalid/example-org/installed.git", cloneURL)
	_, err = db.ExecContext(ctx, `UPDATE repositories SET default_branch = '', clone_url = '' WHERE provider = 'github' AND external_id = 99`)
	require.NoError(t, err)
	duplicateInstallation := processWebhook(t, db, installationEvent, "write")
	require.True(t, duplicateInstallation.Duplicate)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT default_branch, clone_url FROM repositories
		WHERE provider = 'github' AND external_id = 99`).Scan(&defaultBranch, &cloneURL))
	require.Equal(t, "main", defaultBranch)
	require.Equal(t, "https://example.invalid/example-org/installed.git", cloneURL)

	noRule := event
	noRule.DeliveryID = "issues-opened"
	noRule.EventName = "issues"
	noRule.Action = "opened"
	result = processWebhook(t, db, noRule, "write")
	require.Zero(t, result.Jobs)

	label := event
	label.DeliveryID = "issue-label"
	label.EventName = "issues"
	label.Action = "labeled"
	label.Body = "Issue description"
	label.Label = "TYRS-HAND"
	result = processWebhook(t, db, label, "write")
	require.Equal(t, 1, result.Jobs)

	wrongLabel := label
	wrongLabel.DeliveryID = "wrong-label"
	wrongLabel.Label = "documentation"
	result = processWebhook(t, db, wrongLabel, "write")
	require.Zero(t, result.Jobs)

	withoutMention := event
	withoutMention.DeliveryID = "missing-mention"
	withoutMention.Body = "please inspect"
	result = processWebhook(t, db, withoutMention, "write")
	require.Zero(t, result.Jobs)

	mentionCommand := event
	mentionCommand.DeliveryID = "mention-command"
	mentionCommand.Body = "please ask @tyrs-hand to inspect"
	result = processWebhook(t, db, mentionCommand, "write")
	require.Equal(t, 1, result.Jobs)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT j.trigger_evidence FROM codex_turn_intents j
		JOIN webhook_deliveries d ON d.id = j.webhook_delivery_id
		WHERE d.delivery_id = 'mention-command'`).Scan(&evidence))
	require.NoError(t, json.Unmarshal(evidence, &decodedEvidence))
	require.Equal(t, "mention_command", decodedEvidence["kind"])
	require.Equal(t, "comment_first_line_mention", decodedEvidence["source"])

	secondLineMention := event
	secondLineMention.DeliveryID = "second-line-mention"
	secondLineMention.Body = "context first\n@tyrs-hand inspect"
	result = processWebhook(t, db, secondLineMention, "write")
	require.Zero(t, result.Jobs)

	quotedMention := event
	quotedMention.DeliveryID = "quoted-mention"
	quotedMention.Body = "> @tyrs-hand inspect"
	result = processWebhook(t, db, quotedMention, "write")
	require.Zero(t, result.Jobs)

	secondLineCommand := event
	secondLineCommand.DeliveryID = "second-line-command"
	secondLineCommand.Body = "context first\n/tyrs-hand inspect"
	result = processWebhook(t, db, secondLineCommand, "write")
	require.Zero(t, result.Jobs)

	suffixCommand := event
	suffixCommand.DeliveryID = "suffix-command"
	suffixCommand.Body = "/tyrs-hand-extra inspect"
	result = processWebhook(t, db, suffixCommand, "write")
	require.Zero(t, result.Jobs)

	codeMention := event
	codeMention.DeliveryID = "code-mention"
	codeMention.Body = "`@tyrs-hand` is an example"
	result = processWebhook(t, db, codeMention, "write")
	require.Zero(t, result.Jobs)

	escapedMention := event
	escapedMention.DeliveryID = "escaped-mention"
	escapedMention.Body = "\\@tyrs-hand inspect"
	result = processWebhook(t, db, escapedMention, "write")
	require.Zero(t, result.Jobs)

	urlMention := event
	urlMention.DeliveryID = "url-mention"
	urlMention.Body = "https://github.com/@tyrs-hand inspect"
	result = processWebhook(t, db, urlMention, "write")
	require.Zero(t, result.Jobs)

	suffixMention := event
	suffixMention.DeliveryID = "suffix-mention"
	suffixMention.Body = "@tyrs-hand-extra inspect"
	result = processWebhook(t, db, suffixMention, "write")
	require.Zero(t, result.Jobs)

	editedMention := event
	editedMention.DeliveryID = "edited-mention"
	editedMention.Action = "edited"
	editedMention.Body = "@tyrs-hand inspect"
	result = processWebhook(t, db, editedMention, "write")
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

	_, err = db.ExecContext(ctx, "UPDATE repositories SET enabled = false WHERE id = $1", repositoryID)
	require.NoError(t, err)
	disabledRepository := event
	disabledRepository.DeliveryID = "disabled-repository"
	result = processWebhook(t, db, disabledRepository, "write")
	require.Zero(t, result.Jobs)
	var deliveryError string
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT error FROM webhook_deliveries WHERE delivery_id = $1", disabledRepository.DeliveryID).Scan(&deliveryError))
	require.Equal(t, "repository not enabled", deliveryError)
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
	var commentCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/installations/1/access_tokens":
			_ = json.NewEncoder(response).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case "/repos/owner/repo/collaborators/alice/permission":
			_ = json.NewEncoder(response).Encode(map[string]any{"permission": "write"})
		case "/repos/owner/repo/issues/1":
			_ = json.NewEncoder(response).Encode(map[string]any{"id": 10, "number": 1, "title": "test", "state": "open", "user": map[string]any{"login": "alice"}})
		case "/repos/owner/repo/issues/1/comments":
			if request.Method == http.MethodGet {
				time.Sleep(25 * time.Millisecond)
				_ = json.NewEncoder(response).Encode([]any{})
				return
			}
			commentCount.Add(1)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"id": 88, "html_url": "https://github.com/owner/repo/issues/1#issuecomment-88",
			})
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
	_, err = service.GitCredential(context.Background(), capability, "invalid", "")
	require.Error(t, err)
	fetchToken, err := service.GitCredential(context.Background(), capability, "fetch", "")
	require.NoError(t, err)
	require.Equal(t, "installation-token", fetchToken)
	pushToken, err := service.GitCredential(context.Background(), capability, "push", "")
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
	equivalent, err := service.Call(context.Background(), toolservice.CallRequest{
		Capability: capability, ThreadID: "thread", TurnID: "turn", CallID: "call",
		Namespace: "github", Tool: "issue_read",
		Arguments: json.RawMessage(`{"issue_number":1,"repo":"repo","owner":"owner","method":"get"}`),
	})
	require.NoError(t, err)
	require.Equal(t, result, equivalent)
	_, err = service.Call(context.Background(), toolservice.CallRequest{
		Capability: capability, ThreadID: "thread", TurnID: "turn", CallID: "call",
		Namespace: "github", Tool: "create_pull_request",
		Arguments: json.RawMessage(`{"owner":"owner","repo":"repo","title":"conflict","head":"branch","base":"main"}`),
	})
	require.ErrorContains(t, err, "与既有请求不一致")
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
	reply := toolservice.CallRequest{
		Capability: capability, ThreadID: "thread", TurnID: "turn", CallID: "reply-1",
		Namespace: "tyrs_hand", Tool: "reply_to_github",
		Arguments: json.RawMessage(`{"body":"Finished the requested work."}`),
	}
	var replies [2]codex.ToolCallResult
	var replyErrors [2]error
	var replyGroup sync.WaitGroup
	for index := range replies {
		replyGroup.Add(1)
		go func(index int) {
			defer replyGroup.Done()
			request := reply
			request.CallID = fmt.Sprintf("reply-%d", index+1)
			replies[index], replyErrors[index] = service.Call(context.Background(), request)
		}(index)
	}
	replyGroup.Wait()
	require.NoError(t, replyErrors[0])
	require.NoError(t, replyErrors[1])
	require.True(t, replies[0].Success)
	require.Equal(t, replies[0], replies[1])
	require.EqualValues(t, 1, commentCount.Load())
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

func seedQueue(t *testing.T, db *sql.DB) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var installationID, repositoryID, workItemID, profileID uuid.UUID
	nodes := executionnode.NewService(db)
	node, _, err := nodes.Create(ctx, "queue-test", []string{"github"}, 2)
	require.NoError(t, err)
	require.NoError(t, nodes.SetDefaults(ctx, executionnode.Defaults{GitHubNodeID: &node.ID}))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scm_installations(provider,external_id,account_login,account_type) VALUES ('github',1,'test','Organization') RETURNING id`).Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO repositories(installation_id,provider,external_id,owner,name,default_branch,clone_url) VALUES ($1,'github',2,'owner','repo','main','https://example.invalid/repo.git') RETURNING id`, installationID).Scan(&repositoryID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name = 'Default'`).Scan(&profileID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO work_items(repository_id,kind,external_number,title) VALUES ($1,'issue',1,'test') RETURNING id`, repositoryID).Scan(&workItemID))
	return repositoryID, workItemID, profileID, node.ID
}
