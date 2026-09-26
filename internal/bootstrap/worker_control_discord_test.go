//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/stretchr/testify/require"
)

type controlDiscordFixture struct {
	dispatcher *discordintegration.Dispatcher
	mu         sync.Mutex
	deliveries []json.RawMessage
}

// 只替换 Discord 网络，Outbox、REST 序列化、会话绑定及审批业务均使用真实实现。
func startControlDiscordFixture(t *testing.T, ctx context.Context, fixture controlRuntimeFixture) *controlDiscordFixture {
	t.Helper()
	db := fixture.db
	forumID := fmt.Sprint(fixture.discordIDBase + 1)
	_, err := db.ExecContext(ctx, `INSERT INTO discord_resources(guild_id,resource_key,discord_id,kind,name,managed_marker)
		VALUES ($1,'forum.protocol',$2,'forum','Protocol','protocol')`, fixture.guildID, forumID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forums(guild_id,resource_id,forum_type,owner_discord_user_id,workspace_id,workspace_project_id)
		SELECT $1,resource.id,'workspace','1001',project.workspace_id,project.id
		FROM discord_resources resource CROSS JOIN workspace_projects project
		WHERE resource.guild_id=$1 AND resource.resource_key='forum.protocol' AND project.workspace_id=$2`, fixture.guildID, fixture.workspaceID)
	require.NoError(t, err)
	f := &controlDiscordFixture{}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, query := range []string{
			`SELECT row_to_json(q) FROM (SELECT request_method,status,thread_id,app_server_request_id,discord_message_id FROM codex_interactive_requests) q`,
			`SELECT row_to_json(q) FROM (SELECT operation_key,status,last_error FROM integration_outbox) q`,
			`SELECT row_to_json(q) FROM (SELECT status,external_thread_id,forum_id,conversation_id FROM desktop_thread_requests) q`,
		} {
			rows, err := db.QueryContext(context.Background(), query)
			if err != nil {
				t.Log(err)
				continue
			}
			for rows.Next() {
				var value string
				if rows.Scan(&value) == nil {
					t.Log(value)
				}
			}
			_ = rows.Close()
		}
	})
	var sequence atomic.Int64
	sequence.Store(fixture.discordIDBase + 10000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if readErr != nil {
			t.Error(readErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body) > 0 {
			f.mu.Lock()
			f.deliveries = append(f.deliveries, json.RawMessage(body))
			f.mu.Unlock()
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		id := fmt.Sprint(sequence.Add(1))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/channels/"+forumID+"/threads":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "type": 11, "guild_id": fixture.guildID,
				"parent_id": forumID, "owner_id": "1001", "name": "Protocol",
				"message": map[string]any{"id": id, "channel_id": id, "content": ""}})
		case len(parts) >= 3 && parts[0] == "channels" && parts[2] == "messages" &&
			(r.Method == http.MethodPost || r.Method == http.MethodPatch):
			if r.Method == http.MethodPatch {
				if len(parts) != 4 {
					t.Errorf("消息更新缺少消息 ID：%s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				id = parts[3]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "channel_id": parts[1], "content": ""})
		case len(parts) == 4 && parts[0] == "channels" && parts[2] == "thread-members" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 2 && parts[0] == "channels" && (r.Method == http.MethodPatch || r.Method == http.MethodGet):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": parts[1], "type": 11, "parent_id": forumID,
				"guild_id": fixture.guildID, "name": "Protocol", "applied_tags": []string{}})
		default:
			t.Errorf("未登记的 Discord 测试请求：%s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	remote := discordintegration.NewDisgoRemote("mock-only", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })
	f.dispatcher = discordintegration.NewDispatcher(discordintegration.NewSQLoutbox(db), remote)
	return f
}

func (f *controlDiscordFixture) deliverUntil(t *testing.T, ctx context.Context, complete func() bool) {
	t.Helper()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	for !complete() {
		_, err := f.dispatcher.RunOnce(ctx)
		require.NoError(t, err)
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-timeout.C:
			t.Fatal("真实 Discord 投递未在期限内完成")
		case <-time.After(25 * time.Millisecond):
		}
	}
}
