package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/slovx2/tyrs-hand/internal/codexproxy"
)

const (
	defaultCodexReal   = "/opt/tyrs-hand/codex/bin/codex"
	defaultRelaySocket = "/run/tyrs-hand/relay.sock"
)

var execProcess = syscall.Exec

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 2 && arguments[0] == "app-server" && arguments[1] == "proxy" {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return codexproxy.ServeStdio(ctx,
			envOr("TYRS_HAND_RELAY_SOCKET", defaultRelaySocket))
	}
	binary := resolveCodexReal()
	environment := os.Environ()
	if os.Getenv("CODEX_HOME") == "" {
		environment = append(environment, "CODEX_HOME=/var/lib/tyrs-hand/codex")
	}
	processArguments := append([]string{os.Args[0]}, arguments...)
	if err := execProcess(binary, processArguments, environment); err != nil {
		return fmt.Errorf("启动 codex-real: %w", err)
	}
	return nil
}

func resolveCodexReal() string {
	if configured := os.Getenv("TYRS_HAND_CODEX_REAL"); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err == nil {
		userBinary := filepath.Join(home, ".local", "share", "tyrs-hand", "codex",
			"current", "bin", "codex")
		if info, statErr := os.Stat(userBinary); statErr == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return userBinary
		}
	}
	return defaultCodexReal
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
