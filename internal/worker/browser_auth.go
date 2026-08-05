package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
)

type BrowserAppServerTokens struct {
	Worker  string
	Desktop string
}

// DeriveBrowserAppServerTokens 返回宿主 AppServer 使用的 Workspace Token，不暴露主密钥。
// 两个字段保留现有环境变量入口，但使用同一个 scope，避免跨端续写改变浏览器能力。
func DeriveBrowserAppServerTokens(cfg config.Config,
	workspaceID uuid.UUID,
) (BrowserAppServerTokens, error) {
	if cfg.BrowserMCPURL == "" {
		return BrowserAppServerTokens{}, nil
	}
	secret, err := os.ReadFile(cfg.BrowserMCPTokenFile)
	if err != nil {
		return BrowserAppServerTokens{}, fmt.Errorf("读取宿主 Browser MCP Token: %w", err)
	}
	scope := "worker"
	if workspaceID != uuid.Nil {
		scope = workspaceID.String()
	}
	workspaceToken, err := deriveBrowserToken(string(secret), scope)
	if err != nil {
		return BrowserAppServerTokens{}, fmt.Errorf("派生宿主 Workspace Browser MCP Token: %w", err)
	}
	return BrowserAppServerTokens{Worker: workspaceToken, Desktop: workspaceToken}, nil
}

func deriveBrowserToken(secret, scope string) (string, error) {
	secret = strings.TrimSpace(secret)
	scope = strings.ToLower(strings.TrimSpace(scope))
	if secret == "" {
		return "", errors.New("浏览器 MCP 主密钥为空")
	}
	if scope != "worker" {
		parsed, err := uuid.Parse(scope)
		if err != nil || parsed == uuid.Nil {
			return "", errors.New("浏览器 scope 无效")
		}
		scope = parsed.String()
	}
	digest := hmac.New(sha256.New, []byte(secret))
	_, _ = digest.Write([]byte("v1\n" + scope))
	signature := base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
	return "v1." + scope + "." + signature, nil
}
