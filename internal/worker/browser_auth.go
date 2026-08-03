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

// BrowserAppServerToken 返回宿主 AppServer 使用的派生 Token，不暴露主密钥。
func BrowserAppServerToken(cfg config.Config) (string, error) {
	if cfg.BrowserMCPURL == "" {
		return "", nil
	}
	secret, err := os.ReadFile(cfg.BrowserMCPTokenFile)
	if err != nil {
		return "", fmt.Errorf("读取宿主 Browser MCP Token: %w", err)
	}
	token, err := deriveBrowserToken(string(secret), "worker")
	if err != nil {
		return "", fmt.Errorf("派生宿主 Browser MCP Token: %w", err)
	}
	return token, nil
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
