package codex

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/mod/semver"
)

const RequiredVersion = "0.147.0"

func ValidateVersion(ctx context.Context, bin string) error {
	output, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("读取 Codex 版本: %w", err)
	}
	actual := strings.TrimSpace(string(output))
	version := strings.TrimPrefix(actual, "codex-cli ")
	if !IsVersionAtLeast(version, RequiredVersion) {
		return fmt.Errorf("要求 Codex 版本 >= %s，当前为 %s", RequiredVersion, actual)
	}
	return nil
}

// IsVersionAtLeast 判断 Codex 版本是否达到最低要求。预发布版本不会被视为对应稳定版本。
func IsVersionAtLeast(actual, minimum string) bool {
	actual = "v" + strings.TrimPrefix(strings.TrimSpace(actual), "v")
	minimum = "v" + strings.TrimPrefix(strings.TrimSpace(minimum), "v")
	if !semver.IsValid(actual) || !semver.IsValid(minimum) {
		return false
	}
	return semver.Compare(actual, minimum) >= 0
}
