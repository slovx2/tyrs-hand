package discordintegration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeDiscordResult(t *testing.T) {
	value := SanitizeDiscordResult("完成 /Volumes/workspace/private/file.go，token=ghp_abcdefghijklmnopqrstuvwxyz")
	require.NotContains(t, value, "/Volumes/workspace")
	require.NotContains(t, value, "ghp_")
	require.Contains(t, value, "[已隐藏路径]")
	require.Contains(t, value, "[已隐藏凭据]")

	long := SanitizeDiscordResult(strings.Repeat("你", 2100))
	require.LessOrEqual(t, len([]rune(long)), 1910)
	require.Contains(t, long, "内容已截断")
}

func TestValidateIncomingMessageBoundaries(t *testing.T) {
	base := IncomingMessage{GuildID: "1", ThreadID: "2", MessageID: "3", DiscordUserID: "4", Body: "hello"}
	require.NoError(t, validateIncomingMessage(base))
	missing := base
	missing.MessageID = ""
	require.Error(t, validateIncomingMessage(missing))
	empty := base
	empty.Body = "  "
	require.Error(t, validateIncomingMessage(empty))
	empty.Attachments = []IncomingAttachment{{ID: "1"}}
	require.NoError(t, validateIncomingMessage(empty))
	tooMany := base
	tooMany.Attachments = make([]IncomingAttachment, DefaultMaxAttachments+1)
	require.Error(t, validateIncomingMessage(tooMany))
}
