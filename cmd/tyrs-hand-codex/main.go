package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/slovx2/tyrs-hand/internal/codexproxy"
)

const (
	defaultCodexReal   = "/opt/tyrs-hand/libexec/codex-real"
	defaultRelaySocket = "/run/tyrs-hand/relay.sock"
)

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
	binary := envOr("TYRS_HAND_CODEX_REAL", defaultCodexReal)
	command := exec.Command(binary, arguments...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Env = os.Environ()
	if os.Getenv("CODEX_HOME") == "" {
		command.Env = append(command.Env, "CODEX_HOME=/var/lib/tyrs-hand/codex")
	}
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("codex-real 退出：%d", exit.ExitCode())
		}
		return err
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
