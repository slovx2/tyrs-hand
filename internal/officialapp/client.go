package officialapp

import (
	"context"
	"errors"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type RPCClient interface {
	Call(context.Context, string, any, any) error
}

type SubmitRequest struct {
	ThreadID           string
	ClientMessageID    string
	Input              []UserInput
	Preferences        Preferences
	DismissOutstanding func(context.Context, string) error
}

type SubmitResult struct {
	ThreadID     string
	TurnID       string
	Deduplicated bool
}

func ReadThread(ctx context.Context, client RPCClient, threadID string) (Thread, error) {
	var response struct {
		Thread Thread `json:"thread"`
	}
	err := client.Call(ctx, "thread/read", map[string]any{
		"threadId": threadID, "includeTurns": true,
	}, &response)
	return response.Thread, err
}

func Submit(ctx context.Context, client RPCClient, request SubmitRequest) (SubmitResult, error) {
	if err := client.Call(ctx, "thread/resume", map[string]any{
		"threadId": request.ThreadID,
	}, nil); err != nil {
		return SubmitResult{}, err
	}
	thread, err := ReadThread(ctx, client, request.ThreadID)
	if err != nil {
		return SubmitResult{}, err
	}
	if duplicate := thread.FindClientMessage(request.ClientMessageID); duplicate != nil {
		return SubmitResult{ThreadID: thread.ID, TurnID: duplicate.ID, Deduplicated: true}, nil
	}
	if request.DismissOutstanding != nil {
		if err := request.DismissOutstanding(ctx, thread.ID); err != nil {
			return SubmitResult{}, err
		}
	}
	result, err := submitAgainstState(ctx, client, thread, request)
	if !turnStateMismatch(err) {
		return result, err
	}
	thread, readErr := ReadThread(ctx, client, request.ThreadID)
	if readErr != nil {
		return SubmitResult{}, errors.Join(err, readErr)
	}
	if duplicate := thread.FindClientMessage(request.ClientMessageID); duplicate != nil {
		return SubmitResult{ThreadID: thread.ID, TurnID: duplicate.ID, Deduplicated: true}, nil
	}
	if request.DismissOutstanding != nil {
		if err := request.DismissOutstanding(ctx, thread.ID); err != nil {
			return SubmitResult{}, err
		}
	}
	return submitAgainstState(ctx, client, thread, request)
}

func submitAgainstState(ctx context.Context, client RPCClient, thread Thread,
	request SubmitRequest,
) (SubmitResult, error) {
	if active := thread.LatestActiveTurn(); active != nil {
		var response struct {
			TurnID string `json:"turnId"`
		}
		err := client.Call(ctx, "turn/steer", map[string]any{
			"threadId": thread.ID, "clientUserMessageId": request.ClientMessageID,
			"input": request.Input, "expectedTurnId": active.ID,
		}, &response)
		return SubmitResult{ThreadID: thread.ID, TurnID: response.TurnID}, err
	}
	params := map[string]any{
		"threadId": thread.ID, "clientUserMessageId": request.ClientMessageID,
		"input": request.Input, "model": request.Preferences.Model,
		"effort":      request.Preferences.ReasoningEffort,
		"serviceTier": request.Preferences.ServiceTier,
		"collaborationMode": map[string]any{
			"mode": request.Preferences.CollaborationMode,
			"settings": map[string]any{
				"model":                  request.Preferences.Model,
				"reasoning_effort":       request.Preferences.ReasoningEffort,
				"developer_instructions": nil,
			},
		},
	}
	var response struct {
		Turn Turn `json:"turn"`
	}
	err := client.Call(ctx, "turn/start", params, &response)
	return SubmitResult{ThreadID: thread.ID, TurnID: response.Turn.ID}, err
}

func turnStateMismatch(err error) bool {
	var requestError *codex.RequestError
	if !errors.As(err, &requestError) || requestError.State != codex.RequestRejected {
		return false
	}
	var rpcError *codex.RPCError
	if !errors.As(err, &rpcError) {
		return false
	}
	message := strings.ToLower(rpcError.Message)
	return strings.Contains(message, "no active turn") ||
		strings.Contains(message, "active turn already") ||
		strings.Contains(message, "already has an active turn") ||
		strings.Contains(message, "expected active turn") ||
		strings.Contains(message, "expectedturnid")
}
