package worker

import (
	"context"
	"errors"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type appServerTunnelTarget interface {
	ServeAppServerTunnel(context.Context, *websocket.Conn) error
}

func (p *Processor) ServeAppServerTunnel(ctx context.Context,
	connection *websocket.Conn,
) error {
	if p.hostRuntime == nil {
		return errors.New("宿主 Codex Runtime 尚未启动")
	}
	return p.hostRuntime.ServeAppServerTunnel(ctx, connection)
}

func (r *Runner) appServerTunnelLoop(ctx context.Context, target appServerTunnelTarget) {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		claim, err := r.client.ClaimAppServerTunnel(claimCtx)
		cancel()
		if err != nil {
			r.logger.Warn("领取 App Server 隧道失败", zap.Error(err))
			if !waitContext(ctx, time.Second) {
				return
			}
			continue
		}
		if claim.Tunnel == nil {
			continue
		}
		go func() {
			connectCtx, connectCancel := context.WithDeadline(ctx, claim.Tunnel.ExpiresAt)
			connection, connectErr := r.client.OpenAppServerTunnel(connectCtx, claim.Tunnel.ID)
			connectCancel()
			if connectErr != nil {
				r.logger.Warn("连接 App Server 反向隧道失败",
					zap.String("tunnel_id", claim.Tunnel.ID.String()), zap.Error(connectErr))
				return
			}
			if serveErr := target.ServeAppServerTunnel(ctx, connection); serveErr != nil &&
				ctx.Err() == nil {
				r.logger.Warn("App Server 隧道停止",
					zap.String("tunnel_id", claim.Tunnel.ID.String()), zap.Error(serveErr))
			}
		}()
	}
}
