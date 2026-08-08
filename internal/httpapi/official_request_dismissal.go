package httpapi

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
)

type dismissibleOfficialRequest struct {
	ID     uuid.UUID
	Method string
}

func (s *Server) dismissOfficialServerRequests(ctx context.Context, workspaceID uuid.UUID,
	threadID string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id,method FROM official_server_requests
		WHERE workspace_id=$1 AND thread_id=$2 AND owner='control' AND status='pending'
		ORDER BY created_at,id FOR UPDATE`, workspaceID, threadID)
	if err != nil {
		return err
	}
	var requests []dismissibleOfficialRequest
	for rows.Next() {
		var request dismissibleOfficialRequest
		if err = rows.Scan(&request.ID, &request.Method); err != nil {
			_ = rows.Close()
			return err
		}
		requests = append(requests, request)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	project := make([]uuid.UUID, 0, len(requests))
	for _, request := range requests {
		response, ok := officialDismissResponse(request.Method)
		if !ok {
			continue
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE official_server_requests SET
			status='dismissed',response=$2,answer_surface='dismissed',resolved_at=now(),
			updated_at=now() WHERE id=$1 AND status='pending'`, request.ID, response)
		if updateErr != nil {
			return updateErr
		}
		if count, _ := result.RowsAffected(); count == 1 {
			project = append(project, request.ID)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, id := range project {
		if err = discordintegration.ProjectOfficialServerRequest(ctx, s.db, id); err != nil {
			return err
		}
	}
	return nil
}

func officialDismissResponse(method string) (json.RawMessage, bool) {
	var response any
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		response = map[string]any{"decision": "decline"}
	case "item/tool/requestUserInput":
		response = map[string]any{"answers": map[string]any{}}
	case "mcpServer/elicitation/request":
		response = map[string]any{"action": "cancel", "content": nil, "_meta": nil}
	case "item/permissions/requestApproval":
		response = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	case "applyPatchApproval", "execCommandApproval":
		response = map[string]any{"decision": "abort"}
	default:
		return nil, false
	}
	encoded, err := json.Marshal(response)
	return encoded, err == nil
}
