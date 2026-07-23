package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

var errRemoteInterrupt = errors.New("远程 Run 收到用户中断指令")

type remoteCommandHandler func(context.Context, *codex.Runtime, string, string,
	workerprotocol.RunCommand) error

func (p *RemoteProcessor) ensureRemoteThread(ctx context.Context, runtime *codex.Runtime,
	task *workerprotocol.Task, options ports.ThreadOptions, codexHome, signature string,
) (string, error) {
	claimed := &task.Claimed
	threadID := claimed.ExternalThreadID
	if threadID != "" {
		if claimed.CodexHomeKey != "" && claimed.CodexHomeKey != codexHome {
			return "", errors.New("持久化 Control 的 CODEX_HOME 与当前执行节点不一致")
		}
		if claimed.ProviderSignature != "" && claimed.ProviderSignature != signature {
			return "", errors.New("持久化 Control 的 Provider Signature 与当前配置不一致")
		}
		if err := runtime.ResumeThread(ctx, threadID, options); err != nil {
			return "", fmt.Errorf("恢复 Codex Thread: %w", err)
		}
	} else {
		var err error
		threadID, err = runtime.StartThread(ctx, options)
		if err != nil {
			return "", err
		}
	}
	if err := p.client.SetThread(ctx, task, threadID, codexHome, signature); err != nil {
		return "", err
	}
	claimed.ExternalThreadID = threadID
	claimed.CodexHomeKey = codexHome
	claimed.ProviderSignature = signature
	return threadID, nil
}

func (p *RemoteProcessor) reconcileRemoteTurn(ctx context.Context, runtime *codex.Runtime,
	events <-chan codex.Event, task *workerprotocol.Task, threadID string,
	commands <-chan workerprotocol.RunCommand,
	handleCommand remoteCommandHandler, report func(string, json.RawMessage),
) (codexcontrol.TurnResult, bool, error) {
	claimed := &task.Claimed
	snapshot, err := runtime.ReadThread(ctx, threadID)
	if err != nil {
		return codexcontrol.TurnResult{}, false, err
	}
	turn, found := snapshot.TurnByClientID(claimed.ID.String())
	if !found && claimed.ConfirmedTurnID != "" {
		turn, found = snapshot.TurnByID(claimed.ConfirmedTurnID)
	}
	if !found {
		if claimed.ConfirmedTurnID != "" {
			return codexcontrol.TurnResult{}, false,
				errors.New("已确认的 Codex Turn 在执行节点快照中消失")
		}
		return codexcontrol.TurnResult{}, false, nil
	}
	if err := p.client.ConfirmTurn(ctx, task, turn.ID); err != nil {
		return codexcontrol.TurnResult{}, false, err
	}
	if turn.Status == "completed" {
		result, resultErr := remoteCompletedResult(turn.FinalAnswer(), turn.ID, 0, "thread/read")
		return result, true, resultErr
	}
	if !isActiveCodexTurnStatus(turn.Status) {
		return codexcontrol.TurnResult{}, false, remoteTurnTerminalError("快照", turn.Status)
	}
	result, err := p.waitRemoteTurn(ctx, runtime, events, task, threadID, turn.ID,
		commands, handleCommand, report)
	return result, true, err
}

func (p *RemoteProcessor) waitRemoteTurn(ctx context.Context, runtime *codex.Runtime,
	events <-chan codex.Event, task *workerprotocol.Task, threadID, turnID string,
	commands <-chan workerprotocol.RunCommand, handleCommand remoteCommandHandler,
	report func(string, json.RawMessage),
) (codexcontrol.TurnResult, error) {
	startedAt := time.Now()
	maxTimer := time.NewTimer(p.cfg.TurnMaxDuration)
	defer maxTimer.Stop()
	idleTimer := time.NewTimer(p.cfg.TurnIdleTimeout)
	defer idleTimer.Stop()
	pollInterval := p.cfg.CodexStatusPollInterval
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	finalAnswer := ""
	var finalDelta strings.Builder
	appliedCommands := make(map[uuid.UUID]bool)
	for {
		select {
		case event, open := <-events:
			if !open {
				return p.remoteSnapshotTerminal(ctx, runtime, task, threadID, turnID, startedAt)
			}
			if !eventBelongsToTurn(event.Params, threadID, turnID, task.Claimed.ID.String()) {
				continue
			}
			resetTimer(idleTimer, p.cfg.TurnIdleTimeout)
			if report != nil {
				report(event.Method, event.Params)
			}
			if event.Method == "turn/started" {
				if actualID := eventTurnID(event.Params); actualID != "" {
					turnID = actualID
					if err := p.client.ConfirmTurn(ctx, task, actualID); err != nil {
						return codexcontrol.TurnResult{}, err
					}
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
					return codexcontrol.TurnResult{}, remoteTurnTerminalError("结束", status)
				}
				if finalAnswer == "" {
					finalAnswer = p.remoteFinalAnswer(ctx, runtime, threadID, turnID)
				}
				if finalAnswer == "" {
					finalAnswer = strings.TrimSpace(finalDelta.String())
				}
				return remoteCompletedResult(finalAnswer, turnID,
					time.Since(startedAt).Milliseconds(), "turn/completed")
			}
		case <-pollTicker.C:
			snapshot, err := runtime.ReadThread(ctx, threadID)
			if err != nil {
				continue
			}
			turn, found := snapshot.TurnByID(turnID)
			if !found {
				turn, found = snapshot.TurnByClientID(task.Claimed.ID.String())
			}
			if found && turn.Status == "completed" {
				return remoteCompletedResult(turn.FinalAnswer(), turn.ID,
					time.Since(startedAt).Milliseconds(), "thread/read")
			}
			if found && !isActiveCodexTurnStatus(turn.Status) {
				return codexcontrol.TurnResult{}, remoteTurnTerminalError("快照", turn.Status)
			}
		case command := <-commands:
			if appliedCommands[command.ID] {
				continue
			}
			if command.Operation == "interrupt" {
				if err := runtime.InterruptTurn(ctx, threadID, turnID); err != nil {
					return codexcontrol.TurnResult{}, err
				}
				if err := p.client.AckCommand(ctx, task, command, "interrupt", turnID); err != nil {
					return codexcontrol.TurnResult{}, err
				}
				return codexcontrol.TurnResult{}, errRemoteInterrupt
			}
			if handleCommand == nil {
				continue
			}
			if err := handleCommand(ctx, runtime, threadID, turnID, command); err != nil {
				return codexcontrol.TurnResult{}, err
			}
			appliedCommands[command.ID] = true
		case <-idleTimer.C:
			return codexcontrol.TurnResult{}, errors.New("codex turn 长时间没有相关活动")
		case <-maxTimer.C:
			return codexcontrol.TurnResult{}, errors.New("codex turn 超过最大执行时间")
		case <-ctx.Done():
			return codexcontrol.TurnResult{}, ctx.Err()
		}
	}
}

func (p *RemoteProcessor) remoteFinalAnswer(ctx context.Context, runtime *codex.Runtime,
	threadID, turnID string,
) string {
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, err := runtime.ReadThread(ctx, threadID)
		if err == nil {
			if turn, found := snapshot.TurnByID(turnID); found && turn.FinalAnswer() != "" {
				return turn.FinalAnswer()
			}
		}
		if !waitContext(ctx, 100*time.Millisecond) {
			return ""
		}
	}
	return ""
}

func (p *RemoteProcessor) remoteSnapshotTerminal(ctx context.Context, runtime *codex.Runtime,
	task *workerprotocol.Task, threadID, turnID string, startedAt time.Time,
) (codexcontrol.TurnResult, error) {
	snapshot, err := runtime.ReadThread(ctx, threadID)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	turn, found := snapshot.TurnByID(turnID)
	if !found {
		turn, found = snapshot.TurnByClientID(task.Claimed.ID.String())
	}
	if !found {
		return codexcontrol.TurnResult{}, errors.New("codex stdio 在 turn 完成前关闭")
	}
	if turn.Status != "completed" {
		return codexcontrol.TurnResult{}, remoteTurnTerminalError("快照", turn.Status)
	}
	return remoteCompletedResult(turn.FinalAnswer(), turn.ID,
		time.Since(startedAt).Milliseconds(), "thread/read")
}

func remoteTurnTerminalError(evidence, status string) error {
	message := fmt.Sprintf("codex turn %s状态为 %s", evidence, status)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "interrupted", "cancelled", "canceled":
		return fmt.Errorf("%s: %w", message, errRemoteInterrupt)
	default:
		return errors.New(message)
	}
}

func remoteCompletedResult(finalAnswer, turnID string, durationMillis int64,
	evidence string,
) (codexcontrol.TurnResult, error) {
	finalAnswer = strings.TrimSpace(finalAnswer)
	if finalAnswer == "" {
		return codexcontrol.TurnResult{}, errors.New("codex turn 已完成但没有最终回复")
	}
	return codexcontrol.TurnResult{FinalAnswer: finalAnswer, TurnID: turnID,
		DurationMillis: durationMillis, Evidence: evidence}, nil
}
