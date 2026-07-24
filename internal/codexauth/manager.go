package codexauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/settings"
	"go.uber.org/zap"
)

const loginLifetime = 15 * time.Minute

type Account struct {
	Configured bool   `json:"configured"`
	Email      string `json:"email,omitempty"`
	PlanType   string `json:"planType,omitempty"`
}

type Operation struct {
	ID              uuid.UUID  `json:"id"`
	Status          string     `json:"status"`
	VerificationURL string     `json:"verificationUrl,omitempty"`
	UserCode        string     `json:"userCode,omitempty"`
	Email           string     `json:"email,omitempty"`
	PlanType        string     `json:"planType,omitempty"`
	Error           string     `json:"error,omitempty"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
}

type activeLogin struct {
	operationID uuid.UUID
	loginID     string
	client      *codex.Client
	cancel      context.CancelFunc
	finishMu    sync.Mutex
	finished    bool
}

type Manager struct {
	cfg      config.Config
	db       *sql.DB
	settings *settings.Service
	logger   *zap.Logger
	start    func(context.Context, codex.ClientOptions) (*codex.Client, error)

	mu     sync.Mutex
	active *activeLogin
}

func NewManager(cfg config.Config, db *sql.DB, service *settings.Service,
	logger *zap.Logger,
) *Manager {
	manager := &Manager{cfg: cfg, db: db, settings: service, logger: logger, start: codex.Start}
	_, _ = db.Exec(`UPDATE codex_auth_operations SET status='failed',
		error='Control 重启，登录流程已中止', user_code=NULL,
		finished_at=now(), updated_at=now()
		WHERE status IN ('pending','awaiting_user')`)
	return manager
}

func (m *Manager) Start(ctx context.Context, administratorID uuid.UUID) (Operation, error) {
	m.mu.Lock()
	if m.active != nil {
		m.mu.Unlock()
		return Operation{}, errors.New("已有 ChatGPT 登录正在进行")
	}
	m.mu.Unlock()

	sharedHome := filepath.Join(m.cfg.CodexHomeRoot, "shared")
	if err := prepareSharedHome(sharedHome); err != nil {
		return Operation{}, err
	}
	var operation Operation
	err := m.db.QueryRowContext(ctx, `INSERT INTO codex_auth_operations(requested_by)
		VALUES ($1) RETURNING id, status`, administratorID).Scan(&operation.ID, &operation.Status)
	if err != nil {
		return Operation{}, err
	}
	client, err := m.start(ctx, codex.ClientOptions{
		Bin: m.cfg.CodexBin, CWD: sharedHome, CodexHome: sharedHome, Home: sharedHome,
		RequestTimeout: m.cfg.ControlTimeout, ToolTimeout: m.cfg.ToolTimeout,
		Logger: zap.NewNop(),
	})
	if err != nil {
		loginErr := errors.New("后台 Codex 登录进程启动失败")
		m.fail(operation.ID, loginErr)
		return Operation{}, loginErr
	}
	var response struct {
		Type            string `json:"type"`
		LoginID         string `json:"loginId"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if err = client.Call(ctx, "account/login/start",
		map[string]string{"type": "chatgptDeviceCode"}, &response); err != nil {
		_ = client.Close()
		loginErr := errors.New("无法通过 Codex 发起 ChatGPT Device Code 登录")
		m.fail(operation.ID, loginErr)
		return Operation{}, loginErr
	}
	if response.Type != "chatgptDeviceCode" || response.LoginID == "" ||
		response.VerificationURL == "" || response.UserCode == "" {
		_ = client.Close()
		err = errors.New("后台 Codex 没有返回有效的 Device Code 登录信息")
		m.fail(operation.ID, err)
		return Operation{}, err
	}
	expiresAt := time.Now().Add(loginLifetime)
	_, err = m.db.ExecContext(ctx, `UPDATE codex_auth_operations SET login_id=$2,
		verification_url=$3, user_code=$4, status='awaiting_user',
		expires_at=$5, updated_at=now() WHERE id=$1`,
		operation.ID, response.LoginID, response.VerificationURL, response.UserCode, expiresAt)
	if err != nil {
		_ = client.Close()
		m.fail(operation.ID, err)
		return Operation{}, err
	}
	loginCtx, cancel := context.WithDeadline(context.Background(), expiresAt)
	active := &activeLogin{operationID: operation.ID, loginID: response.LoginID,
		client: client, cancel: cancel}
	m.mu.Lock()
	m.active = active
	m.mu.Unlock()
	go m.wait(loginCtx, active)
	operation.Status = "awaiting_user"
	operation.VerificationURL = response.VerificationURL
	operation.UserCode = response.UserCode
	operation.ExpiresAt = &expiresAt
	return operation, nil
}

func (m *Manager) Get(ctx context.Context, id uuid.UUID) (Operation, error) {
	var result Operation
	var expires sql.NullTime
	err := m.db.QueryRowContext(ctx, `SELECT id, status, COALESCE(verification_url,''),
		COALESCE(user_code,''), COALESCE(account_email,''),
		COALESCE(account_plan_type,''), COALESCE(error,''), expires_at
		FROM codex_auth_operations WHERE id=$1`, id).Scan(
		&result.ID, &result.Status, &result.VerificationURL, &result.UserCode,
		&result.Email, &result.PlanType, &result.Error, &expires)
	if expires.Valid {
		result.ExpiresAt = &expires.Time
	}
	return result, err
}

func (m *Manager) Account(ctx context.Context) (Account, error) {
	provider, err := m.settings.AgentProvider(ctx)
	if err != nil || !provider.ChatGPTConfigured {
		return Account{Configured: false}, err
	}
	var account Account
	account.Configured = true
	err = m.db.QueryRowContext(ctx, `SELECT COALESCE(account_email,''),
		COALESCE(account_plan_type,'') FROM codex_auth_operations
		WHERE status='completed' ORDER BY finished_at DESC LIMIT 1`).
		Scan(&account.Email, &account.PlanType)
	if errors.Is(err, sql.ErrNoRows) {
		return account, nil
	}
	return account, err
}

func (m *Manager) Cancel(ctx context.Context, id uuid.UUID) error {
	m.mu.Lock()
	active := m.active
	if active == nil || active.operationID != id {
		m.mu.Unlock()
		return errors.New("登录流程不存在或已经结束")
	}
	if !active.claimFinish() {
		m.mu.Unlock()
		return errors.New("登录流程不存在或已经结束")
	}
	m.active = nil
	m.mu.Unlock()
	_ = active.client.Call(ctx, "account/login/cancel",
		map[string]string{"loginId": active.loginID}, nil)
	active.cancel()
	_ = active.client.Close()
	_, err := m.db.ExecContext(ctx, `UPDATE codex_auth_operations SET status='canceled',
		user_code=NULL, finished_at=now(), updated_at=now()
		WHERE id=$1 AND status='awaiting_user'`, id)
	if err != nil {
		return err
	}
	return nil
}

func (m *Manager) Logout(ctx context.Context) error {
	m.mu.Lock()
	loginActive := m.active != nil
	m.mu.Unlock()
	if loginActive {
		return errors.New("ChatGPT 重新登录正在进行，请先取消登录")
	}
	provider, err := m.settings.AgentProvider(ctx)
	if err != nil {
		return err
	}
	if provider.ModelSource == settings.ModelSourceChatGPT {
		return errors.New("ChatGPT 模式下不能退出全局账号，请先切换到 Provider 模式")
	}
	sharedHome := filepath.Join(m.cfg.CodexHomeRoot, "shared")
	if err := prepareSharedHome(sharedHome); err != nil {
		return err
	}
	client, err := m.start(ctx, codex.ClientOptions{
		Bin: m.cfg.CodexBin, CWD: sharedHome, CodexHome: sharedHome, Home: sharedHome,
		RequestTimeout: m.cfg.ControlTimeout, ToolTimeout: m.cfg.ToolTimeout,
		Logger: zap.NewNop(),
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if err := client.Call(ctx, "account/logout", map[string]any{}, nil); err != nil {
		return errors.New("后台 Codex 无法退出 ChatGPT 账号")
	}
	return m.settings.SetChatGPTConfigured(ctx, false)
}

func (m *Manager) wait(ctx context.Context, active *activeLogin) {
	defer active.cancel()
	defer func() { _ = active.client.Close() }()
	for {
		select {
		case event, ok := <-active.client.Events():
			if !ok {
				if active.claimFinish() {
					m.fail(active.operationID, errors.New("后台 Codex 登录进程已结束"))
				}
				m.clear(active)
				return
			}
			if event.Method != "account/login/completed" {
				continue
			}
			var completed struct {
				LoginID *string `json:"loginId"`
				Success bool    `json:"success"`
			}
			if json.Unmarshal(event.Params, &completed) != nil ||
				(completed.LoginID != nil && *completed.LoginID != active.loginID) {
				continue
			}
			if !active.claimFinish() {
				m.clear(active)
				return
			}
			if !completed.Success {
				m.fail(active.operationID, errors.New("ChatGPT Device Code 登录失败"))
				m.clear(active)
				return
			}
			m.complete(active)
			m.clear(active)
			return
		case <-ctx.Done():
			if active.claimFinish() {
				m.fail(active.operationID, errors.New("ChatGPT Device Code 已过期"))
			}
			m.clear(active)
			return
		}
	}
}

func (a *activeLogin) claimFinish() bool {
	a.finishMu.Lock()
	defer a.finishMu.Unlock()
	if a.finished {
		return false
	}
	a.finished = true
	return true
}
