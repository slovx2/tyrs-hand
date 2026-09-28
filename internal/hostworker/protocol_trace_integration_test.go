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
	mu                  sync.Mutex
	messages            []map[string]any
	expectedClose       string
	expectedErrorMethod string
	expectedErrorCode   int
}

// 负例保留原始报文，仅声明预期参数错误；inventory 仍必须验证实际 -32602 答案。
func (p *protocolTraceTransport) expectParameterError(method string) {
	p.expectRequestError(method, -32602)
}

// 在发送负例前声明精确错误码，正式门禁仍检查原生响应，不能把任意失败算通过。
func (p *protocolTraceTransport) expectRequestError(method string, code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expectedErrorMethod = method
	p.expectedErrorCode = code
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
	if direction == "client" && message["method"] == p.expectedErrorMethod && p.expectedErrorMethod != "" {
		message["expectedErrorCode"] = p.expectedErrorCode
		p.expectedErrorMethod = ""
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
	if caseName == "TestRuntimeCodexMcpEventStreamRealSSH" {
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"MCP-021"}
		}
	}
	if caseName == "TestRuntimeApprovalArbitrationRealSSH" {
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"APPROVAL-001"}
		}
	}
	if caseName == "TestRuntimeCodexTimelineRealSSH" {
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"HISTORY-006"}
		}
	}
	if caseName == "TestRuntimeClaudeTimelineRealSSH" {
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"HISTORY-007"}
		}
	}
	if caseName == "TestRuntimeCodexAttachmentsRealSSH" {
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"ATTACHMENT-001"}
		}
	}
	if caseName == "TestRuntimeCodexProjectsRealSSH" {
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"PROJECT-001"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexErrorRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"EVENTS-011"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexPluginsRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"PLUGIN-002"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexContextRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"CONTEXT-007"}
		}
	}
	if caseName == "TestRuntimeCodexTurnSettingsRealSSH" {
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"TURNSETTINGS-003"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexReviewRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"REVIEW-006"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeReviewRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"REVIEW-004"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexMcpPaginationRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"MCP-017"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeParallelApprovalsRealSSHBothEngines" {
		caseName = rootName
		caseIDs = []string{"ISOLATION-005"}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexMcpRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"MCP-015", "MCP-016"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexMcpOAuthRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"MCP-018"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexMcpOAuthHeadersRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"MCP-019"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeCodexAccountRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Codex {
			caseIDs = []string{"ACCOUNT-001"}
		}
	}
	if rootName, _, _ := strings.Cut(caseName, "/"); rootName == "TestRuntimeClaudeAccountRealSSH" {
		caseName = rootName
		caseIDs = []string{}
		if engine == runtimeidentity.Claude {
			caseIDs = []string{"ACCOUNT-003"}
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
