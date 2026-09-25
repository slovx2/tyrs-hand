//go:build integration

package discordintegration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestDiscordProjectionUsesPersistedEngineAcrossRefreshAndReply(t *testing.T) {
	db := discordDatabase(t)
	ctx := t.Context()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES ($1,'engine-projection',true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	_, err = db.ExecContext(ctx, `INSERT INTO worker_runtimes(worker_id,engine,enabled,status,
		ssh_listen_address,protocol_version,heartbeat_at)
		VALUES ($1,'claude-code',true,'running',':3333','0.147.0',now())`, seed.workerID)
	require.NoError(t, err)
	service := NewConversationService(db)
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Claude, runtimeidentity.Codex} {
		t.Run(string(engine), func(t *testing.T) {
			threadID, messageID := "projection-"+string(engine), "input-"+string(engine)
			conversationID, beginErr := service.BeginPost(ctx, IncomingMessage{
				GuildID: testGuildID, ForumID: seed.workspaceForumChannelID, ThreadID: threadID,
				MessageID: messageID, DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
				Title: "固定引擎", Body: "继续", Engine: engine, ConfigurationConfirmed: true,
			})
			require.NoError(t, beginErr)
			key := "conversation:" + conversationID.String() + ":message:" + messageID
			readCard := func() conversationProjectionPayload {
				var raw json.RawMessage
				require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_payload FROM discord_projections
					WHERE guild_id=$1 AND projection_key=$2`, testGuildID, key).Scan(&raw))
				var desired conversationProjectionPayload
				require.NoError(t, json.Unmarshal(raw, &desired))
				require.Contains(t, desired.Card.Header, engineDisplayName(engine))
				return desired
			}
			readCard()
			// 新的论坛默认引擎不能改写旧帖子的运行、回复或重建卡片。
			other := runtimeidentity.Codex
			if engine == runtimeidentity.Codex {
				other = runtimeidentity.Claude
			}
			require.NoError(t, (&Manager{db: db}).SetWorkspaceForumEngine(ctx, seed.workspaceForumID, other))
			var intentID, controlID, runID uuid.UUID
			require.NoError(t, db.QueryRowContext(ctx, `SELECT id,control_id FROM codex_turn_intents
				WHERE discord_message_id=$1`, messageID).Scan(&intentID, &controlID))
			require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_turn_runs
				(control_id,primary_intent_id,attempt,active_slot,status,collaboration_mode)
				VALUES ($1,$2,1,1,'running','plan') RETURNING id`, controlID, intentID).Scan(&runID))
			for _, state := range []ConversationProgress{ConversationRunning, ConversationFailed, ConversationCanceled, ConversationCompleted} {
				require.NoError(t, ProjectConversationStatus(ctx, db, testGuildID, threadID, conversationID,
					messageID, runID, state, "有内容的进度"))
				readCard()
			}
			_, err = db.ExecContext(ctx, `UPDATE discord_projections
				SET desired_payload=jsonb_set(desired_payload,'{progress,formatVersion}','0'),message_id=$3
				WHERE guild_id=$1 AND projection_key=$2`, testGuildID, key, "card-"+string(engine))
			require.NoError(t, err)
			require.NoError(t, ReconcileConversationProgressCards(ctx, db, testGuildID))
			readCard()
			connector := &DisgoConnector{manager: &Manager{db: db}}
			page, pageErr := connector.conversationProgressPage(ctx, testGuildID, threadID,
				"card-"+string(engine), runID, 0)
			require.NoError(t, pageErr)
			require.Contains(t, page.Header, engineDisplayName(engine))
			tx, txErr := db.BeginTx(ctx, nil)
			require.NoError(t, txErr)
			require.NoError(t, refreshConversationStatusCardTx(ctx, tx, runID, testGuildID, key))
			require.NoError(t, tx.Commit())
			readCard()
			require.NoError(t, ProjectConversationReply(ctx, db, threadID, conversationID,
				messageID, runID, strings.Repeat("需要执行的计划。", 500), "plan"))
			rows, queryErr := db.QueryContext(ctx, `SELECT desired_payload FROM discord_projections
				WHERE guild_id=$1 AND projection_key LIKE $2 ORDER BY projection_key`, testGuildID,
				"conversation-reply:"+conversationID.String()+":message:"+messageID+"%")
			require.NoError(t, queryErr)
			count := 0
			for rows.Next() {
				var raw string
				require.NoError(t, rows.Scan(&raw))
				require.Contains(t, raw, engineDisplayName(engine))
				require.NotContains(t, raw, engineDisplayName(other)+" 回复")
				count++
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			require.Greater(t, count, 2)
			state, stateErr := service.ConversationMode(ctx, testGuildID, threadID, "1001")
			require.NoError(t, stateErr)
			configCard, marshalErr := json.Marshal(conversationModeCard(state, ""))
			require.NoError(t, marshalErr)
			if engine == runtimeidentity.Claude {
				require.NotContains(t, string(configCard), "Codex")
			}
			require.NoError(t, connector.announceConversationConfig(ctx, threadID, messageID, "1001", state.Engine,
				[]ConfigurationChange{{Field: "model", After: ""}, {Field: "trigger_mode", After: "discussion"}}))
			var announcement string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT payload::text FROM integration_outbox
				WHERE operation_key=$1`, "conversation-config-result:"+messageID).Scan(&announcement))
			require.Contains(t, announcement, "已更新 "+engineDisplayName(engine)+" 设置")
			require.Contains(t, announcement, engineDisplayName(engine)+" 默认")
			_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status='completed',finished_at=now() WHERE id=$1`, runID)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE integration_outbox SET status='completed' WHERE integration='discord'`)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET lifecycle_state='archived',lifecycle_revision=1 WHERE id=$1`, conversationID)
			require.NoError(t, err)
			tx, txErr = db.BeginTx(ctx, nil)
			require.NoError(t, txErr)
			require.NoError(t, EnqueueConversationLifecycleTx(ctx, tx, conversationID))
			require.NoError(t, tx.Commit())
			var archiveCard string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT payload::text FROM integration_outbox
				WHERE operation_key=$1`, "conversation-lifecycle-card:"+conversationID.String()).Scan(&archiveCard))
			require.Contains(t, archiveCard, engineDisplayName(engine)+" · 会话已归档")
		})
	}
}
