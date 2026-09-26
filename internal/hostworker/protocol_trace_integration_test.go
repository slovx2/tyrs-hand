//go:build integration

package hostworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

type protocolTraceTransport struct {
	codex.MessageTransport
	mu                   sync.Mutex
	messages             []map[string]any
	expectedClose        string
	parameterErrorMethod string
}

// 负例保留原始报文，仅声明预期参数错误；inventory 仍必须验证实际 -32602 答案。
func (p *protocolTraceTransport) expectParameterError(method string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.parameterErrorMethod = method
}

func (p *protocolTraceTransport) expectClose(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expectedClose = reason
}

func (p *protocolTraceTransport) record(direction string, payload []byte) {
	var message map[string]any
	if json.Unmarshal(payload, &message) != nil {
		return
	}
	message["direction"] = direction
	p.mu.Lock()
	if direction == "client" && message["method"] == p.parameterErrorMethod && p.parameterErrorMethod != "" {
		message["expectedErrorCode"] = -32602
		p.parameterErrorMethod = ""
	}
	p.messages = append(p.messages, message)
	p.mu.Unlock()
}

func (p *protocolTraceTransport) ReadMessage() (int, []byte, error) {
	kind, payload, err := p.MessageTransport.ReadMessage()
	if err == nil {
		p.record("server", payload)
	} else {
		p.mu.Lock()
		if p.expectedClose != "" {
			p.messages = append(p.messages, map[string]any{"transport": map[string]any{
				"event": "closed", "source": "connection", "observed": "read-error",
				"error": err.Error(), "expectedReason": p.expectedClose,
			}})
		}
		p.mu.Unlock()
	}
	return kind, payload, err
}

func (p *protocolTraceTransport) WriteMessage(kind int, payload []byte) error {
	p.record("client", payload)
	return p.MessageTransport.WriteMessage(kind, payload)
}

func (p *protocolTraceTransport) save(t *testing.T, engine runtimeidentity.Engine) {
	t.Helper()
	directory := os.Getenv("PROTOCOL_ARTIFACT_DIR")
	if directory == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	caseName := t.Name()
	caseIDs := []string{"ENTRY-001", "ISOLATION-001", "FAILURE-001"}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexAccountRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"ACCOUNT-001"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeContextInjectionRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"CONTEXT-006"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeShellCommandsRealSSHBothEngines" {
		caseName = rootName
		caseIDs = []string{"SHELL-002"}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeClaudeEventsRealSSH" {
		// 子场景共享根用例执行记录；Codex 仅初始化，不能声称完成 Claude 语义。
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"EVENTS-009"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimePermissionGrantsRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"PERMISSION-011"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexApprovalsRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"APPROVAL-008"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeExperimentalFeaturesRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"FEATURE-002"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeClaudeEventGapsRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"EVENTS-010"}
		}
	}
	data, err := json.MarshalIndent(map[string]any{
		"formatVersion": 1, "runId": os.Getenv("PROTOCOL_RUN_ID"), "engine": engine,
		"caseName": caseName, "caseIds": caseIDs,
		"kind": "wire", "payload": map[string]any{"messages": p.messages, "protocolErrors": []string{}},
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "wire-runtime-"+string(engine)+"-"+uuid.NewString()+".json"), data, 0o600))
}
