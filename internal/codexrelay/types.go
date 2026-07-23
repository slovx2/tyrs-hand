package codexrelay

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type Role string

const (
	RoleDesktop Role = "desktop"
	RoleWorker  Role = "worker"
)

type Options struct {
	SocketPath              string
	UpstreamSocketPath      string
	RequestTimeout          time.Duration
	LifecycleRequestTimeout time.Duration
	ServerRequestTimeout    time.Duration
	EventBacklog            int
	Controller              Controller
}

type ClientOptions struct {
	Role                 Role
	EventBacklog         int
	ServerRequestHandler codex.ServerRequestHandler
}

type Stats struct {
	UpstreamConnections     int64
	UpstreamInitializations int64
	DesktopConnections      int64
	WorkerConnections       int64
}

type archiveOperation struct {
	done     chan struct{}
	wake     chan struct{}
	result   json.RawMessage
	err      error
	canceled bool
	applying bool
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type ProtocolError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *ProtocolError) Error() string { return e.Message }

type serverOutcome struct {
	result json.RawMessage
	err    error
	role   Role
}

// Call 描述一个 Relay 下游发起的客户端请求。
type Call struct {
	Role   Role
	Method string
	Params json.RawMessage
}

// CallPlan 允许控制层修改参数、直接返回幂等结果，或在上游调用后关联事务状态。
type CallPlan struct {
	Params  json.RawMessage
	Result  json.RawMessage
	Forward bool
	State   any
}

// Controller 只参与需要 Tyrs Hand 事务语义的请求；普通协议流量仍由 Relay 直接转发。
type Controller interface {
	PrepareCall(context.Context, Call) (CallPlan, error)
	CompleteCall(context.Context, Call, CallPlan, json.RawMessage, error) (json.RawMessage, error)
	ResolveInteractive(context.Context, codex.ServerRequest, json.RawMessage, Role) (bool, json.RawMessage, error)
}

// ArchiveGate 在 app-server 已空闲后等待外部 Control Run 完成，再允许官方归档。
// 未实现该接口的嵌入场景仍保持纯 Relay 语义。
type ArchiveGate interface {
	WaitArchiveReady(context.Context, Call, CallPlan) error
}

// PassThroughController 只用于测试和显式信任的嵌入场景。
type PassThroughController struct{}

func (PassThroughController) PrepareCall(_ context.Context, call Call) (CallPlan, error) {
	return CallPlan{Params: append(json.RawMessage(nil), call.Params...), Forward: true}, nil
}

func (PassThroughController) CompleteCall(_ context.Context, _ Call, _ CallPlan,
	result json.RawMessage, cause error,
) (json.RawMessage, error) {
	return result, cause
}

func (PassThroughController) ResolveInteractive(_ context.Context, _ codex.ServerRequest,
	answer json.RawMessage, _ Role,
) (bool, json.RawMessage, error) {
	return true, answer, nil
}

var errSessionClosed = errors.New("relay 下游连接已关闭")

func marshalRaw(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		return append(json.RawMessage(nil), raw...), nil
	}
	return json.Marshal(value)
}

func decodeResult(raw json.RawMessage, target any) error {
	if target == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func requestError(method string, cause error) error {
	return &codex.RequestError{Method: method, State: codex.RequestRejected, Cause: cause}
}
