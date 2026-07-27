package discordintegration

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDevelopmentProjectForumName(t *testing.T) {
	forumID := uuid.MustParse("12345678-1234-1234-1234-1234567890ab")
	require.Equal(t, "custom", developmentProjectForumName(
		RemoteGuild{}, "Custom", "Kal", "kal", "workspace", forumID))
	require.Equal(t, "kal-workspace", developmentProjectForumName(
		RemoteGuild{}, "", "Kal", "kal", "workspace", forumID))
	require.Equal(t, "kal-workspace-123456", developmentProjectForumName(RemoteGuild{
		Channels: []RemoteChannel{{Name: "kal-workspace"}},
	}, "", "Kal", "kal", "workspace", forumID))
}
