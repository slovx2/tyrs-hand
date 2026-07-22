package bootstrap

import (
	"context"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/worker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type RemoteWorkerApp struct {
	Runner *worker.RemoteRunner
	Pool   interface{ Close() error }
	Logger *zap.Logger
}

func InitializeRemoteWorker(ctx context.Context, cfg config.Config) (*RemoteWorkerApp, func(), error) {
	logger, cleanupLogger, err := provideLogger(cfg)
	if err != nil {
		return nil, nil, err
	}
	pool, cleanupPool, err := providePool(ctx, cfg, logger)
	if err != nil {
		cleanupLogger()
		return nil, nil, err
	}
	catalog, err := provideCatalog()
	if err != nil {
		cleanupPool()
		cleanupLogger()
		return nil, nil, err
	}
	workspace := provideWorkspace(cfg)
	development, err := provideDevelopmentContainers(cfg, nil, logger)
	if err != nil {
		cleanupPool()
		cleanupLogger()
		return nil, nil, err
	}
	client := workerprotocol.NewClient(cfg.WorkerControlURL, "", cfg.ControlTimeout)
	processor := worker.NewRemoteProcessor(ctx, cfg, client, workspace, catalog, pool, development, logger)
	runner, err := worker.NewRemoteRunner(cfg, client, processor, logger)
	if err != nil {
		cleanupPool()
		cleanupLogger()
		return nil, nil, err
	}
	return &RemoteWorkerApp{Runner: runner, Pool: pool, Logger: logger}, func() {
		cleanupPool()
		cleanupLogger()
	}, nil
}
