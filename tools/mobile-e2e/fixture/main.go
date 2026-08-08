package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type seededFixture struct {
	WorkspaceID uuid.UUID `json:"workspaceId"`
	ProjectID   uuid.UUID `json:"projectId"`
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("用法：fixture seed|seed-project-matrix|wait-ready|snapshot|notification-target|assert-message-once|assert-session-project|assert-attachment-once|assert-turn-status")
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
	case "assert-turn-status":
		assertTurnStatus(ctx, db, os.Args[2:])
	default:
		log.Fatalf("未知命令 %q", os.Args[1])
	}
}

func seed(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("seed", flag.ExitOnError)
	workerText := flags.String("worker-id", "", "Worker UUID")
	projectName := flags.String("project-name", "e2e-project", "首个项目名称")
	_ = flags.Parse(arguments)
	workerID, err := uuid.Parse(*workerText)
	if err != nil {
		log.Fatal("seed 需要 --worker-id")
	}
	workspaceID := uuid.New()
	projectID := uuid.New()
	fixture := seededFixture{WorkspaceID: workspaceID, ProjectID: projectID}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO discord_guilds(guild_id,enabled)
			VALUES ('999000000000000001',true) ON CONFLICT(guild_id) DO NOTHING`, nil},
		{`INSERT INTO discord_members(guild_id,discord_user_id,username,display_name,active)
			VALUES ('999000000000000001','999000000000000002','mobile-e2e','Mobile E2E',true)
			ON CONFLICT(guild_id,discord_user_id) DO UPDATE SET active=true`, nil},
		{`INSERT INTO worker_workspaces(id,guild_id,owner_discord_user_id,worker_id)
			VALUES ($1,'999000000000000001','999000000000000002',$2)`,
			[]any{workspaceID, workerID}},
		{`INSERT INTO workspace_projects(id,workspace_id,relative_path,name,project_kind,
			availability_status,last_seen_at) VALUES ($1,$2,'workspaces/e2e-project',$3,
			'directory','available',now())`, []any{projectID, workspaceID, *projectName}},
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
	workspaceText := flags.String("workspace-id", "", "Workspace UUID")
	primaryName := flags.String("primary-name", "alpha-primary", "首个项目名称")
	secondaryName := flags.String("secondary-name", "zeta-secondary", "第二项目名称")
	_ = flags.Parse(arguments)
	workspaceID, err := uuid.Parse(*workspaceText)
	if err != nil {
		log.Fatal("seed-project-matrix 需要 --workspace-id")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE workspace_projects SET name=$2,updated_at=now()
		WHERE workspace_id=$1 AND relative_path='workspaces/e2e-project'`, workspaceID, *primaryName)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO workspace_projects(workspace_id,relative_path,
			name,project_kind,availability_status,last_seen_at) VALUES
			($1,'workspaces/e2e-secondary',$2,'directory','available',now())
			ON CONFLICT(workspace_id,relative_path) DO UPDATE SET name=EXCLUDED.name,
			availability_status='available',last_seen_at=now(),updated_at=now()`, workspaceID, *secondaryName)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		log.Fatal(err)
	}
	writeJSON(map[string]any{"workspaceId": workspaceID, "projects": []string{*primaryName, *secondaryName}})
}

func waitReady(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("wait-ready", flag.ExitOnError)
	workspaceText := flags.String("workspace-id", "", "Workspace UUID")
	_ = flags.Parse(arguments)
	workspaceID, err := uuid.Parse(*workspaceText)
	if err != nil {
		log.Fatal("wait-ready 需要 --workspace-id")
	}
	for ctx.Err() == nil {
		var projectID sql.NullString
		err = db.QueryRowContext(ctx, `SELECT project.id::text
			FROM workspace_projects project WHERE project.workspace_id=$1
			AND project.relative_path='workspaces/e2e-project'
			AND project.availability_status='available' LIMIT 1`, workspaceID).Scan(&projectID)
		if err == nil && projectID.Valid {
			writeJSON(map[string]any{"workspaceId": workspaceID, "projectId": projectID.String})
			return
		}
		time.Sleep(time.Second)
	}
	log.Fatal("等待Workspace与 e2e-project 就绪超时")
}

func snapshot(ctx context.Context, db *sql.DB) {
	result := map[string]int64{}
	queries := map[string]string{
		"threadBindings":       `SELECT count(*) FROM official_thread_bindings`,
		"threadProjections":    `SELECT count(*) FROM official_thread_projections`,
		"turnSubmissions":      `SELECT count(*) FROM official_turn_submissions`,
		"threadActions":        `SELECT count(*) FROM official_thread_actions`,
		"serverRequests":       `SELECT count(*) FROM official_server_requests`,
		"resolvedRequests":     `SELECT count(*) FROM official_server_requests WHERE status='resolved'`,
		"materializations":     `SELECT count(*) FROM client_materializations`,
		"completedAttachments": `SELECT count(*) FROM client_materializations WHERE status='completed'`,
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
	query := `SELECT binding.thread_id FROM official_thread_bindings binding
		JOIN official_thread_projections projection ON projection.workspace_id=binding.workspace_id
			AND projection.thread_id=binding.thread_id
		WHERE ($1='' OR EXISTS (
			SELECT 1 FROM jsonb_array_elements(COALESCE(projection.thread->'turns','[]')) turn,
			LATERAL jsonb_array_elements(COALESCE(turn->'items','[]')) item,
			LATERAL jsonb_array_elements(COALESCE(item->'content','[]')) input
			WHERE item->>'type'='userMessage' AND input->>'type'='text' AND input->>'text'=$1))
		ORDER BY projection.observed_at DESC,binding.id DESC LIMIT 1`
	err = db.QueryRowContext(ctx, query, *text).Scan(&sessionID)
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
	query := `SELECT count(*) FROM official_thread_projections projection,
		LATERAL jsonb_array_elements(COALESCE(projection.thread->'turns','[]')) turn,
		LATERAL jsonb_array_elements(COALESCE(turn->'items','[]')) item
		WHERE item->>'type'='userMessage' AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(COALESCE(item->'content','[]')) input
			WHERE input->>'type'='text' AND input->>'text'=$1)`
	count, err := waitForCount(ctx, db, query, 1, *text)
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
	query := `SELECT count(DISTINCT projection.thread_id)
		FROM official_thread_projections projection
		JOIN official_thread_bindings binding ON binding.workspace_id=projection.workspace_id
			AND binding.thread_id=projection.thread_id
		JOIN workspace_projects project ON project.id=binding.workspace_project_id
		WHERE project.name=$2 AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(COALESCE(projection.thread->'turns','[]')) turn,
			LATERAL jsonb_array_elements(COALESCE(turn->'items','[]')) item,
			LATERAL jsonb_array_elements(COALESCE(item->'content','[]')) input
			WHERE item->>'type'='userMessage' AND input->>'type'='text' AND input->>'text'=$1)`
	count, err := waitForCount(ctx, db, query, 1, *text, *projectName)
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
	query := `WITH matched AS (
		SELECT item FROM official_thread_projections projection,
		LATERAL jsonb_array_elements(COALESCE(projection.thread->'turns','[]')) turn,
		LATERAL jsonb_array_elements(COALESCE(turn->'items','[]')) item
		WHERE item->>'type'='userMessage' AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(COALESCE(item->'content','[]')) input
			WHERE input->>'type'='text' AND input->>'text'=$1))
		SELECT count(*),COALESCE(sum((SELECT count(*) FROM
			jsonb_array_elements(COALESCE(item->'content','[]')) input
			WHERE input->>'type' IN ('localImage','mention'))),0) FROM matched`
	var messages, attachments int
	deadline := time.NewTicker(150 * time.Millisecond)
	defer deadline.Stop()
	for ctx.Err() == nil {
		err := db.QueryRowContext(ctx, query, *text).Scan(&messages, &attachments)
		if err != nil {
			log.Fatal(err)
		}
		if messages == 1 && attachments > 0 {
			writeJSON(map[string]any{"text": *text, "messages": messages,
				"attachments": attachments})
			return
		}
		<-deadline.C
	}
	log.Fatalf("消息 %q 的官方投影消息=%d、附件=%d，预期一条消息且至少一个附件",
		*text, messages, attachments)
}

func assertTurnStatus(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("assert-turn-status", flag.ExitOnError)
	text := flags.String("text", "", "用于定位 Turn 的用户消息")
	status := flags.String("status", "", "预期官方 Turn 状态")
	_ = flags.Parse(arguments)
	if *text == "" || *status == "" {
		log.Fatal("assert-turn-status 需要 --text 与 --status")
	}
	query := `SELECT count(*) FROM official_thread_projections projection,
		LATERAL jsonb_array_elements(COALESCE(projection.thread->'turns','[]')) turn
		WHERE turn->>'status'=$2 AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(COALESCE(turn->'items','[]')) item,
			LATERAL jsonb_array_elements(COALESCE(item->'content','[]')) input
			WHERE item->>'type'='userMessage' AND input->>'type'='text' AND input->>'text'=$1)`
	count, err := waitForCount(ctx, db, query, 1, *text, *status)
	if err != nil {
		log.Fatal(err)
	}
	if count != 1 {
		log.Fatalf("消息 %q 对应状态 %q 的官方 Turn 数量为 %d，预期 1", *text, *status, count)
	}
	writeJSON(map[string]any{"text": *text, "status": *status, "count": count})
}

func waitForCount(ctx context.Context, db *sql.DB, query string, expected int,
	arguments ...any,
) (int, error) {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	last := 0
	for {
		if err := db.QueryRowContext(ctx, query, arguments...).Scan(&last); err != nil {
			return 0, err
		}
		if last == expected {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-ticker.C:
		}
	}
}

func writeJSON(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}
