package codex

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const RequiredVersion = "0.147.0"

func ValidateVersion(ctx context.Context, bin string) error {
	output, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("读取 Codex 版本: %w", err)
	}
	actual := strings.TrimSpace(string(output))
	version := strings.TrimPrefix(actual, "codex-cli ")
	if version == actual || version != RequiredVersion {
		return fmt.Errorf("要求 Codex 精确版本 %s，当前为 %s", RequiredVersion, actual)
	}
	return nil
}
