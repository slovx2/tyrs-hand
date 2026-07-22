//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/executionnode"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/sshconfig"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

func TestWorkerAPIPlacementLeaseEventsAndIdempotency(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	nodes := server.nodes
	nodeA, enrollmentA, err := nodes.Create(ctx, "home-a", []string{"github", "discord"}, 2)
	require.NoError(t, err)
	nodeB, enrollmentB, err := nodes.Create(ctx, "home-b", []string{"github", "discord"}, 2)
	require.NoError(t, err)
	_, enrollmentGitHubOnly, err := nodes.Create(ctx, "github-only", []string{"github"}, 1)
	require.NoError(t, err)

	clientA := workerprotocol.NewClient(endpoint, "", 5*time.Second)
	enrolledA, err := clientA.Enroll(ctx, enrollmentA)
	require.NoError(t, err)
	clientA.SetCredential(enrolledA.Credential)
	_, err = clientA.Enroll(ctx, enrollmentA)
	require.Error(t, err, "Enrollment Token 只能消费一次")
	rotationToken, err := nodes.NewEnrollment(ctx, nodeA.ID)
	require.NoError(t, err)
	rotated, err := workerprotocol.NewClient(endpoint, "", 5*time.Second).Enroll(ctx, rotationToken)
	require.NoError(t, err)
	require.Error(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "old", ProtocolVersion: workerprotocol.Version,
	}), "凭据轮换后旧节点 Token 必须立即失效")
	clientA.SetCredential(rotated.Credential)
	require.NoError(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
	}))
	require.NoError(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "future", ProtocolVersion: workerprotocol.Version + 1,
	}), "协议不兼容时仍允许心跳上报")
	_, err = clientA.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "worker-a", Role: "github"})
	require.Error(t, err, "协议不兼容时必须拒绝 Claim")
	require.NoError(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
	}))
	_, credentialB, err := nodes.Enroll(ctx, enrollmentB)
	require.NoError(t, err)
	clientB := workerprotocol.NewClient(endpoint, credentialB, 5*time.Second)
	_, githubOnlyCredential, err := nodes.Enroll(ctx, enrollmentGitHubOnly)
	require.NoError(t, err)
	githubOnlyClient := workerprotocol.NewClient(endpoint, githubOnlyCredential, 5*time.Second)
	_, err = githubOnlyClient.Claim(ctx, workerprotocol.ClaimRequest{
		WorkerID: "github-only", Role: "discord",
	})
	require.Error(t, err, "节点不能越权领取未授权角色")
	require.NoError(t, nodes.SetDefaults(ctx, executionnode.Defaults{
		GitHubNodeID: &nodeA.ID, DiscordNodeID: &nodeA.ID,
	}))

	repositoryID, firstItemID, profileID := seedWorkerGitHubQueue(t, db, 1)
	firstIntent := enqueueWorkerIntent(t, db, repositoryID, firstItemID, profileID, "first")
	assertPlacement(t, db, firstItemID, firstIntent, nodeA.ID, "queued")

	require.NoError(t, nodes.SetDefaults(ctx, executionnode.Defaults{
		GitHubNodeID: &nodeB.ID, DiscordNodeID: &nodeB.ID,
	}))
	secondRepositoryID, secondItemID, secondProfileID := seedWorkerGitHubQueue(t, db, 2)
	secondIntent := enqueueWorkerIntent(t, db, secondRepositoryID, secondItemID,
		secondProfileID, "second")
	assertPlacement(t, db, secondItemID, secondIntent, nodeB.ID, "queued")
	thirdIntent := enqueueWorkerIntent(t, db, repositoryID, firstItemID, profileID, "first-again")
	assertPlacement(t, db, firstItemID, thirdIntent, nodeA.ID, "queued")

	claimB, err := clientB.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "worker-b",
		Role: "github"})
	require.NoError(t, err)
	require.NotNil(t, claimB.Task)
	require.Equal(t, secondItemID, claimB.Task.Claimed.WorkItemID)
	claimA, err := clientA.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "worker-a",
		Role: "github"})
	require.NoError(t, err)
	require.NotNil(t, claimA.Task)
	require.Equal(t, firstItemID, claimA.Task.Claimed.WorkItemID)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls
		SET lease_expires_at = now() - interval '1 second' WHERE id = $1`,
		claimA.Task.Claimed.ControlID)
	require.NoError(t, err)
	requeued, err := codexcontrol.NewRepository(db, 2*time.Second).RequeueExpired(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, requeued)
	heartbeat, err := clientA.RunHeartbeat(ctx, claimA.Task)
	require.NoError(t, err)
	require.True(t, heartbeat.Recovery.Recovering,
		"远程 Run 断线后必须由原节点使用 Journal 中的 Lease 恢复")
	require.Len(t, heartbeat.Commands, 1)
	require.Equal(t, thirdIntent, heartbeat.Commands[0].ID)
	require.NoError(t, clientA.AckCommand(ctx, claimA.Task, heartbeat.Commands[0], "steer", "turn-a"))
	interruptID := enqueueWorkerOperation(t, db, repositoryID, firstItemID, profileID,
		"interrupt-a", "interrupt")
	heartbeat, err = clientA.RunHeartbeat(ctx, claimA.Task)
	require.NoError(t, err)
	require.Len(t, heartbeat.Commands, 1)
	require.Equal(t, interruptID, heartbeat.Commands[0].ID)
	require.NoError(t, clientA.AckCommand(ctx, claimA.Task, heartbeat.Commands[0], "interrupt", "turn-a"))

	event := workerprotocol.EventInput{Sequence: 1, Type: "turn.started",
		Payload: json.RawMessage(`{"state":"running"}`)}
	require.NoError(t, clientA.Events(ctx, claimA.Task, []workerprotocol.EventInput{event}))
	require.NoError(t, clientA.Events(ctx, claimA.Task, []workerprotocol.EventInput{event}),
		"重复事件必须幂等")
	require.Error(t, clientA.Events(ctx, claimA.Task, []workerprotocol.EventInput{{
		Sequence: 3, Type: "turn.delta", Payload: json.RawMessage(`{}`),
	}}), "跳号事件必须拒绝")
	require.NoError(t, clientA.Complete(ctx, claimA.Task, codexcontrol.TurnResult{
		TurnID: "turn-a", FinalAnswer: "done",
	}))
	require.NoError(t, clientA.Complete(ctx, claimA.Task, codexcontrol.TurnResult{
		TurnID: "turn-a", FinalAnswer: "done",
	}), "重复完成必须幂等")
	_, err = clientB.RunHeartbeat(ctx, claimA.Task)
	require.Error(t, err, "其他节点不能续租该 Run")
	require.Error(t, nodes.Delete(ctx, nodeA.ID), "仍被资源引用的节点不能删除")
	require.NoError(t, nodes.SetEnabled(ctx, nodeB.ID, false))
	_, err = clientB.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "worker-b", Role: "github"})
	require.Error(t, err, "禁用节点不能继续领取任务")
}

func TestWorkerAPIDiscordRuntimePreferencesFreeze(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	node, enrollment, err := server.nodes.Create(ctx, "discord-home", []string{"discord"}, 1)
	require.NoError(t, err)
	_, credential, err := server.nodes.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.nodes.SetDefaults(ctx, executionnode.Defaults{
		DiscordNodeID: &node.ID,
	}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 31)
	_, forumID := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 repository_id, agent_profile_id, title, model, reasoning_effort, service_tier,
		 configuration_status, title_rename_status)
		VALUES ('worker-test-guild',$1,'runtime-thread','runtime-message-1','worker-owner',
		 $2,$3,'runtime','gpt-5.6-sol','xhigh','standard','configured','completed')
		RETURNING id`, forumID, repositoryID, profileID).Scan(&conversationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body) VALUES
		('runtime-message-1',$1,'worker-owner','Owner','owner','owner','first')
		RETURNING conversation_id`, conversationID).Scan(&conversationID))
	firstIntent := enqueueWorkerDiscordIntent(t, db, conversationID, "runtime-message-1",
		repositoryID, profileID)

	first, err := client.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "discord-worker",
		Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, first.Task)
	require.Equal(t, firstIntent, first.Task.Claimed.ID)
	require.Equal(t, "gpt-5.6-sol", first.Task.Snapshot.Runtime.Model)
	require.Equal(t, "xhigh", first.Task.Snapshot.Runtime.ReasoningEffort)
	require.Equal(t, "standard", first.Task.Snapshot.Runtime.ServiceTier)
	var frozenModel, frozenEffort, frozenTier string
	var frozen bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(model,''),
		COALESCE(reasoning_effort,''), COALESCE(service_tier,''),
		runtime_preferences_frozen_at IS NOT NULL FROM codex_thread_controls
		WHERE id = $1`, first.Task.Claimed.ControlID).
		Scan(&frozenModel, &frozenEffort, &frozenTier, &frozen))
	require.Equal(t, "gpt-5.6-sol", frozenModel)
	require.Equal(t, "xhigh", frozenEffort)
	require.Equal(t, "standard", frozenTier)
	require.True(t, frozen)
	require.NoError(t, client.Complete(ctx, first.Task, codexcontrol.TurnResult{
		TurnID: "runtime-turn-1", FinalAnswer: "done",
	}))

	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET model = 'gpt-5.4',
		reasoning_effort = 'low', service_tier = 'fast' WHERE id = $1`, conversationID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body) VALUES
		('runtime-message-2',$1,'worker-owner','Owner','owner','owner','second')`, conversationID)
	require.NoError(t, err)
	secondIntent := enqueueWorkerDiscordIntent(t, db, conversationID, "runtime-message-2",
		repositoryID, profileID)
	second, err := client.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "discord-worker",
		Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, second.Task)
	require.Equal(t, secondIntent, second.Task.Claimed.ID)
	require.Equal(t, first.Task.Claimed.ControlID, second.Task.Claimed.ControlID)
	require.Equal(t, "gpt-5.6-sol", second.Task.Snapshot.Runtime.Model)
	require.Equal(t, "xhigh", second.Task.Snapshot.Runtime.ReasoningEffort)
	require.Equal(t, "standard", second.Task.Snapshot.Runtime.ServiceTier)
}

func TestWorkerAPIMissingDefaultAndDevelopmentOperationRecovery(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	node, enrollment, err := server.nodes.Create(ctx, "home", []string{"github", "discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.nodes.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, itemID, profileID := seedWorkerGitHubQueue(t, db, 3)
	intentID := enqueueWorkerIntent(t, db, repositoryID, itemID, profileID, "pending")
	assertPlacement(t, db, itemID, intentID, uuid.Nil, "placement_pending")
	require.NoError(t, server.nodes.SetDefaults(ctx, executionnode.Defaults{
		GitHubNodeID: &node.ID, DiscordNodeID: &node.ID,
	}))
	assertPlacement(t, db, itemID, intentID, node.ID, "queued")

	environmentID, forumID := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	var operationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, forum_id, operation, execution_node_id)
		VALUES ($1,$2,'delete_forum',$3) RETURNING id`, environmentID, forumID, node.ID).
		Scan(&operationID))
	first, err := client.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "home-worker",
		Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, first.DevelopmentOperation)
	require.Equal(t, operationID, first.DevelopmentOperation.ID)
	firstEpoch := first.DevelopmentOperation.LeaseEpoch
	_, err = db.ExecContext(ctx, `UPDATE discord_development_operations
		SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, operationID)
	require.NoError(t, err)
	second, err := client.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "home-worker",
		Role: "discord"})
	require.NoError(t, err)
	require.Greater(t, second.DevelopmentOperation.LeaseEpoch, firstEpoch)
	require.Error(t, client.CompleteDevelopmentOperation(ctx, first.DevelopmentOperation),
		"旧 Lease 不能完成 Operation")
	require.NoError(t, client.CompleteDevelopmentOperation(ctx, second.DevelopmentOperation))
	require.NoError(t, client.CompleteDevelopmentOperation(ctx, second.DevelopmentOperation),
		"重复完成 Operation 必须幂等")
	var forumCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_forums WHERE id = $1`,
		forumID).Scan(&forumCount))
	require.Zero(t, forumCount)
	var operationStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM discord_development_operations
		WHERE id = $1`, operationID).Scan(&operationStatus))
	require.Equal(t, "completed", operationStatus)
}

func TestWorkerAPISSHConfigurationRotationConstraintsAndGlobalAgents(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	nodeA, enrollmentA, err := server.nodes.Create(ctx, "ssh-a", []string{"github"}, 1)
	require.NoError(t, err)
	nodeB, enrollmentB, err := server.nodes.Create(ctx, "ssh-b", []string{"github"}, 1)
	require.NoError(t, err)
	_, nodeCredential, err := server.nodes.Enroll(ctx, enrollmentA)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, nodeCredential, 5*time.Second)
	_, nodeCredentialB, err := server.nodes.Enroll(ctx, enrollmentB)
	require.NoError(t, err)
	clientB := workerprotocol.NewClient(endpoint, nodeCredentialB, 5*time.Second)

	privateKeyA := testSSHPrivateKey(t, "")
	credential, err := server.ssh.CreateCredential(ctx, sshconfig.CredentialInput{
		Name: "production", PrivateKey: privateKeyA,
	})
	require.NoError(t, err)
	var ciphertext []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT es.ciphertext FROM encrypted_secrets es
		JOIN ssh_credentials c ON c.secret_id=es.id WHERE c.id=$1`, credential.ID).Scan(&ciphertext))
	require.False(t, bytes.Contains(ciphertext, []byte("PRIVATE KEY")))
	jump, err := server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "jump", Hostname: "192.0.2.1", Port: 22, Username: "ubuntu",
		CredentialID: credential.ID, ExecutionNodeIDs: []uuid.UUID{nodeA.ID},
	})
	require.NoError(t, err)
	_, err = server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "wrong-node", Hostname: "192.0.2.2", Port: 22, Username: "ubuntu",
		CredentialID: credential.ID, ProxyJumpHostID: &jump.ID,
		ExecutionNodeIDs: []uuid.UUID{nodeB.ID},
	})
	require.ErrorContains(t, err, "相同的 Execution Node")
	target, err := server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "target", Hostname: "192.0.2.3", Port: 2222, Username: "deploy",
		CredentialID: credential.ID, ProxyJumpHostID: &jump.ID,
		ExecutionNodeIDs: []uuid.UUID{nodeA.ID},
	})
	require.NoError(t, err)
	_, err = server.ssh.UpdateHost(ctx, jump.ID, sshconfig.HostInput{
		Alias: jump.Alias, Hostname: jump.Hostname, Port: jump.Port, Username: jump.Username,
		CredentialID: credential.ID, ProxyJumpHostID: &target.ID,
		ExecutionNodeIDs: []uuid.UUID{nodeA.ID},
	})
	require.ErrorContains(t, err, "循环")
	_, err = server.ssh.UpdateHost(ctx, jump.ID, sshconfig.HostInput{
		Alias: jump.Alias, Hostname: jump.Hostname, Port: jump.Port, Username: jump.Username,
		CredentialID: credential.ID, ExecutionNodeIDs: nil,
	})
	require.ErrorContains(t, err, "仍被已启用主机")

	configuration, etag, changed, err := client.SSHConfiguration(ctx, "")
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, configuration.Hosts, 2)
	require.Len(t, configuration.Credentials, 1)
	require.Equal(t, strings.TrimSpace(privateKeyA), configuration.Credentials[0].PrivateKey)
	require.NotEmpty(t, etag)
	configurationB, _, changed, err := clientB.SSHConfiguration(ctx, "")
	require.NoError(t, err)
	require.True(t, changed)
	require.Empty(t, configurationB.Hosts)
	require.Empty(t, configurationB.Credentials)
	_, sameETag, changed, err := client.SSHConfiguration(ctx, etag)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, etag, sameETag)

	privateKeyB := testSSHPrivateKey(t, "rotation-passphrase")
	enabled := true
	rotated, err := server.ssh.UpdateCredential(ctx, credential.ID, sshconfig.CredentialInput{
		Name: "production", PrivateKey: privateKeyB, Passphrase: "rotation-passphrase",
		Enabled: &enabled,
	})
	require.NoError(t, err)
	require.Greater(t, rotated.Version, credential.Version)
	configuration, rotatedETag, changed, err := client.SSHConfiguration(ctx, etag)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotEqual(t, etag, rotatedETag)
	require.Equal(t, strings.TrimSpace(privateKeyB), configuration.Credentials[0].PrivateKey)
	require.Equal(t, "rotation-passphrase", configuration.Credentials[0].Passphrase)
	require.ErrorContains(t, server.ssh.DeleteCredential(ctx, credential.ID), "关联主机")

	disabled := false
	_, err = server.ssh.UpdateCredential(ctx, credential.ID, sshconfig.CredentialInput{
		Name: "production", Enabled: &disabled,
	})
	require.NoError(t, err)
	configuration, _, changed, err = client.SSHConfiguration(ctx, rotatedETag)
	require.NoError(t, err)
	require.True(t, changed)
	require.Empty(t, configuration.Hosts)
	require.Empty(t, configuration.Credentials)

	apiKey := testSSHPrivateKey(t, "api-passphrase")
	createBody, err := json.Marshal(sshconfig.CredentialInput{
		Name: "api-managed", PrivateKey: apiKey, Passphrase: "api-passphrase",
	})
	require.NoError(t, err)
	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Request = httptest.NewRequest("POST", "/api/v1/ssh/credentials",
		bytes.NewReader(createBody))
	createContext.Request.Header.Set("Content-Type", "application/json")
	server.createSSHCredential(createContext)
	require.Equal(t, 201, createRecorder.Code)
	require.NotContains(t, createRecorder.Body.String(), "PRIVATE KEY")
	require.NotContains(t, createRecorder.Body.String(), "api-passphrase")
	require.NotContains(t, createRecorder.Body.String(), "ciphertext")
	var apiCredential sshconfig.Credential
	require.NoError(t, json.Unmarshal(createRecorder.Body.Bytes(), &apiCredential))
	var secretBeforeUpdate []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT es.ciphertext FROM encrypted_secrets es
		JOIN ssh_credentials c ON c.secret_id=es.id WHERE c.id=$1`, apiCredential.ID).
		Scan(&secretBeforeUpdate))

	updateBody, err := json.Marshal(sshconfig.CredentialInput{Name: "api-renamed", Enabled: &enabled})
	require.NoError(t, err)
	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Request = httptest.NewRequest("PUT", "/api/v1/ssh/credentials/"+apiCredential.ID.String(),
		bytes.NewReader(updateBody))
	updateContext.Request.Header.Set("Content-Type", "application/json")
	updateContext.Params = gin.Params{{Key: "id", Value: apiCredential.ID.String()}}
	server.updateSSHCredential(updateContext)
	require.Equal(t, 200, updateRecorder.Code)
	require.NotContains(t, updateRecorder.Body.String(), "PRIVATE KEY")
	var storedSecret []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT es.ciphertext FROM encrypted_secrets es
		JOIN ssh_credentials c ON c.secret_id=es.id WHERE c.id=$1`, apiCredential.ID).Scan(&storedSecret))
	require.Equal(t, secretBeforeUpdate, storedSecret)

	deleteRecorder := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRecorder)
	deleteContext.Request = httptest.NewRequest("DELETE", "/api/v1/ssh/credentials/"+apiCredential.ID.String(), nil)
	deleteContext.Params = gin.Params{{Key: "id", Value: apiCredential.ID.String()}}
	server.deleteSSHCredential(deleteContext)
	require.Equal(t, 204, deleteContext.Writer.Status())
	var auditActions []string
	rows, err := db.QueryContext(ctx, `SELECT action FROM audit_logs
		WHERE resource_id=$1 ORDER BY created_at`, apiCredential.ID.String())
	require.NoError(t, err)
	for rows.Next() {
		var action string
		require.NoError(t, rows.Scan(&action))
		auditActions = append(auditActions, action)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"ssh_credential.create", "ssh_credential.update",
		"ssh_credential.delete"}, auditActions)

	repositoryID, itemID, _ := seedWorkerGitHubQueue(t, db, 80)
	_ = repositoryID
	var before int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT context_version FROM work_items WHERE id=$1`,
		itemID).Scan(&before))
	providerBefore, err := server.settings.AgentProvider(ctx)
	require.NoError(t, err)
	require.NoError(t, server.settings.SaveGlobalAgents(ctx,
		platformsettings.GlobalAgents{Content: "# Global\r\n"}))
	agents, err := server.settings.GlobalAgents(ctx)
	require.NoError(t, err)
	require.Equal(t, "# Global\n", agents.Content)
	providerAfter, err := server.settings.AgentProvider(ctx)
	require.NoError(t, err)
	require.NotEqual(t, providerBefore.ConfigSignature, providerAfter.ConfigSignature)
	var after int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT context_version FROM work_items WHERE id=$1`,
		itemID).Scan(&after))
	require.Equal(t, before+1, after)
	require.NoError(t, server.settings.SaveGlobalAgents(ctx,
		platformsettings.GlobalAgents{Content: "# Global\n"}))
	var unchanged int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT context_version FROM work_items WHERE id=$1`,
		itemID).Scan(&unchanged))
	require.Equal(t, after, unchanged)
	agentsRecorder := httptest.NewRecorder()
	agentsContext, _ := gin.CreateTestContext(agentsRecorder)
	agentsContext.Request = httptest.NewRequest("PUT", "/api/v1/settings/global-agents",
		strings.NewReader(`{"content":"# Managed through API\n"}`))
	agentsContext.Request.Header.Set("Content-Type", "application/json")
	server.putGlobalAgents(agentsContext)
	require.Equal(t, 204, agentsContext.Writer.Status())
	var globalAuditCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs
		WHERE action='settings.global_agents.update' AND resource_id='codex.global_agents'`).
		Scan(&globalAuditCount))
	require.Equal(t, 1, globalAuditCount)
}

func testSSHPrivateKey(t *testing.T, passphrase string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "integration")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "integration", []byte(passphrase))
	}
	require.NoError(t, err)
	return string(pem.EncodeToMemory(block))
}

func workerTestServer(t *testing.T, db *sql.DB) (*Server, string) {
	t.Helper()
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	settings := platformsettings.NewService(db, secrets.NewStore(db, box))
	require.NoError(t, settings.SaveAgentProvider(context.Background(),
		platformsettings.AgentProviderInput{ProviderType: "api-key", APIKey: "test-key"}))
	server := &Server{cfg: config.Config{LeaseDuration: 2 * time.Second,
		CodexMaxSteersPerTurn: 5, CodexReconcileMaxAttempts: 3}, db: db,
		nodes: executionnode.NewService(db), settings: settings,
		ssh: sshconfig.NewService(db, secrets.NewStore(db, box))}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	server.registerWorkerRoutes(router)
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)
	return server, httpServer.URL
}

func workerDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{Image: "postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba",
			Env:          map[string]string{"POSTGRES_DB": "tyrs_hand", "POSTGRES_USER": "tyrs_hand", "POSTGRES_PASSWORD": "test-password"},
			ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second)}, Started: true})
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

func seedWorkerGitHubQueue(t *testing.T, db *sql.DB, number int) (uuid.UUID, uuid.UUID,
	uuid.UUID,
) {
	t.Helper()
	ctx := context.Background()
	var installationID, repositoryID, itemID, profileID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scm_installations
		(provider, external_id, account_login, account_type)
		VALUES ('github',$1,$2,'Organization') RETURNING id`, number,
		"owner-"+uuid.NewString()).Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO repositories
		(installation_id, provider, external_id, owner, name, default_branch, clone_url)
		VALUES ($1,'github',$2,$3,$4,'main',$5) RETURNING id`, installationID,
		number, "owner", "repo-"+uuid.NewString(), "https://example.invalid/repo.git").
		Scan(&repositoryID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO work_items
		(repository_id, kind, external_number, title) VALUES ($1,'issue',$2,'test')
		RETURNING id`, repositoryID, number).Scan(&itemID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name = 'Default'`).
		Scan(&profileID))
	return repositoryID, itemID, profileID
}

func enqueueWorkerIntent(t *testing.T, db *sql.DB, repositoryID, itemID, profileID uuid.UUID,
	key string,
) uuid.UUID {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	intentID, inserted, err := codexcontrol.NewRepository(db, 2*time.Second).Enqueue(
		context.Background(), tx, codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceGitHub,
			WorkItemID: itemID, RepositoryID: repositoryID, AgentProfileID: profileID,
			ContextVersion: 1, IdempotencyKey: key, Instruction: "test", ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func enqueueWorkerDiscordIntent(t *testing.T, db *sql.DB, conversationID uuid.UUID,
	messageID string, repositoryID, profileID uuid.UUID,
) uuid.UUID {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	intentID, inserted, err := codexcontrol.NewRepository(db, 2*time.Second).Enqueue(
		context.Background(), tx, codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceDiscord, DiscordConversationID: conversationID,
			DiscordMessageID: messageID, RepositoryID: repositoryID, AgentProfileID: profileID,
			ContextVersion: 1, IdempotencyKey: "discord:" + messageID,
			Instruction: messageID, ReplyPolicy: "silent", Behavior: "steer_if_active",
		})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func enqueueWorkerOperation(t *testing.T, db *sql.DB, repositoryID, itemID, profileID uuid.UUID,
	key, operation string,
) uuid.UUID {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	intentID, inserted, err := codexcontrol.NewRepository(db, 2*time.Second).Enqueue(
		context.Background(), tx, codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceGitHub,
			WorkItemID: itemID, RepositoryID: repositoryID, AgentProfileID: profileID,
			ContextVersion: 1, IdempotencyKey: key, Instruction: "stop", Operation: operation,
			ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func assertPlacement(t *testing.T, db *sql.DB, itemID, intentID, expectedNodeID uuid.UUID,
	expectedStatus string,
) {
	t.Helper()
	var itemNode, controlNode sql.NullString
	var status string
	require.NoError(t, db.QueryRow(`SELECT w.execution_node_id::text,
		c.execution_node_id::text, i.status FROM work_items w
		JOIN codex_turn_intents i ON i.id = $2
		JOIN codex_thread_controls c ON c.id = i.control_id WHERE w.id = $1`, itemID, intentID).
		Scan(&itemNode, &controlNode, &status))
	require.Equal(t, expectedStatus, status)
	if expectedNodeID == uuid.Nil {
		require.False(t, itemNode.Valid)
		require.False(t, controlNode.Valid)
		return
	}
	require.Equal(t, expectedNodeID.String(), itemNode.String)
	require.Equal(t, expectedNodeID.String(), controlNode.String)
}

func seedDevelopmentOperation(t *testing.T, db *sql.DB, repositoryID,
	nodeID uuid.UUID,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, enabled)
		VALUES ('worker-test-guild', true)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('worker-test-guild','worker-owner','owner','Owner')`)
	require.NoError(t, err)
	var environmentID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_environments
		(guild_id, owner_discord_user_id, build_repository_id, container_name,
		 data_volume_name, home_volume_name, network_name, execution_node_id)
		VALUES ('worker-test-guild','worker-owner',$1,'dev-worker','dev-worker-data',
		'dev-worker-home','dev-worker-net',$2) RETURNING id`, repositoryID, nodeID).
		Scan(&environmentID))
	var resourceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_resources
		(guild_id, resource_key, discord_id, kind, name, managed_marker)
		VALUES ('worker-test-guild','forum.worker','123456','forum','worker','marker')
		RETURNING id`).Scan(&resourceID))
	var forumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums
		(guild_id, resource_id, forum_type, owner_discord_user_id, repository_id,
		 development_environment_id)
		VALUES ('worker-test-guild',$1,'development','worker-owner',$2,$3) RETURNING id`,
		resourceID, repositoryID, environmentID).Scan(&forumID))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_workspaces
		(forum_id, environment_id, relative_path, branch, status)
		VALUES ($1,$2,$3,'worker/test','ready')`, forumID, environmentID,
		"workspaces/"+forumID.String())
	require.NoError(t, err)
	return environmentID, forumID
}
