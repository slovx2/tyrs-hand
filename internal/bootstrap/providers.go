package bootstrap

import (
	"context"
	"database/sql"

	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	"github.com/slovx2/tyrs-hand/internal/gitworkspace"
	"github.com/slovx2/tyrs-hand/internal/httpapi"
	"github.com/slovx2/tyrs-hand/internal/logging"
	"github.com/slovx2/tyrs-hand/internal/queue"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/worker"
	"go.uber.org/zap"
)

type ServerApp struct {
	API    *httpapi.Server
	DB     *sql.DB
	Redis  *redis.Client
	Logger *zap.Logger
}

type WorkerApp struct {
	Runner *worker.Runner
	DB     *sql.DB
	Redis  *redis.Client
	Pool   *codex.Pool
	Logger *zap.Logger
}

type DiscordApp struct {
	Daemon *discordintegration.Daemon
	DB     *sql.DB
	Redis  *redis.Client
	Logger *zap.Logger
}

func provideDatabase(ctx context.Context, cfg config.Config) (*sql.DB, func(), error) {
	db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { _ = db.Close() }, nil
}

func provideRedis(cfg config.Config) (*redis.Client, func(), error) {
	options, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, nil, err
	}
	client := redis.NewClient(options)
	return client, func() { _ = client.Close() }, nil
}

func provideLogger(cfg config.Config) (*zap.Logger, func(), error) {
	logger, err := logging.New(cfg)
	if err != nil {
		return nil, nil, err
	}
	return logger, func() { _ = logger.Sync() }, nil
}

func provideSecretBox(cfg config.Config) (*security.SecretBox, error) {
	return security.NewSecretBox(cfg.MasterKey)
}

func provideGitHubManager(ctx context.Context, db *sql.DB, store *secrets.Store) (*ghadapter.Manager, error) {
	manager := ghadapter.NewManager(db, store)
	if err := manager.Load(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func provideCatalog() (*githubtools.Catalog, error) {
	return githubtools.NewCatalog(githubtools.RegisteredTools)
}

func provideAuth(cfg config.Config, db *sql.DB, box *security.SecretBox) *auth.Service {
	return auth.NewService(db, box, cfg.SetupToken, cfg.PublicURL)
}

func provideWorkspace(cfg config.Config) *gitworkspace.Manager {
	return gitworkspace.NewManager(cfg.RepoCacheRoot, cfg.WorktreeRoot)
}

func provideControl(cfg config.Config) *worker.ControlClient {
	return worker.NewControlClient(cfg.InternalServerURL, cfg.ToolTimeout)
}

func provideQueue(cfg config.Config, db *sql.DB) *queue.Repository {
	return queue.NewRepository(db, cfg.LeaseDuration)
}

func providePool(ctx context.Context, cfg config.Config, logger *zap.Logger) (*codex.Pool, func(), error) {
	if err := codex.ValidateVersion(ctx, cfg.CodexBin); err != nil {
		return nil, nil, err
	}
	pool := codex.NewPool(codex.PoolOptions{Bin: cfg.CodexBin, RequestTimeout: cfg.ControlTimeout, ToolTimeout: cfg.ToolTimeout, Logger: logger})
	return pool, func() { _ = pool.Close() }, nil
}

func provideSettings(db *sql.DB, store *secrets.Store) *platformsettings.Service {
	return platformsettings.NewService(db, store)
}

func provideDiscordManager(db *sql.DB, store *secrets.Store) *discordintegration.Manager {
	return discordintegration.NewManager(db, store)
}

func provideBindingService(cfg config.Config, db *sql.DB, box *security.SecretBox, githubManager *ghadapter.Manager) *discordintegration.BindingService {
	return discordintegration.NewBindingService(discordintegration.NewSQLBindingStore(db), box,
		discordintegration.NewGitHubOAuthApp(githubManager), cfg.PublicURL, cfg.GitHubAPIURL)
}
