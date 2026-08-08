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
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOfficialRequestsUseOneAuthoritativeResponse(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES ($1,'official-request-test',true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000821", MessageID: "100000000000000822",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Official request", Body: "answer", ConfigurationConfirmed: true,
	})
	require.NoError(t, err)
	const officialThreadID = "thread-official-request"
	_, err = db.ExecContext(ctx, `INSERT INTO official_thread_bindings(
		workspace_id,conversation_id,workspace_project_id,thread_id,interactive_owner)
		VALUES ($1,$2,$3,$4,'control')`, seed.workspaceID, conversationID,
		seed.workspaceProjectID, officialThreadID)
	require.NoError(t, err)

	params := json.RawMessage(`{"questions":[
		{"id":"q1","header":"First","question":"Pick","options":[{"label":"A"}]},
		{"id":"q2","header":"Second","question":"Explain","isOther":true}
	]}`)
	requestID := insertOfficialRequestForTest(t, db, seed.workspaceID,
		conversationID, officialThreadID, "item/tool/requestUserInput", params, "a")
	require.NoError(t, ProjectOfficialServerRequest(ctx, db, requestID))
	_, err = db.ExecContext(ctx, `UPDATE discord_projections SET message_id='request-card'
		WHERE projection_key=$1`, "official-request:"+requestID.String())
	require.NoError(t, err)
	require.NoError(t, ProjectOfficialServerRequest(ctx, db, requestID))
	var operation string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type
		FROM integration_outbox WHERE operation_key=$1`,
		"projection:official-request:"+requestID.String()).Scan(&operation))
	require.Equal(t, "message.update", operation)

	client := &bot.Client{ApplicationID: snowflake.ID(900)}
	manager := &Manager{db: db}
	connector := &DisgoConnector{manager: manager, conversations: service,
		guildID: testGuildID}
	connector.answerOfficialComponent(newComponentEvent(t, client, "8201",
		"100000000000000821", officialInputButtonPrefix+requestID.String()+":0:0", nil),
		officialInputButtonPrefix+requestID.String()+":0:0")
	connector.answerOfficialComponent(newComponentEvent(t, client, "8202",
		"100000000000000821", officialInputButtonPrefix+requestID.String()+":1:-1", nil),
		officialInputButtonPrefix+requestID.String()+":1:-1")
	connector.answerOfficialModal(newModalEvent(t, client, "8203",
		"100000000000000821", officialInputModalPrefix+requestID.String()+":1",
		[]discord.LayoutComponent{discord.NewLabel("回答",
			discord.TextInputComponent{CustomID: "answer", Value: "details"})}))
	var status, surface string
	var response json.RawMessage
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status,answer_surface,response
		FROM official_server_requests WHERE id=$1`, requestID).
		Scan(&status, &surface, &response))
	require.Equal(t, "answered", status)
	require.Equal(t, "discord", surface)
	require.JSONEq(t, `{"answers":{"q1":{"answers":["A"]},"q2":{"answers":["details"]}}}`,
		string(response))
	repeated, err := manager.AnswerOfficialInput(ctx, testGuildID, requestID, 0, 0, "")
	require.NoError(t, err)
	require.True(t, repeated.Complete)

	secretID := insertOfficialRequestForTest(t, db, seed.workspaceID,
		conversationID, officialThreadID, "item/tool/requestUserInput",
		json.RawMessage(`{"questions":[{"id":"secret","isSecret":true}]}`), "b")
	_, err = manager.AnswerOfficialInput(ctx, "wrong-guild", secretID, 0, -1, "hidden")
	require.ErrorContains(t, err, "不属于")
	_, err = manager.AnswerOfficialInput(ctx, testGuildID, secretID, 0, -1, "hidden")
	require.ErrorContains(t, err, "敏感问题")
	_, err = manager.AnswerOfficialInput(ctx, testGuildID, secretID, 2, -1, "hidden")
	require.ErrorContains(t, err, "序号")

	approvalID := insertOfficialRequestForTest(t, db, seed.workspaceID,
		conversationID, officialThreadID, "item/commandExecution/requestApproval",
		json.RawMessage(`{"command":"go test ./..."}`), "c")
	require.NoError(t, ProjectOfficialServerRequest(ctx, db, approvalID))
	connector.answerOfficialComponent(newComponentEvent(t, client, "8204",
		"100000000000000821", officialApprovalPrefix+approvalID.String()+":accept", nil),
		officialApprovalPrefix+approvalID.String()+":accept")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status,response
		FROM official_server_requests WHERE id=$1`, approvalID).Scan(&status, &response))
	require.Equal(t, "answered", status)
	require.JSONEq(t, `{"decision":"accept"}`, string(response))
	_, err = manager.AnswerOfficialApproval(ctx, "wrong-guild", secretID, "decline")
	require.ErrorContains(t, err, "不属于")

	connector.answerOfficialComponent(newComponentEvent(t, client, "8205",
		"100000000000000821", "official-approval:bad", nil), "official-approval:bad")
	connector.answerOfficialModal(newModalEvent(t, client, "8206",
		"100000000000000821", officialInputModalPrefix+requestID.String()+":0",
		[]discord.LayoutComponent{discord.NewLabel("回答",
			discord.TextInputComponent{CustomID: "answer"})}))
}

func insertOfficialRequestForTest(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}, workspaceID, conversationID uuid.UUID, threadID, method string,
	params json.RawMessage, keyByte string,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.QueryRow(`INSERT INTO official_server_requests(
		workspace_id,conversation_id,connection_id,request_key,app_server_request_id,
		method,thread_id,turn_id,params,owner,status)
		VALUES($1,$2,$3,$4,'1'::jsonb,$5,$6,'turn-request',$7,'control','pending')
		RETURNING id`, workspaceID, conversationID, uuid.New(),
		strings.Repeat(keyByte, 64), method, threadID, params).Scan(&id))
	return id
}

func TestOfficialLifecycleAndLateDesktopBinding(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES ($1,'official-lifecycle-test',true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000831", MessageID: "100000000000000832",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Official lifecycle", Body: "archive", ConfigurationConfirmed: true,
	})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO official_thread_bindings(
		workspace_id,conversation_id,workspace_project_id,thread_id)
		VALUES($1,$2,$3,'thread-official-lifecycle')`, seed.workspaceID,
		conversationID, seed.workspaceProjectID)
	require.NoError(t, err)

	archive, err := service.Archive(ctx, testGuildID, "100000000000000831",
		"1001", "8301")
	require.NoError(t, err)
	require.Equal(t, "queued", archive.Status)
	repeatedArchive, err := service.Archive(ctx, testGuildID, "100000000000000831",
		"1001", "8302")
	require.NoError(t, err)
	require.Equal(t, archive.ID, repeatedArchive.ID)
	staleRevision := archive.Revision - 1
	_, err = service.Restore(ctx, testGuildID, "100000000000000831",
		"1001", "8303", &staleRevision)
	require.ErrorIs(t, err, ErrLifecycleRevisionStale)
	_, err = service.Archive(ctx, testGuildID, "100000000000000831", "1002", "8304")
	require.ErrorIs(t, err, ErrReadOnly)

	_, err = db.ExecContext(ctx, `UPDATE official_thread_actions SET status='completed'
		WHERE id=$1`, archive.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations
		SET lifecycle_state='archived' WHERE id=$1`, conversationID)
	require.NoError(t, err)
	alreadyArchived, err := service.Archive(ctx, testGuildID, "100000000000000831",
		"1001", "8305")
	require.NoError(t, err)
	require.True(t, alreadyArchived.AlreadyInState)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, EnqueueConversationLifecycleTx(ctx, tx, conversationID))
	require.NoError(t, tx.Commit())
	cardKey := "conversation-lifecycle-card:" + conversationID.String()
	var operation string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type
		FROM integration_outbox WHERE operation_key=$1`, cardKey).Scan(&operation))
	require.Equal(t, "message.create", operation)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations
		SET lifecycle_card_message_id='lifecycle-card' WHERE id=$1`, conversationID)
	require.NoError(t, err)
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, EnqueueConversationLifecycleTx(ctx, tx, conversationID))
	require.NoError(t, tx.Commit())
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type
		FROM integration_outbox WHERE operation_key=$1`, cardKey).Scan(&operation))
	require.Equal(t, "message.update", operation)
	completeOutboxForTest(t, ctx, db, cardKey,
		json.RawMessage(`{"messageId":"lifecycle-card"}`))
	lifecycleKey := "conversation-lifecycle:" + conversationID.String()
	completeOutboxForTest(t, ctx, db, lifecycleKey, json.RawMessage(`{}`))

	expectedRevision := archive.Revision
	restore, err := service.Restore(ctx, testGuildID, "100000000000000831",
		"1001", "8306", &expectedRevision)
	require.NoError(t, err)
	require.Equal(t, "active", restore.DesiredState)
	repeatedRestore, err := service.Restore(ctx, testGuildID, "100000000000000831",
		"1001", "8307", &restore.Revision)
	require.NoError(t, err)
	require.Equal(t, restore.ID, repeatedRestore.ID)
	_, err = db.ExecContext(ctx, `UPDATE official_thread_actions SET status='completed'
		WHERE id=$1`, restore.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations
		SET lifecycle_state='active' WHERE id=$1`, conversationID)
	require.NoError(t, err)
	require.NoError(t, ReconcileConversationLifecycles(ctx, db, testGuildID))
	completeOutboxForTest(t, ctx, db, lifecycleKey, json.RawMessage(`{}`))
	deleteKey := "conversation-lifecycle-delete:" + conversationID.String() + ":" +
		int64Text(restore.Revision)
	completeOutboxForTest(t, ctx, db, deleteKey, json.RawMessage(`{}`))
	var cardMessageID sql.NullString
	var appliedRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_card_message_id,
		discord_lifecycle_applied_revision FROM discord_conversations WHERE id=$1`,
		conversationID).Scan(&cardMessageID, &appliedRevision))
	require.False(t, cardMessageID.Valid)
	require.Equal(t, restore.Revision, appliedRevision)
	require.NoError(t, ReconcileConversationLifecycles(ctx, db, testGuildID))
	guildSnowflake, err := snowflake.Parse(testGuildID)
	require.NoError(t, err)
	threadSnowflake, err := snowflake.Parse("100000000000000831")
	require.NoError(t, err)
	client := &bot.Client{ApplicationID: snowflake.ID(900)}
	connector := &DisgoConnector{manager: &Manager{db: db}, conversations: service,
		guildID: testGuildID, logger: zap.NewNop()}
	connector.onThreadUpdate(&events.ThreadUpdate{GenericThread: &events.GenericThread{
		GenericEvent: events.NewGenericEvent(client, 1, 0), GuildID: guildSnowflake,
		ThreadID: threadSnowflake, Thread: discord.GuildThread{ThreadMetadata: discord.ThreadMetadata{
			Archived: true, Locked: true,
		}},
	}})
	var pendingLifecycle int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key=$1 AND status='pending'`, lifecycleKey).Scan(&pendingLifecycle))
	require.Equal(t, 1, pendingLifecycle)

	const desktopThreadID = "thread-late-desktop"
	var bindingID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO official_thread_bindings(
		workspace_id,workspace_project_id,thread_id)
		VALUES($1,$2,$3) RETURNING id`, seed.workspaceID, seed.workspaceProjectID,
		desktopThreadID).Scan(&bindingID))
	clientID := "desktop-client-message"
	thread := officialapp.Thread{ID: desktopThreadID, Turns: []officialapp.Turn{{
		ID: "turn-late", Status: "completed", Items: []officialapp.Item{
			{Type: "userMessage", ID: "user-late", ClientID: &clientID, Text: "hello"},
			{Type: "agentMessage", ID: "agent-late", Text: "world"},
		},
	}}}
	require.NoError(t, ProjectOfficialThread(ctx, db, seed.workspaceID, thread))
	postKey := "official-thread-post:" + bindingID.String()
	require.NoError(t, NewSQLoutbox(db).Enqueue(ctx, postKey, "forum.post.create",
		"channels/"+seed.workspaceForumChannelID+"/threads", map[string]any{
			"channelId": seed.workspaceForumChannelID, "threadName": "Desktop task",
		}, postKey))
	completeOutboxForTest(t, ctx, db, postKey,
		json.RawMessage(`{"threadId":"100000000000000841","messageId":"100000000000000842"}`))
	var lateConversationID uuid.UUID
	var title string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT binding.conversation_id,
		conversation.title FROM official_thread_bindings binding
		JOIN discord_conversations conversation ON conversation.id=binding.conversation_id
		WHERE binding.id=$1`, bindingID).Scan(&lateConversationID, &title))
	require.Equal(t, "Desktop task", title)
	var projectionCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE projection_key LIKE $1`, "official:"+lateConversationID.String()+":%").
		Scan(&projectionCount))
	require.Equal(t, 1, projectionCount)
	require.NoError(t, ReplayOfficialThreadProjection(ctx, db, bindingID))

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	replayedBindingID, err := NewSQLoutbox(db).completeOfficialThreadPost(ctx, tx,
		OutboxItem{OperationKey: postKey,
			Payload: json.RawMessage(`{"threadName":"ignored"}`)},
		json.RawMessage(`{"threadId":"other","messageId":"other"}`))
	require.NoError(t, err)
	require.Equal(t, bindingID, replayedBindingID)
	require.NoError(t, tx.Rollback())
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = NewSQLoutbox(db).completeOfficialThreadPost(ctx, tx,
		OutboxItem{OperationKey: "bad"}, json.RawMessage(`{}`))
	require.Error(t, err)
	require.NoError(t, tx.Rollback())
}

func TestExecuteOfficialPlanUsesLatestCompletedItemAndIsIdempotent(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES ($1,'official-plan-test',true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000801", MessageID: "100000000000000802",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Official plan", Body: "先规划", CollaborationMode: "plan",
		ConfigurationConfirmed: true,
	})
	require.NoError(t, err)
	const officialThreadID = "thread-official-plan"
	_, err = db.ExecContext(ctx, `INSERT INTO official_thread_bindings(
		workspace_id,conversation_id,workspace_project_id,thread_id)
		VALUES ($1,$2,$3,$4)`, seed.workspaceID, conversationID,
		seed.workspaceProjectID, officialThreadID)
	require.NoError(t, err)

	firstPlan := "# 第一版计划\n\n1. 旧步骤"
	require.NoError(t, ProjectOfficialThread(ctx, db, seed.workspaceID, officialapp.Thread{
		ID: officialThreadID,
		Turns: []officialapp.Turn{{ID: "turn-plan-1", Status: "completed",
			Items: []officialapp.Item{{Type: "plan", ID: "plan-item-1", Text: firstPlan}}}},
	}))
	var staleActionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM official_plan_actions
		WHERE conversation_id=$1 AND turn_id='turn-plan-1'`, conversationID).
		Scan(&staleActionID))

	latestPlan := "# 最终计划\n\n1. 修改实现\n2. 运行测试"
	require.NoError(t, ProjectOfficialThread(ctx, db, seed.workspaceID, officialapp.Thread{
		ID: officialThreadID,
		Turns: []officialapp.Turn{
			{ID: "turn-plan-1", Status: "completed",
				Items: []officialapp.Item{{Type: "plan", ID: "plan-item-1", Text: firstPlan}}},
			{ID: "turn-plan-2", Status: "completed",
				Items: []officialapp.Item{{Type: "plan", ID: "plan-item-2", Text: latestPlan}}},
		},
	}))
	rows, err := db.QueryContext(ctx, `SELECT desired_payload FROM discord_projections
		WHERE projection_key LIKE $1 ORDER BY projection_key`,
		"official:"+conversationID.String()+":%")
	require.NoError(t, err)
	defer rows.Close()
	projectedCards := make([]ComponentCardPayload, 0, 2)
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		var payload struct {
			Card ComponentCardPayload `json:"card"`
		}
		require.NoError(t, json.Unmarshal(raw, &payload))
		require.NoError(t, validateOfficialCard(payload.Card),
			"官方历史投影必须能通过真实 Discord 组件校验")
		projectedCards = append(projectedCards, payload.Card)
	}
	require.NoError(t, rows.Err())
	require.Len(t, projectedCards, 2)
	require.Empty(t, projectedCards[0].Buttons)
	require.Len(t, projectedCards[1].Buttons, 1)
	var actionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM official_plan_actions
		WHERE conversation_id=$1 AND turn_id='turn-plan-2'`, conversationID).
		Scan(&actionID))

	_, err = service.ExecutePlan(ctx, testGuildID, "100000000000000801",
		"1001", "Alice", "alice", staleActionID, "100000000000000803")
	require.ErrorIs(t, err, ErrPlanExecutionStale)
	_, err = service.ExecutePlan(ctx, testGuildID, "100000000000000801",
		"1001", "Alice", "alice", uuid.Nil, "100000000000000804")
	require.ErrorContains(t, err, "action ID")

	result, err := service.ExecutePlan(ctx, testGuildID, "100000000000000801",
		"1001", "Alice", "alice", actionID, "100000000000000805")
	require.NoError(t, err)
	require.False(t, result.AlreadyExecuted)
	repeated, err := service.ExecutePlan(ctx, testGuildID, "100000000000000801",
		"1001", "Alice", "alice", actionID, "100000000000000805")
	require.NoError(t, err)
	require.True(t, repeated.AlreadyExecuted)

	var sourceType, clientMessageID, instruction, displayInstruction, mode, actionStatus string
	var submissionCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT source_type,client_user_message_id,
		instruction,display_instruction,preferences->>'collaborationMode'
		FROM official_turn_submissions WHERE plan_action_id=$1`, actionID).
		Scan(&sourceType, &clientMessageID, &instruction, &displayInstruction, &mode))
	require.Equal(t, "discord_plan", sourceType)
	require.Equal(t, "discord-plan:"+actionID.String(), clientMessageID)
	require.Equal(t, codexcontrol.PlanExecutionInstruction(latestPlan), instruction)
	require.Equal(t, codexcontrol.PlanExecutionDisplayText, displayInstruction)
	require.Equal(t, "default", mode)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM official_plan_actions
		WHERE id=$1`, actionID).Scan(&actionStatus))
	require.Equal(t, "executed", actionStatus)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM official_turn_submissions
		WHERE plan_action_id=$1`, actionID).Scan(&submissionCount))
	require.Equal(t, 1, submissionCount, "双击执行计划不得创建第二次提交")

	var collaborationMode string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT collaboration_mode
		FROM discord_conversations WHERE id=$1`, conversationID).Scan(&collaborationMode))
	require.Equal(t, "default", collaborationMode)

	thirdPlan := "# 第三版计划\n\n1. 从 Discord 执行"
	require.NoError(t, ProjectOfficialThread(ctx, db, seed.workspaceID, officialapp.Thread{
		ID: officialThreadID,
		Turns: []officialapp.Turn{
			{ID: "turn-plan-1", Status: "completed",
				Items: []officialapp.Item{{Type: "plan", ID: "plan-item-1", Text: firstPlan}}},
			{ID: "turn-plan-2", Status: "completed",
				Items: []officialapp.Item{{Type: "plan", ID: "plan-item-2", Text: latestPlan}}},
			{ID: "turn-plan-3", Status: "completed",
				Items: []officialapp.Item{{Type: "plan", ID: "plan-item-3", Text: thirdPlan}}},
		},
	}))
	var componentActionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM official_plan_actions
		WHERE conversation_id=$1 AND turn_id='turn-plan-3'`, conversationID).
		Scan(&componentActionID))
	client := &bot.Client{ApplicationID: snowflake.ID(900)}
	connector := &DisgoConnector{manager: &Manager{db: db}, conversations: service,
		guildID: testGuildID}
	connector.executePlanComponent(newComponentEvent(t, client,
		"100000000000000806", "100000000000000801", planExecuteButtonPrefix+"bad", nil),
		planExecuteButtonPrefix+"bad")
	connector.executePlanComponent(newComponentEvent(t, client,
		"100000000000000807", "100000000000000801",
		planExecuteButtonPrefix+componentActionID.String(), nil),
		planExecuteButtonPrefix+componentActionID.String())
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM official_plan_actions
		WHERE id=$1`, componentActionID).Scan(&actionStatus))
	require.Equal(t, "executed", actionStatus)

	var buttonPayload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_payload::text
		FROM discord_projections WHERE projection_key LIKE $1
		ORDER BY projection_key DESC LIMIT 1`, "official:"+conversationID.String()+"%").
		Scan(&buttonPayload))
	require.Contains(t, buttonPayload, planExecuteButtonPrefix+componentActionID.String())
	require.True(t, strings.Contains(instruction, latestPlan))
}
