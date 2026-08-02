//go:build integration

package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/executionnode"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
)

func TestClientDevicePairingApprovalAndRevocation(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "device-setup-token", "")
	_, err = authService.Setup(ctx, "device-setup-token", "device-admin", "test-password-123")
	require.NoError(t, err)

	var administratorID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT id FROM administrators WHERE username='device-admin'").Scan(&administratorID))
	_, endpoint := clientDeviceIntegrationServer(t, db, authService, administratorID)

	created := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client-device-pairings", "", map[string]any{})
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var pairing struct {
		ID         uuid.UUID `json:"id"`
		PairingURI string    `json:"pairingUri"`
		QRDataURL  string    `json:"qrDataUrl"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &pairing))
	require.NotEqual(t, uuid.Nil, pairing.ID)
	require.True(t, strings.HasPrefix(pairing.QRDataURL, "data:image/png;base64,"))

	pairingURL, err := url.Parse(pairing.PairingURI)
	require.NoError(t, err)
	require.Equal(t, "tyrshand", pairingURL.Scheme)
	require.Equal(t, "device-pair", pairingURL.Host)
	require.Equal(t, "2", pairingURL.Query().Get("v"))
	_, err = uuid.Parse(pairingURL.Query().Get("serverId"))
	require.NoError(t, err)
	require.Equal(t, pairing.ID.String(), pairingURL.Query().Get("pairingId"))
	pairingSecret := pairingURL.Query().Get("secret")
	require.NotEmpty(t, pairingSecret)

	deviceID := uuid.New()
	deviceToken := "tdv1." + deviceID.String() + ".permanent-device-secret"
	claim := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/claim", "",
		map[string]any{
			"pairingSecret":  pairingSecret,
			"deviceId":       deviceID,
			"name":           "Pixel E2E",
			"platform":       "android",
			"credentialHash": security.Digest(deviceToken),
		})
	require.Equal(t, http.StatusOK, claim.Code, claim.Body.String())
	var claimed struct {
		ClaimToken string `json:"claimToken"`
		Status     string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(claim.Body.Bytes(), &claimed))
	require.NotEmpty(t, claimed.ClaimToken)
	require.Equal(t, "waiting_confirmation", claimed.Status)
	secondClaim := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/claim", "",
		map[string]any{
			"pairingSecret":  pairingSecret,
			"deviceId":       uuid.New(),
			"name":           "Second device",
			"platform":       "android",
			"credentialHash": security.Digest("second-device-token"),
		})
	require.Equal(t, http.StatusUnauthorized, secondClaim.Code)

	beforeApproval := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/bootstrap", deviceToken, nil)
	require.Equal(t, http.StatusUnauthorized, beforeApproval.Code)

	status := clientPairingStatusRequest(t,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/status",
		claimed.ClaimToken)
	require.Equal(t, http.StatusOK, status.Code)
	require.Contains(t, status.Body.String(), "waiting_confirmation")

	approved := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client-device-pairings/"+pairing.ID.String()+"/approve", "",
		map[string]any{})
	require.Equal(t, http.StatusOK, approved.Code, approved.Body.String())
	require.Contains(t, approved.Body.String(), "Pixel E2E")

	afterApproval := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/bootstrap", deviceToken, nil)
	require.Equal(t, http.StatusOK, afterApproval.Code, afterApproval.Body.String())
	listed := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client-devices", "", nil)
	require.Equal(t, http.StatusOK, listed.Code)
	require.Contains(t, listed.Body.String(), "Pixel E2E")

	wsURL := "ws" + strings.TrimPrefix(endpoint, "http") + "/api/v1/client/updates?cursor=0"
	protocol := clientBearerWebSocketPrefix + deviceToken
	connection, response, err := (&websocket.Dialer{Subprotocols: []string{protocol}}).Dial(wsURL, nil)
	require.NoError(t, err)
	require.Equal(t, protocol, response.Header.Get("Sec-WebSocket-Protocol"))
	require.NoError(t, connection.Close())

	approvedStatus := clientPairingStatusRequest(t,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/status",
		claimed.ClaimToken)
	require.Equal(t, http.StatusOK, approvedStatus.Code)
	require.Contains(t, approvedStatus.Body.String(), "approved")

	deleted := clientJSONRequest(t, http.MethodDelete,
		endpoint+"/api/v1/client-devices/"+deviceID.String(), "", nil)
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())

	afterDeletion := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/bootstrap", deviceToken, nil)
	require.Equal(t, http.StatusUnauthorized, afterDeletion.Code)
}

func clientDeviceIntegrationServer(t *testing.T, db *sql.DB, authService *auth.Service,
	administratorID uuid.UUID,
) (*Server, string) {
	t.Helper()
	server := &Server{cfg: config.Config{LeaseDuration: time.Minute, PublicURL: "http://127.0.0.1"},
		db: db, auth: authService, nodes: executionnode.NewService(db),
		clientUpdateHub: newClientUpdateHub()}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	administrator := router.Group("/api/v1")
	administrator.Use(func(c *gin.Context) {
		c.Set("session", auth.Session{AdministratorID: administratorID, Username: "device-admin"})
		c.Next()
	})
	administrator.GET("/client-devices", server.listClientDevices)
	administrator.POST("/client-device-pairings", server.createClientDevicePairing)
	administrator.POST("/client-device-pairings/:id/approve", server.approveClientDevicePairing)
	administrator.DELETE("/client-devices/:id", server.deleteClientDevice)
	router.POST("/api/v1/client/device-pairings/:id/claim", server.claimClientDevicePairing)
	router.GET("/api/v1/client/device-pairings/:id/status", server.clientDevicePairingStatus)
	client := router.Group("/api/v1/client")
	client.Use(server.requireClientBearer())
	client.GET("/bootstrap", server.clientBootstrap)
	client.GET("/updates", server.clientUpdates)
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)
	return server, httpServer.URL
}

func clientPairingStatusRequest(t *testing.T, endpoint, claimToken string) *httptest.ResponseRecorder {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Pairing "+claimToken)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	recorder := httptest.NewRecorder()
	recorder.Code = response.StatusCode
	_, err = io.Copy(recorder.Body, response.Body)
	require.NoError(t, err)
	return recorder
}
