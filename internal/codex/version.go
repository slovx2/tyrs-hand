package codex

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/mod/semver"
)

const RequiredVersion = "0.145.0"

func ValidateVersion(ctx context.Context, bin string) error {
	output, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("读取 Codex 版本: %w", err)
	}
	actual := strings.TrimSpace(string(output))
	version := strings.TrimPrefix(actual, "codex-cli ")
	canonical := "v" + version
	if version == actual || !semver.IsValid(canonical) ||
		semver.Compare(canonical, "v"+RequiredVersion) < 0 {
		return fmt.Errorf("要求 Codex 版本不低于 %s，当前为 %s", RequiredVersion, actual)
	}
	return nil
}
