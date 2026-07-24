//go:build integration

package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/stretchr/testify/require"
)

func TestInteractiveProjectionCollectsDiscordAnswers(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	insertInteractiveGuild(t, db)
	seed := seedDiscordManagerData(t, db)
	manager := NewManager(db, nil, "")
	controlID, runID := insertInteractiveControl(t, db, seed)
	questions := json.RawMessage(`[
		{"id":"choice","header":"确认","question":"继续吗？","options":[
			{"label":"是","description":"继续"},{"label":"否","description":"停止"}]},
		{"id":"detail","header":"说明","question":"为什么？"}
	]`)
	requestID := insertInteractiveRequest(t, db, controlID, runID, "item-1", questions)

	require.NoError(t, ProjectInteractiveRequest(ctx, db, requestID))
	var operationType string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key=$1`, "interactive:"+requestID.String()).Scan(&operationType))
	require.Equal(t, "message.create", operationType)

	_, err := manager.AnswerInteractive(ctx, "other-guild", requestID, 0, 0, "")
	require.ErrorContains(t, err, "不属于")
	card, err := manager.AnswerInteractive(ctx, testGuildID, requestID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, card.Buttons, 1)
	require.Equal(t, "填写答案", card.Buttons[0].Label)
	card, err = manager.AnswerInteractive(ctx, testGuildID, requestID, 1, -1, "  因为需要  ")
	require.NoError(t, err)
	require.Empty(t, card.Buttons)
	require.Contains(t, card.Body, "Discord")

	var status, surface string
	var answer json.RawMessage
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, answer_surface, answer
		FROM codex_interactive_requests WHERE id=$1`, requestID).Scan(&status, &surface, &answer))
	require.Equal(t, "resolved", status)
	require.Equal(t, "discord", surface)
	require.JSONEq(t, `{"choice":{"answers":["是"]},"detail":{"answers":["因为需要"]}}`, string(answer))

	card, err = manager.AnswerInteractive(ctx, testGuildID, requestID, 0, 1, "")
	require.NoError(t, err, "旧按钮必须幂等返回已完成状态")
	require.Empty(t, card.Buttons)
	_, err = loadInteractiveProjection(ctx, db, requestID, true)
	require.ErrorContains(t, err, "事务")

	secretID := insertInteractiveRequest(t, db, controlID, runID, "item-secret",
		json.RawMessage(`[{"id":"secret","header":"密钥","question":"Token？","isSecret":true}]`))
	_, err = manager.AnswerInteractive(ctx, testGuildID, secretID, 0, -1, "secret")
	require.ErrorContains(t, err, "Desktop")

	connector := NewDisgoConnector(manager, nil, nil, testGuildID, "token", nil)
	client := &bot.Client{}
	optionID := insertInteractiveRequest(t, db, controlID, runID, "item-component",
		json.RawMessage(`[{"id":"choice","header":"确认","question":"继续吗？","options":[{"label":"是"},{"label":"否"}]}]`))
	buttonID := interactiveButtonID(optionID, 0, 0)
	connector.answerInteractiveComponent(newComponentEvent(t, client, "9101", "2001", buttonID, nil), buttonID)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_interactive_requests
		WHERE id=$1`, optionID).Scan(&status))
	require.Equal(t, "resolved", status)
	connector.answerInteractiveComponent(newComponentEvent(t, client, "9102", "2001", "invalid", nil), "invalid")

	freeID := insertInteractiveRequest(t, db, controlID, runID, "item-modal",
		json.RawMessage(`[{"id":"detail","header":"说明","question":"为什么？"}]`))
	freeButtonID := interactiveButtonID(freeID, 0, -1)
	connector.answerInteractiveComponent(newComponentEvent(t, client, "9103", "2001", freeButtonID, nil), freeButtonID)
	connector.answerInteractiveModal(newModalEvent(t, client, "9104", "2001",
		interactiveModalPrefix+freeID.String()+":0", []discord.LayoutComponent{
			discord.NewLabel("回答", discord.TextInputComponent{CustomID: "answer", Value: "Modal answer"}),
		}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_interactive_requests
		WHERE id=$1`, freeID).Scan(&status))
	require.Equal(t, "resolved", status)
	connector.answerInteractiveModal(newModalEvent(t, client, "9105", "2001", "invalid", nil))
}

func insertInteractiveControl(t *testing.T, db *sql.DB, seed discordManagerSeed) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var profileID, environmentID, conversationID, controlID, intentID, runID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name='Default'`).Scan(&profileID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT development_environment_id FROM discord_forums
		WHERE id=$1`, seed.developmentForumID).Scan(&environmentID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 repository_id, agent_profile_id, title)
		VALUES ($1,$2,'interactive-thread','interactive-starter','1001',$3,$4,'Interactive') RETURNING id`,
		testGuildID, seed.developmentForumID, seed.repositoryID, profileID).Scan(&conversationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_thread_controls
		(source_type, discord_conversation_id, repository_id, agent_profile_id,
		 execution_node_id, development_environment_id, external_thread_id)
		VALUES ('discord_conversation',$1,$2,$3,$4,$5,'codex-interactive-thread') RETURNING id`,
		conversationID, seed.repositoryID, profileID, seed.executionNodeID, environmentID).Scan(&controlID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_turn_intents
		(control_id, sequence_no, behavior, source_type, discord_conversation_id,
		 repository_id, agent_profile_id, idempotency_key, status)
		VALUES ($1,1,'start_when_idle','discord_conversation',$2,$3,$4,$5,'waiting_for_user') RETURNING id`,
		controlID, conversationID, seed.repositoryID, profileID, "interactive-"+uuid.NewString()).Scan(&intentID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_turn_runs
		(control_id, primary_intent_id, attempt, worker_id, lease_epoch, capability_hash,
		 status, execution_node_id)
		VALUES ($1,$2,1,'worker',1,$3,'waiting_for_user',$4) RETURNING id`, controlID, intentID,
		strings.Repeat("a", 64), seed.executionNodeID).Scan(&runID))
	return controlID, runID
}

func insertInteractiveRequest(t *testing.T, db *sql.DB, controlID, runID uuid.UUID,
	itemID string, questions json.RawMessage,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.QueryRow(`INSERT INTO codex_interactive_requests
		(control_id, run_id, thread_id, turn_id, item_id, app_server_generation,
		 app_server_request_id, questions)
		VALUES ($1,$2,'codex-interactive-thread','turn-1',$3,1,'"request-1"',$4) RETURNING id`,
		controlID, runID, itemID, questions).Scan(&id))
	return id
}

func TestClearDevelopmentEnvironmentSSHQueuesReconfigure(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	insertInteractiveGuild(t, db)
	seed := seedDiscordManagerData(t, db)
	var environmentID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT development_environment_id FROM discord_forums
		WHERE id=$1`, seed.developmentForumID).Scan(&environmentID))
	_, err := db.ExecContext(ctx, `UPDATE discord_development_environments SET ssh_public_key=$2,
		ssh_fingerprint='SHA256:test', ssh_port=2222,
		ssh_discord_user_id=owner_discord_user_id WHERE id=$1`, environmentID,
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest")
	require.NoError(t, err)
	require.NoError(t, NewManager(db, nil, "").ClearDevelopmentEnvironmentSSH(ctx, environmentID))
	var key, sshUserID sql.NullString
	var revision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT ssh_public_key, ssh_discord_user_id,
		ssh_config_revision FROM discord_development_environments WHERE id=$1`,
		environmentID).Scan(&key, &sshUserID, &revision))
	require.False(t, key.Valid)
	require.False(t, sshUserID.Valid)
	require.Equal(t, int64(1), revision)
}

func insertInteractiveGuild(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO discord_guilds(guild_id, enabled) VALUES ($1, true)`, testGuildID)
	require.NoError(t, err)
}
