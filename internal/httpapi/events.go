package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *Server) eventsStream(c *gin.Context) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		problem(c, http.StatusInternalServerError, "当前响应不支持事件流", nil)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	subscriber := s.redis.Subscribe(c.Request.Context(), "tyrs-hand:events")
	defer subscriber.Close()
	messages := subscriber.Channel()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case message, ok := <-messages:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(c.Writer, "event: update\ndata: %s\n\n", message.Payload)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = fmt.Fprint(c.Writer, ": keepalive\n\n")
			flusher.Flush()
		case <-c.Request.Context().Done():
			return
		}
	}
}
