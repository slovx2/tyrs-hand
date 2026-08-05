//go:build integration

package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/stretchr/testify/require"
)

func TestInteractiveProjectionCollectsDiscordAnswers(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	insertInteractiveGuild(t, db)
	seed := seedDiscordManagerData(t, db)
	manager := NewManager(db, nil)
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
	outbox := NewSQLoutbox(db)
	questionItem, err := claimOutboxOperation(t, ctx, outbox, "interactive:"+requestID.String())
	require.NoError(t, err)
	require.Equal(t, "interactive:"+requestID.String(), questionItem.OperationKey)
	completeDiscordDelivery(t, ctx, outbox, questionItem,
		json.RawMessage(`{"messageId":"interactive-question-message"}`))

	_, err = manager.AnswerInteractive(ctx, "other-guild", requestID, 0, 0, "")
	require.ErrorContains(t, err, "不属于")
	answerResult, err := manager.AnswerInteractive(ctx, testGuildID, requestID, 0, 0, "")
	require.NoError(t, err)
	card := answerResult.Card
	require.Len(t, card.Buttons, 1)
	require.Equal(t, "填写答案", card.Buttons[0].Label)
	answerResult, err = manager.AnswerInteractive(ctx, testGuildID, requestID, 1, -1, "  因为需要  ")
	require.NoError(t, err)
	card = answerResult.Card
	require.True(t, answerResult.Complete)
	require.Empty(t, card.Buttons)
	require.Contains(t, card.Body, "Discord")
	require.Len(t, card.Sections, 2)
	require.Contains(t, card.Sections[0], "是")
	require.Contains(t, card.Sections[1], "因为需要")

	var status, surface string
	var answer json.RawMessage
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, answer_surface, answer
		FROM codex_interactive_requests WHERE id=$1`, requestID).Scan(&status, &surface, &answer))
	require.Equal(t, "resolved", status)
	require.Equal(t, "discord", surface)
	require.JSONEq(t, `{"answers":{"choice":{"answers":["是"]},`+
		`"detail":{"answers":["因为需要"]}}}`, string(answer))
	require.NoError(t, ProjectInteractiveRequest(ctx, db, requestID))
	answerItem, err := outbox.Claim(ctx, 30*time.Second)
	require.NoError(t, err)
	require.Equal(t, "interactive-answer:"+requestID.String(), answerItem.OperationKey)
	require.Equal(t, "message.create", answerItem.OperationType)
	completeDiscordDelivery(t, ctx, outbox, answerItem,
		json.RawMessage(`{"messageId":"interactive-answer-message"}`))
	linkItem, err := outbox.Claim(ctx, 30*time.Second)
	require.NoError(t, err)
	require.Equal(t, "interactive-answer-link:"+requestID.String(), linkItem.OperationKey)
	require.Equal(t, "message.update", linkItem.OperationType)
	require.Contains(t, string(linkItem.Payload), "interactive-answer-message")
	var questionMessageID, answerMessageID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT discord_message_id,
		discord_answer_message_id FROM codex_interactive_requests WHERE id=$1`,
		requestID).Scan(&questionMessageID, &answerMessageID))
	require.Equal(t, "interactive-question-message", questionMessageID)
	require.Equal(t, "interactive-answer-message", answerMessageID)

	answerResult, err = manager.AnswerInteractive(ctx, testGuildID, requestID, 0, 1, "")
	require.NoError(t, err, "旧按钮必须幂等返回已完成状态")
	card = answerResult.Card
	require.True(t, answerResult.Complete)
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

func TestExecutePlanSwitchesDefaultAndIsIdempotent(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	insertInteractiveGuild(t, db)
	seed := seedDiscordManagerData(t, db)
	controlID, runID := insertInteractiveControl(t, db, seed)
	var conversationID, forumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT discord_conversation_id
		FROM codex_thread_controls WHERE id=$1`, controlID).Scan(&conversationID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT forum_id FROM discord_conversations
		WHERE id=$1`, conversationID).Scan(&forumID))
	service := NewConversationService(db)
	_, err := service.ExecutePlan(ctx, testGuildID, "interactive-thread",
		"1001", "Owner", "owner", uuid.Nil)
	require.ErrorContains(t, err, "Run ID")
	_, err = service.ExecutePlan(ctx, testGuildID, "interactive-thread",
		"1001", "Owner", "owner", uuid.New())
	require.ErrorContains(t, err, "不存在")
	_, err = service.ExecutePlan(ctx, testGuildID, "interactive-thread",
		"1001", "Owner", "owner", runID)
	require.ErrorContains(t, err, "尚未完成")
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status='completed',
		active_slot=NULL, collaboration_mode='plan', finished_at=now()-interval '1 second'
		WHERE id=$1`, runID)
	require.NoError(t, err)
	storedPlan := "# 实施计划\n\n1. 修改实现\n2. 运行测试"
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='completed',
		result=jsonb_build_object('finalAnswer',$2::text,'finalOutputType','plan'),
		finished_at=now()-interval '1 second' WHERE id=(
			SELECT primary_intent_id FROM codex_turn_runs WHERE id=$1)`, runID, storedPlan)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET collaboration_mode='plan',
		collaboration_mode_revision=1, settings_revision=1 WHERE id=$1`, conversationID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET collaboration_mode='plan',
		collaboration_mode_revision=1, settings_revision=1 WHERE id=$1`, controlID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_access
		(forum_id, discord_user_id, access_level) VALUES ($1,'2002','operator')`, forumID)
	require.NoError(t, err)
	_, err = service.ExecutePlan(ctx, "wrong-guild", "interactive-thread",
		"1001", "Owner", "owner", runID)
	require.ErrorContains(t, err, "不属于")

	require.NoError(t, ProjectConversationReply(ctx, db, "interactive-thread",
		conversationID, "desktop-explanation", runID, "这只是解释，不是计划。", "agentMessage"))
	var unexpectedPlanCards int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE projection_key LIKE $1 AND desired_payload->'card'->>'header'=$2`,
		"conversation-reply:"+conversationID.String()+":message:desktop-explanation%",
		"📋 Codex · Plan 已完成").Scan(&unexpectedPlanCards))
	require.Zero(t, unexpectedPlanCards)

	planBody := strings.Repeat("计划步骤内容\n", 700)
	require.NoError(t, ProjectConversationReply(ctx, db, "interactive-thread",
		conversationID, "desktop-plan", runID, planBody, "plan"))
	outbox := NewSQLoutbox(db)
	created, actionFound := 0, false
	for step := 0; step < 30; step++ {
		item, claimErr := outbox.Claim(ctx, 30*time.Second)
		require.NoError(t, claimErr)
		require.NotNil(t, item)
		hasAction := strings.Contains(string(item.Payload), planExecuteButtonPrefix+runID.String())
		if item.OperationType == "message.create" {
			created++
			completeDiscordDelivery(t, ctx, outbox, item, json.RawMessage(
				fmt.Sprintf(`{"messageId":"plan-part-%d"}`, created)))
		} else {
			completeDiscordDelivery(t, ctx, outbox, item, json.RawMessage(`{}`))
		}
		if hasAction {
			actionFound = true
			break
		}
	}
	require.True(t, actionFound)
	require.Greater(t, created, 1)
	startedRunID := uuid.New()
	require.NoError(t, ExpireConversationPlanCards(ctx, db, conversationID, startedRunID))
	var activePlanCards, expiredPlanCards int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE desired_payload->'card'->>'header'='📋 Codex · Plan 已完成'
			AND projection_key LIKE $1`, "conversation-reply:"+conversationID.String()+"%").
		Scan(&activePlanCards))
	require.Zero(t, activePlanCards)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key LIKE $1 AND operation_type='message.delete'`,
		"plan-expire:"+startedRunID.String()+":%").Scan(&expiredPlanCards))
	require.Equal(t, 1, expiredPlanCards)

	_, err = db.ExecContext(ctx, `INSERT INTO codex_turn_intents
		(control_id, sequence_no, behavior, source_type, discord_conversation_id, session_id,
		 workspace_project_id, agent_profile_id, idempotency_key, status)
		SELECT control.id, 2, 'start_when_idle', 'workspace_session',
			control.discord_conversation_id, control.session_id, control.workspace_project_id,
			control.agent_profile_id, $2, 'queued'
		FROM codex_thread_controls control WHERE control.id=$1`,
		controlID, "plan-busy-"+uuid.NewString())
	require.NoError(t, err)
	_, err = service.ExecutePlan(ctx, testGuildID, "interactive-thread",
		"2002", "Operator", "operator", runID)
	require.ErrorIs(t, err, ErrPlanExecutionBusy)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='completed',
		finished_at=now() WHERE control_id=$1 AND sequence_no=2`, controlID)
	require.NoError(t, err)
	_, err = service.ExecutePlan(ctx, testGuildID, "interactive-thread",
		"2002", "Operator", "operator", runID)
	require.ErrorIs(t, err, ErrPlanExecutionStale)
	_, err = db.ExecContext(ctx, `DELETE FROM codex_turn_intents
		WHERE control_id=$1 AND sequence_no=2`, controlID)
	require.NoError(t, err)

	result, err := service.ExecutePlan(ctx, testGuildID, "interactive-thread",
		"2002", "", "operator", runID)
	require.NoError(t, err)
	require.False(t, result.AlreadyExecuted)
	var conversationMode, controlMode, body, access string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT conversation.collaboration_mode,
		control.collaboration_mode FROM discord_conversations conversation
		JOIN codex_thread_controls control ON control.discord_conversation_id=conversation.id
		WHERE conversation.id=$1`, conversationID).Scan(&conversationMode, &controlMode))
	require.Equal(t, "default", conversationMode)
	require.Equal(t, "default", controlMode)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT body, access_snapshot
		FROM discord_input_messages WHERE message_id=$1`,
		"plan-execution:"+runID.String()).Scan(&body, &access))
	require.Equal(t, codexcontrol.PlanExecutionInstruction(storedPlan), body)
	require.Equal(t, AccessOperator, access)
	var intentInstruction, sessionMessage string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT intent.instruction,
		message.content #>> '{v,content,data,message}'
		FROM discord_input_messages input
		JOIN codex_turn_intents intent ON intent.id=input.turn_intent_id
		JOIN session_messages message ON message.turn_intent_id=intent.id
		WHERE input.message_id=$1`, "plan-execution:"+runID.String()).
		Scan(&intentInstruction, &sessionMessage))
	require.Equal(t, codexcontrol.PlanExecutionInstruction(storedPlan), intentInstruction)
	require.Equal(t, codexcontrol.PlanExecutionDisplayText, sessionMessage)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET thread_id='2001'
		WHERE id=$1`, conversationID)
	require.NoError(t, err)
	connector := NewDisgoConnector(NewManager(db, nil), service, nil,
		testGuildID, "token", nil)
	client := &bot.Client{}
	customID := planExecuteButtonPrefix + runID.String()
	connector.executePlanComponent(newComponentEvent(t, client, "9201",
		"2001", customID, nil), customID)
	connector.executePlanComponent(newComponentEvent(t, client, "9202",
		"2001", "invalid", nil), "invalid")

	result, err = service.ExecutePlan(ctx, testGuildID, "2001",
		"1001", "Owner", "owner", runID)
	require.NoError(t, err)
	require.True(t, result.AlreadyExecuted)
	_, err = service.ExecutePlan(ctx, testGuildID, "2001",
		"2999", "Read only", "readonly", runID)
	require.ErrorContains(t, err, "readonly")
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_input_messages
		WHERE message_id=$1`, "plan-execution:"+runID.String()).Scan(&count))
	require.Equal(t, 1, count)
}

func claimOutboxOperation(t *testing.T, ctx context.Context, outbox *SQLoutbox,
	operationKey string,
) (*OutboxItem, error) {
	t.Helper()
	for attempt := 0; attempt < 10; attempt++ {
		item, err := outbox.Claim(ctx, 30*time.Second)
		if err != nil || item == nil {
			return item, err
		}
		if item.OperationKey == operationKey {
			return item, nil
		}
		completeDiscordDelivery(t, ctx, outbox, item, json.RawMessage(`{}`))
	}
	return nil, fmt.Errorf("未领取到 Outbox 操作 %s", operationKey)
}

func insertInteractiveControl(t *testing.T, db *sql.DB, seed discordManagerSeed) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var profileID, workspaceID, conversationID, controlID, intentID, runID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name='Default'`).Scan(&profileID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT workspace_id FROM discord_forums
		WHERE id=$1`, seed.workspaceForumID).Scan(&workspaceID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 workspace_project_id, agent_profile_id, title)
		VALUES ($1,$2,'interactive-thread','interactive-starter','1001',$3,$4,'Interactive') RETURNING id`,
		testGuildID, seed.workspaceForumID, seed.workspaceProjectID, profileID).Scan(&conversationID))
	sessionID := bindDiscordConversationSessionForTest(t, db, conversationID)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_thread_controls
		(source_type, discord_conversation_id, session_id, workspace_project_id, agent_profile_id,
		 worker_id, workspace_id, external_thread_id)
		VALUES ('workspace_session',$1,$2,$3,$4,$5,$6,'codex-interactive-thread') RETURNING id`,
		conversationID, sessionID, seed.workspaceProjectID, profileID, seed.workerID,
		workspaceID).Scan(&controlID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_turn_intents
		(control_id, sequence_no, behavior, source_type, discord_conversation_id, session_id,
		 workspace_project_id, agent_profile_id, idempotency_key, status)
		VALUES ($1,1,'start_when_idle','workspace_session',$2,$3,$4,$5,$6,'waiting_for_user') RETURNING id`,
		controlID, conversationID, sessionID, seed.workspaceProjectID, profileID,
		"interactive-"+uuid.NewString()).Scan(&intentID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_turn_runs
		(control_id, primary_intent_id, attempt, lease_owner, lease_epoch, capability_hash,
		 status, worker_id)
		VALUES ($1,$2,1,'worker',1,$3,'waiting_for_user',$4) RETURNING id`, controlID, intentID,
		strings.Repeat("a", 64), seed.workerID).Scan(&runID))
	_, err := db.ExecContext(ctx, `UPDATE codex_thread_controls
		SET next_sequence_no=2 WHERE id=$1`, controlID)
	require.NoError(t, err)
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

func completeDiscordDelivery(t *testing.T, ctx context.Context, store *SQLoutbox,
	item *OutboxItem, response json.RawMessage,
) {
	t.Helper()
	require.NoError(t, store.RecordDelivery(ctx, item, response))
	require.NoError(t, store.Apply(ctx, *item))
}

func insertInteractiveGuild(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO discord_guilds(guild_id, enabled) VALUES ($1, true)`, testGuildID)
	require.NoError(t, err)
}
