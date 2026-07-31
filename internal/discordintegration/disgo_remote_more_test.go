package discordintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDisgoRemoteGuildChannelsAndOperations(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string]int)
	var guildUpdates []map[string]any
	var messageBodies []map[string]any
	var threadBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests[request.Method+" "+request.URL.Path]++
		mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /guilds/123":
			_, _ = response.Write([]byte(`{"id":"123","name":"private","owner_id":"1","features":["COMMUNITY"]}`))
		case "GET /guilds/123/members":
			_, _ = response.Write([]byte(`[
				{"user":{"id":"456","username":"alice","global_name":"Alice","discriminator":"0","bot":false},"nick":"Atlas","roles":[],"joined_at":"2026-07-27T00:00:00Z"},
				{"user":{"id":"900","username":"helper","discriminator":"0","bot":true},"roles":[],"joined_at":"2026-07-27T00:00:00Z"}
			]`))
		case "GET /guilds/123/channels":
			_, _ = response.Write([]byte(`[
				{"id":"10","guild_id":"123","type":4,"name":"System","position":0,"permission_overwrites":[]},
				{"id":"11","guild_id":"123","type":0,"name":"status","position":1,"parent_id":"10","topic":"managed","permission_overwrites":[]},
				{"id":"12","guild_id":"123","type":15,"name":"tasks","position":2,"topic":"forum","permission_overwrites":[],"available_tags":[{"id":"91","name":"Running","moderated":false,"emoji_id":null,"emoji_name":null}],"default_sort_order":null,"default_forum_layout":1,"default_reaction_emoji":null}
			]`))
		case "PATCH /guilds/123":
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			mu.Lock()
			guildUpdates = append(guildUpdates, body)
			mu.Unlock()
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
		case "GET /channels/12":
			_, _ = response.Write([]byte(`{"id":"12","guild_id":"123","type":15,"name":"tasks","position":2,"topic":"forum","permission_overwrites":[],"available_tags":[{"id":"91","name":"Running","moderated":false,"emoji_id":null,"emoji_name":null},{"id":"92","name":"Manual","moderated":false,"emoji_id":null,"emoji_name":null}],"default_sort_order":null,"default_forum_layout":1,"default_reaction_emoji":null}`))
		case "DELETE /channels/11":
			response.WriteHeader(http.StatusNoContent)
		case "GET /guilds/123/threads/active":
			_, _ = response.Write([]byte(`{"threads":[
				{"id":"40","guild_id":"123","parent_id":"12","type":11,"name":"target","owner_id":"900","message_count":1,"member_count":1,"rate_limit_per_user":0,"applied_tags":["91"],"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-27T00:00:00Z","locked":false}},
				{"id":"43","guild_id":"123","parent_id":"12","type":11,"name":"empty","owner_id":"900","message_count":0,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-27T00:00:00Z","locked":false}},
				{"id":"41","guild_id":"123","parent_id":"99","type":11,"name":"other","owner_id":"1","message_count":1,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-27T00:00:00Z","locked":false}}
			],"members":[]}`))
		case "GET /channels/40/messages":
			_, _ = fmt.Fprintf(response, `[{"id":"44","channel_id":"40","timestamp":"2026-07-27T00:01:00Z","author":{"id":"900","username":"bot","discriminator":"0","bot":true},"content":"later"},{"id":"42","channel_id":"40","timestamp":"2026-07-27T00:00:00Z","author":{"id":"900","username":"bot","discriminator":"0","bot":true},"content":"","flags":32768,"components":[{"type":17,"id":101,"accent_color":%d,"components":[{"type":10,"id":102,"content":"Task"},{"type":14,"id":103,"divider":true,"spacing":1},{"type":10,"id":104,"content":"Friendly"}]}]}]`, cardColorGreen)
		case "GET /channels/43/messages":
			_, _ = response.Write([]byte(`[]`))
		case "DELETE /channels/20/messages/21":
			response.WriteHeader(http.StatusNoContent)
		case "PUT /channels/30/thread-members/456":
			response.WriteHeader(http.StatusNoContent)
		case "PATCH /channels/20/messages/21":
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			mu.Lock()
			messageBodies = append(messageBodies, body)
			mu.Unlock()
			_, _ = response.Write([]byte(`{"id":"21","channel_id":"20","content":"updated"}`))
		case "POST /channels/20/messages":
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			mu.Lock()
			messageBodies = append(messageBodies, body)
			mu.Unlock()
			_, _ = response.Write([]byte(`{"id":"22","channel_id":"20","content":""}`))
		case "POST /channels/12/threads":
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			mu.Lock()
			messageBodies = append(messageBodies, body)
			mu.Unlock()
			_, _ = response.Write([]byte(`{"id":"30","guild_id":"123","parent_id":"12","type":11,"name":"Issue","owner_id":"1","message_count":1,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-18T00:00:00Z","locked":false},"message":{"id":"31","channel_id":"30","content":"card"}}`))
		case "PATCH /channels/30":
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			mu.Lock()
			threadBodies = append(threadBodies, body)
			mu.Unlock()
			_, _ = response.Write([]byte(`{"id":"30","guild_id":"123","parent_id":"12","type":11,"name":"Issue","owner_id":"1","message_count":1,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-18T00:00:00Z","locked":false}}`))
		case "GET /channels/30":
			_, _ = response.Write([]byte(`{"id":"30","guild_id":"123","parent_id":"12","type":11,"name":"Issue","owner_id":"1","message_count":1,"member_count":1,"rate_limit_per_user":0,"applied_tags":["92"],"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-18T00:00:00Z","locked":false}}`))
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
	members, err := remote.Members(ctx, "123")
	require.NoError(t, err)
	require.Equal(t, []RemoteMember{
		{DiscordUserID: "456", Username: "alice", DisplayName: "Atlas"},
		{DiscordUserID: "900", Username: "helper", DisplayName: "helper", IsBot: true},
	}, members)
	require.NoError(t, remote.DisableCommunity(ctx, "123"))
	require.NoError(t, remote.EnableCommunity(ctx, "123", "11", "11"))
	mu.Lock()
	require.Len(t, guildUpdates, 2)
	require.Equal(t, float64(discord.VerificationLevelLow), guildUpdates[1]["verification_level"])
	require.Equal(t, float64(discord.ExplicitContentFilterLevelAllMembers), guildUpdates[1]["explicit_content_filter"])
	mu.Unlock()

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
	receipts, err := remote.ActiveForumPostReceipts(ctx, "123", "12")
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.Equal(t, "40", receipts[0].ThreadID)
	require.Equal(t, "42", receipts[0].MessageID)
	fingerprint, err := ForumPostRequestFingerprint(rawJSON(map[string]any{
		"threadName": "target", "tagIds": []string{"91"},
		"card": ComponentCardPayload{Header: "Task", Body: "Friendly",
			AccentColor: cardColorGreen},
	}), "900")
	require.NoError(t, err)
	require.Equal(t, fingerprint, receipts[0].Fingerprint)
	plainFingerprint, err := ForumPostRequestFingerprint(rawJSON(map[string]any{
		"threadName": "plain", "content": "body", "tagIds": []string{"92", "91"},
	}), "900")
	require.NoError(t, err)
	expectedPlain, err := forumPostFingerprint("plain", "body", nil,
		[]string{"91", "92"}, "900")
	require.NoError(t, err)
	require.Equal(t, expectedPlain, plainFingerprint)
	_, err = ForumPostRequestFingerprint(json.RawMessage(`{`), "900")
	require.Error(t, err)
	_, err = ForumPostRequestFingerprint(rawJSON(map[string]string{"threadName": "x"}), "bad")
	require.Error(t, err)
	_, err = ForumPostRequestFingerprint(rawJSON(map[string]string{"threadName": " "}), "900")
	require.Error(t, err)
	_, err = ForumPostRequestFingerprint(rawJSON(map[string]any{
		"threadName": "x", "tagIds": []string{"bad"},
	}), "900")
	require.Error(t, err)
	_, err = ForumPostRequestFingerprint(rawJSON(map[string]any{
		"threadName": "x", "card": ComponentCardPayload{},
	}), "900")
	require.Error(t, err)

	testDisgoSendOperations(t, ctx, remote)
	mu.Lock()
	require.GreaterOrEqual(t, requests["PATCH /channels/30"], 3)
	require.Contains(t, threadBodies, map[string]any{"archived": true, "locked": true})
	require.Contains(t, threadBodies, map[string]any{"applied_tags": []any{"92", "91"}})
	require.Len(t, messageBodies, 3)
	allowedMentions := messageBodies[0]["allowed_mentions"].(map[string]any)
	require.Equal(t, false, allowedMentions["replied_user"])
	require.Nil(t, allowedMentions["parse"])
	require.Equal(t, float64(discord.MessageFlagIsComponentsV2), messageBodies[1]["flags"])
	require.Nil(t, messageBodies[1]["embeds"])
	container := messageBodies[1]["components"].([]any)[0].(map[string]any)
	require.Equal(t, float64(discord.ComponentTypeContainer), container["type"])
	require.Equal(t, "Card", container["components"].([]any)[0].(map[string]any)["content"])
	threadMessage := messageBodies[2]["message"].(map[string]any)
	require.Equal(t, float64(discord.AutoArchiveDuration1w), messageBodies[2]["auto_archive_duration"])
	require.Equal(t, float64(discord.MessageFlagIsComponentsV2), threadMessage["flags"])
	threadContainer := threadMessage["components"].([]any)[0].(map[string]any)
	require.Equal(t, "Task", threadContainer["components"].([]any)[0].(map[string]any)["content"])
	mu.Unlock()
}

func TestDisgoRemoteRejectsMalformedRequestsBeforeNetworkWrites(t *testing.T) {
	remote := NewDisgoRemote("token", "", nil)
	t.Cleanup(func() { remote.Close(context.Background()) })
	ctx := context.Background()

	_, err := remote.Guild(ctx, "bad")
	require.Error(t, err)
	_, err = remote.Members(ctx, "bad")
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
		{OperationType: "message.delete", Payload: rawJSON(map[string]string{"channelId": "1", "messageId": "bad"})},
		{OperationType: "thread.member.add", Payload: rawJSON(map[string]string{"channelId": "1", "userId": "bad"})},
		{OperationType: "interaction.defer", Payload: rawJSON(map[string]string{"interactionId": "bad"})},
		{OperationType: "channel.permissions", Payload: rawJSON(map[string]string{"channelId": "bad"})},
		{OperationType: "forum.post.create", Payload: rawJSON(map[string]any{"channelId": "1", "tagIds": []string{"bad"}})},
		{OperationType: "thread.archive", Payload: rawJSON(map[string]string{"channelId": "bad"})},
		{OperationType: "thread.lifecycle", Payload: rawJSON(map[string]string{"channelId": "bad"})},
		{OperationType: "thread.tags", Payload: rawJSON(map[string]any{"channelId": "1", "tagIds": []string{"bad"}})},
	}
	for _, operation := range invalidOperations {
		_, err = remote.Send(ctx, operation)
		require.Error(t, err, operation.OperationType)
	}
}

func TestDisgoRemoteStreamsDesktopImageAndReconcilesByFilename(t *testing.T) {
	const filename = "01-0123456789ab-shot.png"
	patches := 0
	delivered := false
	var payload map[string]any
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /channels/20/messages/21":
			attachments := `[{"id":"30","filename":"existing.png"}]`
			if delivered {
				attachments = `[{"id":"30","filename":"existing.png"},` +
					`{"id":"31","filename":"` + filename + `"}]`
			}
			_, _ = response.Write([]byte(`{"id":"21","channel_id":"20","attachments":` +
				attachments + `}`))
		case "PATCH /channels/20/messages/21":
			patches++
			if strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				_, _ = response.Write([]byte(`{"id":"21","channel_id":"20"}`))
				return
			}
			reader, err := request.MultipartReader()
			require.NoError(t, err)
			for {
				part, partErr := reader.NextPart()
				if partErr == io.EOF {
					break
				}
				require.NoError(t, partErr)
				body, readErr := io.ReadAll(part)
				require.NoError(t, readErr)
				if part.FormName() == "payload_json" {
					require.NoError(t, json.Unmarshal(body, &payload))
				} else {
					uploaded = body
				}
			}
			delivered = true
			// Discord 真实 API 可能在 multipart PATCH 成功响应中省略 attachments；
			// 客户端必须随后 GET 消息按文件名对账。
			_, _ = response.Write([]byte(`{"id":"21","channel_id":"20"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	remote := NewDisgoRemote("token", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })
	card := ComponentCardPayload{AccentColor: cardColorBlurple, Header: "Desktop",
		Media: []ComponentMediaPayload{
			{Filename: "existing.png", Description: "existing.png"},
			{Filename: filename, Description: "shot.png"},
		}}

	attachmentID, err := remote.UploadDesktopImage(context.Background(), "20", "21", card,
		filename, "shot.png", bytes.NewReader([]byte("image-content")))

	require.NoError(t, err)
	require.Equal(t, "31", attachmentID)
	require.Equal(t, []byte("image-content"), uploaded)
	require.Equal(t, 1, patches)
	attachments := payload["attachments"].([]any)
	require.Len(t, attachments, 2)
	require.Equal(t, "30", attachments[0].(map[string]any)["id"])
	require.Equal(t, float64(0), attachments[1].(map[string]any)["id"])
	require.Equal(t, filename, attachments[1].(map[string]any)["filename"])
	container := payload["components"].([]any)[0].(map[string]any)
	components := container["components"].([]any)
	require.Equal(t, float64(discord.ComponentTypeMediaGallery),
		components[len(components)-1].(map[string]any)["type"])
	require.NoError(t, remote.UpdateDesktopCard(context.Background(), "20", "21", card))
	require.Equal(t, 2, patches)

	attachmentID, err = remote.UploadDesktopImage(context.Background(), "20", "21", card,
		filename, "shot.png", bytes.NewReader([]byte("must-not-upload")))
	require.NoError(t, err)
	require.Equal(t, "31", attachmentID)
	require.Equal(t, 2, patches)
}

func TestDiscordCardComponentsRejectsInvalidMedia(t *testing.T) {
	card := ComponentCardPayload{AccentColor: cardColorBlurple, Header: "Desktop"}
	for index := 0; index < 11; index++ {
		card.Media = append(card.Media, ComponentMediaPayload{
			Filename: fmt.Sprintf("image-%d.png", index)})
	}
	_, err := discordCardComponents(card)
	require.ErrorContains(t, err, "最多包含 10")

	card.Media = []ComponentMediaPayload{{Filename: "bad/path.png"}}
	_, err = discordCardComponents(card)
	require.ErrorContains(t, err, "文件名无效")

	card.Media = []ComponentMediaPayload{{Filename: "same.png"}, {Filename: "same.png"}}
	_, err = discordCardComponents(card)
	require.ErrorContains(t, err, "重复")
}

func TestDisgoRemoteTreatsDeletedLifecycleCardAsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, `{"message":"Unknown Message","code":10008}`, http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	remote := NewDisgoRemote("token", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })
	_, err := remote.Send(context.Background(), OutboxItem{OperationType: "message.delete",
		Payload: rawJSON(map[string]string{"channelId": "20", "messageId": "21"})})
	require.NoError(t, err)
}

func TestDisgoRemoteDefersMessageUpdateForArchivedThread(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(`{"message":"Thread is archived","code":50083}`))
	}))
	t.Cleanup(server.Close)
	remote := NewDisgoRemote("token", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })
	_, err := remote.Send(context.Background(), OutboxItem{OperationType: "message.update",
		Payload: rawJSON(map[string]string{"channelId": "20", "messageId": "21",
			"content": "updated"})})
	require.NoError(t, err)
}

func TestDisgoRemoteAllowsOnlyExplicitUserMentions(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, "POST /channels/20/messages", request.Method+" "+request.URL.Path)
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"22","channel_id":"20","content":"done"}`))
	}))
	t.Cleanup(server.Close)
	remote := NewDisgoRemote("token", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })

	_, err := remote.Send(context.Background(), OutboxItem{OperationType: "message.create",
		Payload: rawJSON(map[string]any{
			"channelId": "20", "content": "<@456> done", "mentionUserIds": []string{"456"},
		})})
	require.NoError(t, err)
	allowed := body["allowed_mentions"].(map[string]any)
	require.Equal(t, []any{"456"}, allowed["users"])
	require.Nil(t, allowed["parse"])
	require.Equal(t, false, allowed["replied_user"])

	_, err = explicitUserMentions([]string{"invalid"})
	require.Error(t, err)
}

func TestDiscordRestoreReferencesAndCardRevision(t *testing.T) {
	id, err := parseDiscordPostReference("<#100000000000000070>")
	require.NoError(t, err)
	require.Equal(t, "100000000000000070", id)
	id, err = parseDiscordPostReference("100000000000000071")
	require.NoError(t, err)
	require.Equal(t, "100000000000000071", id)
	_, err = parseDiscordPostReference("not-a-post")
	require.Error(t, err)
	conversationID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	card := lifecycleCard(conversationID, 7)
	require.Len(t, card.Buttons, 1)
	require.Equal(t, "codex-restore:"+conversationID.String()+":7",
		card.Buttons[0].CustomID)
}

func testDisgoSendOperations(t *testing.T, ctx context.Context, remote *DisgoRemote) {
	t.Helper()
	operations := []OutboxItem{
		{OperationType: "channel.delete", Payload: rawJSON(map[string]any{"channelId": "11"})},
		{OperationType: "message.update", Payload: rawJSON(map[string]any{"channelId": "20", "messageId": "21", "content": "updated"})},
		{OperationType: "channel.permissions", Payload: rawJSON(map[string]any{"channelId": "12", "permissions": []PermissionSpec{{ID: "123", Type: "role", Allow: 1}}})},
		{OperationType: "thread.archive", Payload: rawJSON(map[string]any{"channelId": "30", "archived": true})},
		{OperationType: "thread.lifecycle", Payload: rawJSON(map[string]any{
			"channelId": "30", "archived": true, "locked": true,
		})},
		{OperationType: "thread.tags", Payload: rawJSON(map[string]any{"channelId": "30", "tagIds": []string{"91"}})},
		{OperationType: "thread.tag.toggle", Payload: rawJSON(map[string]any{
			"channelId": "30", "tagName": "Running", "enabled": true,
		})},
		{OperationType: "thread.member.add", Payload: rawJSON(map[string]any{"channelId": "30", "userId": "456"})},
		{OperationType: "thread.rename", Payload: rawJSON(map[string]any{"channelId": "30", "threadName": "Renamed"})},
		{OperationType: "message.delete", Payload: rawJSON(map[string]any{"channelId": "20", "messageId": "21"})},
	}
	for _, operation := range operations {
		_, err := remote.Send(ctx, operation)
		require.NoError(t, err)
	}
	created, err := remote.Send(ctx, OutboxItem{OperationType: "message.create", Nonce: "card-nonce", Payload: rawJSON(map[string]any{
		"channelId": "20", "card": ComponentCardPayload{Header: "Card", Body: "Friendly",
			AccentColor: cardColorBlurple},
	})})
	require.NoError(t, err)
	require.JSONEq(t, `{"messageId":"22"}`, string(created))
	result, err := remote.Send(ctx, OutboxItem{OperationType: "forum.post.create", Nonce: "post-nonce", Payload: rawJSON(map[string]any{
		"channelId": "12", "threadName": "Issue", "card": ComponentCardPayload{Header: "Task",
			AccentColor: cardColorGreen}, "tagIds": []string{"91"},
	})})
	require.NoError(t, err)
	require.JSONEq(t, `{"threadId":"30","messageId":"31"}`, string(result))
	_, err = remote.Send(ctx, OutboxItem{OperationType: "unsupported", Payload: rawJSON(map[string]any{})})
	require.Error(t, err)
	_, err = remote.Send(ctx, OutboxItem{OperationType: "message.create", Payload: rawJSON(map[string]any{
		"channelId": "20", "card": ComponentCardPayload{Header: "x",
			Buttons: []ComponentButtonPayload{{Label: "a", CustomID: "same"}, {Label: "b", CustomID: "same"}}},
	})})
	require.ErrorContains(t, err, "重复")
	_, err = remote.CreateChannel(ctx, "123", ChannelSpec{Kind: "voice"}, "")
	require.Error(t, err)
	_, err = permissionOverwrites([]PermissionSpec{{ID: "123", Type: "unknown"}})
	require.Error(t, err)
	_, _, err = twoSnowflakes("bad", "2")
	require.Error(t, err)
	topic := "pointer topic"
	forum := &discord.GuildForumChannel{Topic: &topic,
		AvailableTags: []discord.ChannelTag{{ID: 91, Name: "Running"}}}
	require.Equal(t, topic, channelTopic(forum))
	require.Equal(t, map[string]string{"Running": "91"}, channelTags(forum))
	require.Empty(t, stringValue(nil))
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
