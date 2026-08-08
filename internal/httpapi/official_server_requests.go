package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"go.uber.org/zap"
)

type officialServerRequestScope struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
}

func (s *Server) handleOfficialServerRequest(ctx context.Context, workspaceID,
	connectionID uuid.UUID, request codex.ServerRequest,
) (any, error) {
	var scope officialServerRequestScope
	if err := json.Unmarshal(request.Params, &scope); err != nil {
		return nil, err
	}
	owner, conversationID, err := s.officialRequestOwner(ctx, workspaceID, scope)
	if err != nil {
		return nil, err
	}
	if !officialInteractiveMethod(request.Method) {
		owner = "external"
	}
	id, status, response, err := s.recordOfficialServerRequest(ctx, workspaceID,
		connectionID, conversationID, owner, scope, request)
	if err != nil {
		return nil, err
	}
	if owner != "control" {
		return nil, codex.ErrServerRequestUnclaimed
	}
	if status == "answered" || status == "dismissed" {
		return response, nil
	}
	if status != "pending" {
		return nil, codex.ErrServerRequestUnclaimed
	}
	if err = discordintegration.ProjectOfficialServerRequest(ctx, s.db, id); err != nil {
		s.logOfficialWarning("投影官方交互请求失败", err, zap.String("request_id", id.String()))
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.markOfficialServerRequestStale(id)
			return nil, codex.ErrServerRequestUnclaimed
		case <-ticker.C:
			err = s.db.QueryRowContext(ctx, `SELECT status,COALESCE(response,'null'::jsonb)
				FROM official_server_requests WHERE id=$1`, id).Scan(&status, &response)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, codex.ErrServerRequestUnclaimed
			}
			if err != nil {
				return nil, err
			}
			switch status {
			case "answered", "dismissed":
				return response, nil
			case "resolved", "stale":
				return nil, codex.ErrServerRequestUnclaimed
			}
		}
	}
}

func (s *Server) officialRequestOwner(ctx context.Context, workspaceID uuid.UUID,
	scope officialServerRequestScope,
) (string, uuid.NullUUID, error) {
	var conversationID uuid.NullUUID
	var owner, ownedTurnID string
	err := s.db.QueryRowContext(ctx, `SELECT conversation_id,interactive_owner,
		COALESCE(owned_turn_id,'') FROM official_thread_bindings
		WHERE workspace_id=$1 AND thread_id=$2`, workspaceID, scope.ThreadID).
		Scan(&conversationID, &owner, &ownedTurnID)
	if errors.Is(err, sql.ErrNoRows) {
		return "external", conversationID, nil
	}
	if err != nil {
		return "", conversationID, err
	}
	if owner != "control" || !conversationID.Valid ||
		(ownedTurnID != "" && scope.TurnID != "" && ownedTurnID != scope.TurnID) {
		owner = "external"
	}
	return owner, conversationID, nil
}

func officialInteractiveMethod(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval",
		"item/tool/requestUserInput", "mcpServer/elicitation/request",
		"item/permissions/requestApproval", "applyPatchApproval", "execCommandApproval":
		return true
	default:
		return false
	}
}

func (s *Server) recordOfficialServerRequest(ctx context.Context, workspaceID,
	connectionID uuid.UUID, conversationID uuid.NullUUID, owner string,
	scope officialServerRequestScope, request codex.ServerRequest,
) (uuid.UUID, string, json.RawMessage, error) {
	status := "observed"
	if owner == "control" {
		status = "pending"
	}
	key := officialServerRequestKey(workspaceID, connectionID, scope.ThreadID, request.ID)
	var id uuid.UUID
	var savedStatus string
	var response json.RawMessage
	err := s.db.QueryRowContext(ctx, `INSERT INTO official_server_requests(
		workspace_id,conversation_id,connection_id,request_key,app_server_request_id,
		method,thread_id,turn_id,item_id,params,owner,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),$10,$11,$12)
		ON CONFLICT(request_key) DO UPDATE SET
			conversation_id=COALESCE(official_server_requests.conversation_id,EXCLUDED.conversation_id),
			params=EXCLUDED.params,updated_at=now()
		RETURNING id,status,COALESCE(response,'null'::jsonb)`, workspaceID,
		nullOfficialUUID(conversationID), connectionID, key, request.ID, request.Method,
		scope.ThreadID, scope.TurnID, scope.ItemID, request.Params, owner, status).
		Scan(&id, &savedStatus, &response)
	return id, savedStatus, response, err
}

func nullOfficialUUID(value uuid.NullUUID) any {
	if !value.Valid {
		return nil
	}
	return value.UUID
}

func officialServerRequestKey(workspaceID, connectionID uuid.UUID, threadID string,
	requestID json.RawMessage,
) string {
	var compact bytes.Buffer
	if json.Compact(&compact, requestID) != nil {
		compact.Write(requestID)
	}
	digest := sha256.Sum256([]byte(workspaceID.String() + "\x00" + connectionID.String() +
		"\x00" + threadID + "\x00" + compact.String()))
	return hex.EncodeToString(digest[:])
}

func (s *Server) handleOfficialResolvedNotification(workspaceID, connectionID uuid.UUID,
	event codex.Event,
) {
	if event.Method != "serverRequest/resolved" {
		return
	}
	var value struct {
		ThreadID  string          `json:"threadId"`
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(event.Params, &value) != nil || value.ThreadID == "" ||
		len(value.RequestID) == 0 {
		return
	}
	key := officialServerRequestKey(workspaceID, connectionID, value.ThreadID, value.RequestID)
	for range 25 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var id uuid.UUID
		err := s.db.QueryRowContext(ctx, `UPDATE official_server_requests SET
			status='resolved',answer_surface='external',resolved_at=now(),updated_at=now()
			WHERE request_key=$1 AND status IN ('observed','pending') RETURNING id`, key).Scan(&id)
		cancel()
		if err == nil {
			ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			_ = discordintegration.ProjectOfficialServerRequest(ctx, s.db, id)
			cancel()
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			s.logOfficialWarning("更新官方交互请求完成状态失败", err)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *Server) staleOfficialServerRequests(workspaceID, connectionID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `UPDATE official_server_requests SET status='stale',
		updated_at=now() WHERE workspace_id=$1 AND connection_id=$2
		AND status IN ('observed','pending') RETURNING id`, workspaceID, connectionID)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			_ = discordintegration.ProjectOfficialServerRequest(ctx, s.db, id)
		}
	}
}

func (s *Server) markOfficialServerRequestStale(id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `UPDATE official_server_requests SET status='stale',
		updated_at=now() WHERE id=$1 AND status='pending'`, id)
	if err == nil {
		if count, _ := result.RowsAffected(); count == 1 {
			_ = discordintegration.ProjectOfficialServerRequest(ctx, s.db, id)
		}
	}
}
