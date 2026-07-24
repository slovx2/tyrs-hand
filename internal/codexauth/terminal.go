package codexauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"go.uber.org/zap"
)

const accountReadReadyTimeout = 5 * time.Second

type loginAccount struct {
	Type     string `json:"type"`
	Email    string `json:"email"`
	PlanType string `json:"planType"`
}

func prepareSharedHome(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	path := filepath.Join(home, "config.toml")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(string(existing), "\n")
	filtered := make([]string, 0, len(lines))
	inRoot := true
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inRoot = false
		}
		if inRoot && strings.HasPrefix(trimmed, "cli_auth_credentials_store") {
			continue
		}
		filtered = append(filtered, line)
	}
	content := "cli_auth_credentials_store = " + strconv.Quote("file") + "\n"
	remaining := strings.TrimLeft(strings.Join(filtered, "\n"), "\n")
	if remaining != "" {
		content += remaining
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (m *Manager) complete(active *activeLogin) {
	timeout := accountReadReadyTimeout
	if m.cfg.ControlTimeout > 0 && m.cfg.ControlTimeout < timeout {
		timeout = m.cfg.ControlTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	account, err := waitForAccount(ctx, active.client)
	if err != nil {
		m.fail(active.operationID, errors.New("登录完成但无法读取 ChatGPT 账号"))
		return
	}
	if err := m.settings.SetChatGPTConfigured(ctx, true); err != nil {
		m.fail(active.operationID, err)
		return
	}
	_, err = m.db.ExecContext(ctx, `UPDATE codex_auth_operations
		SET status='completed', account_email=$2, account_plan_type=$3,
		user_code=NULL, finished_at=now(), updated_at=now() WHERE id=$1`,
		active.operationID, account.Email, account.PlanType)
	if err != nil {
		m.logger.Error("保存 ChatGPT 登录结果失败", zap.Error(err))
	}
}

func waitForAccount(ctx context.Context, client *codex.Client) (loginAccount, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var response struct {
			Account *loginAccount `json:"account"`
		}
		err := client.Call(ctx, "account/read", map[string]any{
			"refreshToken": true,
		}, &response)
		if err == nil && response.Account != nil && response.Account.Type == "chatgpt" {
			return *response.Account, nil
		}
		select {
		case <-ctx.Done():
			return loginAccount{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) fail(id uuid.UUID, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := m.db.ExecContext(ctx, `UPDATE codex_auth_operations SET status='failed',
		error=$2, user_code=NULL, finished_at=now(), updated_at=now()
		WHERE id=$1 AND status IN ('pending','awaiting_user')`, id, cause.Error())
	if err != nil {
		m.logger.Error("保存 ChatGPT 登录失败状态失败", zap.Error(err))
	}
}

func (m *Manager) clear(active *activeLogin) {
	m.mu.Lock()
	if m.active == active {
		m.active = nil
	}
	m.mu.Unlock()
}
