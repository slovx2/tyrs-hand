package appserverhub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
)

type connectionResource struct {
	owner                    int64
	kind, field, id, cleanup string
}

// 单条上游连接不能共享客户端的连接级标识，否则相同 watch/进程 ID 会互相覆盖。
func (r *Hub) scopeResourceCall(source *session, method string, params json.RawMessage) (json.RawMessage, func(error), error) {
	kind, field, create, release, cleanup := "", "", false, false, ""
	switch method {
	case "fs/watch", "fs/unwatch":
		kind, field, create, release, cleanup = "watch", "watchId", method == "fs/watch", method == "fs/unwatch", "fs/unwatch"
	case "command/exec", "command/exec/write", "command/exec/resize", "command/exec/terminate":
		kind, field, create, cleanup = "command", "processId", method == "command/exec", "command/exec/terminate"
	case "process/spawn", "process/writeStdin", "process/resizePty", "process/kill":
		kind, field, create, cleanup = "process", "processHandle", method == "process/spawn", "process/kill"
	default:
		return params, func(error) {}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(params, &values); err != nil {
		return nil, nil, &ProtocolError{Code: -32602, Message: "连接资源参数无效"}
	}
	var id string
	if method == "command/exec" && (len(values[field]) == 0 || string(values[field]) == "null") &&
		string(values["tty"]) != "true" && string(values["streamStdin"]) != "true" && string(values["streamStdoutStderr"]) != "true" {
		values[field], _ = json.Marshal(uuid.NewString())
	}
	if json.Unmarshal(values[field], &id) != nil || id == "" {
		// 缺参及类型错误由真实上游校验。
		return params, func(error) {}, nil
	}
	scoped := fmt.Sprintf("tyrs:%d:%s:%s", source.id, kind, base64.RawURLEncoding.EncodeToString([]byte(id)))
	r.mu.Lock()
	if r.resources == nil {
		r.resources = make(map[string]connectionResource)
	}
	if create {
		if _, exists := r.resources[scoped]; exists {
			r.mu.Unlock()
			return nil, nil, &ProtocolError{Code: -32602, Message: field + " 已在当前连接使用"}
		}
		if r.sessions[source.id] != source {
			r.mu.Unlock()
			return nil, nil, errSessionClosed
		}
		r.resources[scoped] = connectionResource{owner: source.id, kind: kind, field: field, id: scoped, cleanup: cleanup}
	}
	r.mu.Unlock()
	values[field], _ = json.Marshal(scoped)
	encoded, err := json.Marshal(values)
	return encoded, func(cause error) {
		if create && cause != nil && r.upstream != nil {
			// 响应丢失或连接取消时可能已经创建进程，必须撤销，不能遗留后台副作用。
			go r.cleanupResource(connectionResource{field: field, id: scoped, cleanup: cleanup})
		}
		if (create && cause != nil) || (release && cause == nil) || method == "command/exec" {
			r.mu.Lock()
			delete(r.resources, scoped)
			r.mu.Unlock()
		}
		if create && cause == nil && kind != "command" {
			r.mu.Lock()
			closed := r.sessions[source.id] != source
			r.mu.Unlock()
			if closed {
				// 断线清理可能先于上游创建；创建响应后再次撤销，避免泄漏 watch/进程。
				go r.cleanupResource(connectionResource{field: field, id: scoped, cleanup: cleanup})
			}
		}
	}, err
}

func (r *Hub) cleanupResource(resource connectionResource) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result any
	_ = r.upstream.Call(ctx, resource.cleanup, map[string]any{resource.field: resource.id}, &result)
}

// 返回 true 表示这是连接级事件，未知或已断开的归属也必须丢弃，不能广播。
func (r *Hub) forwardResourceEvent(event codex.Event) bool {
	field, kind := "", ""
	switch event.Method {
	case "fs/changed":
		field, kind = "watchId", "watch"
	case "command/exec/outputDelta":
		field, kind = "processId", "command"
	case "process/outputDelta", "process/exited":
		field, kind = "processHandle", "process"
	default:
		return false
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(event.Params, &values) != nil {
		return true
	}
	var scoped string
	if json.Unmarshal(values[field], &scoped) != nil {
		return true
	}
	parts := strings.SplitN(scoped, ":", 4)
	if len(parts) != 4 || parts[0] != "tyrs" || parts[2] != kind {
		return true
	}
	owner, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return true
	}
	id, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return true
	}
	r.mu.Lock()
	target := r.sessions[owner]
	if event.Method == "process/exited" {
		delete(r.resources, scoped)
	}
	r.mu.Unlock()
	if target != nil {
		values[field], _ = json.Marshal(string(id))
		event.Params, _ = json.Marshal(values)
		if err := target.publish(event); err != nil {
			// 回调运行于上游 reader，关闭客户端的 RPC 清理不能反过来等待 reader。
			go r.removeSession(target)
		}
	}
	return true
}

func (r *Hub) closeSessionResources(owner int64) {
	r.mu.Lock()
	var resources []connectionResource
	for id, resource := range r.resources {
		if resource.owner == owner {
			resources = append(resources, resource)
			delete(r.resources, id)
		}
	}
	r.mu.Unlock()
	if len(resources) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, resource := range resources {
			var result any
			_ = r.upstream.Call(ctx, resource.cleanup, map[string]any{resource.field: resource.id}, &result)
		}
	}()
}
