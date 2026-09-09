package appserverhub

import (
	"encoding/json"
	"fmt"
)

// SSH/WebSocket 的 RoleDesktop 同时包含手机和内部临时客户端，不能用它判断工具执行能力。
// Codex Desktop 的正式 initialize.clientInfo.name 为 "Codex Desktop"；移动端为 tyrs_hand_mobile。
func (s *session) identifyClient(params json.RawMessage) error {
	var value struct {
		ClientInfo struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	if err := json.Unmarshal(params, &value); err != nil {
		return fmt.Errorf("解析客户端身份: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identitySet && s.clientName != value.ClientInfo.Name {
		return fmt.Errorf("连接初始化后不能更换客户端身份")
	}
	s.clientName, s.identitySet = value.ClientInfo.Name, true
	return nil
}

func (s *session) initializeDesktopTools() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.desktopTools = s.role == RoleDesktop && s.identitySet && s.clientName == "Codex Desktop"
}

func (s *session) canExecuteDesktopTools() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.role == RoleDesktop && s.desktopTools && !s.closed
}
