package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/google/uuid"
)

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
