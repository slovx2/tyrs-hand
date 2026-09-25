package hostworker

import (
	"crypto/sha256"
	"os"
	"time"

	"go.uber.org/zap"
)

// WatchAuthorizedClients 在启动阶段调用一次；所有入口立即采用同一授权快照。
// 文件丢失、不可读或格式损坏时关闭现有授权，修复后自动恢复。
func (r *RuntimeRegistry) WatchAuthorizedClients(path string, logger *zap.Logger) {
	if logger == nil {
		logger = zap.NewNop()
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		var previous [32]byte
		var previousError string
		initialized := false
		reload := func() {
			data, err := os.ReadFile(path)
			digest := sha256.Sum256(data)
			var message string
			if err != nil {
				message = err.Error()
			}
			if initialized && previous == digest && previousError == message {
				return
			}
			previous, previousError, initialized = digest, message, true
			var clients []AuthorizedClient
			if err == nil && len(data) != 0 {
				clients, err = ParseAuthorizedClients(data)
			}
			if err != nil {
				logger.Warn("SSH 授权文件无效，两个入口均已撤销授权，修复后自动恢复", zap.Error(err))
				clients = nil
			}
			r.Authorization.Replace(clients)
		}
		reload()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				reload()
			}
		}
	}()
}
