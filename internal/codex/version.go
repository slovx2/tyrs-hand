package codex

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const PinnedVersion = "0.147.0"

func ValidateVersion(ctx context.Context, bin string) error {
	output, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("读取 Codex 版本: %w", err)
	}
	actual := strings.TrimSpace(string(output))
	expected := "codex-cli " + PinnedVersion
	if actual != expected {
		return fmt.Errorf("要求 Codex 精确版本 %s，当前为 %s", PinnedVersion, actual)
	}
	return nil
}
