package discordintegration

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDevelopmentForumName(t *testing.T) {
	forumID := uuid.MustParse("6ff13ea7-e4c8-4da8-82f2-6d7a8d0ae091")
	tests := []struct {
		name          string
		guild         RemoteGuild
		requestedName string
		displayName   string
		username      string
		owner         string
		repository    string
		expected      string
	}{
		{
			name: "默认使用成员显示名和仓库名", displayName: "Kal", username: "kal032699",
			owner: "datawake-ai", repository: "WakeQora", expected: "kal-wakeqora",
		},
		{
			name:        "默认名称冲突时加入仓库 owner",
			guild:       RemoteGuild{Channels: []RemoteChannel{{Name: "kal-wakeqora"}}},
			displayName: "Kal", username: "kal032699", owner: "datawake-ai",
			repository: "WakeQora", expected: "kal-datawake-ai-wakeqora",
		},
		{
			name: "owner 名称仍冲突时加入稳定短 ID",
			guild: RemoteGuild{Channels: []RemoteChannel{
				{Name: "kal-wakeqora"}, {Name: "kal-datawake-ai-wakeqora"},
			}},
			displayName: "Kal", username: "kal032699", owner: "datawake-ai",
			repository: "WakeQora", expected: "kal-datawake-ai-wakeqora-6ff13e",
		},
		{
			name: "显示名无法用于频道时回退 username", displayName: "开发者", username: "kal032699",
			owner: "datawake-ai", repository: "WakeQora", expected: "kal032699-wakeqora",
		},
		{
			name: "自定义名称保持优先并规范化", requestedName: " Kal Custom Forum ",
			displayName: "Kal", username: "kal032699", owner: "datawake-ai",
			repository: "WakeQora", expected: "kal-custom-forum",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := developmentForumName(test.guild, test.requestedName, test.displayName,
				test.username, test.owner, test.repository, forumID)
			require.Equal(t, test.expected, actual)
		})
	}
}
