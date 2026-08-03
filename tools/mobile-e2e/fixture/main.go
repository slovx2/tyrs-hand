package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
)

type seededFixture struct {
	EnvironmentID uuid.UUID `json:"environmentId"`
	ContainerName string    `json:"containerName"`
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("用法：fixture configure-provider|seed|seed-project-matrix|wait-ready|snapshot|notification-target|assert-message-once|assert-session-project|assert-attachment-once|assert-preference|assert-intent-once|seed-history|seed-forward-history|force-cursor-reset")
	}
	databaseURL := os.Getenv("TYRS_HAND_DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("缺少 TYRS_HAND_DATABASE_URL")
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch os.Args[1] {
	case "configure-provider":
		configureProvider(ctx, db, os.Args[2:])
	case "seed":
		seed(ctx, db, os.Args[2:])
	case "seed-project-matrix":
		seedProjectMatrix(ctx, db, os.Args[2:])
	case "wait-ready":
		waitReady(ctx, db, os.Args[2:])
	case "snapshot":
		snapshot(ctx, db)
	case "notification-target":
		notificationTarget(ctx, db, os.Args[2:])
	case "assert-message-once":
		assertMessageOnce(ctx, db, os.Args[2:])
	case "assert-session-project":
		assertSessionProject(ctx, db, os.Args[2:])
	case "assert-attachment-once":
		assertAttachmentOnce(ctx, db, os.Args[2:])
	case "assert-preference":
		assertPreference(ctx, db, os.Args[2:])
	case "assert-intent-once":
		assertIntentOnce(ctx, db, os.Args[2:])
	case "seed-history":
		seedHistory(ctx, db)
	case "seed-forward-history":
		seedForwardHistory(ctx, db)
	case "force-cursor-reset":
		forceCursorReset(ctx, db)
	default:
		log.Fatalf("未知命令 %q", os.Args[1])
	}
}

func configureProvider(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("configure-provider", flag.ExitOnError)
	baseURL := flags.String("base-url", "", "OpenAI-compatible Responses API Base URL")
	_ = flags.Parse(arguments)
	masterKey, err := base64.StdEncoding.DecodeString(os.Getenv("TYRS_HAND_MASTER_KEY"))
	if err != nil || len(masterKey) != 32 || strings.TrimSpace(*baseURL) == "" {
		log.Fatal("configure-provider 需要 --base-url 与 32 字节 base64 TYRS_HAND_MASTER_KEY")
	}
	box, err := security.NewSecretBox(masterKey)
	if err != nil {
		log.Fatal(err)
	}
	service := platformsettings.NewService(db, secrets.NewStore(db, box))
	err = service.SaveAgentProvider(ctx, platformsettings.AgentProviderInput{
		ModelSource: platformsettings.ModelSourceProvider,
		BaseURL:     *baseURL,
		APIKey:      "mobile-e2e-key",
		Model:       "gpt-5.6-sol",
		Reasoning:   "high",
		ServiceTier: "standard",
	})
	if err != nil {
		log.Fatal(err)
	}
	writeJSON(map[string]any{"configured": true})
}

func seed(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("seed", flag.ExitOnError)
	nodeText := flags.String("node-id", "", "执行节点 UUID")
	image := flags.String("image", "", "开发镜像")
	protocol := flags.Bool("protocol", false, "直接创建协议 Worker 使用的就绪环境")
	projectName := flags.String("project-name", "e2e-project", "首个项目名称")
	_ = flags.Parse(arguments)
	nodeID, err := uuid.Parse(*nodeText)
	if err != nil || strings.TrimSpace(*image) == "" {
		log.Fatal("seed 需要 --node-id 与 --image")
	}
	environmentID := uuid.New()
	compact := strings.ReplaceAll(environmentID.String(), "-", "")
	fixture := seededFixture{EnvironmentID: environmentID,
		ContainerName: "tyrs-hand-e2e-dev-" + compact}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	status := "pending"
	containerID := any(nil)
	runtimeUser := any(nil)
	runtimeUID := any(nil)
	runtimeGID := any(nil)
	runtimeHome := any(nil)
	if *protocol {
		status, containerID, runtimeUser = "running", "protocol-container", "developer"
		runtimeUID, runtimeGID, runtimeHome = int64(1000), int64(1000), "/home/developer"
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO discord_guilds(guild_id,enabled)
			VALUES ('999000000000000001',true) ON CONFLICT(guild_id) DO NOTHING`, nil},
		{`INSERT INTO discord_members(guild_id,discord_user_id,username,display_name,active)
			VALUES ('999000000000000001','999000000000000002','mobile-e2e','Mobile E2E',true)
			ON CONFLICT(guild_id,discord_user_id) DO UPDATE SET active=true`, nil},
		{`INSERT INTO discord_development_environments
			(id,guild_id,owner_discord_user_id,image_ref,container_name,data_volume_name,
			home_volume_name,network_name,execution_node_id,status,container_id,
			runtime_user,runtime_uid,runtime_gid,runtime_home)
			VALUES ($1,'999000000000000001','999000000000000002',$2,$3,$4,$5,$6,$7,
				$8,$9,$10,$11,$12,$13)`,
			[]any{environmentID, *image, fixture.ContainerName, "tyrs-hand-e2e-data-" + compact,
				"tyrs-hand-e2e-home-" + compact, "tyrs-hand-e2e-net-" + compact, nodeID,
				status, containerID, runtimeUser, runtimeUID, runtimeGID, runtimeHome}},
	}
	if *protocol {
		statements = append(statements, struct {
			query string
			args  []any
		}{`INSERT INTO development_projects(environment_id,relative_path,name,project_kind,
			availability_status,last_seen_at) VALUES ($1,'workspaces/e2e-project',$2,
			'directory','available',now())`, []any{environmentID, *projectName}})
	} else {
		statements = append(statements, struct {
			query string
			args  []any
		}{`INSERT INTO discord_development_operations(environment_id,operation,execution_node_id)
			VALUES ($1,'provision_environment',$2)`, []any{environmentID, nodeID}})
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			log.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		log.Fatal(err)
	}
	writeJSON(fixture)
}

func seedProjectMatrix(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("seed-project-matrix", flag.ExitOnError)
	environmentText := flags.String("environment-id", "", "环境 UUID")
	primaryName := flags.String("primary-name", "alpha-primary", "首个项目名称")
	secondaryName := flags.String("secondary-name", "zeta-secondary", "第二项目名称")
	_ = flags.Parse(arguments)
	environmentID, err := uuid.Parse(*environmentText)
	if err != nil {
		log.Fatal("seed-project-matrix 需要 --environment-id")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE development_projects SET name=$2,updated_at=now()
		WHERE environment_id=$1 AND relative_path='workspaces/e2e-project'`, environmentID, *primaryName)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO development_projects(environment_id,relative_path,
			name,project_kind,availability_status,last_seen_at) VALUES
			($1,'workspaces/e2e-secondary',$2,'directory','available',now())
			ON CONFLICT(environment_id,relative_path) DO UPDATE SET name=EXCLUDED.name,
			availability_status='available',last_seen_at=now(),updated_at=now()`, environmentID, *secondaryName)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		log.Fatal(err)
	}
	writeJSON(map[string]any{"environmentId": environmentID, "projects": []string{*primaryName, *secondaryName}})
}

func waitReady(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("wait-ready", flag.ExitOnError)
	environmentText := flags.String("environment-id", "", "环境 UUID")
	_ = flags.Parse(arguments)
	environmentID, err := uuid.Parse(*environmentText)
	if err != nil {
		log.Fatal("wait-ready 需要 --environment-id")
	}
	for ctx.Err() == nil {
		var status, container string
		var projectID sql.NullString
		err = db.QueryRowContext(ctx, `SELECT environment.status,
			COALESCE(environment.container_id,''),
			(SELECT project.id::text FROM development_projects project
			 WHERE project.environment_id=environment.id
			   AND project.relative_path='workspaces/e2e-project'
			   AND project.availability_status='available' LIMIT 1)
			FROM discord_development_environments environment WHERE environment.id=$1`,
			environmentID).Scan(&status, &container, &projectID)
		if err == nil && status == "running" && container != "" && projectID.Valid {
			writeJSON(map[string]any{"environmentId": environmentID, "projectId": projectID.String,
				"containerId": container})
			return
		}
		time.Sleep(time.Second)
	}
	log.Fatal("等待开发环境与 e2e-project 就绪超时")
}

func snapshot(ctx context.Context, db *sql.DB) {
	result := map[string]int64{}
	queries := map[string]string{
		"sessions":      `SELECT count(*) FROM development_sessions`,
		"messages":      `SELECT count(*) FROM session_messages`,
		"runs":          `SELECT count(*) FROM codex_turn_runs`,
		"completedRuns": `SELECT count(*) FROM codex_turn_runs WHERE status='completed'`,
		"failedRuns":    `SELECT count(*) FROM codex_turn_runs WHERE status='failed'`,
		"canceledRuns":  `SELECT count(*) FROM codex_turn_runs WHERE status='canceled'`,
		"interactives":  `SELECT count(*) FROM codex_interactive_requests`,
		"attachments":   `SELECT count(*) FROM session_attachments WHERE status='attached'`,
	}
	for name, query := range queries {
		var count int64
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			log.Fatal(err)
		}
		result[name] = count
	}
	writeJSON(result)
}

func notificationTarget(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("notification-target", flag.ExitOnError)
	text := flags.String("text", "", "用于定位会话的用户消息，可省略")
	_ = flags.Parse(arguments)
	var serverID, sessionID uuid.UUID
	err := db.QueryRowContext(ctx, `SELECT id FROM control_instances WHERE singleton=true`).Scan(&serverID)
	if err != nil {
		log.Fatal(err)
	}
	if *text == "" {
		err = db.QueryRowContext(ctx, `SELECT id FROM development_sessions
			ORDER BY last_activity_at DESC,id DESC LIMIT 1`).Scan(&sessionID)
	} else {
		err = db.QueryRowContext(ctx, `SELECT session.id FROM development_sessions session
			JOIN session_messages message ON message.session_id=session.id
			WHERE message.message_role='user' AND COALESCE(message.content->>'text',
			message.content #>> '{v,content,data,message}','')=$1
			ORDER BY session.last_activity_at DESC LIMIT 1`, *text).Scan(&sessionID)
	}
	if err != nil {
		log.Fatal(err)
	}
	writeJSON(map[string]any{"serverId": serverID, "sessionId": sessionID})
}

func assertMessageOnce(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("assert-message-once", flag.ExitOnError)
	text := flags.String("text", "", "消息正文")
	_ = flags.Parse(arguments)
	if *text == "" {
		log.Fatal("assert-message-once 需要 --text")
	}
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM session_messages
		WHERE message_role='user' AND COALESCE(content #>> '{v,content,data,message}',
			content->>'text','')=$1`, *text).Scan(&count)
	if err != nil {
		log.Fatal(err)
	}
	if count != 1 {
		log.Fatalf("消息 %q 的服务端数量为 %d，预期 1", *text, count)
	}
	writeJSON(map[string]any{"text": *text, "count": count})
}

func assertSessionProject(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("assert-session-project", flag.ExitOnError)
	text := flags.String("text", "", "会话内用户消息正文")
	projectName := flags.String("project-name", "", "预期项目名称")
	_ = flags.Parse(arguments)
	if *text == "" || *projectName == "" {
		log.Fatal("assert-session-project 需要 --text 与 --project-name")
	}
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(DISTINCT session.id)
		FROM development_sessions session
		JOIN development_projects project ON project.id=session.development_project_id
		JOIN session_messages message ON message.session_id=session.id
		WHERE project.name=$2 AND message.message_role='user'
		AND COALESCE(message.content->>'text',message.content #>> '{v,content,data,message}','')=$1`,
		*text, *projectName).Scan(&count)
	if err != nil {
		log.Fatal(err)
	}
	if count != 1 {
		log.Fatalf("消息 %q 位于项目 %q 的会话数量为 %d，预期 1", *text, *projectName, count)
	}
	writeJSON(map[string]any{"text": *text, "projectName": *projectName, "count": count})
}

func assertAttachmentOnce(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("assert-attachment-once", flag.ExitOnError)
	text := flags.String("text", "", "关联消息正文")
	_ = flags.Parse(arguments)
	if *text == "" {
		log.Fatal("assert-attachment-once 需要 --text")
	}
	var attachments, links int
	err := db.QueryRowContext(ctx, `SELECT count(DISTINCT attachment.id),count(link.attachment_id)
		FROM session_messages message
		JOIN session_message_attachments link ON link.message_id=message.id
		JOIN session_attachments attachment ON attachment.id=link.attachment_id
		WHERE message.message_role='user' AND attachment.status='attached'
		AND COALESCE(message.content->>'text',message.content #>> '{v,content,data,message}','')=$1`,
		*text).Scan(&attachments, &links)
	if err != nil {
		log.Fatal(err)
	}
	if attachments != 1 || links != 1 {
		log.Fatalf("消息 %q 的附件=%d、关联=%d，预期均为 1", *text, attachments, links)
	}
	writeJSON(map[string]any{"text": *text, "attachments": attachments, "links": links})
}

func assertPreference(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("assert-preference", flag.ExitOnError)
	mode := flags.String("mode", "", "预期 Default/Plan 模式")
	tier := flags.String("tier", "", "预期速度档位")
	effort := flags.String("effort", "", "预期推理等级")
	_ = flags.Parse(arguments)
	if *mode == "" || *tier == "" || *effort == "" {
		log.Fatal("assert-preference 需要 --mode、--tier 与 --effort")
	}
	var actualMode, actualTier string
	var actualEffort sql.NullString
	err := db.QueryRowContext(ctx, `SELECT collaboration_mode,service_tier,reasoning_effort
		FROM client_user_preferences ORDER BY updated_at DESC LIMIT 1`).Scan(
		&actualMode, &actualTier, &actualEffort)
	if err != nil {
		log.Fatal(err)
	}
	if actualMode != *mode || actualTier != *tier || actualEffort.String != *effort {
		log.Fatalf("记忆参数为 %s/%s/%s，预期 %s/%s/%s", actualMode, actualTier,
			actualEffort.String, *mode, *tier, *effort)
	}
	writeJSON(map[string]any{"mode": actualMode, "tier": actualTier, "effort": actualEffort.String})
}

func assertIntentOnce(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("assert-intent-once", flag.ExitOnError)
	text := flags.String("session-text", "", "标识会话的用户消息")
	operation := flags.String("operation", "turn_input", "Intent operation")
	instruction := flags.String("instruction", "", "可选的完整 instruction")
	_ = flags.Parse(arguments)
	if *text == "" {
		log.Fatal("assert-intent-once 需要 --session-text")
	}
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents intent
		JOIN codex_thread_controls control ON control.id=intent.control_id
		WHERE control.session_id IN (SELECT message.session_id FROM session_messages message
			WHERE message.message_role='user' AND COALESCE(message.content->>'text',
			message.content #>> '{v,content,data,message}','')=$1)
		AND intent.operation=$2 AND ($3='' OR intent.instruction=$3)`,
		*text, *operation, *instruction).Scan(&count)
	if err != nil {
		log.Fatal(err)
	}
	if count != 1 {
		log.Fatalf("会话消息 %q 的 %s Intent 数量为 %d，预期 1", *text, *operation, count)
	}
	writeJSON(map[string]any{"sessionText": *text, "operation": *operation, "count": count})
}

func seedHistory(ctx context.Context, db *sql.DB) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID uuid.UUID
	var sequence int64
	err = tx.QueryRowContext(ctx, `SELECT id,last_message_seq FROM development_sessions
		ORDER BY last_activity_at DESC,id DESC LIMIT 1 FOR UPDATE`).Scan(&sessionID, &sequence)
	if err != nil {
		log.Fatal(err)
	}
	for index := 1; index <= 180; index++ {
		sequence++
		localID := fmt.Sprintf("history-%03d", index)
		content, _ := json.Marshal(map[string]any{"type": "text",
			"text": fmt.Sprintf("历史分页消息 %03d", index)})
		_, err = tx.ExecContext(ctx, `INSERT INTO session_messages
			(session_id,seq,local_id,message_role,content) VALUES ($1,$2,$3,'agent',$4)`,
			sessionID, sequence, localID, content)
		if err != nil {
			log.Fatal(err)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE development_sessions SET last_message_seq=$2,
		last_activity_at=now(),updated_at=now() WHERE id=$1`, sessionID, sequence)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		log.Fatal(err)
	}
	writeJSON(map[string]any{"sessionId": sessionID, "messages": 180})
}

func seedForwardHistory(ctx context.Context, db *sql.DB) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID uuid.UUID
	var sequence int64
	err = tx.QueryRowContext(ctx, `SELECT id,last_message_seq FROM development_sessions
		ORDER BY last_activity_at DESC,id DESC LIMIT 1 FOR UPDATE`).Scan(&sessionID, &sequence)
	if err != nil {
		log.Fatal(err)
	}
	for index := 1; index <= 125; index++ {
		sequence++
		localID := fmt.Sprintf("forward-%03d", index)
		content, _ := json.Marshal(map[string]any{"type": "text",
			"text": fmt.Sprintf("向前分页消息 %03d", index)})
		var messageID uuid.UUID
		err = tx.QueryRowContext(ctx, `INSERT INTO session_messages
			(session_id,seq,local_id,message_role,content) VALUES ($1,$2,$3,'agent',$4)
			RETURNING id`, sessionID, sequence, localID, content).Scan(&messageID)
		if err != nil {
			log.Fatal(err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO client_updates(session_id,update_type,
			entity_type,entity_id,entity_seq,entity_version,payload,durable)
			VALUES ($1,'message.created','message',$2,$3,$3,$4,true)`, sessionID,
			messageID.String(), sequence, content)
		if err != nil {
			log.Fatal(err)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE development_sessions SET last_message_seq=$2,
		last_activity_at=now(),updated_at=now() WHERE id=$1`, sessionID, sequence)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		log.Fatal(err)
	}
	writeJSON(map[string]any{"sessionId": sessionID, "messages": 125})
}

func forceCursorReset(ctx context.Context, db *sql.DB) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE client_updates
		SET created_at=now()-interval '31 days'`); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO client_updates
			(update_type,entity_type,entity_id,entity_version,payload,created_at)
			VALUES ('preference.updated','preference','e2e-expired',1,'{}',
				now()-interval '31 days'),
			('preference.updated','preference','e2e-current',2,'{}',now())`)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		log.Fatal(err)
	}
	writeJSON(map[string]any{"resetRequired": true})
}

func writeJSON(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}
