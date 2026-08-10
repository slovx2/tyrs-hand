package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type officialConnectionEntry struct {
	workerID uuid.UUID
	cancel   context.CancelFunc
	done     chan struct{}
}

func (s *Server) runOfficialConnections(ctx context.Context) error {
	entries := make(map[uuid.UUID]officialConnectionEntry)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	reconcile := func() {
		available, err := s.availableOfficialWorkspaces(ctx)
		if err != nil {
			s.logOfficialWarning("读取官方连接 Workspace 失败", err)
			return
		}
		for workspaceID, entry := range entries {
			workerID, exists := available[workspaceID]
			if exists && workerID == entry.workerID {
				continue
			}
			entry.cancel()
			delete(entries, workspaceID)
		}
		for workspaceID, workerID := range available {
			if _, exists := entries[workspaceID]; exists {
				continue
			}
			connectionCtx, cancel := context.WithCancel(ctx)
			entry := officialConnectionEntry{workerID: workerID, cancel: cancel,
				done: make(chan struct{})}
			entries[workspaceID] = entry
			go func() {
				defer close(entry.done)
				s.runOfficialWorkspace(connectionCtx, workspaceID, workerID)
			}()
		}
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			for _, entry := range entries {
				entry.cancel()
			}
			for _, entry := range entries {
				<-entry.done
			}
			return ctx.Err()
		case <-ticker.C:
			reconcile()
		}
	}
}

func (s *Server) availableOfficialWorkspaces(ctx context.Context) (map[uuid.UUID]uuid.UUID,
	error,
) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace.id,worker.id
		FROM worker_workspaces workspace JOIN workers worker ON worker.id=workspace.worker_id
		WHERE worker.enabled=true AND worker.protocol_version=$1
		  AND worker.status='online' AND worker.heartbeat_at>now()-interval '2 minutes'`,
		workerprotocol.Version)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[uuid.UUID]uuid.UUID)
	for rows.Next() {
		var workspaceID, workerID uuid.UUID
		if err = rows.Scan(&workspaceID, &workerID); err != nil {
			return nil, err
		}
		result[workspaceID] = workerID
	}
	return result, rows.Err()
}

func (s *Server) runOfficialWorkspace(ctx context.Context, workspaceID,
	workerID uuid.UUID,
) {
	for ctx.Err() == nil {
		if err := s.serveOfficialWorkspace(ctx, workspaceID, workerID); err != nil &&
			ctx.Err() == nil {
			s.logOfficialWarning("Workspace 官方连接已断开", err,
				zap.String("workspace_id", workspaceID.String()))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) serveOfficialWorkspace(ctx context.Context, workspaceID,
	workerID uuid.UUID,
) error {
	transport, _, err := s.appServerTunnels.issueSystem(workerID)
	if err != nil {
		return err
	}
	connectionID := uuid.New()
	client, err := codex.ConnectTransport(ctx, transport, codex.SocketClientOptions{
		RequestTimeout: 30 * time.Second, ServerRequestTimeout: 30 * time.Minute,
		ServerRequestHandler: func(requestCtx context.Context,
			request codex.ServerRequest,
		) (any, error) {
			return s.handleOfficialServerRequest(requestCtx, workspaceID, connectionID, request)
		},
	})
	if err != nil {
		_ = transport.Close()
		return err
	}
	defer func() {
		_ = client.Close()
		s.staleOfficialServerRequests(workspaceID, connectionID)
	}()
	subscription := client.Subscribe(codex.ThreadFilter{})
	defer subscription.Close()
	if err = s.syncOfficialWorkspace(ctx, client, workspaceID); err != nil {
		return err
	}
	if err = s.reconcileAmbiguousOfficialSubmissions(ctx, client, workspaceID); err != nil {
		return err
	}
	dirty := make(map[string]struct{})
	var dirtyMu sync.Mutex
	refreshTicker := time.NewTicker(150 * time.Millisecond)
	submitTicker := time.NewTicker(250 * time.Millisecond)
	fullTicker := time.NewTicker(30 * time.Second)
	defer refreshTicker.Stop()
	defer submitTicker.Stop()
	defer fullTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-client.Done():
			return client.Err()
		case event, open := <-subscription.Events():
			if !open {
				return client.Err()
			}
			s.handleOfficialResolvedNotification(workspaceID, connectionID, event)
			if metadataErr := s.handleOfficialMetadataNotification(ctx, workspaceID,
				event); metadataErr != nil {
				s.logOfficialWarning("同步官方 Thread 元数据失败", metadataErr,
					zap.String("method", event.Method))
			}
			if threadID := officialEventThreadID(event.Params); threadID != "" {
				dirtyMu.Lock()
				dirty[threadID] = struct{}{}
				dirtyMu.Unlock()
			}
		case <-refreshTicker.C:
			dirtyMu.Lock()
			threadIDs := make([]string, 0, len(dirty))
			for threadID := range dirty {
				threadIDs = append(threadIDs, threadID)
				delete(dirty, threadID)
			}
			dirtyMu.Unlock()
			for _, threadID := range threadIDs {
				if refreshErr := s.syncOfficialThread(ctx, client, workspaceID,
					threadID); refreshErr != nil {
					s.logOfficialWarning("刷新官方 Thread 失败", refreshErr,
						zap.String("thread_id", threadID))
				}
			}
		case <-submitTicker.C:
			var actionWorked bool
			actionWorked, err = s.processOfficialThreadAction(ctx, client, workspaceID)
			if err != nil {
				s.logOfficialWarning("处理官方 Thread action 失败", err,
					zap.String("workspace_id", workspaceID.String()))
			}
			if !actionWorked && err == nil {
				err = s.processOfficialSubmission(ctx, client, workspaceID)
			}
			if err != nil && !actionWorked {
				s.logOfficialWarning("处理官方提交失败", err,
					zap.String("workspace_id", workspaceID.String()))
			}
		case <-fullTicker.C:
			if err = s.syncOfficialWorkspace(ctx, client, workspaceID); err != nil {
				return err
			}
		}
	}
}

func (s *Server) syncOfficialWorkspace(ctx context.Context, client *codex.SocketClient,
	workspaceID uuid.UUID,
) error {
	threadIDs := make(map[string]struct{})
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id FROM official_thread_bindings
		WHERE workspace_id=$1`, workspaceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var threadID string
		if err = rows.Scan(&threadID); err != nil {
			_ = rows.Close()
			return err
		}
		threadIDs[threadID] = struct{}{}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, archived := range []bool{false, true} {
		var cursor any
		for {
			var page struct {
				Data       []officialapp.Thread `json:"data"`
				NextCursor *string              `json:"nextCursor"`
			}
			params := officialThreadListParams(archived, cursor)
			if err = client.Call(ctx, "thread/list", params, &page); err != nil {
				return err
			}
			for _, thread := range page.Data {
				_, alreadyBound := threadIDs[thread.ID]
				bound, bindErr := s.bindDiscoveredOfficialThread(ctx, workspaceID, thread,
					archived)
				if bindErr != nil {
					s.logOfficialWarning("绑定晚订阅官方 Thread 失败", bindErr,
						zap.String("thread_id", thread.ID))
				}
				if alreadyBound || bound {
					threadIDs[thread.ID] = struct{}{}
				}
			}
			if page.NextCursor == nil || *page.NextCursor == "" {
				break
			}
			cursor = *page.NextCursor
		}
	}
	for threadID := range threadIDs {
		if err = s.syncOfficialThread(ctx, client, workspaceID, threadID); err != nil {
			s.logOfficialWarning("同步完整官方 Thread 失败", err,
				zap.String("thread_id", threadID))
		}
	}
	return nil
}

func (s *Server) syncOfficialThread(ctx context.Context, client *codex.SocketClient,
	workspaceID uuid.UUID, threadID string,
) error {
	thread, err := officialapp.ReadThread(ctx, client, threadID)
	if err != nil {
		return err
	}
	return discordintegration.ProjectOfficialThread(ctx, s.db, workspaceID, thread)
}

func officialEventThreadID(raw json.RawMessage) string {
	var value struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
		Turn struct {
			ThreadID string `json:"threadId"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &value)
	if value.ThreadID != "" {
		return value.ThreadID
	}
	if value.Thread.ID != "" {
		return value.Thread.ID
	}
	return value.Turn.ThreadID
}

func (s *Server) logOfficialWarning(message string, err error, fields ...zap.Field) {
	if s.logger != nil && !errors.Is(err, context.Canceled) {
		s.logger.Warn(message, append(fields, zap.Error(err))...)
	}
}
