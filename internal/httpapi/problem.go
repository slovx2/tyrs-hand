package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

func problem(c *gin.Context, status int, title string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
		_ = c.Error(err)
	}
	if status >= http.StatusInternalServerError {
		detail = "服务器内部错误，请使用 Request ID 查询日志"
	}
	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(status, Problem{
		Type: "about:blank", Title: title, Status: status, Detail: detail, Instance: c.Request.URL.Path, RequestID: c.GetString("request_id"),
	})
}

func badRequest(c *gin.Context, err error) { problem(c, http.StatusBadRequest, "请求无效", err) }
