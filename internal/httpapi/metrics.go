package httpapi

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "tyrs_hand", Subsystem: "http", Name: "requests_total",
		Help: "HTTP 请求总数。",
	}, []string{"method", "route", "status"})
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "tyrs_hand", Subsystem: "http", Name: "request_duration_seconds",
		Help: "HTTP 请求耗时。", Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
	queueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "tyrs_hand", Name: "queue_depth", Help: "按状态统计任务数量。",
	}, []string{"status"})
	workerCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "tyrs_hand", Name: "workers", Help: "按状态统计 Worker 数量。",
	}, []string{"status"})
)

func init() {
	prometheus.MustRegister(httpRequests, httpDuration, queueDepth, workerCount)
}

func (s *Server) refreshOperationalMetrics(ctx context.Context) {
	for _, status := range []string{"queued", "dispatching", "awaiting_confirmation", "running", "reconciling", "retry_wait", "completed", "failed", "canceled"} {
		var count int64
		if s.db.QueryRowContext(ctx, "SELECT count(*) FROM codex_turn_intents WHERE status = $1", status).Scan(&count) == nil {
			queueDepth.WithLabelValues(status).Set(float64(count))
		}
	}
	for _, status := range []string{"online", "offline"} {
		var count int64
		if s.db.QueryRowContext(ctx, "SELECT count(*) FROM worker_nodes WHERE status = $1", status).Scan(&count) == nil {
			workerCount.WithLabelValues(status).Set(float64(count))
		}
	}
}

func metricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		httpRequests.WithLabelValues(c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Inc()
		httpDuration.WithLabelValues(c.Request.Method, route).Observe(time.Since(started).Seconds())
	}
}
