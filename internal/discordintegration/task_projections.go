package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type taskProjection struct {
	WorkItemID       string
	ForumDBID        string
	ForumDiscordID   string
	Kind             string
	Number           int
	Title            string
	Owner            string
	Repository       string
	WorkItemState    string
	JobStatus        string
	ClosedAt         sql.NullTime
	ThreadID         string
	StarterMessageID string
	LastState        string
	Archived         bool
}

func (d *Daemon) refreshTaskProjections(ctx context.Context, guildID string, remote Remote) error {
	guild, err := remote.Guild(ctx, guildID)
	if err != nil {
		return err
	}
	tags := make(map[string]map[string]string)
	for _, channel := range guild.Channels {
		if len(channel.Tags) > 0 {
			tags[channel.ID] = channel.Tags
		}
	}
	rows, err := d.manager.db.QueryContext(ctx, `SELECT w.id::text, f.id::text, dr.discord_id,
		w.kind, w.external_number, w.title, repo.owner, repo.name, w.state, COALESCE(j.status, ''), w.closed_at,
		COALESCE(p.thread_id, ''), COALESCE(p.starter_message_id, ''), COALESCE(p.last_state, ''),
		COALESCE(p.archived, false)
		FROM work_items w JOIN discord_forums f ON f.repository_id = w.repository_id AND f.forum_type = 'repository'
		JOIN discord_resources dr ON dr.id = f.resource_id
		JOIN repositories repo ON repo.id = w.repository_id
		LEFT JOIN LATERAL (SELECT status FROM job_intents WHERE work_item_id = w.id
			ORDER BY created_at DESC LIMIT 1) j ON true
		LEFT JOIN discord_task_posts p ON p.work_item_id = w.id
		WHERE f.guild_id = $1 AND (w.state <> 'closed' OR w.updated_at > now() - interval '30 days')
		ORDER BY w.updated_at, w.id`, guildID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var task taskProjection
		if err := rows.Scan(&task.WorkItemID, &task.ForumDBID, &task.ForumDiscordID,
			&task.Kind, &task.Number, &task.Title, &task.Owner, &task.Repository,
			&task.WorkItemState, &task.JobStatus, &task.ClosedAt,
			&task.ThreadID, &task.StarterMessageID, &task.LastState, &task.Archived); err != nil {
			return err
		}
		if err := d.projectTask(ctx, task, tags[task.ForumDiscordID]); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (d *Daemon) projectTask(ctx context.Context, task taskProjection, tags map[string]string) error {
	state := projectedTaskState(task.WorkItemState, task.JobStatus)
	card := taskCard(task, state)
	outbox := NewSQLoutbox(d.manager.db)
	if task.ThreadID == "" {
		payload := map[string]any{
			"channelId": task.ForumDiscordID, "threadName": taskThreadName(task), "content": "",
			"embeds": []EmbedPayload{card},
			"tagIds": taskTagIDs(tags, state), "workItemId": task.WorkItemID,
			"forumId": task.ForumDBID, "state": state,
		}
		return outbox.Enqueue(ctx, "task-post:"+task.WorkItemID, "forum.post.create",
			"channels/"+task.ForumDiscordID+"/threads", payload, "task-post-"+task.WorkItemID)
	}
	cardPayload := map[string]any{"channelId": task.ThreadID, "messageId": task.StarterMessageID,
		"content": "", "embeds": []EmbedPayload{card}, "workItemId": task.WorkItemID, "state": state}
	if err := outbox.Enqueue(ctx, "task-card:"+task.WorkItemID, "message.update",
		"channels/"+task.ThreadID+"/messages/"+task.StarterMessageID, cardPayload, ""); err != nil {
		return err
	}
	if task.LastState != "" && task.LastState != state {
		logPayload := map[string]any{"channelId": task.ThreadID, "content": "",
			"embeds":     []EmbedPayload{taskStateChangeCard(task.LastState, state)},
			"workItemId": task.WorkItemID, "state": state}
		if err := outbox.Enqueue(ctx, "task-log:"+task.WorkItemID+":"+state, "message.create",
			"channels/"+task.ThreadID+"/messages", logPayload, "task-log-"+task.WorkItemID+"-"+state); err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, "task-tags:"+task.WorkItemID, "thread.tags",
			"channels/"+task.ThreadID, map[string]any{"channelId": task.ThreadID, "tagIds": taskTagIDs(tags, state)}, ""); err != nil {
			return err
		}
	}
	shouldArchive := task.WorkItemState == "closed" && task.ClosedAt.Valid && time.Since(task.ClosedAt.Time) >= 7*24*time.Hour
	if shouldArchive != task.Archived {
		return outbox.Enqueue(ctx, "task-archive:"+task.WorkItemID, "thread.archive",
			"channels/"+task.ThreadID, map[string]any{"channelId": task.ThreadID,
				"workItemId": task.WorkItemID, "archived": shouldArchive}, "")
	}
	return nil
}

func projectedTaskState(workItemState, jobStatus string) string {
	switch jobStatus {
	case "running", "queued":
		return "Running"
	case "blocked":
		return "Needs Attention"
	case "failed":
		return "Failed"
	case "succeeded":
		return "Completed"
	}
	if workItemState == "closed" {
		return "Completed"
	}
	return "Open"
}

func taskTagIDs(tags map[string]string, state string) []string {
	if id := tags[state]; id != "" && id != "0" {
		return []string{id}
	}
	return nil
}

func taskThreadName(task taskProjection) string {
	value := fmt.Sprintf("#%d %s", task.Number, task.Title)
	runes := []rune(value)
	if len(runes) > 100 {
		value = string(runes[:100])
	}
	return value
}

func (d *Daemon) refreshTodoProjections(ctx context.Context, guildID string) error {
	rows, err := d.manager.db.QueryContext(ctx, `SELECT f.owner_discord_user_id, r.discord_id,
		COALESCE(p.resource_id, r.discord_id), COALESCE(p.message_id, '')
		FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		LEFT JOIN discord_projections p ON p.guild_id = f.guild_id
			AND p.projection_key = 'todo:' || f.owner_discord_user_id
		WHERE f.guild_id = $1 AND f.forum_type = 'personal' ORDER BY f.owner_discord_user_id`, guildID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var userID, forumID, resourceID, messageID string
		if err := rows.Scan(&userID, &forumID, &resourceID, &messageID); err != nil {
			return err
		}
		card, err := d.todoCard(ctx, guildID, userID)
		if err != nil {
			return err
		}
		if err := d.upsertForumProjection(ctx, guildID, "todo:"+userID, forumID,
			resourceID, messageID, "待我处理", card); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (d *Daemon) todoCard(ctx context.Context, guildID, userID string) (EmbedPayload, error) {
	var login string
	err := d.manager.db.QueryRowContext(ctx, `SELECT github_login FROM discord_identity_bindings
		WHERE guild_id = $1 AND discord_user_id = $2 AND status = 'active'`, guildID, userID).Scan(&login)
	if errors.Is(err, sql.ErrNoRows) {
		return EmbedPayload{Title: "🟡 待我处理", Description: "尚未绑定 GitHub，暂时无法关联你的任务。",
			Color: cardColorYellow, Footer: "使用 /github bind 绑定身份"}, nil
	}
	if err != nil {
		return EmbedPayload{}, err
	}
	rows, err := d.manager.db.QueryContext(ctx, `SELECT DISTINCT r.owner, r.name, w.kind, w.external_number,
		w.title, j.status FROM job_intents j JOIN work_items w ON w.id = j.work_item_id
		JOIN repositories r ON r.id = w.repository_id
		WHERE lower(j.actor_login) = lower($1) AND j.status IN ('blocked', 'failed')
		ORDER BY r.owner, r.name, w.external_number LIMIT 25`, login)
	if err != nil {
		return EmbedPayload{}, err
	}
	defer func() { _ = rows.Close() }()
	lines := make([]string, 0, 25)
	for rows.Next() {
		var owner, repo, kind, title, status string
		var number int
		if err := rows.Scan(&owner, &repo, &kind, &number, &title, &status); err != nil {
			return EmbedPayload{}, err
		}
		lines = append(lines, fmt.Sprintf("• `%s` **%s/%s** %s #%d · %s", cardText(status, 30),
			cardText(owner, 100), cardText(repo, 100), cardText(kind, 30), number, cardText(title, 200)))
	}
	if err := rows.Err(); err != nil {
		return EmbedPayload{}, err
	}
	if len(lines) == 0 {
		return EmbedPayload{Title: "✅ 待我处理", Description: "当前没有需要处理的任务。",
			Color: cardColorGreen, Footer: "每分钟自动更新", Timestamp: time.Now().UTC().Format(time.RFC3339)}, nil
	}
	return EmbedPayload{Title: fmt.Sprintf("🟡 待我处理 · %d 项", len(lines)), Description: strings.Join(lines, "\n"),
		Color: cardColorYellow, Footer: "每分钟自动更新", Timestamp: time.Now().UTC().Format(time.RFC3339)}, nil
}

func (d *Daemon) upsertForumProjection(ctx context.Context, guildID, key, forumID, resourceID, messageID, title string,
	card EmbedPayload,
) error {
	payload := map[string]any{"content": "", "embeds": []EmbedPayload{card}}
	_, err := d.manager.db.ExecContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, desired_payload) VALUES ($1, $2, $3, $4)
		ON CONFLICT(guild_id, projection_key) DO UPDATE SET desired_payload = EXCLUDED.desired_payload,
			desired_version = discord_projections.desired_version + 1, updated_at = now()`,
		guildID, key, resourceID, mustJSON(payload))
	if err != nil {
		return err
	}
	outbox := NewSQLoutbox(d.manager.db)
	if messageID == "" {
		return outbox.Enqueue(ctx, "projection:"+key, "forum.post.create",
			"channels/"+forumID+"/threads", map[string]any{"channelId": forumID,
				"threadName": title, "content": "", "embeds": []EmbedPayload{card}}, "projection-"+key)
	}
	return outbox.Enqueue(ctx, "projection:"+key, "message.update",
		"channels/"+resourceID+"/messages/"+messageID, map[string]any{
			"channelId": resourceID, "messageId": messageID, "content": "", "embeds": []EmbedPayload{card}}, "")
}
