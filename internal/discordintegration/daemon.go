package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"go.uber.org/zap"
)

type Daemon struct {
	manager            *Manager
	conversations      *ConversationService
	bindings           *BindingService
	redis              *redis.Client
	logger             *zap.Logger
	apiURL             string
	newRemote          func(string, string) Remote
	newGateway         func(Settings, string) GatewayConnector
	githubPermission   func(context.Context, int64, string, string, string) (string, error)
	outboxInterval     time.Duration
	operationInterval  time.Duration
	projectionInterval time.Duration
	permissionInterval time.Duration
}

func NewDaemon(manager *Manager, conversations *ConversationService, bindings *BindingService,
	githubManager *ghadapter.Manager, redisClient *redis.Client, logger *zap.Logger,
) *Daemon {
	d := &Daemon{manager: manager, conversations: conversations, bindings: bindings,
		redis: redisClient, logger: logger, apiURL: "https://discord.com/api/v10",
		outboxInterval: 250 * time.Millisecond, operationInterval: 2 * time.Second,
		projectionInterval: time.Minute, permissionInterval: 5 * time.Minute}
	conversations.redis = redisClient
	d.newRemote = func(token, apiURL string) Remote { return NewDisgoRemote(token, apiURL, nil) }
	d.newGateway = func(settings Settings, token string) GatewayConnector {
		return NewDisgoConnector(manager, conversations, bindings, settings.GuildID, token, logger)
	}
	d.githubPermission = func(ctx context.Context, installationID int64, owner, repository, login string) (string, error) {
		if githubManager == nil {
			return "", errors.New("github App 尚未配置")
		}
		_, app, _, ok := githubManager.Current()
		if !ok {
			return "", errors.New("github App 尚未配置")
		}
		return app.Permission(ctx, installationID, owner, repository, login)
	}
	return d
}

func (d *Daemon) Run(ctx context.Context) error {
	settings, token, err := d.waitUntilEnabled(ctx)
	if err != nil {
		return err
	}
	remote := d.newRemote(token, d.apiURL)
	defer remote.Close(context.Background())
	connector := d.newGateway(settings, token)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	gatewayRunner := NewGatewayRunner(d.manager, settings.GuildID, connector)
	errCh := make(chan error, 2)
	go func() { errCh <- gatewayRunner.Run(runCtx) }()
	go func() { errCh <- d.runBackground(runCtx, settings.GuildID, remote) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (d *Daemon) waitUntilEnabled(ctx context.Context) (Settings, string, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		settings, err := d.manager.Settings(ctx)
		if err != nil {
			return Settings{}, "", err
		}
		if settings.Enabled && settings.GuildID != "" && settings.TokenConfigured {
			token, tokenErr := d.manager.BotToken(ctx)
			return settings, token, tokenErr
		}
		if settings.GuildID != "" {
			_ = d.manager.SetGatewayStatus(ctx, settings.GuildID, "disabled", nil)
		}
		select {
		case <-ctx.Done():
			return Settings{}, "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d *Daemon) runBackground(ctx context.Context, guildID string, remote Remote) error {
	dispatcher := NewDispatcher(NewSQLoutbox(d.manager.db), remote)
	var permissionMessages <-chan *redis.Message
	if d.redis != nil {
		subscription := d.redis.Subscribe(ctx, RepositoryPermissionSyncChannel)
		defer func() { _ = subscription.Close() }()
		permissionMessages = subscription.Channel()
	}
	outboxTicker := time.NewTicker(d.outboxInterval)
	defer outboxTicker.Stop()
	operationTicker := time.NewTicker(d.operationInterval)
	defer operationTicker.Stop()
	projectionTicker := time.NewTicker(d.projectionInterval)
	defer projectionTicker.Stop()
	permissionTicker := time.NewTicker(d.permissionInterval)
	defer permissionTicker.Stop()
	d.refreshAllProjections(ctx, guildID, remote)
	if err := d.syncRepositoryPermissions(ctx, guildID); err != nil {
		d.logger.Warn("同步 Discord 仓库权限失败", zap.Error(err))
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-outboxTicker.C:
			for count := 0; count < 20; count++ {
				worked, err := dispatcher.RunOnce(ctx)
				if err != nil {
					d.logger.Error("投递 Discord Outbox 失败", zap.Error(err))
					break
				}
				if !worked {
					break
				}
			}
		case <-operationTicker.C:
			if err := d.resumeInitialization(ctx, guildID, remote); err != nil {
				d.logger.Warn("执行 Discord 初始化失败", zap.Error(err))
			}
			for count := 0; count < 20; count++ {
				worked, err := d.conversations.StartDueConfiguration(ctx)
				if err != nil {
					d.logger.Warn("启动到期 Discord 会话失败", zap.Error(err))
					break
				}
				if !worked {
					break
				}
			}
		case <-projectionTicker.C:
			d.refreshAllProjections(ctx, guildID, remote)
		case <-permissionTicker.C:
			if err := d.syncRepositoryPermissions(ctx, guildID); err != nil {
				d.logger.Warn("同步 Discord 仓库权限失败", zap.Error(err))
			}
		case message, open := <-permissionMessages:
			if !open {
				permissionMessages = nil
				d.logger.Warn("Discord 仓库权限同步订阅已关闭，将使用定时同步")
				continue
			}
			if err := d.handleRepositoryPermissionSync(ctx, guildID, message.Payload); err != nil {
				d.logger.Warn("处理 Discord 仓库权限定向同步失败", zap.Error(err))
			}
		}
	}
}

func (d *Daemon) refreshAllProjections(ctx context.Context, guildID string, remote Remote) {
	paused, err := d.projectionsPaused(ctx, guildID)
	if err != nil {
		d.logger.Warn("检查 Discord 初始化状态失败", zap.Error(err))
		return
	}
	if paused {
		return
	}
	refreshes := []struct {
		name string
		run  func() error
	}{
		{name: "系统状态", run: func() error { return d.refreshSystemStatus(ctx, guildID) }},
		{name: "系统告警", run: func() error { return d.refreshSystemAlerts(ctx, guildID) }},
		{name: "任务 Forum", run: func() error { return d.refreshTaskProjections(ctx, guildID, remote) }},
	}
	for _, refresh := range refreshes {
		if err := refresh.run(); err != nil {
			d.logger.Warn("刷新 Discord 投影失败", zap.String("projection", refresh.name), zap.Error(err))
		}
	}
}

func (d *Daemon) projectionsPaused(ctx context.Context, guildID string) (bool, error) {
	var paused bool
	err := d.manager.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_initialization_operations o
		WHERE o.guild_id = $1 AND o.status IN ('pending', 'running', 'failed')
		AND COALESCE((SELECT s.attempt_count < $2 FROM discord_initialization_steps s
			WHERE s.operation_id = o.id AND s.status <> 'completed'
			ORDER BY s.ordinal LIMIT 1), false)
	)`, guildID, initializationMaxAttempts).Scan(&paused)
	return paused, err
}

func (d *Daemon) resumeInitialization(ctx context.Context, guildID string, remote Remote) error {
	var operationID uuid.UUID
	err := d.manager.db.QueryRowContext(ctx, `SELECT o.id FROM discord_initialization_operations o
		WHERE o.guild_id = $1 AND o.status IN ('pending', 'failed')
			AND COALESCE((SELECT s.attempt_count < $2 FROM discord_initialization_steps s
				WHERE s.operation_id = o.id AND s.status <> 'completed'
				ORDER BY s.ordinal LIMIT 1), false)
		ORDER BY o.created_at LIMIT 1`, guildID, initializationMaxAttempts).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return d.manager.RunInitialization(ctx, operationID, remote)
}

func (d *Daemon) refreshSystemStatus(ctx context.Context, guildID string) error {
	var channelID string
	err := d.manager.db.QueryRowContext(ctx, `SELECT discord_id FROM discord_resources
		WHERE guild_id = $1 AND resource_key = 'system.status' AND status = 'active'`, guildID).Scan(&channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var queued, running, failed, workers, outbox int64
	var gatewayStatus string
	err = d.manager.db.QueryRowContext(ctx, `WITH latest_jobs AS (
		SELECT status, row_number() OVER (
			PARTITION BY source_type, work_item_id, discord_conversation_id
			ORDER BY created_at DESC, id DESC
		) AS position
		FROM codex_turn_intents
	), job_counts AS (
		SELECT
			count(*) FILTER (WHERE position = 1 AND status IN ('queued','retry_wait')) AS queued,
			count(*) FILTER (WHERE position = 1 AND status IN ('dispatching','awaiting_confirmation','running','reconciling')) AS running,
			count(*) FILTER (WHERE position = 1 AND status = 'failed') AS failed
		FROM latest_jobs
	)
	SELECT queued, running, failed,
		(SELECT count(*) FROM worker_nodes WHERE status = 'online' AND heartbeat_at > now() - interval '2 minutes'),
		(SELECT count(*) FROM integration_outbox WHERE integration = 'discord' AND status IN ('pending','retrying','sending')),
		COALESCE((SELECT last_gateway_status FROM discord_guilds WHERE guild_id = $1), 'unknown')
		FROM job_counts`, guildID).
		Scan(&queued, &running, &failed, &workers, &outbox, &gatewayStatus)
	if err != nil {
		return err
	}
	card := systemStatusCard(queued, running, failed, workers, outbox, gatewayStatus)
	buttons := []map[string]string{{"label": "新建 Codex 帖子", "customId": "codex-new-open", "style": "primary"}}
	projectionPayload := map[string]any{"content": "", "embeds": []EmbedPayload{card}, "buttons": buttons}
	var messageID string
	err = d.manager.db.QueryRowContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, desired_payload)
		VALUES ($1, 'system.status', $2, $3)
		ON CONFLICT(guild_id, projection_key) DO UPDATE SET desired_payload = EXCLUDED.desired_payload,
			desired_version = discord_projections.desired_version + 1, updated_at = now()
		RETURNING COALESCE(message_id, '')`, guildID, channelID,
		mustJSON(projectionPayload)).Scan(&messageID)
	if err != nil {
		return err
	}
	operationType := "message.create"
	payload := map[string]any{"channelId": channelID, "content": "", "embeds": []EmbedPayload{card}, "buttons": buttons}
	if messageID != "" {
		operationType = "message.update"
		payload["messageId"] = messageID
	}
	return NewSQLoutbox(d.manager.db).Enqueue(ctx, "projection:system.status", operationType,
		"channels/"+channelID+"/messages", payload, fmt.Sprintf("system-status-%s", guildID))
}

func (d *Daemon) refreshSystemAlerts(ctx context.Context, guildID string) error {
	var channelID string
	err := d.manager.db.QueryRowContext(ctx, `SELECT discord_id FROM discord_resources
		WHERE guild_id = $1 AND resource_key = 'system.alerts' AND status = 'active'`, guildID).Scan(&channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var gatewayStatus, gatewayError string
	var workers, failedOutbox int64
	err = d.manager.db.QueryRowContext(ctx, `SELECT last_gateway_status, COALESCE(last_gateway_error, ''),
		(SELECT count(*) FROM worker_nodes WHERE status = 'online' AND heartbeat_at > now() - interval '2 minutes'),
		(SELECT count(*) FROM integration_outbox WHERE integration = 'discord' AND status = 'failed')
		FROM discord_guilds WHERE guild_id = $1`, guildID).Scan(&gatewayStatus, &gatewayError, &workers, &failedOutbox)
	if err != nil {
		return err
	}
	card := systemAlertsCard(gatewayStatus, gatewayError != "", workers, failedOutbox)
	projectionPayload := map[string]any{"content": "", "embeds": []EmbedPayload{card}}
	var messageID string
	err = d.manager.db.QueryRowContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, desired_payload)
		VALUES ($1, 'system.alerts', $2, $3)
		ON CONFLICT(guild_id, projection_key) DO UPDATE SET desired_payload = EXCLUDED.desired_payload,
			desired_version = discord_projections.desired_version + 1, updated_at = now()
		RETURNING COALESCE(message_id, '')`, guildID, channelID,
		mustJSON(projectionPayload)).Scan(&messageID)
	if err != nil {
		return err
	}
	operationType := "message.create"
	payload := map[string]any{"channelId": channelID, "content": "", "embeds": []EmbedPayload{card}}
	if messageID != "" {
		operationType = "message.update"
		payload["messageId"] = messageID
	}
	return NewSQLoutbox(d.manager.db).Enqueue(ctx, "projection:system.alerts", operationType,
		"channels/"+channelID+"/messages", payload, "system-alerts-"+guildID)
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
