package hostworker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
