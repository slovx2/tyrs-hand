package hostworker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const remoteDesktopLauncherFixture = `sh -c 'if [ -z "$SHELL" ] || [ ! -x "$SHELL" ]; then ` +
	`echo "Codex remote SSH requires SHELL to point to an executable login shell" >&2; ` +
	`exit 127; fi; CODEX_REMOTE_PAYLOAD="$1"; export CODEX_REMOTE_PAYLOAD; ` +
	`exec /bin/sh -c "$CODEX_REMOTE_PAYLOAD"' sh ` +
	`'printf '\''%b'\'' '\''\373\351\203\326\054\020\265\233'\''; ` +
	`PATH="${CODEX_INSTALL_DIR:-$HOME/.local/bin}:$PATH"; export PATH; codex app-server proxy'`

func TestParseDesktopProxyCommand(t *testing.T) {
	for _, command := range []string{"codex app-server proxy", "exec codex app-server proxy"} {
		handshake, matched, err := parseDesktopProxyCommand(command)
		require.NoError(t, err)
		require.True(t, matched)
		require.Empty(t, handshake)
	}

	command := `printf '%b' '\033\124\376\322\310\106\334\116'; ` +
		`PATH="${CODEX_INSTALL_DIR:-$HOME/.local/bin}:$PATH"; export PATH; codex app-server proxy`
	handshake, matched, err := parseDesktopProxyCommand(command)
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, []byte{0x1b, 'T', 0xfe, 0xd2, 0xc8, 'F', 0xdc, 'N'}, handshake)

	handshake, matched, err = parseDesktopProxyCommand(remoteDesktopLauncherFixture)
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, []byte{0xfb, 0xe9, 0x83, 0xd6, ',', 0x10, 0xb5, 0x9b}, handshake)

	for _, command := range []string{
		"printf wrong; codex app-server proxy",
		"printf wrong; exec codex app-server proxy",
	} {
		handshake, matched, err = parseDesktopProxyCommand(command)
		require.ErrorContains(t, err, "格式不受支持")
		require.True(t, matched)
		require.Empty(t, handshake)
	}

	handshake, matched, err = parseDesktopProxyCommand("printf ordinary")
	require.NoError(t, err)
	require.False(t, matched)
	require.Empty(t, handshake)
}
