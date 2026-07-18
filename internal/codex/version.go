package codex

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const RequiredVersion = "0.142.5"

func ValidateVersion(ctx context.Context, bin string) error {
	output, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("读取 Codex 版本: %w", err)
	}
	expected := "codex-cli " + RequiredVersion
	if actual := strings.TrimSpace(string(output)); actual != expected {
		return fmt.Errorf("要求 Codex 版本为 %s，当前为 %s", RequiredVersion, actual)
	}
	return nil
}
