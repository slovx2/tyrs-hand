package httpapi

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/web"
	"go.uber.org/zap"
)

const sessionCookie = "tyrs_hand_session"

type Server struct {
	cfg      config.Config
	db       *sql.DB
	redis    *redis.Client
	auth     *auth.Service
	github   *ghadapter.Manager
	catalog  *githubtools.Catalog
	settings *platformsettings.Service
	discord  *discordintegration.Manager
	bindings *discordintegration.BindingService
	logger   *zap.Logger
	assets   fs.FS
}

func NewServer(cfg config.Config, db *sql.DB, redisClient *redis.Client, authService *auth.Service, githubManager *ghadapter.Manager, catalog *githubtools.Catalog, settingsService *platformsettings.Service, discordManager *discordintegration.Manager, bindingService *discordintegration.BindingService, logger *zap.Logger) (*Server, error) {
	assets, err := web.Assets()
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, db: db, redis: redisClient, auth: authService, github: githubManager,
		catalog: catalog, settings: settingsService, discord: discordManager, bindings: bindingService,
		logger: logger, assets: assets}, nil
}

func (s *Server) baseRouter() *gin.Engine {
	if s.cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(s.requestID(), s.securityHeaders(), s.rateLimit(), gin.Recovery(), metricsMiddleware(), s.accessLog())
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/readyz", s.ready)
	return router
}

func (s *Server) Router() http.Handler {
	return s.adminRouter(true)
}

func (s *Server) AdminRouter() http.Handler {
	return s.adminRouter(false)
}

func (s *Server) adminRouter(includeWebhook bool) http.Handler {
	router := s.baseRouter()
	router.GET("/metrics", func(c *gin.Context) {
		s.refreshOperationalMetrics(c.Request.Context())
		gin.WrapH(promhttp.Handler())(c)
	})
	router.POST("/internal/v1/tools/call", s.internalToolCall)
	router.POST("/internal/v1/git/credential", s.internalGitCredential)
	if includeWebhook {
		router.POST("/webhooks/github", s.githubWebhook)
	}

	api := router.Group("/api/v1")
	api.GET("/setup/status", s.setupStatus)
	api.POST("/setup/admin", s.setupAdmin)
	api.POST("/auth/login", s.login)
	api.GET("/github/app/manifest/callback", s.githubManifestCallback)
	api.GET("/discord/github/bind/callback", s.discordGitHubBindCallback)

	authenticated := api.Group("")
	authenticated.Use(s.requireSession())
	authenticated.GET("/auth/me", s.me)
	authenticated.POST("/auth/logout", s.requireCSRF(), s.logout)
	authenticated.GET("/github/app", s.getGitHubApp)
	authenticated.PUT("/github/app", s.requireCSRF(), s.putGitHubApp)
	authenticated.GET("/github/app/manifest", s.githubManifest)
	authenticated.GET("/repositories", s.listRepositories)
	authenticated.GET("/installations", s.listInstallations)
	authenticated.POST("/repositories", s.requireCSRF(), s.createRepository)
	authenticated.GET("/agent-profiles", s.listAgentProfiles)
	authenticated.POST("/agent-profiles", s.requireCSRF(), s.createAgentProfile)
	authenticated.GET("/trigger-rules", s.listTriggerRules)
	authenticated.POST("/trigger-rules", s.requireCSRF(), s.createTriggerRule)
	authenticated.GET("/work-items", s.listWorkItems)
	authenticated.GET("/jobs", s.listJobs)
	authenticated.GET("/workers", s.listWorkers)
	authenticated.GET("/threads", s.listThreads)
	authenticated.GET("/worktrees", s.listWorktrees)
	authenticated.GET("/repo-caches", s.listRepoCaches)
	authenticated.GET("/audit-logs", s.listAuditLogs)
	authenticated.GET("/settings/agent-provider", s.getAgentProviderSettings)
	authenticated.PUT("/settings/agent-provider", s.requireCSRF(), s.putAgentProviderSettings)
	authenticated.GET("/settings/discord", s.getDiscordSettings)
	authenticated.PUT("/settings/discord", s.requireCSRF(), s.putDiscordSettings)
	authenticated.GET("/discord/status", s.discordStatus)
	authenticated.POST("/discord/initializations/preflight", s.requireCSRF(), s.discordInitializationPreflight)
	authenticated.POST("/discord/initializations", s.requireCSRF(), s.createDiscordInitialization)
	authenticated.GET("/discord/initializations/:id", s.getDiscordInitialization)
	authenticated.GET("/discord/members", s.listDiscordMembers)
	authenticated.POST("/discord/members/:id/forum", s.requireCSRF(), s.createDiscordMemberForum)
	authenticated.PUT("/discord/forums/:forumId/access/:memberId", s.requireCSRF(), s.putDiscordForumAccess)
	authenticated.DELETE("/discord/forums/:forumId/access/:memberId", s.requireCSRF(), s.deleteDiscordForumAccess)
	authenticated.POST("/discord/github/bind", s.requireCSRF(), s.startDiscordGitHubBind)
	authenticated.POST("/discord/github/unbind", s.requireCSRF(), s.unbindDiscordGitHub)
	authenticated.GET("/system/status", s.systemStatus)
	authenticated.GET("/events/stream", s.eventsStream)

	router.NoRoute(s.serveSPA)
	return router
}

func (s *Server) WebhookRouter() http.Handler {
	router := s.baseRouter()
	router.POST("/webhooks/github", s.githubWebhook)
	return router
}

func (s *Server) rateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := int64(600)
		switch c.Request.URL.Path {
		case "/api/v1/auth/login", "/api/v1/setup/admin":
			limit = 10
		case "/webhooks/github":
			limit = 300
		}
		bucket := time.Now().UTC().Unix() / 60
		key := "tyrs-hand:rate:" + c.ClientIP() + ":" + strconv.FormatInt(bucket, 10)
		count, err := s.redis.Incr(c.Request.Context(), key).Result()
		if err == nil {
			if count == 1 {
				_ = s.redis.Expire(c.Request.Context(), key, 2*time.Minute).Err()
			}
			c.Header("X-RateLimit-Limit", strconv.FormatInt(limit, 10))
			c.Header("X-RateLimit-Remaining", strconv.FormatInt(max(0, limit-count), 10))
			if count > limit {
				problem(c, http.StatusTooManyRequests, "请求过于频繁", nil)
				return
			}
		}
		c.Next()
	}
}

func (s *Server) ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		problem(c, http.StatusServiceUnavailable, "PostgreSQL 不可用", err)
		return
	}
	if err := s.redis.Ping(ctx).Err(); err != nil {
		problem(c, http.StatusServiceUnavailable, "Redis 不可用", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (s *Server) requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func (s *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'")
		c.Next()
	}
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		fields := []zap.Field{zap.String("method", c.Request.Method), zap.String("path", c.Request.URL.Path), zap.Int("status", c.Writer.Status()), zap.Duration("duration", time.Since(started)), zap.String("request_id", c.GetString("request_id"))}
		if len(c.Errors) > 0 {
			fields = append(fields, zap.String("errors", c.Errors.String()))
		}
		s.logger.Info("http request", fields...)
	}
}

func (s *Server) serveSPA(c *gin.Context) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Status(http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(c.Request.URL.Path, "/")
	if path != "" {
		if _, err := fs.Stat(s.assets, path); err == nil {
			if strings.HasPrefix(path, "assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.FileServer(http.FS(s.assets)).ServeHTTP(c.Writer, c.Request)
			return
		}
	}
	c.Header("Cache-Control", "no-cache")
	data, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		problem(c, http.StatusNotFound, "页面不存在", err)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}
