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
		log.Fatal("用法：fixture seed|wait-ready|snapshot|assert-message-once|seed-history|force-cursor-reset")
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
	case "wait-ready":
		waitReady(ctx, db, os.Args[2:])
	case "snapshot":
		snapshot(ctx, db)
	case "assert-message-once":
		assertMessageOnce(ctx, db, os.Args[2:])
	case "seed-history":
		seedHistory(ctx, db)
	case "force-cursor-reset":
		forceCursorReset(ctx, db)
	default:
		log.Fatalf("未知命令 %q", os.Args[1])
	}
}

func seed(ctx context.Context, db *sql.DB, arguments []string) {
	flags := flag.NewFlagSet("seed", flag.ExitOnError)
	workerText := flags.String("worker-id", "", "Worker UUID")
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
			availability_status,last_seen_at) VALUES ($1,$2,'workspaces/e2e-project','e2e-project',
			'directory','available',now())`, []any{projectID, workspaceID}},
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
		"sessions":      `SELECT count(*) FROM workspace_sessions`,
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

func seedHistory(ctx context.Context, db *sql.DB) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID uuid.UUID
	var sequence int64
	err = tx.QueryRowContext(ctx, `SELECT id,last_message_seq FROM workspace_sessions
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
	_, err = tx.ExecContext(ctx, `UPDATE workspace_sessions SET last_message_seq=$2,
		last_activity_at=now(),updated_at=now() WHERE id=$1`, sessionID, sequence)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		log.Fatal(err)
	}
	writeJSON(map[string]any{"sessionId": sessionID, "messages": 180})
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
