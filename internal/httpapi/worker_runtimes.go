package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (s *Server) getWorkerRuntimes(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if !s.requireWorkerAccess(c, id) {
		return
	}
	runtimes, err := s.workers.Runtimes(c.Request.Context(), id)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取运行时失败", err)
		return
	}
	c.JSON(http.StatusOK, runtimes)
}
