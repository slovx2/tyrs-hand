package config

import (
	"fmt"
	"net"
	"path/filepath"
)

func (c Config) PiStateDir() string    { return filepath.Join(c.WorkerDataRoot, "pi") }
func (c Config) PiAdapterHome() string { return filepath.Join(c.PiStateDir(), "config") }
func (c Config) PiHostKeyFile() string { return filepath.Join(c.PiStateDir(), "ssh", "host_key") }

func (c Config) validatePiRuntime() error {
	if !c.WorkerPiEnabled {
		return nil
	}
	if !filepath.IsAbs(c.WorkerPiBin) {
		return fmt.Errorf("必须为 Pi 适配器配置绝对路径")
	}
	pi, err := net.ResolveTCPAddr("tcp", c.WorkerPiSSHListenAddr)
	if err != nil {
		return err
	}
	if pi.Port <= 0 {
		return fmt.Errorf("必须固定 Pi SSH 端口")
	}
	others := []string{c.WorkerSSHListenAddr}
	if c.WorkerClaudeEnabled {
		others = append(others, c.WorkerClaudeSSHListenAddr)
	}
	for _, value := range others {
		address, err := net.ResolveTCPAddr("tcp", value)
		if err != nil {
			return err
		}
		if address.Port == pi.Port && (len(pi.IP) == 0 || len(address.IP) == 0 || pi.IP.IsUnspecified() || address.IP.IsUnspecified() || pi.IP.Equal(address.IP)) {
			return fmt.Errorf("pi SSH 端口与其他引擎冲突")
		}
	}
	return nil
}
