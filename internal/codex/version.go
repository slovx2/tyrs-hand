package codex

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/mod/semver"
)

const RequiredVersion = "0.157.1"

func ValidateVersion(ctx context.Context, bin string) error {
	_, err := ValidatedVersion(ctx, bin)
	return err
}

// ValidatedVersion 同时返回已验证的真实 CLI 版本，供运行时登记构建身份。
func ValidatedVersion(ctx context.Context, bin string) (string, error) {
	// Linux CLI 可能向 stderr 输出 PATH helper 警告；版本只取 stdout。
	output, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("读取 Codex 版本: %w", err)
	}
	actual := strings.TrimSpace(string(output))
	version := strings.TrimPrefix(actual, "codex-cli ")
	if !IsSupportedVersion(version) {
		return "", fmt.Errorf("要求 Codex 版本 >= %s，当前为 %s", RequiredVersion, actual)
	}
	return version, nil
}

// IsSupportedVersion 接受不低于协议验收版本的稳定版。
func IsSupportedVersion(actual string) bool {
	return VersionAtLeast(actual, RequiredVersion)
}

// VersionAtLeast 判断 actual 是不低于 minimum 的 X.Y.Z 稳定版，拒绝预发布、构建后缀和非法格式。
func VersionAtLeast(actual, minimum string) bool {
	a, m := "v"+actual, "v"+minimum
	if !stableVersion(a) || !stableVersion(m) {
		return false
	}
	return semver.Compare(a, m) >= 0
}

func stableVersion(v string) bool {
	return semver.IsValid(v) && semver.Canonical(v) == v && semver.Prerelease(v) == ""
}
