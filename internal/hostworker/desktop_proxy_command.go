package hostworker

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var (
	directDesktopProxyCommand = regexp.MustCompile(
		`^(exec[[:space:]]+)?codex[[:space:]]+app-server[[:space:]]+proxy[[:space:]]*$`)
	wrappedDesktopProxyCommand = regexp.MustCompile(
		`^printf[[:space:]]+'%b'[[:space:]]+'((\\[0-7]{3})+)'[[:space:]]*;` +
			`[[:space:]]*PATH="\$\{CODEX_INSTALL_DIR:-\$HOME/\.local/bin\}:\$PATH"[[:space:]]*;` +
			`[[:space:]]*export[[:space:]]+PATH[[:space:]]*;[[:space:]]*` +
			`(exec[[:space:]]+)?codex[[:space:]]+app-server[[:space:]]+proxy[[:space:]]*$`)
	remoteDesktopHandshake = regexp.MustCompile(`((\\[0-7]{3}){8})`)
)

func parseDesktopProxyCommand(command string) ([]byte, bool, error) {
	trimmed := strings.TrimSpace(command)
	if directDesktopProxyCommand.MatchString(trimmed) {
		return nil, true, nil
	}
	matches := wrappedDesktopProxyCommand.FindStringSubmatch(trimmed)
	if len(matches) > 1 {
		return decodeDesktopHandshake(matches[1])
	}
	if isRemoteDesktopLauncher(trimmed) {
		matches = remoteDesktopHandshake.FindStringSubmatch(trimmed)
		if len(matches) <= 1 {
			return nil, true, errors.New("codex Desktop SSH 远程启动器缺少握手")
		}
		return decodeDesktopHandshake(matches[1])
	}
	// SSH 登录方本来就可以执行普通 shell 命令。这里只识别需要转接到
	// Worker AppServerHub 的已知代理命令；其他命令继续交给登录 shell。
	// App Server 的异常退出由 Runtime Supervisor 自愈，不在这里拦截生命周期命令。
	return nil, false, nil
}

func isRemoteDesktopLauncher(command string) bool {
	for _, marker := range []string{
		"sh -c ",
		"Codex remote SSH requires SHELL to point to an executable login shell",
		`CODEX_REMOTE_PAYLOAD="$1"`,
		`exec /bin/sh -c "$CODEX_REMOTE_PAYLOAD"`,
		"codex app-server proxy",
	} {
		if !strings.Contains(command, marker) {
			return false
		}
	}
	return strings.HasPrefix(command, "sh -c ")
}

func decodeDesktopHandshake(encoded string) ([]byte, bool, error) {
	handshake, err := strconv.Unquote(`"` + encoded + `"`)
	if err != nil {
		return nil, true, errors.New("codex Desktop SSH 握手无效")
	}
	return []byte(handshake), true, nil
}
