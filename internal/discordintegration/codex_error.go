package discordintegration

import "github.com/slovx2/tyrs-hand/internal/workerprotocol"

func CodexErrorForProjection(value *workerprotocol.CodexTurnError) *ComponentErrorPayload {
	if value == nil {
		return nil
	}
	return &ComponentErrorPayload{
		Message: value.Message, CodexErrorInfo: value.CodexErrorInfo,
		AdditionalDetails: value.AdditionalDetails, WillRetry: value.WillRetry,
		ThreadID: value.ThreadID, TurnID: value.TurnID,
	}
}
