//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

// 旧 GitHub Worker 租约测试停止后，认证与 webhook 仍是必需的独立回归。
func TestControlAuthenticationLifecycle(t *testing.T) {
	db := postgresDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	require.NoError(t, database.CheckMigrations(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	service := auth.NewService(db, box, "setup-test-token", "http://localhost")
	required, err := service.SetupRequired(ctx)
	require.NoError(t, err)
	require.True(t, required)
	_, err = service.Setup(ctx, "invalid", "admin", "integration-password")
	require.ErrorIs(t, err, auth.ErrInvalidSetupToken)
	setup, err := service.Setup(ctx, "setup-test-token", "admin", "integration-password")
	require.NoError(t, err)
	require.Len(t, setup.RecoveryCodes, 10)
	required, err = service.SetupRequired(ctx)
	require.NoError(t, err)
	require.False(t, required)
	_, err = service.Setup(ctx, "setup-test-token", "other", "integration-password")
	require.ErrorIs(t, err, auth.ErrSetupComplete)
	code, err := totp.GenerateCode(setup.TOTPSecret, time.Now())
	require.NoError(t, err)
	_, err = service.Login(ctx, "admin", "invalid", code)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
	_, err = service.Login(ctx, "missing", "integration-password", code)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
	session, err := service.Login(ctx, "admin", "integration-password", code)
	require.NoError(t, err)
	restored, err := service.Authenticate(ctx, session.Token)
	require.NoError(t, err)
	require.Equal(t, session.AdministratorID, restored.AdministratorID)
	require.Equal(t, session.CSRFToken, restored.CSRFToken)
	require.Equal(t, "admin", restored.Role)
	require.True(t, service.ValidateCSRF(ctx, session.Token, session.CSRFToken))
	require.False(t, service.ValidateCSRF(ctx, session.Token, "invalid"))
	_, err = service.Authenticate(ctx, "")
	require.ErrorIs(t, err, auth.ErrSessionInvalid)

	invited, err := service.CreateInvitation(ctx, session.AdministratorID, "invited-user", time.Hour)
	require.NoError(t, err)
	pending, err := service.ListInvitations(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "pending", pending[0].Status)
	serialized, err := json.Marshal(pending)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), invited.Token, "列表不得返回邀请凭据")
	accepted, err := service.AcceptInvitation(ctx, invited.Token, "invited-password", "")
	require.NoError(t, err)
	_, err = service.AcceptInvitation(ctx, invited.Token, "invited-password", "")
	require.ErrorIs(t, err, auth.ErrInvitationInvalid)
	invitedCode, err := totp.GenerateCode(accepted.TOTPSecret, time.Now())
	require.NoError(t, err)
	user, err := service.Login(ctx, "invited-user", "invited-password", invitedCode)
	require.NoError(t, err)
	require.Equal(t, "user", user.Role, "受邀用户不能取得管理员权限")
	_, err = db.ExecContext(ctx, "UPDATE administrators SET enabled=false WHERE id=$1", user.AdministratorID)
	require.NoError(t, err)
	_, err = service.Authenticate(ctx, user.Token)
	require.ErrorIs(t, err, auth.ErrSessionInvalid)
	_, err = service.Role(ctx, user.AdministratorID)
	require.ErrorIs(t, err, auth.ErrSessionInvalid)

	revoked, err := service.CreateInvitation(ctx, session.AdministratorID, "revoked-user", 0)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(72*time.Hour), revoked.ExpiresAt, time.Minute)
	require.NoError(t, service.RevokeInvitation(ctx, revoked.ID))
	require.ErrorIs(t, service.RevokeInvitation(ctx, revoked.ID), auth.ErrInvitationNotFound)
	require.ErrorIs(t, service.RevokeInvitation(ctx, uuid.New()), auth.ErrInvitationNotFound)
	_, err = service.AcceptInvitation(ctx, revoked.Token, "invited-password", "")
	require.ErrorIs(t, err, auth.ErrInvitationInvalid)
	expired, err := service.CreateInvitation(ctx, session.AdministratorID, "expired-user", time.Hour)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE administrator_invitations SET expires_at=now()-interval '1 second' WHERE id=$1", expired.ID)
	require.NoError(t, err)
	_, err = service.AcceptInvitation(ctx, expired.Token, "invited-password", "")
	require.ErrorIs(t, err, auth.ErrInvitationInvalid)
	summaries, err := service.ListInvitations(ctx)
	require.NoError(t, err)
	states := map[uuid.UUID]string{}
	for _, summary := range summaries {
		states[summary.ID] = summary.Status
	}
	require.Equal(t, map[uuid.UUID]string{invited.ID: "accepted", revoked.ID: "revoked", expired.ID: "expired"}, states)
	require.NoError(t, service.Logout(ctx, session.Token))
	_, err = service.Authenticate(ctx, session.Token)
	require.ErrorIs(t, err, auth.ErrSessionInvalid)
}

func TestGitHubWebhookRegression(t *testing.T) {
	db := postgresDatabase(t)
	require.NoError(t, database.Migrate(t.Context(), db))
	// Webhook 仍负责入队；已停用的 GitHub Worker 角色不再是测试前提。
	var installationID, repositoryID uuid.UUID
	require.NoError(t, db.QueryRowContext(t.Context(), "INSERT INTO scm_installations(provider,external_id,account_login,account_type) VALUES ('github',1,'test','Organization') RETURNING id").Scan(&installationID))
	require.NoError(t, db.QueryRowContext(t.Context(), "INSERT INTO repositories(installation_id,provider,external_id,owner,name,default_branch,clone_url) VALUES ($1,'github',2,'owner','repo','main','https://example.invalid/repo.git') RETURNING id", installationID).Scan(&repositoryID))
	testWebhookOrchestration(t, db, repositoryID)
	var total, codexOnly int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*),count(*) FILTER (WHERE engine='codex') FROM codex_thread_controls WHERE source_type=$1", codexcontrol.SourceGitHub).Scan(&total, &codexOnly))
	require.Positive(t, total)
	require.Equal(t, total, codexOnly, "GitHub 自动触发保持原有 Codex 引擎")
}

// 工具权限与副作用仍需验证，不依赖已经移除的 GitHub Worker 角色。
func TestGitHubToolCapabilityRegression(t *testing.T) {
	db := postgresDatabase(t)
	ctx := t.Context()
	require.NoError(t, database.Migrate(ctx, db))
	node, _, err := workerregistry.NewService(db).Create(ctx, "tool-regression", []string{"discord"}, 1)
	require.NoError(t, err)
	var installationID, repositoryID, workItemID, profileID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO scm_installations(provider,external_id,account_login,account_type) VALUES ('github',1,'test','Organization') RETURNING id").Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO repositories(installation_id,provider,external_id,owner,name,default_branch,clone_url) VALUES ($1,'github',2,'owner','repo','main','https://example.invalid/repo.git') RETURNING id", installationID).Scan(&repositoryID))
	require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO work_items(repository_id,kind,external_number,title) VALUES ($1,'issue',1,'test') RETURNING id", repositoryID).Scan(&workItemID))
	_, err = db.ExecContext(ctx, "UPDATE work_items SET worker_id=$2 WHERE id=$1", workItemID, node.ID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM agent_profiles WHERE name='Default'").Scan(&profileID))
	repository := codexcontrol.NewRepository(db, time.Minute)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, inserted, err := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceGitHub, WorkItemID: workItemID, RepositoryID: repositoryID,
		AgentProfileID: profileID, IdempotencyKey: "github-tool-regression", Instruction: "验证授权工具",
		AllowedTools: []string{"issue_read", "create_pull_request"}, ActorLogin: "alice",
		ActorPermission: "write", ReplyPolicy: "required",
	})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	claimed, err := repository.ClaimWorker(ctx, "tool-regression", codexcontrol.SourceGitHub, node.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, repository.SetThread(ctx, claimed, "thread"))
	require.NoError(t, repository.RecordSubmission(ctx, claimed, "turn"))
	require.NoError(t, repository.ConfirmTurn(ctx, claimed, "turn"))
	testOfficialGitHubTool(t, db, claimed.Capability)
	satisfied, err := repository.ReplySatisfied(ctx, claimed)
	require.NoError(t, err)
	require.True(t, satisfied, "并发回复必须只交付一次并落库")
	require.NoError(t, repository.Complete(ctx, claimed, codexcontrol.TurnResult{TurnID: "turn", FinalAnswer: "已完成"}))
	var active int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM codex_turn_runs WHERE capability_hash=$1 AND active_slot=1", security.Digest(claimed.Capability)).Scan(&active))
	require.Zero(t, active, "任务终结后旧 capability 不能继续执行工具")
}
