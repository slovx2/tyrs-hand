package worker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/queue"
	"go.uber.org/zap"
)

func (p *Processor) loadContext(ctx context.Context, job domain.Job) (jobContext, error) {
	var result jobContext
	err := p.db.QueryRowContext(ctx, `
		SELECT r.owner, r.name, r.clone_url, r.default_branch,
			w.kind, w.external_number, COALESCE(w.head_sha, ''), w.context_version,
			p.name, COALESCE(p.model, ''), COALESCE(p.reasoning_effort, ''),
			COALESCE(p.service_tier, ''), p.sandbox, p.approval_policy, p.network_enabled
		FROM repositories r
		JOIN work_items w ON w.repository_id = r.id
		JOIN agent_profiles p ON p.id = $3
		WHERE r.id = $1 AND w.id = $2`, job.RepositoryID, job.WorkItemID, job.AgentProfileID).
		Scan(&result.Owner, &result.Repository, &result.CloneURL, &result.DefaultBranch,
			&result.Kind, &result.Number, &result.HeadSHA, &result.ContextVersion,
			&result.ProfileName, &result.Model, &result.ReasoningEffort, &result.ServiceTier,
			&result.Sandbox, &result.ApprovalPolicy, &result.NetworkEnabled)
	return result, err
}

func (p *Processor) ensureThread(ctx context.Context, runtime *codex.Runtime, job domain.Job, options ports.ThreadOptions, codexHome, providerSignature string) (uuid.UUID, string, error) {
	var dbID uuid.UUID
	var threadID string
	err := p.db.QueryRowContext(ctx, `
		SELECT id, external_thread_id FROM agent_threads
		WHERE work_item_id = $1 AND agent_profile_id = $2 AND context_version = (
			SELECT context_version FROM work_items WHERE id = $1
		) AND status = 'active' AND codex_home_key = $3 AND provider_signature = $4`,
		job.WorkItemID, job.AgentProfileID, codexHome, providerSignature).Scan(&dbID, &threadID)
	if err == nil {
		if resumeErr := runtime.ResumeThread(ctx, threadID, options); resumeErr == nil {
			return dbID, threadID, nil
		}
		_, _ = p.db.ExecContext(ctx, `UPDATE agent_threads SET status = 'stale' WHERE id = $1`, dbID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, "", err
	}
	summary, summaryErr := p.handoffSummary(ctx, job.WorkItemID)
	if summaryErr != nil {
		return uuid.Nil, "", summaryErr
	}
	if summary != "" {
		options.DeveloperInstructions += "\n\nHandoff summary from the previous thread:\n" + summary
	}
	threadID, err = runtime.StartThread(ctx, options)
	if err != nil {
		return uuid.Nil, "", err
	}
	err = p.db.QueryRowContext(ctx, `
		INSERT INTO agent_threads(work_item_id, agent_profile_id, provider, external_thread_id, context_version, codex_home_key, provider_signature)
		VALUES ($1, $2, 'codex', $3, (SELECT context_version FROM work_items WHERE id = $1), $4, $5)
		ON CONFLICT(work_item_id, agent_profile_id, context_version) DO UPDATE
		SET external_thread_id = EXCLUDED.external_thread_id, codex_home_key = EXCLUDED.codex_home_key,
			provider_signature = EXCLUDED.provider_signature, status = 'active', last_used_at = now()
		RETURNING id`, job.WorkItemID, job.AgentProfileID, threadID, codexHome, providerSignature).Scan(&dbID)
	return dbID, threadID, err
}

func (p *Processor) handoffSummary(ctx context.Context, workItemID uuid.UUID) (string, error) {
	var summary string
	err := p.db.QueryRowContext(ctx, `
		SELECT summary FROM work_item_memories
		WHERE work_item_id = $1 AND scope = 'work_item'
		ORDER BY version DESC LIMIT 1`, workItemID).Scan(&summary)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return summary, err
}

func (p *Processor) persistMemorySummary(ctx context.Context, workItemID, threadDBID uuid.UUID, externalThreadID string) error {
	var summary string
	err := p.db.QueryRowContext(ctx, `
		SELECT CASE
			WHEN event_type = 'item/completed' THEN payload->'item'->>'text'
			ELSE payload::text
		END
		FROM agent_events
		WHERE thread_id = $1 AND (
			event_type = 'turn/completed' OR (
				event_type = 'item/completed'
				AND payload->'item'->>'type' = 'agentMessage'
				AND payload->'item'->>'phase' = 'final_answer'
			)
		)
		ORDER BY CASE WHEN event_type = 'item/completed' THEN 0 ELSE 1 END, id DESC
		LIMIT 1`, threadDBID).Scan(&summary)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	const maxSummaryBytes = 32 * 1024
	raw := []byte(summary)
	if len(raw) > maxSummaryBytes {
		raw = raw[:maxSummaryBytes]
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO work_item_memories(work_item_id, scope, summary, source_thread_id, version)
		VALUES ($1, 'work_item', $2, $3,
			COALESCE((SELECT max(version) + 1 FROM work_item_memories WHERE work_item_id = $1 AND scope = 'work_item'), 1))`,
		workItemID, string(raw), externalThreadID)
	return err
}

func (p *Processor) waitTurn(ctx context.Context, runtime *codex.Runtime, events <-chan codex.Event, claimed *queue.ClaimedJob, threadDBID uuid.UUID, externalThreadID, turnID string) error {
	maxTimer := time.NewTimer(p.cfg.TurnMaxDuration)
	defer maxTimer.Stop()
	idleTimer := time.NewTimer(p.cfg.TurnIdleTimeout)
	defer idleTimer.Stop()
	steerTicker := time.NewTicker(2 * time.Second)
	defer steerTicker.Stop()
	turnStarted := false
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return errors.New("当前 Codex 事件流在 Turn 完成前关闭")
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(p.cfg.TurnIdleTimeout)
			_, _ = p.db.ExecContext(ctx, `INSERT INTO agent_events(thread_id, job_id, event_type, payload) VALUES ($1,$2,$3,$4)`, threadDBID, claimed.ID, event.Method, event.Params)
			if event.Method == "turn/started" && eventMatchesTurn(event.Params, externalThreadID, turnID) {
				turnStarted = true
			}
			if event.Method == "turn/completed" {
				matched, status := completedTurn(event.Params, externalThreadID, turnID)
				if matched {
					if status != "completed" {
						return fmt.Errorf("当前 Codex Turn 结束状态为 %s", status)
					}
					return nil
				}
			}
		case <-steerTicker.C:
			if turnStarted {
				if err := p.steerQueuedInstruction(ctx, runtime, claimed, externalThreadID, turnID); err != nil {
					p.logger.Warn("合并同一 Work Item 的新指令失败", zap.Error(err), zap.String("work_item_id", claimed.WorkItemID.String()))
				}
			}
		case <-idleTimer.C:
			return errors.New("当前 Codex Turn 长时间没有活动")
		case <-maxTimer.C:
			return errors.New("当前 Codex Turn 超过最大执行时间")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func completedTurn(raw json.RawMessage, threadID, turnID string) (bool, string) {
	var payload struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.ThreadID != threadID || payload.Turn.ID != turnID {
		return false, ""
	}
	return true, payload.Turn.Status
}

func eventMatchesTurn(raw json.RawMessage, threadID, turnID string) bool {
	var payload struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	return json.Unmarshal(raw, &payload) == nil && payload.ThreadID == threadID && payload.Turn.ID == turnID
}

func (p *Processor) steerQueuedInstruction(ctx context.Context, runtime *codex.Runtime, claimed *queue.ClaimedJob, threadID, turnID string) error {
	var jobID uuid.UUID
	var instruction string
	skills, err := json.Marshal(claimed.Skills)
	if err != nil {
		return err
	}
	allowedTools, err := json.Marshal(claimed.AllowedTools)
	if err != nil {
		return err
	}
	dangerousActions, err := json.Marshal(claimed.DangerousActions)
	if err != nil {
		return err
	}
	err = p.db.QueryRowContext(ctx, `
		SELECT id, instruction FROM job_intents
		WHERE work_item_id = $1 AND agent_profile_id = $2 AND status = 'queued' AND available_at <= now()
		  AND actor_login = $3 AND actor_permission = $4
		  AND skills = $5::jsonb AND allowed_tools = $6::jsonb AND dangerous_actions = $7::jsonb
		ORDER BY created_at LIMIT 1`, claimed.WorkItemID, claimed.AgentProfileID,
		claimed.ActorLogin, claimed.ActorPermission, string(skills), string(allowedTools), string(dangerousActions)).Scan(&jobID, &instruction)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := runtime.SteerTurn(ctx, threadID, turnID, instruction); err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
		UPDATE job_intents SET status = 'canceled', last_error = $2, updated_at = now()
		WHERE id = $1 AND status = 'queued'`, jobID, "steered into active turn "+claimed.ID.String())
	return err
}

func resolveSkills(worktree string, names []string) ([]ports.SkillRef, error) {
	result := make([]ports.SkillRef, 0, len(names))
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, `/\\`) {
			return nil, fmt.Errorf("仓库 Skill 名称 %q 无效", name)
		}
		path := filepath.Join(worktree, ".agents", "skills", name, "SKILL.md")
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(absolute); err != nil {
			return nil, fmt.Errorf("仓库 Skill %s 不存在: %w", name, err)
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		result = append(result, ports.SkillRef{Name: name, Path: absolute})
	}
	return result, nil
}

func localGitSpec() ports.DynamicToolSpec {
	return ports.DynamicToolSpec{
		Type: "namespace", Name: "git", Description: "Inspect and publish the current managed worktree.",
		Tools: []ports.DynamicToolSpec{
			{Type: "function", Name: "status", Description: "Read the current worktree status.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
			{Type: "function", Name: "commit", Description: "Stage all current worktree changes and create a commit.", InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","minLength":1,"maxLength":200}},"required":["message"],"additionalProperties":false}`)},
			{Type: "function", Name: "publish_branch", Description: "Push the current HEAD to its managed GitHub branch.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		},
	}
}

func threadConfigSignature(providerSignature string, options ports.ThreadOptions) string {
	data, _ := json.Marshal(struct {
		ProviderSignature string              `json:"providerSignature"`
		Options           ports.ThreadOptions `json:"options"`
	}{ProviderSignature: providerSignature, Options: options})
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

func shortID(id uuid.UUID) string { return strings.ReplaceAll(id.String()[:8], "-", "") }

func (p *Processor) recordWorkspace(ctx context.Context, claimed *queue.ClaimedJob, workspace ports.Workspace, baseRef string) error {
	var cacheID uuid.UUID
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO repo_caches(repository_id, path, last_fetch_at)
		VALUES ($1, $2, now())
		ON CONFLICT(repository_id) DO UPDATE SET path = EXCLUDED.path, status = 'ready',
			last_fetch_at = now(), last_used_at = now(), error = NULL
		RETURNING id`, claimed.RepositoryID, workspace.CachePath).Scan(&cacheID)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO worktrees(work_item_id, repo_cache_id, path, branch, base_sha, head_sha)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(work_item_id) DO UPDATE SET repo_cache_id = EXCLUDED.repo_cache_id,
			path = EXCLUDED.path, branch = EXCLUDED.branch, head_sha = EXCLUDED.head_sha,
			status = 'ready', last_used_at = now(), expires_at = NULL, error = NULL`,
		claimed.WorkItemID, cacheID, workspace.WorktreePath, workspace.Branch, baseRef, workspace.HeadSHA)
	return err
}

func (p *Processor) refreshWorkspaceState(parent context.Context, workItemID uuid.UUID, path string) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	status, err := p.workspace.Status(ctx, path)
	if err != nil {
		_, _ = p.db.ExecContext(ctx, `UPDATE worktrees SET status = 'failed', error = $2, last_used_at = now() WHERE work_item_id = $1`, workItemID, err.Error())
		return
	}
	dirty := false
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if line != "" && !strings.HasPrefix(line, "##") {
			dirty = true
			break
		}
	}
	_, _ = p.db.ExecContext(ctx, `UPDATE worktrees SET dirty = $2, status = 'ready', error = NULL, last_used_at = now() WHERE work_item_id = $1`, workItemID, dirty)
}
