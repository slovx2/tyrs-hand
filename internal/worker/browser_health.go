package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type browserCapabilityStatus struct {
	Status           string `json:"status"`
	BridgeVersion    string `json:"bridgeVersion,omitempty"`
	ExtensionVersion string `json:"extensionVersion,omitempty"`
	ChromeVersion    string `json:"chromeVersion,omitempty"`
	Profile          string `json:"profile,omitempty"`
	TabCount         int    `json:"tabCount"`
	LastSeenAt       string `json:"lastSeenAt,omitempty"`
	LastError        string `json:"lastError,omitempty"`
}

type browserHealthMonitor struct {
	endpoint string
	client   *http.Client
	mu       sync.RWMutex
	status   browserCapabilityStatus
}

func newBrowserHealthMonitor(mcpURL string) (*browserHealthMonitor, error) {
	endpoint, err := url.Parse(mcpURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, errors.New("浏览器 MCP URL 无效")
	}
	endpoint.Path, endpoint.RawQuery, endpoint.Fragment = "/health", "", ""
	return &browserHealthMonitor{endpoint: endpoint.String(),
		client: &http.Client{Timeout: 3 * time.Second},
		status: browserCapabilityStatus{Status: "starting"}}, nil
}

func (m *browserHealthMonitor) Refresh(ctx context.Context) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint, nil)
	if err != nil {
		m.setError(err)
		return
	}
	response, err := m.client.Do(request)
	if err != nil {
		m.setError(err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		m.setError(errors.New("浏览器 Bridge 健康检查返回 " + response.Status))
		return
	}
	var value struct {
		Status           string `json:"status"`
		BridgeVersion    string `json:"bridgeVersion"`
		ExtensionVersion string `json:"extensionVersion"`
		ChromeVersion    string `json:"chromeVersion"`
		Profile          string `json:"profile"`
		TabCount         int    `json:"tabCount"`
		LastSeenAt       string `json:"lastSeenAt"`
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		m.setError(err)
		return
	}
	status := strings.TrimSpace(value.Status)
	if status != "ready" {
		status = "degraded"
	}
	m.mu.Lock()
	m.status = browserCapabilityStatus{Status: status, BridgeVersion: value.BridgeVersion,
		ExtensionVersion: value.ExtensionVersion, ChromeVersion: value.ChromeVersion,
		Profile: value.Profile, TabCount: value.TabCount, LastSeenAt: value.LastSeenAt}
	m.mu.Unlock()
}

func (m *browserHealthMonitor) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Status = "degraded"
	m.status.LastError = err.Error()
}

func (m *browserHealthMonitor) Status() browserCapabilityStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}
