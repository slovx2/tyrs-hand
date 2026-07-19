//go:build wireinject

package bootstrap

import (
	"context"

	"github.com/google/wire"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/gitworkspace"
	"github.com/slovx2/tyrs-hand/internal/httpapi"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/worker"
)

func InitializeServer(ctx context.Context, cfg config.Config) (*ServerApp, func(), error) {
	wire.Build(
		provideDatabase, provideRedis, provideLogger, provideSecretBox,
		secrets.NewStore, provideGitHubManager, provideCatalog, provideAuth,
		provideSettings, provideDiscordManager, provideBindingService,
		httpapi.NewServer, wire.Struct(new(ServerApp), "*"),
	)
	return nil, nil, nil
}

func InitializeWorker(ctx context.Context, cfg config.Config) (*WorkerApp, func(), error) {
	wire.Build(
		provideDatabase, provideRedis, provideLogger, provideSecretBox, secrets.NewStore,
		provideSettings, provideCatalog, provideWorkspace, provideControl, provideQueue,
		providePool, provideDevelopmentEnvironment, wire.Bind(new(ports.WorkspaceManager), new(*gitworkspace.Manager)),
		worker.NewProcessor, worker.NewRunner, wire.Struct(new(WorkerApp), "*"),
	)
	return nil, nil, nil
}

func InitializeDiscord(ctx context.Context, cfg config.Config) (*DiscordApp, func(), error) {
	wire.Build(
		provideDatabase, provideRedis, provideLogger, provideSecretBox, secrets.NewStore,
		provideGitHubManager, provideDiscordManager, provideBindingService,
		discordintegration.NewConversationService, discordintegration.NewDaemon,
		wire.Struct(new(DiscordApp), "*"),
	)
	return nil, nil, nil
}
