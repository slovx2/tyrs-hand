package discordintegration

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIncrementalPreflightReportsUnmanagedNameConflictWithoutWrites(t *testing.T) {
	guild := RemoteGuild{ID: "123", CommunityEnabled: true, Channels: []RemoteChannel{
		{ID: "900", Name: "系统", Kind: "category"},
	}}
	plan, err := BuildInitializationPlan(InitializationIncremental, guild, nil, BaseChannelSpecs())
	require.NoError(t, err)
	require.False(t, plan.Preflight.Safe)
	require.Contains(t, plan.Preflight.Conflicts, Conflict{Name: "系统", Reason: "存在未受 Tyrs Hand 管理的同名 Channel"})
	// 预检只生成声明式计划，不调用任何 Remote 写接口。
	require.NotEmpty(t, plan.Actions)
}

func TestIncrementalPreflightCorrectsManagedDrift(t *testing.T) {
	marker := managedMarker("system.status")
	guild := RemoteGuild{ID: "123", CommunityEnabled: true, Channels: []RemoteChannel{
		{ID: "10", Name: "系统", Kind: "category"},
		{ID: "11", ParentID: "10", Name: "旧状态", Kind: "text", Topic: managedTopic("old", marker)},
	}}
	managed := []ManagedResource{
		{Key: "category.system", DiscordID: "10", Name: "系统", Kind: "category"},
		{Key: "system.status", DiscordID: "11", ParentID: "10", Name: "旧状态", Kind: "text", Marker: marker},
	}
	desired := []ChannelSpec{{Key: "category.system", Name: "系统", Kind: "category"},
		{Key: "system.status", ParentKey: "category.system", Name: "系统状态", Kind: "text", Topic: "每分钟更新"}}
	plan, err := BuildInitializationPlan(InitializationIncremental, guild, managed, desired)
	require.NoError(t, err)
	require.True(t, plan.Preflight.Safe)
	require.Equal(t, []string{"系统状态"}, plan.Preflight.Updates)
	require.Equal(t, "channel.update", plan.Actions[0].Kind)
	require.Equal(t, "10", plan.Actions[0].Spec.ParentKey)
}

func TestFreshInitializationOrderAndConfirmation(t *testing.T) {
	guild := RemoteGuild{ID: "123", CommunityEnabled: true, Channels: []RemoteChannel{
		{ID: "1", Name: "旧分类", Kind: "category"},
		{ID: "2", ParentID: "1", Name: "旧频道", Kind: "text"},
	}}
	plan, err := BuildInitializationPlan(InitializationFresh, guild, nil, BaseChannelSpecs())
	require.NoError(t, err)
	require.True(t, plan.Preflight.Safe)
	require.Equal(t, "community.disable", plan.Actions[0].Kind)
	require.Equal(t, "2", plan.Actions[1].ResourceID)
	require.Equal(t, "1", plan.Actions[2].ResourceID)
	require.Equal(t, "community.enable", plan.Actions[len(plan.Actions)-1].Kind)
	require.NoError(t, ValidateFreshConfirmation("123", "DELETE ALL CHANNELS 123"))
	require.Error(t, ValidateFreshConfirmation("123", "delete all channels 123"))
	require.Error(t, ValidateFreshConfirmation("123", "DELETE ALL CHANNELS 123 "))
}

func TestManagedResourceTypeMismatchIsConflict(t *testing.T) {
	guild := RemoteGuild{ID: "123", CommunityEnabled: true, Channels: []RemoteChannel{
		{ID: "1", Name: "系统", Kind: "text"},
	}}
	managed := []ManagedResource{{Key: "category.system", DiscordID: "1", Name: "系统", Kind: "category"}}
	plan, err := BuildInitializationPlan(InitializationIncremental, guild, managed,
		[]ChannelSpec{{Key: "category.system", Name: "系统", Kind: "category"}})
	require.NoError(t, err)
	require.False(t, plan.Preflight.Safe)
	require.Contains(t, plan.Preflight.Conflicts[0].Reason, "预期为 category")
}
