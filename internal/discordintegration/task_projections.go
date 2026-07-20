package discordintegration

import (
	"context"
	"database/sql"
	"fmt"
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
		LEFT JOIN LATERAL (SELECT status FROM codex_turn_intents WHERE work_item_id = w.id
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
	case "queued", "dispatching", "awaiting_confirmation", "running", "reconciling", "retry_wait":
		return "Running"
	case "failed":
		return "Failed"
	case "completed":
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
