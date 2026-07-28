package replygate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const HookCommand = "tyrs-hand-reply-hook"
const HookTimeoutSeconds = 5
const sessionFlagsConfigPath = "/<session-flags>/config.toml"

type State struct {
	Required   bool   `json:"required"`
	Delivered  bool   `json:"delivered"`
	Bypass     bool   `json:"bypass"`
	IntentID   string `json:"intentId"`
	BlockCount int    `json:"blockCount"`
	MaxBlocks  int    `json:"maxBlocks"`
}

type Decision struct {
	Block  bool
	Reason string
}

func Evaluate(codexHome, threadID string) Decision {
	state, err := Read(codexHome, threadID)
	if err != nil || !state.Required || state.Delivered || state.Bypass {
		return Decision{}
	}
	state.BlockCount++
	if Write(codexHome, threadID, state) != nil || state.BlockCount > state.MaxBlocks {
		return Decision{}
	}
	return Decision{Block: true,
		Reason: "You must call tyrs_hand.reply_to_github once with the final user-facing result before ending this turn."}
}

// Install 清理旧版写入 CODEX_HOME 的全局 Hook。
// GitHub 回复门禁只通过 SessionConfig 注入目标 Thread，避免影响同一 Home 中的其他会话。
func Install(codexHome string) error {
	hookPath := filepath.Join(codexHome, "hooks.json")
	if err := removeInstalledHookConfig(codexHome); err != nil {
		return err
	}
	if err := os.Remove(hookPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SessionConfig 返回 app-server 当前 Thread 使用的 Hook 配置。
// Codex 将 thread/start 的配置视为 /<session-flags>/config.toml，信任键必须使用该虚拟路径。
func SessionConfig() map[string]any {
	return map[string]any{
		"hooks": map[string]any{
			"Stop": []any{map[string]any{
				"hooks": []any{hookDefinition()},
			}},
			"state": map[string]any{
				sessionFlagsConfigPath + ":stop:0:0": map[string]any{
					"trusted_hash": hookTrustedHash(),
				},
			},
		},
	}
}

func Initialize(codexHome, threadID, intentID string, required bool, maxBlocks int) error {
	if maxBlocks <= 0 {
		maxBlocks = 3
	}
	return Write(codexHome, threadID, State{
		Required: required, IntentID: intentID, MaxBlocks: maxBlocks,
	})
}

func MarkDelivered(codexHome, threadID string) error {
	state, err := Read(codexHome, threadID)
	if err != nil {
		return err
	}
	state.Delivered = true
	return Write(codexHome, threadID, state)
}

func SetBypass(codexHome, threadID string) error {
	state, err := Read(codexHome, threadID)
	if err != nil {
		return err
	}
	state.Bypass = true
	return Write(codexHome, threadID, state)
}

func Read(codexHome, threadID string) (State, error) {
	var state State
	data, err := os.ReadFile(Path(codexHome, threadID))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func Write(codexHome, threadID string, state State) error {
	if !validThreadID(threadID) {
		return errors.New("codex thread ID 不能用于回复门禁文件")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(Path(codexHome, threadID), append(data, '\n'), 0o600)
}

func Path(codexHome, threadID string) string {
	return filepath.Join(codexHome, ".tyrs-hand", "reply-gates", threadID+".json")
}

func validThreadID(value string) bool {
	return value != "" && filepath.Base(value) == value && value != "." && value != ".."
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func removeInstalledHookConfig(codexHome string) error {
	configPath := filepath.Join(codexHome, "config.toml")
	existing, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	content := removeBlock(string(existing), "# BEGIN TYRS HAND REPLY HOOK", "# END TYRS HAND REPLY HOOK")
	if content == string(existing) {
		return nil
	}
	content = strings.TrimSpace(content)
	if content != "" {
		content += "\n"
	}
	return atomicWrite(configPath, []byte(content), 0o600)
}

func hookDefinition() map[string]any {
	return map[string]any{
		"type": "command", "command": HookCommand, "timeout": HookTimeoutSeconds,
	}
}

func hookTrustedHash() string {
	identity := fmt.Sprintf(`{"event_name":"stop","hooks":[{"async":false,"command":%s,"timeout":%d,"type":"command"}]}`,
		strconv.Quote(HookCommand), HookTimeoutSeconds)
	digest := sha256.Sum256([]byte(identity))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func removeBlock(value, begin, end string) string {
	start := strings.Index(value, begin)
	if start < 0 {
		return value
	}
	stop := strings.Index(value[start:], end)
	if stop < 0 {
		return value[:start]
	}
	return value[:start] + value[start+stop+len(end):]
}
