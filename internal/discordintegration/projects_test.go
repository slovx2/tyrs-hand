package discordintegration

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProjectForumName(t *testing.T) {
	projectID := uuid.MustParse("12345678-1234-1234-1234-1234567890ab")
	require.Equal(t, "common", projectForumName(RemoteGuild{}, "Common", projectID))
	require.Equal(t, "project-123456", projectForumName(RemoteGuild{}, "中文项目", projectID))
	require.Equal(t, "common-123456", projectForumName(RemoteGuild{
		Channels: []RemoteChannel{{Name: "common"}},
	}, "Common", projectID))
}
