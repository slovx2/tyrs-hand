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
	anyDesktopProxyCommand = regexp.MustCompile(
		`codex[[:space:]]+app-server[[:space:]]+proxy([[:space:]]|$)`)
)

func parseDesktopProxyCommand(command string) ([]byte, bool, error) {
	trimmed := strings.TrimSpace(command)
	if directDesktopProxyCommand.MatchString(trimmed) {
		return nil, true, nil
	}
	matches := wrappedDesktopProxyCommand.FindStringSubmatch(trimmed)
	if len(matches) > 1 {
		handshake, err := strconv.Unquote(`"` + matches[1] + `"`)
		if err != nil {
			return nil, true, errors.New("codex Desktop SSH 握手无效")
		}
		return []byte(handshake), true, nil
	}
	if anyDesktopProxyCommand.MatchString(trimmed) {
		return nil, true, errors.New("codex Desktop SSH 命令格式不受支持")
	}
	return nil, false, nil
}
