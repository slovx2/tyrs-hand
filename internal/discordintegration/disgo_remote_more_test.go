package discordintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDisgoRemoteGuildChannelsAndOperations(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests[request.Method+" "+request.URL.Path]++
		mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /guilds/123":
			_, _ = response.Write([]byte(`{"id":"123","name":"private","owner_id":"1","features":["COMMUNITY"]}`))
		case "GET /guilds/123/channels":
			_, _ = response.Write([]byte(`[
				{"id":"10","guild_id":"123","type":4,"name":"System","position":0,"permission_overwrites":[]},
				{"id":"11","guild_id":"123","type":0,"name":"status","position":1,"parent_id":"10","topic":"managed","permission_overwrites":[]},
				{"id":"12","guild_id":"123","type":15,"name":"tasks","position":2,"topic":"forum","permission_overwrites":[],"available_tags":[{"id":"91","name":"Running","moderated":false,"emoji_id":null,"emoji_name":null}],"default_sort_order":null,"default_forum_layout":1,"default_reaction_emoji":null}
			]`))
		case "PATCH /guilds/123":
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			_, _ = response.Write([]byte(`{"id":"123","name":"private","owner_id":"1","features":["COMMUNITY"]}`))
		case "POST /guilds/123/channels":
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			channelType := int(body["type"].(float64))
			id := fmt.Sprintf("8%d", channelType)
			_, _ = response.Write([]byte(channelJSON(id, channelType, body["name"].(string))))
		case "PATCH /channels/10":
			_, _ = response.Write([]byte(channelJSON("10", 4, "System")))
		case "PATCH /channels/11":
			_, _ = response.Write([]byte(channelJSON("11", 0, "status")))
		case "PATCH /channels/12":
			_, _ = response.Write([]byte(channelJSON("12", 15, "tasks")))
		case "DELETE /channels/11":
			response.WriteHeader(http.StatusNoContent)
		case "PATCH /channels/20/messages/21":
			_, _ = response.Write([]byte(`{"id":"21","channel_id":"20","content":"updated"}`))
		case "POST /channels/12/threads":
			_, _ = response.Write([]byte(`{"id":"30","guild_id":"123","parent_id":"12","type":11,"name":"Issue","owner_id":"1","message_count":1,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-18T00:00:00Z","locked":false},"message":{"id":"31","channel_id":"30","content":"card"}}`))
		case "PATCH /channels/30":
			_, _ = response.Write([]byte(`{"id":"30","guild_id":"123","parent_id":"12","type":11,"name":"Issue","owner_id":"1","message_count":1,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-18T00:00:00Z","locked":false}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	remote := NewDisgoRemote("token", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })
	ctx := context.Background()

	guild, err := remote.Guild(ctx, "123")
	require.NoError(t, err)
	require.True(t, guild.CommunityEnabled)
	require.Len(t, guild.Channels, 3)
	require.Equal(t, "10", guild.Channels[1].ParentID)
	require.Equal(t, "91", guild.Channels[2].Tags["Running"])
	require.NoError(t, remote.DisableCommunity(ctx, "123"))
	require.NoError(t, remote.EnableCommunity(ctx, "123", "11", "11"))

	permission := []PermissionSpec{{ID: "123", Type: "role", Allow: 1}, {ID: "456", Type: "member", Deny: 2}}
	for _, spec := range []ChannelSpec{
		{Key: "category", Name: "category", Kind: "category", PermissionOverwrites: permission},
		{Key: "text", Name: "text", Kind: "text", ParentKey: "10", Topic: "topic", PermissionOverwrites: permission},
		{Key: "forum", Name: "forum", Kind: "forum", ParentKey: "10", Tags: []string{"Running"}, PermissionOverwrites: permission},
	} {
		created, err := remote.CreateChannel(ctx, "123", spec, "marker")
		require.NoError(t, err)
		require.NotEmpty(t, created.ID)
	}
	require.NoError(t, remote.UpdateChannel(ctx, "10", ChannelSpec{Name: "System", Kind: "category"}))
	require.NoError(t, remote.UpdateChannel(ctx, "11", ChannelSpec{Name: "status", Kind: "text", ParentKey: "10"}))
	require.NoError(t, remote.UpdateChannel(ctx, "12", ChannelSpec{Name: "tasks", Kind: "forum", ParentKey: "10"}))
	require.NoError(t, remote.DeleteChannel(ctx, "11"))

	testDisgoSendOperations(t, ctx, remote)
	mu.Lock()
	require.GreaterOrEqual(t, requests["PATCH /channels/30"], 2)
	mu.Unlock()
}

func TestDisgoRemoteRejectsMalformedRequestsBeforeNetworkWrites(t *testing.T) {
	remote := NewDisgoRemote("token", "", nil)
	t.Cleanup(func() { remote.Close(context.Background()) })
	ctx := context.Background()

	_, err := remote.Guild(ctx, "bad")
	require.Error(t, err)
	require.Error(t, remote.DisableCommunity(ctx, "bad"))
	require.Error(t, remote.EnableCommunity(ctx, "bad", "2", "3"))
	require.Error(t, remote.EnableCommunity(ctx, "1", "bad", "3"))
	require.Error(t, remote.EnableCommunity(ctx, "1", "2", "bad"))

	_, err = remote.CreateChannel(ctx, "bad", ChannelSpec{Kind: "text"}, "")
	require.Error(t, err)
	_, err = remote.CreateChannel(ctx, "1", ChannelSpec{Kind: "text", ParentKey: "bad"}, "")
	require.Error(t, err)
	_, err = remote.CreateChannel(ctx, "1", ChannelSpec{
		Kind: "text", PermissionOverwrites: []PermissionSpec{{ID: "bad", Type: "member"}},
	}, "")
	require.Error(t, err)

	require.Error(t, remote.UpdateChannel(ctx, "bad", ChannelSpec{Kind: "text"}))
	require.Error(t, remote.UpdateChannel(ctx, "1", ChannelSpec{Kind: "text", ParentKey: "bad"}))
	require.Error(t, remote.UpdateChannel(ctx, "1", ChannelSpec{
		Kind: "text", PermissionOverwrites: []PermissionSpec{{ID: "bad", Type: "member"}},
	}))
	require.Error(t, remote.UpdateChannel(ctx, "1", ChannelSpec{Kind: "voice"}))
	require.Error(t, remote.DeleteChannel(ctx, "bad"))

	_, err = remote.Send(ctx, OutboxItem{OperationType: "message.create", Payload: json.RawMessage("{")})
	require.Error(t, err)
	invalidOperations := []OutboxItem{
		{OperationType: "message.create", Payload: rawJSON(map[string]string{"channelId": "bad"})},
		{OperationType: "message.update", Payload: rawJSON(map[string]string{"channelId": "1", "messageId": "bad"})},
		{OperationType: "interaction.defer", Payload: rawJSON(map[string]string{"interactionId": "bad"})},
		{OperationType: "channel.permissions", Payload: rawJSON(map[string]string{"channelId": "bad"})},
		{OperationType: "forum.post.create", Payload: rawJSON(map[string]any{"channelId": "1", "tagIds": []string{"bad"}})},
		{OperationType: "thread.archive", Payload: rawJSON(map[string]string{"channelId": "bad"})},
		{OperationType: "thread.tags", Payload: rawJSON(map[string]any{"channelId": "1", "tagIds": []string{"bad"}})},
	}
	for _, operation := range invalidOperations {
		_, err = remote.Send(ctx, operation)
		require.Error(t, err, operation.OperationType)
	}
}

func testDisgoSendOperations(t *testing.T, ctx context.Context, remote *DisgoRemote) {
	t.Helper()
	operations := []OutboxItem{
		{OperationType: "message.update", Payload: rawJSON(map[string]any{"channelId": "20", "messageId": "21", "content": "updated"})},
		{OperationType: "channel.permissions", Payload: rawJSON(map[string]any{"channelId": "12", "permissions": []PermissionSpec{{ID: "123", Type: "role", Allow: 1}}})},
		{OperationType: "thread.archive", Payload: rawJSON(map[string]any{"channelId": "30", "archived": true})},
		{OperationType: "thread.tags", Payload: rawJSON(map[string]any{"channelId": "30", "tagIds": []string{"91"}})},
	}
	for _, operation := range operations {
		_, err := remote.Send(ctx, operation)
		require.NoError(t, err)
	}
	result, err := remote.Send(ctx, OutboxItem{OperationType: "forum.post.create", Nonce: "post-nonce", Payload: rawJSON(map[string]any{
		"channelId": "12", "threadName": "Issue", "content": "card", "tagIds": []string{"91"},
	})})
	require.NoError(t, err)
	require.JSONEq(t, `{"threadId":"30","messageId":"31"}`, string(result))
	_, err = remote.Send(ctx, OutboxItem{OperationType: "unsupported", Payload: rawJSON(map[string]any{})})
	require.Error(t, err)
	_, err = remote.CreateChannel(ctx, "123", ChannelSpec{Kind: "voice"}, "")
	require.Error(t, err)
	_, err = permissionOverwrites([]PermissionSpec{{ID: "123", Type: "unknown"}})
	require.Error(t, err)
	_, _, err = twoSnowflakes("bad", "2")
	require.Error(t, err)
}

func channelJSON(id string, channelType int, name string) string {
	base := fmt.Sprintf(`{"id":%q,"guild_id":"123","type":%d,"name":%q,"position":0,"permission_overwrites":[]`, id, channelType, name)
	if channelType == 15 {
		base += `,"available_tags":[],"default_sort_order":null,"default_forum_layout":1,"default_reaction_emoji":null`
	}
	return base + "}"
}

func rawJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
