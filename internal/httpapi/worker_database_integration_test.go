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
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/sshconfig"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
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
	workers := server.workers
	workerA, enrollmentA, err := workers.Create(ctx, "home-a", []string{"github"}, 2)
	require.NoError(t, err)
	workerB, enrollmentB, err := workers.Create(ctx, "home-b", []string{"github"}, 2)
	require.NoError(t, err)

	clientA := workerprotocol.NewClient(endpoint, "", 5*time.Second)
	enrolledA, err := clientA.Enroll(ctx, enrollmentA)
	require.NoError(t, err)
	clientA.SetCredential(enrolledA.Credential)
	_, err = clientA.Enroll(ctx, enrollmentA)
	require.Error(t, err, "Enrollment Token 只能消费一次")
	rotationToken, err := workers.NewEnrollment(ctx, workerA.ID)
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
	_, err = clientA.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
	require.Error(t, err, "协议不兼容时必须拒绝 Claim")
	require.NoError(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
	}))
	_, credentialB, err := workers.Enroll(ctx, enrollmentB)
	require.NoError(t, err)
	clientB := workerprotocol.NewClient(endpoint, credentialB, 5*time.Second)
	_, err = clientA.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "discord",
	})
	require.Error(t, err, "Workspace 交互不再通过 Worker claim")
	require.NoError(t, workers.SetDefaults(ctx, workerregistry.Defaults{
		GitHubWorkerID: &workerA.ID,
	}))

	repositoryID, firstItemID, profileID := seedWorkerGitHubQueue(t, db, 1)
	firstIntent := enqueueWorkerIntent(t, db, repositoryID, firstItemID, profileID, "first")
	assertPlacement(t, db, firstItemID, firstIntent, workerA.ID, "queued")

	require.NoError(t, workers.SetDefaults(ctx, workerregistry.Defaults{
		GitHubWorkerID: &workerB.ID,
	}))
	secondRepositoryID, secondItemID, secondProfileID := seedWorkerGitHubQueue(t, db, 2)
	secondIntent := enqueueWorkerIntent(t, db, secondRepositoryID, secondItemID,
		secondProfileID, "second")
	assertPlacement(t, db, secondItemID, secondIntent, workerB.ID, "queued")
	claimB, err := clientB.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
	require.NoError(t, err)
	require.NotNil(t, claimB.Task)
	require.Equal(t, secondItemID, claimB.Task.Claimed.WorkItemID)
	claimA, err := clientA.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
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
	_, err = clientA.RunHeartbeat(ctx, claimA.Task)
	require.Error(t, err, "过期 GitHub Job lease 不得继续续租")
	claimA, err = clientA.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
	require.NoError(t, err)
	require.NotNil(t, claimA.Task)
	require.True(t, claimA.Task.Claimed.Recovering)
	heartbeat, err := clientA.RunHeartbeat(ctx, claimA.Task)
	require.NoError(t, err)
	require.True(t, heartbeat.Recovery.Recovering,
		"远程 Run 断线后必须先重新领取再从 Journal 恢复")

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
	require.NoError(t, clientA.Events(ctx, claimA.Task, []workerprotocol.EventInput{{
		Sequence: 2, Type: "turn.delta", Payload: json.RawMessage(`{"state":"late"}`),
	}}), "终态完成后仍必须接受 Journal 补发的中间事件")
	_, err = clientB.RunHeartbeat(ctx, claimA.Task)
	require.Error(t, err, "其他节点不能续租该 Run")
	require.Error(t, workers.Delete(ctx, workerA.ID), "仍被资源引用的节点不能删除")
	require.NoError(t, workers.SetEnabled(ctx, workerB.ID, false))
	_, err = clientB.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
	require.Error(t, err, "禁用节点不能继续领取任务")
}

func TestWorkerAPINonRetryableCodexErrorDoesNotRequeue(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "codex-non-retry", []string{"github"}, 1)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.workers.SetDefaults(ctx, workerregistry.Defaults{GitHubWorkerID: &worker.ID}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, itemID, profileID := seedWorkerGitHubQueue(t, db, 92)
	intentID := enqueueWorkerIntent(t, db, repositoryID, itemID, profileID, "capacity")
	claimed, err := client.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "github",
	})
	require.NoError(t, err)
	require.NotNil(t, claimed.Task)
	require.Equal(t, intentID, claimed.Task.Claimed.ID)
	require.NoError(t, client.SetThread(ctx, claimed.Task, "thread-capacity"))
	require.NoError(t, client.RecordSubmission(ctx, claimed.Task, "turn-capacity"))
	require.NoError(t, client.ConfirmTurn(ctx, claimed.Task, "turn-capacity"))

	codexError := &workerprotocol.CodexTurnError{
		Message:        "Selected model is at capacity. Please try a different model.",
		CodexErrorInfo: json.RawMessage(`"serverOverloaded"`), WillRetry: false,
		ThreadID: "thread-capacity", TurnID: "turn-capacity",
	}
	require.NoError(t, client.FailWithCodexError(ctx, claimed.Task,
		"codex_non_retryable_error", codexError, codexError))

	var intentStatus, runStatus, errorType string
	var attempts int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT intent.status, intent.attempt_count,
		run.status, run.codex_error->>'codexErrorInfo'
		FROM codex_turn_intents intent JOIN codex_turn_runs run ON run.primary_intent_id=intent.id
		WHERE intent.id=$1`, intentID).Scan(&intentStatus, &attempts, &runStatus, &errorType))
	require.Equal(t, "failed", intentStatus)
	require.Equal(t, 1, attempts)
	require.Equal(t, "failed", runStatus)
	require.Equal(t, "serverOverloaded", errorType)

	next, err := client.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "github",
	})
	require.NoError(t, err)
	require.Nil(t, next.Task)
}

func TestWorkerAPISSHConfigurationAndGitHubAgentInstructions(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	workerA, enrollmentA, err := server.workers.Create(ctx, "ssh-a", []string{"github"}, 1)
	require.NoError(t, err)
	workerB, enrollmentB, err := server.workers.Create(ctx, "ssh-b", []string{"github"}, 1)
	require.NoError(t, err)
	_, workerCredential, err := server.workers.Enroll(ctx, enrollmentA)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, workerCredential, 5*time.Second)
	_, workerCredentialB, err := server.workers.Enroll(ctx, enrollmentB)
	require.NoError(t, err)
	clientB := workerprotocol.NewClient(endpoint, workerCredentialB, 5*time.Second)

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
		CredentialID: credential.ID, WorkerIDs: []uuid.UUID{workerA.ID},
	})
	require.NoError(t, err)
	_, err = server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "wrong-worker", Hostname: "192.0.2.2", Port: 22, Username: "ubuntu",
		CredentialID: credential.ID, ProxyJumpHostID: &jump.ID,
		WorkerIDs: []uuid.UUID{workerB.ID},
	})
	require.ErrorContains(t, err, "相同的 Worker")
	target, err := server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "target", Hostname: "192.0.2.3", Port: 2222, Username: "deploy",
		CredentialID: credential.ID, ProxyJumpHostID: &jump.ID,
		WorkerIDs: []uuid.UUID{workerA.ID},
	})
	require.NoError(t, err)
	_, err = server.ssh.UpdateHost(ctx, jump.ID, sshconfig.HostInput{
		Alias: jump.Alias, Hostname: jump.Hostname, Port: jump.Port, Username: jump.Username,
		CredentialID: credential.ID, ProxyJumpHostID: &target.ID,
		WorkerIDs: []uuid.UUID{workerA.ID},
	})
	require.ErrorContains(t, err, "循环")
	_, err = server.ssh.UpdateHost(ctx, jump.ID, sshconfig.HostInput{
		Alias: jump.Alias, Hostname: jump.Hostname, Port: jump.Port, Username: jump.Username,
		CredentialID: credential.ID, WorkerIDs: nil,
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

	require.NoError(t, server.settings.SaveGitHubAgentInstructions(ctx,
		platformsettings.GitHubAgentInstructions{Content: "# GitHub Agent\r\n"}))
	agents, err := server.settings.GitHubAgentInstructions(ctx)
	require.NoError(t, err)
	require.Equal(t, "# GitHub Agent\n", agents.Content)
	agentsRecorder := httptest.NewRecorder()
	agentsContext, _ := gin.CreateTestContext(agentsRecorder)
	agentsContext.Request = httptest.NewRequest("PUT", "/api/v1/settings/github-agent-instructions",
		strings.NewReader(`{"content":"# Managed through API\n"}`))
	agentsContext.Request.Header.Set("Content-Type", "application/json")
	server.putGitHubAgentInstructions(agentsContext)
	require.Equal(t, 204, agentsContext.Writer.Status())
	var globalAuditCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs
		WHERE action='settings.github_agent_instructions.update'
		AND resource_id='github.agent.instructions'`).
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
	secretStore := secrets.NewStore(db, box)
	settings := platformsettings.NewService(db)
	server := &Server{cfg: config.Config{LeaseDuration: 2 * time.Second,
		CodexMaxSteersPerTurn: 5, CodexReconcileMaxAttempts: 3}, db: db,
		workers: workerregistry.NewService(db), settings: settings,
		ssh: sshconfig.NewService(db, secretStore), secrets: secretStore}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		for _, item := range c.Errors {
			t.Logf("worker API error: %v", item.Err)
		}
	})
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
	for attempt := 0; err != nil && attempt < 50; attempt++ {
		time.Sleep(100 * time.Millisecond)
		port, err = container.MappedPort(ctx, "5432/tcp")
	}
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
			IdempotencyKey: key, Instruction: "test", ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func assertPlacement(t *testing.T, db *sql.DB, itemID, intentID, expectedWorkerID uuid.UUID,
	expectedStatus string,
) {
	t.Helper()
	var itemWorker, controlWorker sql.NullString
	var status string
	require.NoError(t, db.QueryRow(`SELECT w.worker_id::text,
		c.worker_id::text, i.status FROM work_items w
		JOIN codex_turn_intents i ON i.id = $2
		JOIN codex_thread_controls c ON c.id = i.control_id WHERE w.id = $1`, itemID, intentID).
		Scan(&itemWorker, &controlWorker, &status))
	require.Equal(t, expectedStatus, status)
	if expectedWorkerID == uuid.Nil {
		require.False(t, itemWorker.Valid)
		require.False(t, controlWorker.Valid)
		return
	}
	require.Equal(t, expectedWorkerID.String(), itemWorker.String)
	require.Equal(t, expectedWorkerID.String(), controlWorker.String)
}
