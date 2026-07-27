package discordintegration

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDevelopmentProjectForumName(t *testing.T) {
	forumID := uuid.MustParse("12345678-1234-1234-1234-1234567890ab")
	require.Equal(t, "custom", developmentProjectForumName(
		RemoteGuild{}, "Custom", "Avery", "avery", "workspace", forumID))
	require.Equal(t, "avery-workspace", developmentProjectForumName(
		RemoteGuild{}, "", "Avery", "avery", "workspace", forumID))
	require.Equal(t, "avery-workspace-123456", developmentProjectForumName(RemoteGuild{
		Channels: []RemoteChannel{{Name: "avery-workspace"}},
	}, "", "Avery", "avery", "workspace", forumID))
}
