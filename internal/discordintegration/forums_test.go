package discordintegration

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDevelopmentForumName(t *testing.T) {
	forumID := uuid.MustParse("11111111-2222-4333-8444-555555555555")
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
			name: "默认使用成员显示名和仓库名", displayName: "Avery", username: "avery-dev",
			owner: "example-org", repository: "Atlas", expected: "avery-atlas",
		},
		{
			name:        "默认名称冲突时加入仓库 owner",
			guild:       RemoteGuild{Channels: []RemoteChannel{{Name: "avery-atlas"}}},
			displayName: "Avery", username: "avery-dev", owner: "example-org",
			repository: "Atlas", expected: "avery-example-org-atlas",
		},
		{
			name: "owner 名称仍冲突时加入稳定短 ID",
			guild: RemoteGuild{Channels: []RemoteChannel{
				{Name: "avery-atlas"}, {Name: "avery-example-org-atlas"},
			}},
			displayName: "Avery", username: "avery-dev", owner: "example-org",
			repository: "Atlas", expected: "avery-example-org-atlas-111111",
		},
		{
			name: "显示名无法用于频道时回退 username", displayName: "开发者", username: "avery-dev",
			owner: "example-org", repository: "Atlas", expected: "avery-dev-atlas",
		},
		{
			name: "自定义名称保持优先并规范化", requestedName: " Avery Custom Forum ",
			displayName: "Avery", username: "avery-dev", owner: "example-org",
			repository: "Atlas", expected: "avery-custom-forum",
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
