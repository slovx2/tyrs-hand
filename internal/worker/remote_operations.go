package worker

import (
	"context"
	"sync"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

func (r *RemoteRunner) runDevelopmentOperation(ctx context.Context,
	operation *workerprotocol.DevelopmentOperation, slots chan struct{}, active *sync.WaitGroup,
) {
	defer active.Done()
	defer func() { <-slots }()
	logger := r.logger.With(zap.String("development_operation_id", operation.ID.String()),
		zap.String("operation", operation.Operation))
	operationCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		r.developmentOperationHeartbeat(operationCtx, operation, logger)
	}()
	err := r.processor.ProcessDevelopmentOperation(operationCtx, operation)
	cancel()
	<-heartbeatDone
	for ctx.Err() == nil {
		requestCtx, requestCancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
		var submitErr error
		if err == nil {
			submitErr = r.client.CompleteDevelopmentOperation(requestCtx, operation)
		} else {
			submitErr = r.client.FailDevelopmentOperation(requestCtx, operation, err)
		}
		requestCancel()
		if submitErr == nil || workerprotocol.IsAlreadyFinished(submitErr) {
			return
		}
		if workerprotocol.IsLeaseLost(submitErr) {
			logger.Error("开发环境 Operation 结果未被接受，Lease 已失效", zap.Error(submitErr))
			return
		}
		logger.Warn("提交开发环境 Operation 结果失败，稍后重试", zap.Error(submitErr))
		if !waitContext(ctx, 3*time.Second) {
			return
		}
	}
}

func (r *RemoteRunner) developmentOperationHeartbeat(ctx context.Context,
	operation *workerprotocol.DevelopmentOperation, logger *zap.Logger,
) {
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requestCtx, cancel := context.WithTimeout(context.Background(), r.cfg.ControlTimeout)
			err := r.client.DevelopmentOperationHeartbeat(requestCtx, operation)
			cancel()
			if err != nil {
				logger.Warn("开发环境 Operation 续租失败，本地操作继续", zap.Error(err))
			}
		}
	}
}
