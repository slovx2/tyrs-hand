package hostworker

const remoteDesktopLauncherFixture = `sh -c 'if [ -z "$SHELL" ] || [ ! -x "$SHELL" ]; then ` +
	`echo "Codex remote SSH requires SHELL to point to an executable login shell" >&2; ` +
	`exit 127; fi; CODEX_REMOTE_PAYLOAD="$1"; export CODEX_REMOTE_PAYLOAD; ` +
	`exec /bin/sh -c "$CODEX_REMOTE_PAYLOAD"' sh ` +
	`'printf '\''%b'\'' '\''\373\351\203\326\054\020\265\233'\''; ` +
	`PATH="${CODEX_INSTALL_DIR:-$HOME/.local/bin}:$PATH"; export PATH; codex app-server proxy'`
