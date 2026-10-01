package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestNonCodexRuntimeRequiresDeclaredScope(t *testing.T) {
	for _, engine := range []string{"codex", "claude-code", "pi"} {
		for _, path := range []string{"/worker/v1/runs/:id/workspace-state", "/worker/v1/future", "/worker/v1/claims"} {
			t.Run(engine+path, func(t *testing.T) {
				router := gin.New()
				router.GET(path, func(c *gin.Context) {
					if validateWorkerRuntimeScope(c) {
						c.Status(http.StatusOK)
					}
				})
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.Header.Set(workerprotocol.EngineHeader, engine)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				want := http.StatusOK
				if engine != "codex" && path != "/worker/v1/claims" {
					want = http.StatusNotImplemented
				}
				require.Equal(t, want, response.Code)
			})
		}
	}
}

func TestPiTitleClaimDoesNotAccessDatabase(t *testing.T) {
	router := gin.New()
	router.POST("/claim", (&Server{}).workerClaimSessionTitle)
	request := httptest.NewRequest(http.MethodPost, "/claim", nil)
	request.Header.Set(workerprotocol.EngineHeader, "pi")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{}`, response.Body.String())
}
