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
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/replygate"
	"github.com/slovx2/tyrs-hand/internal/settings"
	"go.uber.org/zap"
)

var errDiscordTurnStopped = errors.New("当前 Discord Codex Turn 已被停止")

const (
	turnCleanupTimeout        = 5 * time.Second
	workerCodexSandbox        = "danger-full-access"
	workerCodexApprovalPolicy = "never"
)

// workerThreadOptions 在平台边界固定无人值守 Codex 的命令权限，避免 Profile 或任务快照覆盖。
func workerThreadOptions(options ports.ThreadOptions) ports.ThreadOptions {
	options.Sandbox = workerCodexSandbox
	options.ApprovalPolicy = workerCodexApprovalPolicy
	return options
}

func needsCleanupInterrupt(err error) bool {
	return err != nil && !errors.Is(err, errDiscordTurnStopped)
}

func interruptTurnBestEffort(runtime *codex.Runtime, threadID, turnID string) {
	ctx, cancel := context.WithTimeout(context.Background(), turnCleanupTimeout)
	defer cancel()
	_ = runtime.InterruptTurn(ctx, threadID, turnID)
}

func discordStopRequested(ctx context.Context, db *sql.DB, intentID uuid.UUID, cause error) bool {
	if errors.Is(cause, errDiscordTurnStopped) {
		return true
	}
	if db == nil {
		return false
	}
	var status, code string
	err := db.QueryRowContext(ctx, `SELECT status, COALESCE(last_error_code, '')
		FROM codex_turn_intents WHERE id = $1`, intentID).Scan(&status, &code)
	return err == nil && status == "canceled" && code == "user_interrupt"
}

func (p *Processor) loadContext(ctx context.Context, intent codexcontrol.Intent) (jobContext, error) {
	var result jobContext
	err := p.db.QueryRowContext(ctx, `SELECT r.owner, r.name, r.clone_url, r.default_branch,
		w.kind, w.external_number, COALESCE(w.head_sha, ''), COALESCE(w.head_ref, ''),
		COALESCE(w.head_repository, ''), COALESCE(w.base_sha, ''), COALESCE(w.base_ref, ''),
		COALESCE(w.html_url, ''), w.context_version,
		p.name, COALESCE(p.model, ''), COALESCE(p.reasoning_effort, ''),
		COALESCE(p.service_tier, ''), p.sandbox, p.approval_policy, p.network_enabled
		FROM repositories r JOIN work_items w ON w.repository_id = r.id
		JOIN agent_profiles p ON p.id = $3 WHERE r.id = $1 AND w.id = $2`,
		intent.RepositoryID, intent.WorkItemID, intent.AgentProfileID).Scan(
		&result.Owner, &result.Repository, &result.CloneURL, &result.DefaultBranch,
		&result.Kind, &result.Number, &result.HeadSHA, &result.HeadRef,
		&result.HeadRepository, &result.BaseSHA, &result.BaseRef, &result.HTMLURL,
		&result.ContextVersion,
		&result.ProfileName, &result.Model, &result.ReasoningEffort, &result.ServiceTier,
		&result.Sandbox, &result.ApprovalPolicy, &result.NetworkEnabled)
	return result, err
}

func (p *Processor) freezeRuntimePreferences(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (codexsettings.EffectivePreferences, error) {
	var result codexsettings.EffectivePreferences
	var model, effort, tier sql.NullString
	var frozen sql.NullTime
	err := p.db.QueryRowContext(ctx, `SELECT model, reasoning_effort, service_tier,
		runtime_preferences_frozen_at FROM codex_thread_controls WHERE id = $1`, claimed.ControlID).
		Scan(&model, &effort, &tier, &frozen)
	if err != nil {
		return result, err
	}
	if frozen.Valid {
		result.Model, result.ReasoningEffort = model.String, effort.String
		result.ServiceTier = tier.String
		if result.ServiceTier == "" && claimed.InputSurface != "desktop" {
			result.ServiceTier = "standard"
		}
		return result, nil
	}
	if claimed.SourceType == codexcontrol.SourceDiscord {
		err = p.db.QueryRowContext(ctx, `SELECT COALESCE(model,''), COALESCE(reasoning_effort,''),
			COALESCE(service_tier,'standard')
			FROM discord_conversations WHERE id = $1`, claimed.DiscordConversationID).
			Scan(&result.Model, &result.ReasoningEffort, &result.ServiceTier)
	} else {
		result, err = codexsettings.NewService(p.db).Resolve(ctx, claimed.RepositoryID, uuid.Nil,
			claimed.AgentProfileID)
	}
	if err != nil {
		return codexsettings.EffectivePreferences{}, err
	}
	err = p.db.QueryRowContext(ctx, `UPDATE codex_thread_controls SET model = $2,
		reasoning_effort = $3, service_tier = $4, runtime_preferences_frozen_at = now(), updated_at = now()
		WHERE id = $1 AND runtime_preferences_frozen_at IS NULL
		RETURNING COALESCE(model,''), COALESCE(reasoning_effort,''), service_tier`, claimed.ControlID,
		nullIfEmpty(result.Model), nullIfEmpty(result.ReasoningEffort), result.ServiceTier).
		Scan(&result.Model, &result.ReasoningEffort, &result.ServiceTier)
	if errors.Is(err, sql.ErrNoRows) {
		return p.freezeRuntimePreferences(ctx, claimed)
	}
	return result, err
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func githubWorkItemAdditionalContext(job jobContext, workspace ports.Workspace) map[string]ports.AdditionalContextEntry {
	url := job.HTMLURL
	if url == "" {
		path := "issues"
		if job.Kind == "pull_request" {
			path = "pull"
		}
		url = fmt.Sprintf("https://github.com/%s/%s/%s/%d", job.Owner, job.Repository, path, job.Number)
	}
	payload := map[string]any{
		"provider": "github", "repository": job.Owner + "/" + job.Repository,
		"kind": job.Kind, "number": job.Number, "url": url,
		"workspace": map[string]any{"branch": workspace.Branch, "policy": "temporary_lightweight"},
	}
	if job.Kind == "pull_request" {
		payload["pullRequest"] = map[string]any{
			"sourceRepository": job.HeadRepository, "sourceBranch": job.HeadRef,
			"sourceSha": job.HeadSHA, "targetBranch": job.BaseRef,
			"targetSha": job.BaseSHA, "fetchedRef": fmt.Sprintf("refs/remotes/pull/%d", job.Number),
		}
	}
	encoded, _ := json.Marshal(payload)
	return map[string]ports.AdditionalContextEntry{
		"github_work_item": {Kind: "application", Value: string(encoded)},
	}
}

func (p *Processor) ensureThread(ctx context.Context, runtime *codex.Runtime,
	claimed *codexcontrol.ClaimedControl, options ports.ThreadOptions, codexHome, signature string,
) (string, error) {
	threadID := claimed.ExternalThreadID
	if threadID != "" {
		if claimed.CodexHomeKey != "" && claimed.CodexHomeKey != codexHome {
			return "", errors.New("持久化 Control 的 CODEX_HOME 与当前运行配置不一致")
		}
		if claimed.ProviderSignature != "" && claimed.ProviderSignature != signature {
			return "", errors.New("持久化 Control 的 Provider Signature 与当前运行配置不一致")
		}
		if err := runtime.ResumeThread(ctx, threadID, options); err != nil {
			return "", fmt.Errorf("恢复 Codex Thread: %w", err)
		}
		if !claimed.Recovering {
			snapshot, readErr := runtime.ReadThread(ctx, threadID)
			if readErr != nil {
				return "", fmt.Errorf("恢复后读取 Codex Thread: %w", readErr)
			}
			if active, exists := snapshot.ActiveTurn(); exists {
				return "", fmt.Errorf("codex thread 存在不属于当前 intent 的活动 turn %s", active.ID)
			}
		}
	} else {
		var err error
		threadID, err = runtime.StartThread(ctx, options)
		if err != nil {
			return "", err
		}
	}
	if err := p.controls.SetThread(ctx, claimed, threadID, codexHome, signature); err != nil {
		return "", err
	}
	claimed.ExternalThreadID = threadID
	claimed.CodexHomeKey = codexHome
	claimed.ProviderSignature = signature
	return threadID, nil
}

type turnEventObserver func(context.Context, codex.Event)

func (p *Processor) reconcileTurn(ctx context.Context, runtime *codex.Runtime,
	claimed *codexcontrol.ClaimedControl, threadID string, observer turnEventObserver,
) (codexcontrol.TurnResult, bool, error) {
	snapshot, err := runtime.ReadThread(ctx, threadID)
	if err != nil {
		return codexcontrol.TurnResult{}, false, fmt.Errorf("读取 Codex Thread 快照: %w", err)
	}
	if snapshot.StatusType() == "systemError" || snapshot.StatusType() == "notLoaded" {
		return codexcontrol.TurnResult{}, false, fmt.Errorf("codex thread 快照状态为 %s", snapshot.StatusType())
	}
	turn, found := snapshot.TurnByClientID(claimed.ID.String())
	if !found && claimed.ConfirmedTurnID != "" {
		turn, found = snapshot.TurnByID(claimed.ConfirmedTurnID)
	}
	if !found {
		if claimed.ConfirmedTurnID != "" {
			return codexcontrol.TurnResult{}, false, errors.New("已确认的 Codex Turn 在快照中消失，拒绝重放用户任务")
		}
		if claimed.Attempt >= 3 {
			return codexcontrol.TurnResult{}, false, errors.New("codex start 缺少远端证据且安全重发次数已经耗尽")
		}
		return codexcontrol.TurnResult{}, false, nil
	}
	if err := p.controls.ConfirmTurn(ctx, claimed, turn.ID); err != nil {
		return codexcontrol.TurnResult{}, false, err
	}
	if turn.Status == "completed" {
		return codexcontrol.TurnResult{FinalAnswer: turn.FinalAnswer(), TurnID: turn.ID,
			Evidence: "thread/read"}, true, nil
	}
	if !isActiveCodexTurnStatus(turn.Status) {
		return codexcontrol.TurnResult{}, false, fmt.Errorf("codex turn 快照终态为 %s", turn.Status)
	}
	result, err := p.waitTurn(ctx, runtime, runtime.Events(), claimed, threadID, turn.ID, observer)
	return result, true, err
}

func (p *Processor) waitTurn(ctx context.Context, runtime *codex.Runtime, events <-chan codex.Event,
	claimed *codexcontrol.ClaimedControl, threadID, turnID string, observer turnEventObserver,
) (codexcontrol.TurnResult, error) {
	startedAt := time.Now()
	maxTimer := time.NewTimer(p.cfg.TurnMaxDuration)
	defer maxTimer.Stop()
	idleTimer := time.NewTimer(p.cfg.TurnIdleTimeout)
	defer idleTimer.Stop()
	steerTicker := time.NewTicker(2 * time.Second)
	defer steerTicker.Stop()
	pollInterval := p.cfg.CodexStatusPollInterval
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	confirmed := claimed.ConfirmedTurnID != ""
	finalAnswer := ""
	var finalDelta strings.Builder
	for {
		select {
		case event, ok := <-events:
			if !ok {
				result, recovered, err := p.snapshotTerminal(ctx, runtime, claimed, threadID, turnID, startedAt)
				if recovered || err != nil {
					return result, err
				}
				return codexcontrol.TurnResult{}, errors.New("codex stdio 在 turn 完成前关闭")
			}
			if !eventBelongsToTurn(event.Params, threadID, turnID, claimed.ID.String()) {
				continue
			}
			resetTimer(idleTimer, p.cfg.TurnIdleTimeout)
			p.persistAgentEvent(ctx, claimed, event)
			if observer != nil {
				observer(ctx, event)
			}
			if event.Method == "turn/started" {
				actualID := eventTurnID(event.Params)
				if actualID != "" {
					turnID = actualID
					if err := p.controls.ConfirmTurn(ctx, claimed, actualID); err != nil {
						return codexcontrol.TurnResult{}, err
					}
					confirmed = true
				}
			}
			if value := finalAnswerFromEvent(event); value != "" {
				finalAnswer = value
			}
			if value := finalAnswerDelta(event); value != "" {
				finalDelta.WriteString(value)
			}
			if event.Method == "turn/completed" {
				_, status := completedTurn(event.Params, threadID, turnID)
				if status != "completed" {
					return codexcontrol.TurnResult{}, fmt.Errorf("codex turn 结束状态为 %s", status)
				}
				if finalAnswer == "" {
					finalAnswer = p.readFinalAnswer(ctx, runtime, threadID, turnID)
				}
				if finalAnswer == "" {
					finalAnswer = strings.TrimSpace(finalDelta.String())
				}
				return codexcontrol.TurnResult{FinalAnswer: finalAnswer, TurnID: turnID,
					DurationMillis: time.Since(startedAt).Milliseconds(), Evidence: "turn/completed"}, nil
			}
		case <-steerTicker.C:
			if confirmed && claimed.SourceType == codexcontrol.SourceDiscord {
				if err := p.dispatchPendingIntent(ctx, runtime, claimed, threadID, turnID); err != nil {
					return codexcontrol.TurnResult{}, fmt.Errorf("合并同一 control 的后续 intent: %w", err)
				}
			}
		case <-pollTicker.C:
			snapshot, readErr := runtime.ReadThread(ctx, threadID)
			if readErr != nil {
				continue
			}
			turn, found := snapshot.TurnByID(turnID)
			if !found {
				turn, found = snapshot.TurnByClientID(claimed.ID.String())
			}
			if found && turn.Status == "completed" {
				return codexcontrol.TurnResult{FinalAnswer: turn.FinalAnswer(), TurnID: turn.ID,
					DurationMillis: time.Since(startedAt).Milliseconds(), Evidence: "thread/read"}, nil
			}
			if found && (turn.Status == "failed" || turn.Status == "interrupted") {
				return codexcontrol.TurnResult{}, fmt.Errorf("codex turn 快照终态为 %s", turn.Status)
			}
		case <-idleTimer.C:
			return codexcontrol.TurnResult{}, errors.New("codex turn 长时间没有相关活动")
		case <-maxTimer.C:
			return codexcontrol.TurnResult{}, errors.New("codex turn 超过最大执行时间")
		case <-ctx.Done():
			return codexcontrol.TurnResult{}, ctx.Err()
		}
	}
}

func (p *Processor) readFinalAnswer(ctx context.Context, runtime *codex.Runtime, threadID, turnID string) string {
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, err := runtime.ReadThread(ctx, threadID)
		if err == nil {
			if turn, ok := snapshot.TurnByID(turnID); ok {
				if answer := turn.FinalAnswer(); answer != "" {
					return answer
				}
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return ""
}

func (p *Processor) snapshotTerminal(ctx context.Context, runtime *codex.Runtime,
	claimed *codexcontrol.ClaimedControl, threadID, turnID string, startedAt time.Time,
) (codexcontrol.TurnResult, bool, error) {
	snapshot, err := runtime.ReadThread(ctx, threadID)
	if err != nil {
		return codexcontrol.TurnResult{}, false, err
	}
	turn, found := snapshot.TurnByID(turnID)
	if !found {
		turn, found = snapshot.TurnByClientID(claimed.ID.String())
	}
	if !found || turn.Status != "completed" {
		return codexcontrol.TurnResult{}, false, nil
	}
	return codexcontrol.TurnResult{FinalAnswer: turn.FinalAnswer(), TurnID: turn.ID,
		DurationMillis: time.Since(startedAt).Milliseconds(), Evidence: "thread/read"}, true, nil
}

func (p *Processor) persistAgentEvent(ctx context.Context, claimed *codexcontrol.ClaimedControl, event codex.Event) {
	_, _ = p.db.ExecContext(ctx, `INSERT INTO agent_events
		(control_id, intent_id, run_id, event_type, payload) VALUES ($1,$2,$3,$4,$5)`,
		claimed.ControlID, claimed.ID, claimed.RunID, event.Method, event.Params)
}

func (p *Processor) dispatchPendingIntent(ctx context.Context, runtime *codex.Runtime,
	claimed *codexcontrol.ClaimedControl, threadID, turnID string,
) error {
	var intentID uuid.UUID
	var operation, instruction, messageID string
	err := p.db.QueryRowContext(ctx, `SELECT id, operation, instruction, COALESCE(discord_message_id,'')
		FROM codex_turn_intents WHERE control_id = $1 AND sequence_no > $2
		  AND status IN ('queued','retry_wait') AND available_at <= now()
		  AND (SELECT append_count < max_append_count FROM codex_turn_runs WHERE id = $3)
		ORDER BY sequence_no LIMIT 1`, claimed.ControlID, claimed.Sequence, claimed.RunID).Scan(
		&intentID, &operation, &instruction, &messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if operation == "interrupt" {
		_ = replygate.SetBypass(claimed.CodexHomeKey, threadID)
		if err := runtime.InterruptTurn(ctx, threadID, turnID); err != nil {
			return err
		}
		_, err = p.db.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'completed',
			resolved_action = 'interrupt', confirmed_codex_turn_id = $2, finished_at = now(), updated_at = now()
			WHERE id = $1 AND status IN ('queued','retry_wait')`, intentID, turnID)
		if err != nil {
			return err
		}
		return errDiscordTurnStopped
	}
	input := ports.TurnInput{Text: instruction, ClientUserMessageID: intentID.String()}
	if claimed.SourceType == codexcontrol.SourceDiscord && messageID != "" {
		jobCtx, loadErr := p.loadDiscordContext(ctx, codexcontrol.Intent{
			DiscordConversationID: claimed.DiscordConversationID, DiscordMessageID: messageID,
		})
		if loadErr != nil {
			return loadErr
		}
		containerRuntime, runtimeErr := p.development.Runtime(ctx, jobCtx.EnvironmentID,
			jobCtx.ForumID, jobCtx.ConversationID)
		if runtimeErr != nil {
			return runtimeErr
		}
		workspace := containerRuntime.Workspace
		if loadErr != nil {
			return loadErr
		}
		input, loadErr = p.discordTurnInput(ctx, jobCtx, workspace, nil)
		if loadErr != nil {
			return loadErr
		}
	}
	applied, err := p.steerAlreadyApplied(ctx, runtime, threadID, turnID, intentID.String())
	if err != nil {
		return err
	}
	if !applied {
		steerErr := runtime.SteerTurn(ctx, threadID, turnID, input)
		if steerErr != nil {
			var requestErr *codex.RequestError
			if !errors.As(steerErr, &requestErr) || requestErr.State != codex.RequestUnknown {
				return steerErr
			}
			for attempt := 0; attempt < 2 && !applied; attempt++ {
				if attempt > 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(100 * time.Millisecond):
					}
				}
				applied, err = p.steerAlreadyApplied(ctx, runtime, threadID, turnID, intentID.String())
				if err != nil {
					return fmt.Errorf("对账 steer 结果: %w", err)
				}
			}
			if !applied {
				return fmt.Errorf("steer 响应丢失且快照中没有应用证据: %w", steerErr)
			}
		}
	}
	return p.markSteerApplied(ctx, claimed, intentID, turnID, messageID)
}

func (p *Processor) steerAlreadyApplied(ctx context.Context, runtime *codex.Runtime,
	threadID, turnID, clientID string,
) (bool, error) {
	snapshot, err := runtime.ReadThread(ctx, threadID)
	if err != nil {
		return false, err
	}
	return steerSnapshotApplied(snapshot, turnID, clientID)
}

func steerSnapshotApplied(snapshot codex.ThreadSnapshot, turnID, clientID string) (bool, error) {
	turn, found := snapshot.TurnByClientID(clientID)
	if !found {
		return false, nil
	}
	if turn.ID != turnID {
		return false, fmt.Errorf("steer client ID 出现在其他 turn %s", turn.ID)
	}
	return true, nil
}

func (p *Processor) markSteerApplied(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	intentID uuid.UUID, turnID, messageID string,
) error {
	_, err := p.db.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'running',
		resolved_action = 'steer', confirmed_codex_turn_id = $2, confirmed_at = now(), updated_at = now()
		WHERE id = $1 AND status IN ('queued','retry_wait')`, intentID, turnID)
	if err == nil {
		_, err = p.db.ExecContext(ctx, `UPDATE codex_turn_runs SET append_count = append_count + 1
			WHERE id = $1 AND append_count < max_append_count`, claimed.RunID)
	}
	if err == nil && claimed.SourceType == codexcontrol.SourceDiscord {
		err = p.addDiscordContributor(ctx, claimed.RunID, claimed.DiscordConversationID, turnID, messageID)
	}
	return err
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

func isActiveCodexTurnStatus(status string) bool {
	return status == "inProgress" || status == "active" || status == "running"
}

func eventBelongsToTurn(raw json.RawMessage, threadID, turnID, clientID string) bool {
	var payload struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID                  string `json:"id"`
			ClientUserMessageID string `json:"clientUserMessageId"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.ThreadID != threadID {
		return false
	}
	eventTurn := payload.Turn.ID
	if eventTurn == "" {
		eventTurn = payload.TurnID
	}
	return eventTurn == turnID || (clientID != "" && payload.Turn.ClientUserMessageID == clientID)
}

func eventTurnID(raw json.RawMessage) string {
	var payload struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &payload)
	if payload.Turn.ID != "" {
		return payload.Turn.ID
	}
	return payload.TurnID
}

func finalAnswerFromEvent(event codex.Event) string {
	if event.Method != "item/completed" {
		return ""
	}
	var payload struct {
		Item struct {
			Type  string `json:"type"`
			Phase string `json:"phase"`
			Text  string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(event.Params, &payload) != nil || payload.Item.Type != "agentMessage" {
		return ""
	}
	if payload.Item.Phase == "final_answer" || payload.Item.Phase == "" {
		return strings.TrimSpace(payload.Item.Text)
	}
	return ""
}

func finalAnswerDelta(event codex.Event) string {
	if event.Method != "item/agentMessage/delta" && event.Method != "item/delta" {
		return ""
	}
	var payload struct {
		Delta string `json:"delta"`
		Text  string `json:"text"`
	}
	if json.Unmarshal(event.Params, &payload) != nil {
		return ""
	}
	if payload.Delta != "" {
		return payload.Delta
	}
	return payload.Text
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
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

func resolveContainerSkills(workspace string, names []string) ([]ports.SkillRef, error) {
	result := make([]ports.SkillRef, 0, len(names))
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("仓库 Skill 名称 %q 无效", name)
		}
		result = append(result, ports.SkillRef{Name: name,
			Path: filepath.ToSlash(filepath.Join(workspace, ".agents", "skills", name, "SKILL.md"))})
	}
	return result, nil
}

func localGitSpec() ports.DynamicToolSpec {
	return ports.DynamicToolSpec{
		Type: "namespace", Name: "git", Description: "Inspect and publish the current managed Git workspace.",
		Tools: []ports.DynamicToolSpec{
			{Type: "function", Name: "status", Description: "Read the current worktree status.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
			{Type: "function", Name: "commit", Description: "Stage all current worktree changes and create a commit.", InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","minLength":1,"maxLength":200}},"required":["message"],"additionalProperties":false}`)},
			{Type: "function", Name: "publish_branch", Description: "Push the current HEAD to its managed GitHub branch.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		},
	}
}

func githubReplySpec() ports.DynamicToolSpec {
	return ports.DynamicToolSpec{
		Type: "namespace", Name: "tyrs_hand", Description: "Send the required final reply through the platform.",
		Tools: []ports.DynamicToolSpec{{
			Type: "function", Name: "reply_to_github",
			Description: "Post the one final user-facing reply to the current authorized GitHub issue or pull request.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"body":{"type":"string","minLength":1,"maxLength":60000}},"required":["body"],"additionalProperties":false}`),
		}},
	}
}

func codexRuntimeConfig(environment []string, workerDataRoot string,
	capabilities ...config.Config,
) map[string]any {
	config := replygate.SessionConfig()
	if len(capabilities) > 0 {
		cfg := capabilities[0]
		if cfg.BrowserMCPURL != "" && environmentValue(environment,
			"TYRS_BROWSER_MCP_TOKEN") != "" {
			config["mcp_servers"] = map[string]any{"chrome": map[string]any{
				"url": cfg.BrowserMCPURL, "bearer_token_env_var": "TYRS_BROWSER_MCP_TOKEN",
				"startup_timeout_sec": 10.0, "tool_timeout_sec": 120.0,
				"required": false, "default_tools_approval_mode": "approve",
			}}
		}
	}
	values := make(map[string]any, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found && key != "" && key != "TYRS_BROWSER_MCP_TOKEN" &&
			key != "TYRS_HAND_MODEL_API_KEY" {
			values[key] = value
		}
	}
	config["shell_environment_policy"] = map[string]any{
		"inherit": "all",
		"set":     values,
	}
	hideModelAPIKey(config)
	if strings.TrimSpace(workerDataRoot) != "" {
		config["sandbox_workspace_write"] = map[string]any{
			"writable_roots": []string{
				filepath.Join(workerDataRoot, "caches"),
				filepath.Join(workerDataRoot, "state"),
			},
		}
	}
	return config
}

func applyModelProviderConfig(config map[string]any, modelSource, baseURL string) {
	if modelSource == settings.ModelSourceChatGPT {
		config["model_provider"] = "openai"
		return
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com/v1"
	}
	config["model_provider"] = "tyrs-hand-provider"
	providers, _ := config["model_providers"].(map[string]any)
	if providers == nil {
		providers = make(map[string]any)
	}
	providers["tyrs-hand-provider"] = map[string]any{
		"name": "Tyrs Hand Provider", "base_url": strings.TrimRight(baseURL, "/"),
		"wire_api": "responses", "env_key": "TYRS_HAND_MODEL_API_KEY",
		"requires_openai_auth": true,
	}
	config["model_providers"] = providers
}

func hideModelAPIKey(config map[string]any) {
	policy, _ := config["shell_environment_policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{"inherit": "all"}
	}
	if values, ok := policy["set"].(map[string]any); ok {
		delete(values, "TYRS_HAND_MODEL_API_KEY")
	}
	excluded := make([]string, 0, 4)
	switch values := policy["exclude"].(type) {
	case []string:
		excluded = append(excluded, values...)
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				excluded = append(excluded, text)
			}
		}
	}
	for _, value := range excluded {
		if value == "TYRS_HAND_MODEL_API_KEY" {
			policy["exclude"] = excluded
			config["shell_environment_policy"] = policy
			return
		}
	}
	policy["exclude"] = append(excluded, "TYRS_HAND_MODEL_API_KEY")
	config["shell_environment_policy"] = policy
}

func prepareCodexRuntime(environment []string, workerDataRoot string,
	cfg config.Config,
) ([]string, map[string]any) {
	processEnvironment := codexProcessEnvironment(environment, cfg)
	return processEnvironment, codexRuntimeConfig(processEnvironment, workerDataRoot, cfg)
}

func codexProcessEnvironment(environment []string, cfg config.Config) []string {
	result := append([]string(nil), environment...)
	if cfg.EnableSSH {
		result = setEnvironmentValue(result, "SSH_AUTH_SOCK",
			filepath.Join(cfg.SSHAgentDir, "current.sock"))
	}
	if cfg.BrowserMCPURL == "" {
		return result
	}
	token, err := os.ReadFile(cfg.BrowserMCPTokenFile)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return result
	}
	return setEnvironmentValue(result, "TYRS_BROWSER_MCP_TOKEN",
		strings.TrimSpace(string(token)))
}

func setEnvironmentValue(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		entryKey, _, found := strings.Cut(entry, "=")
		if found && entryKey == key {
			continue
		}
		result = append(result, entry)
	}
	return append(result, key+"="+value)
}

func environmentValue(environment []string, key string) string {
	for index := len(environment) - 1; index >= 0; index-- {
		entryKey, value, found := strings.Cut(environment[index], "=")
		if found && entryKey == key {
			return value
		}
	}
	return ""
}

func browserDeveloperInstructions(cfg config.Config, current string) string {
	if cfg.BrowserMCPURL == "" {
		return current
	}
	return current + "\nThe host Chrome profile is the only browser backend. Use host_browser tools only to expose local services or exchange files with it. If the chrome MCP is unavailable, report that directly; do not start Chrome, CDP, a container browser, or a headless browser."
}

func withoutGenericReply(tools []string) []string {
	result := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != "add_issue_comment" {
			result = append(result, tool)
		}
	}
	return result
}

func threadConfigSignature(providerSignature string, options ports.ThreadOptions) string {
	stableOptions := options
	data, _ := json.Marshal(options.RuntimeConfig)
	var runtimeConfig map[string]any
	if json.Unmarshal(data, &runtimeConfig) == nil {
		if policy, ok := runtimeConfig["shell_environment_policy"].(map[string]any); ok {
			if values, ok := policy["set"].(map[string]any); ok {
				delete(values, "TYRS_HAND_DOCKER_INTENT_ID")
				delete(values, "TYRS_HAND_DOCKER_RUN_ID")
			}
		}
		stableOptions.RuntimeConfig = runtimeConfig
	}
	data, _ = json.Marshal(struct {
		ProviderSignature string              `json:"providerSignature"`
		Options           ports.ThreadOptions `json:"options"`
	}{ProviderSignature: providerSignature, Options: stableOptions})
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

func shortID(id uuid.UUID) string { return strings.ReplaceAll(id.String()[:8], "-", "") }

func (p *Processor) recordWorkspace(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	workspace ports.Workspace, baseRef string,
) error {
	var cacheID uuid.UUID
	err := p.db.QueryRowContext(ctx, `INSERT INTO repo_caches(repository_id, path, last_fetch_at)
		VALUES ($1, $2, now()) ON CONFLICT(repository_id) DO UPDATE SET path = EXCLUDED.path,
		status = 'ready', last_fetch_at = now(), last_used_at = now(), error = NULL RETURNING id`,
		claimed.RepositoryID, workspace.CachePath).Scan(&cacheID)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `INSERT INTO worktrees
		(work_item_id, repo_cache_id, path, branch, base_sha, head_sha)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT(work_item_id) DO UPDATE SET
		repo_cache_id = EXCLUDED.repo_cache_id, path = EXCLUDED.path, branch = EXCLUDED.branch,
		head_sha = EXCLUDED.head_sha, status = 'ready', last_used_at = now(), expires_at = NULL, error = NULL`,
		claimed.WorkItemID, cacheID, workspace.WorktreePath, workspace.Branch, baseRef, workspace.HeadSHA)
	return err
}

func (p *Processor) refreshWorkspaceState(parent context.Context, workItemID uuid.UUID, path string) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	status, err := p.workspace.Status(ctx, path)
	if err != nil {
		_, _ = p.db.ExecContext(ctx, `UPDATE worktrees SET status = 'failed', error = $2,
			last_used_at = now() WHERE work_item_id = $1`, workItemID, err.Error())
		return
	}
	dirty := false
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if line != "" && !strings.HasPrefix(line, "##") {
			dirty = true
			break
		}
	}
	_, _ = p.db.ExecContext(ctx, `UPDATE worktrees SET dirty = $2, status = 'ready',
		error = NULL, last_used_at = now() WHERE work_item_id = $1`, workItemID, dirty)
}

func (p *Processor) cleanupClosedWorktrees(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT r.id::text, w.id::text
		FROM work_items w JOIN repositories r ON r.id = w.repository_id
		JOIN worktrees wt ON wt.work_item_id = w.id
		WHERE w.closed_at < now() - interval '7 days'
		  AND NOT EXISTS (
			SELECT 1 FROM codex_turn_intents i WHERE i.work_item_id = w.id
			  AND i.status IN ('dispatching','awaiting_confirmation','running','reconciling'))`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var repositoryID, workItemID string
		if err := rows.Scan(&repositoryID, &workItemID); err != nil {
			return err
		}
		if err := p.workspace.Remove(ctx, repositoryID, workItemID); err != nil {
			p.logger.Warn("删除 GitHub Worktree 失败", zap.String("work_item_id", workItemID), zap.Error(err))
			continue
		}
		if _, err := p.db.ExecContext(ctx, "DELETE FROM worktrees WHERE work_item_id = $1", workItemID); err != nil {
			return err
		}
	}
	return rows.Err()
}
