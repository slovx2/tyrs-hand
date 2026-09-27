package codex

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const RequiredVersion = "0.157.1"

func ValidateVersion(ctx context.Context, bin string) error {
	_, err := ValidatedVersion(ctx, bin)
	return err
}

// ValidatedVersion 同时返回已验证的真实 CLI 版本，供运行时登记构建身份。
func ValidatedVersion(ctx context.Context, bin string) (string, error) {
	// Linux CLI 可能向 stderr 输出 PATH helper 警告；版本只取 stdout，仍严格校验真实版本。
	output, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("读取 Codex 版本: %w", err)
	}
	actual := strings.TrimSpace(string(output))
	version := strings.TrimPrefix(actual, "codex-cli ")
	if !IsSupportedVersion(version) {
		return "", fmt.Errorf("要求 Codex 版本恰好为 %s，当前为 %s", RequiredVersion, actual)
	}
	return version, nil
}

// IsSupportedVersion 只接受经过协议验收的精确稳定版，防止新版删除接口后仍通过探测。
func IsSupportedVersion(actual string) bool {
	return actual == RequiredVersion
}
