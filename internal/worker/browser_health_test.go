package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBrowserHealthMonitorReadyDegradedAndTransportErrors(t *testing.T) {
	state := "ready"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		switch state {
		case "ready":
			_, _ = response.Write([]byte(`{"status":"ready","bridgeVersion":"0.1.0","extensionVersion":"0.2.0","chromeVersion":"Chrome/140","profile":"current","tabCount":4,"lastSeenAt":"2026-07-22T00:00:00Z"}`))
		case "degraded":
			_, _ = response.Write([]byte(`{"status":"degraded","bridgeVersion":"0.1.0","tabCount":0}`))
		case "invalid":
			_, _ = response.Write([]byte(`{"status":`))
		default:
			response.WriteHeader(http.StatusServiceUnavailable)
		}
	}))

	monitor, err := newBrowserHealthMonitor(server.URL + "/mcp")
	require.NoError(t, err)
	monitor.Refresh(context.Background())
	status := monitor.Status()
	require.Equal(t, "ready", status.Status)
	require.Equal(t, "0.1.0", status.BridgeVersion)
	require.Equal(t, "0.2.0", status.ExtensionVersion)
	require.Equal(t, 4, status.TabCount)

	state = "degraded"
	monitor.Refresh(context.Background())
	status = monitor.Status()
	require.Equal(t, "degraded", status.Status)
	require.Empty(t, status.LastError)

	state = "invalid"
	monitor.Refresh(context.Background())
	require.Equal(t, "degraded", monitor.Status().Status)
	require.NotEmpty(t, monitor.Status().LastError)
	state = "unavailable"
	monitor.Refresh(context.Background())
	require.Contains(t, monitor.Status().LastError, "503")

	server.Close()
	monitor.Refresh(context.Background())
	require.Equal(t, "degraded", monitor.Status().Status)
	require.NotEmpty(t, monitor.Status().LastError)
}

func TestBrowserHealthMonitorRejectsInvalidURL(t *testing.T) {
	_, err := newBrowserHealthMonitor("not-an-absolute-url")
	require.ErrorContains(t, err, "无效")
}
