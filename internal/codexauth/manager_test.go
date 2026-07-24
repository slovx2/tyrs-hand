package codexauth

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPrepareSharedHomeForcesFileCredentialStorage(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(
		"model = \"personal\"\ncli_auth_credentials_store = \"keyring\"\n"+
			"[mcp_servers.personal]\nurl = \"https://example.com\"\n"), 0o644))
	require.NoError(t, prepareSharedHome(home))
	require.NoError(t, prepareSharedHome(home))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(data), "cli_auth_credentials_store"))
	require.Contains(t, string(data), `cli_auth_credentials_store = "file"`)
	require.Contains(t, string(data), `model = "personal"`)
	require.Contains(t, string(data), `[mcp_servers.personal]`)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestManagerCompletesLoginWithNullableLoginID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	operationID, administratorID := uuid.New(), uuid.New()
	expectManagerCleanup(mock)
	mock.ExpectQuery("INSERT INTO codex_auth_operations").
		WithArgs(administratorID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).
			AddRow(operationID, "pending"))
	mock.ExpectExec("UPDATE codex_auth_operations SET login_id").
		WithArgs(operationID, "login-1", "https://auth.openai.com/codex/device",
			"ABCD-1234", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT value FROM platform_settings").
		WithArgs("agent.provider").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(
			[]byte(`{"modelSource":"provider","providerConfigured":true,"credentialVersion":"v1"}`)))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO platform_settings").
		WithArgs("agent.provider", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE work_items").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE discord_conversations").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE codex_auth_operations").
		WithArgs(operationID, "user@example.com", "plus").
		WillReturnResult(sqlmock.NewResult(0, 1))

	loginRequest := make(chan map[string]any, 1)
	launcher := &rpcLauncher{script: func(server *rpcServer) {
		server.initialize()
		request := server.request()
		loginRequest <- request
		server.send(map[string]any{"id": request["id"], "result": map[string]any{
			"type": "chatgptDeviceCode", "loginId": "login-1",
			"verificationUrl": "https://auth.openai.com/codex/device",
			"userCode":        "ABCD-1234",
		}})
		server.send(map[string]any{"method": "account/login/completed", "params": map[string]any{
			"loginId": nil, "success": true, "error": nil,
		}})
		request = server.request()
		server.send(map[string]any{"id": request["id"], "result": map[string]any{
			"account": nil,
		}})
		request = server.request()
		server.send(map[string]any{"id": request["id"], "result": map[string]any{
			"account": map[string]any{
				"type": "chatgpt", "email": "user@example.com", "planType": "plus",
			},
		}})
		for server.request() != nil {
		}
	}}
	manager := newTestManager(t, db, launcher)
	operation, err := manager.Start(context.Background(), administratorID)
	require.NoError(t, err)
	require.Equal(t, operationID, operation.ID)
	require.Equal(t, "awaiting_user", operation.Status)
	require.Equal(t, "https://auth.openai.com/codex/device", operation.VerificationURL)
	require.Equal(t, "ABCD-1234", operation.UserCode)
	request := <-loginRequest
	require.Equal(t, map[string]any{"type": "chatgptDeviceCode"}, request["params"])
	require.Eventually(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return manager.active == nil
	}, 2*time.Second, 10*time.Millisecond)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerCancelDoesNotRewriteStatusAsExpired(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	operationID, administratorID := uuid.New(), uuid.New()
	expectManagerCleanup(mock)
	mock.ExpectQuery("INSERT INTO codex_auth_operations").
		WithArgs(administratorID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).
			AddRow(operationID, "pending"))
	mock.ExpectExec("UPDATE codex_auth_operations SET login_id").
		WithArgs(operationID, "login-1", "https://auth.openai.com/codex/device",
			"ABCD-1234", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_auth_operations SET status='canceled'").
		WithArgs(operationID).WillReturnResult(sqlmock.NewResult(0, 1))

	launcher := &rpcLauncher{script: func(server *rpcServer) {
		server.initialize()
		request := server.request()
		server.send(map[string]any{"id": request["id"], "result": map[string]any{
			"type": "chatgptDeviceCode", "loginId": "login-1",
			"verificationUrl": "https://auth.openai.com/codex/device",
			"userCode":        "ABCD-1234",
		}})
		request = server.request()
		server.send(map[string]any{"id": request["id"], "result": map[string]any{}})
		for server.request() != nil {
		}
	}}
	manager := newTestManager(t, db, launcher)
	_, err = manager.Start(context.Background(), administratorID)
	require.NoError(t, err)
	require.NoError(t, manager.Cancel(context.Background(), operationID))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerRejectsLogoutWhileChatGPTProvidesModels(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	expectManagerCleanup(mock)
	mock.ExpectQuery("SELECT value FROM platform_settings").
		WithArgs("agent.provider").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(
			[]byte(`{"modelSource":"chatgpt","chatgptConfigured":true}`)))
	mock.ExpectQuery("SELECT value FROM platform_settings").
		WithArgs("codex.global_agents").WillReturnError(sql.ErrNoRows)
	manager := NewManager(config.Config{}, db, settings.NewService(db, nil), zap.NewNop())
	err = manager.Logout(context.Background())
	require.ErrorContains(t, err, "不能退出")
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectManagerCleanup(mock sqlmock.Sqlmock) {
	mock.ExpectExec("UPDATE codex_auth_operations SET status='failed'").
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func newTestManager(t *testing.T, db *sql.DB, launcher codex.Launcher) *Manager {
	t.Helper()
	manager := NewManager(config.Config{
		CodexHomeRoot: t.TempDir(), CodexBin: "codex",
		ControlTimeout: time.Second, ToolTimeout: time.Second,
	}, db, settings.NewService(db, nil), zap.NewNop())
	manager.start = func(ctx context.Context, options codex.ClientOptions) (*codex.Client, error) {
		options.Launcher = launcher
		return codex.Start(ctx, options)
	}
	return manager
}

type rpcLauncher struct {
	script func(*rpcServer)
}

func (l *rpcLauncher) Launch(codex.ProcessSpec) (codex.Process, error) {
	serverIn, clientIn := io.Pipe()
	clientOut, serverOut := io.Pipe()
	clientErr, serverErr := io.Pipe()
	process := &rpcProcess{
		stdin: clientIn, stdout: clientOut, stderr: clientErr,
		serverIn: serverIn, serverOut: serverOut, serverErr: serverErr,
		exited: make(chan struct{}),
	}
	go func() {
		l.script(&rpcServer{process: process, scanner: bufio.NewScanner(serverIn)})
		process.exit(nil)
	}()
	return process, nil
}

type rpcProcess struct {
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	serverIn   *io.PipeReader
	serverOut  *io.PipeWriter
	serverErr  *io.PipeWriter
	exited     chan struct{}
	once       sync.Once
	processErr error
}

func (p *rpcProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *rpcProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *rpcProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *rpcProcess) Signal(os.Signal) error {
	p.exit(nil)
	return nil
}
func (p *rpcProcess) Kill() error {
	p.exit(errors.New("killed"))
	return nil
}
func (p *rpcProcess) Wait() error {
	<-p.exited
	return p.processErr
}
func (p *rpcProcess) exit(err error) {
	p.once.Do(func() {
		p.processErr = err
		_ = p.serverOut.Close()
		_ = p.serverErr.Close()
		_ = p.serverIn.Close()
		close(p.exited)
	})
}

type rpcServer struct {
	process *rpcProcess
	scanner *bufio.Scanner
}

func (s *rpcServer) request() map[string]any {
	if !s.scanner.Scan() {
		return nil
	}
	var result map[string]any
	_ = json.Unmarshal(s.scanner.Bytes(), &result)
	return result
}

func (s *rpcServer) send(value any) {
	_ = json.NewEncoder(s.process.serverOut).Encode(value)
}

func (s *rpcServer) initialize() {
	request := s.request()
	s.send(map[string]any{"id": request["id"], "result": map[string]any{}})
	_ = s.request()
}
