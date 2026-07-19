package worker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/devenv"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"go.uber.org/zap"
)

func (p *Processor) prepareWorkItemEnvironment(ctx context.Context, workItemID uuid.UUID, path string) devenv.Result {
	result := p.prepareEnvironment(ctx, path)
	p.persistEnvironment(ctx, `UPDATE worktrees SET environment_status = $2,
		runtime_fingerprint = NULLIF($3, ''), dependency_fingerprint = NULLIF($4, ''),
		environment_diagnostics = $5, environment_projects = $6, environment_prepared_at = $7
		WHERE work_item_id = $1`, workItemID, result)
	return result
}

func (p *Processor) prepareDiscordEnvironment(ctx context.Context, path string) devenv.Result {
	result := p.prepareEnvironment(ctx, path)
	p.persistEnvironment(ctx, `UPDATE discord_workspaces SET environment_status = $2,
		runtime_fingerprint = NULLIF($3, ''), dependency_fingerprint = NULLIF($4, ''),
		environment_diagnostics = $5, environment_projects = $6,
		environment_prepared_at = $7, updated_at = now()
		WHERE path = $1`, path, result)
	return result
}

func (p *Processor) prepareEnvironment(ctx context.Context, path string) devenv.Result {
	if p.environment == nil {
		now := time.Now().UTC()
		return devenv.Result{Status: "ready", PreparedAt: &now}
	}
	prepareCtx, cancel := context.WithTimeout(ctx, p.cfg.EnvironmentPrepareWaitTimeout)
	defer cancel()
	return p.environment.Prepare(prepareCtx, path)
}

func (p *Processor) persistEnvironment(ctx context.Context, query string, key any, result devenv.Result) {
	diagnostics, _ := json.Marshal(result.Diagnostics)
	projects, _ := json.Marshal(result.Projects)
	if _, err := p.db.ExecContext(ctx, query, key, result.Status, result.RuntimeFingerprint,
		result.DependencyFingerprint, diagnostics, projects, result.PreparedAt); err != nil {
		p.logger.Warn("保存 Workspace 环境状态失败", zap.Error(err))
		return
	}
	if p.redis != nil {
		payload, _ := json.Marshal(map[string]any{
			"type": "workspace.environment." + result.Status, "workspace": key,
		})
		if err := p.redis.Publish(ctx, "tyrs-hand:events", payload).Err(); err != nil {
			p.logger.Warn("发布 Workspace 环境状态失败", zap.Error(err))
		}
	}
}

func environmentAdditionalContext(result devenv.Result) map[string]ports.AdditionalContextEntry {
	if result.Status != "degraded" {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"status": "degraded", "diagnostics": result.Diagnostics,
		"instruction": "开发环境未完全准备。只有当前任务需要运行或调试项目时才尝试修复；否则继续完成任务。",
	})
	return map[string]ports.AdditionalContextEntry{
		"tyrs_hand_development_environment": {Kind: "application", Value: string(payload)},
	}
}

func mergeAdditionalContext(target map[string]ports.AdditionalContextEntry, extra map[string]ports.AdditionalContextEntry) map[string]ports.AdditionalContextEntry {
	if len(extra) == 0 {
		return target
	}
	if target == nil {
		target = make(map[string]ports.AdditionalContextEntry, len(extra))
	}
	for key, value := range extra {
		target[key] = value
	}
	return target
}
