package protocol

import (
	_ "embed"
	"encoding/json"
)

//go:embed adapter-lock.json
var adapterLockJSON []byte

// AdapterLock 是构建时固定的版本组合，不读取运行目录中的可变配置。
var AdapterLock = func() struct {
	Node           string `json:"node"`
	ClaudeAgentSDK string `json:"claudeAgentSdk"`
	ClaudeCLI      string `json:"claudeCli"`
	PiCodingAgent  string `json:"piCodingAgent"`
	PiCLI          string `json:"piCli"`
	PiPlanMode     string `json:"piPlanMode"`
	PiTuiKit       string `json:"piTuiKit"`
	PiSubagents    string `json:"piSubagents"`
} {
	var lock struct {
		Node           string `json:"node"`
		ClaudeAgentSDK string `json:"claudeAgentSdk"`
		ClaudeCLI      string `json:"claudeCli"`
		PiCodingAgent  string `json:"piCodingAgent"`
		PiCLI          string `json:"piCli"`
		PiPlanMode     string `json:"piPlanMode"`
		PiTuiKit       string `json:"piTuiKit"`
		PiSubagents    string `json:"piSubagents"`
	}
	if err := json.Unmarshal(adapterLockJSON, &lock); err != nil {
		panic(err)
	}
	return lock
}()
