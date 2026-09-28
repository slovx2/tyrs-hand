package appserverhub

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
)

const mcpStreamKind = "mcp-stream"

// 订阅属于连接而非 Thread 广播；每次创建分配新上游 ID，隔离复用本地 ID 后的迟到事件。
func (r *Hub) scopeMcpStreamCall(source *session, method string, params json.RawMessage) (json.RawMessage, func(error), error) {
	var values map[string]json.RawMessage
	if json.Unmarshal(params, &values) != nil {
		return nil, nil, &ProtocolError{Code: -32602, Message: "MCP 订阅参数无效"}
	}
	var id, threadID string
	if json.Unmarshal(values["subscriptionId"], &id) != nil || id == "" {
		return nil, nil, &ProtocolError{Code: -32602, Message: "MCP subscriptionId 无效"}
	}
	create := method == "mcpServer/event/stream/start"
	if create && (json.Unmarshal(values["threadId"], &threadID) != nil || threadID == "") {
		return nil, nil, &ProtocolError{Code: -32602, Message: "MCP threadId 无效"}
	}
	r.mu.Lock()
	if r.sessions[source.id] != source {
		r.mu.Unlock()
		return nil, nil, errSessionClosed
	}
	if create && source.role == RoleDesktop && !source.subscribed(threadID) {
		r.mu.Unlock()
		return nil, nil, &ProtocolError{Code: -32602, Message: "当前连接未订阅指定会话"}
	}
	resource := connectionResource{owner: source.id, kind: mcpStreamKind, field: "subscriptionId",
		id: "tyrs:mcp-stream:" + uuid.NewString(), cleanup: "mcpServer/event/stream/stop", clientID: id, threadID: threadID}
	for _, current := range r.resources {
		if current.owner == source.id && current.kind == mcpStreamKind && current.clientID == id {
			if create {
				r.mu.Unlock()
				return nil, nil, &ProtocolError{Code: -32602, Message: "subscriptionId 已在当前连接使用"}
			}
			resource = current
			break
		}
	}
	if create {
		if r.resources == nil {
			r.resources = make(map[string]connectionResource)
		}
		r.resources[resource.id] = resource
	}
	r.mu.Unlock()
	values["subscriptionId"], _ = json.Marshal(resource.id)
	encoded, err := json.Marshal(values)
	return encoded, func(cause error) {
		r.mu.Lock()
		closed := r.sessions[source.id] != source
		current, exists := r.resources[resource.id]
		// 取消 Thread 订阅可能先于启动响应；不让迟到成功恢复已撤销的订阅。
		revoked := !exists || current != resource
		cleanup := create && (cause != nil || closed || revoked)
		if cleanup || (!create && cause == nil) {
			delete(r.resources, resource.id)
		}
		r.mu.Unlock()
		if cleanup && r.upstream != nil {
			go r.cleanupResource(resource)
		}
	}, err
}

func (r *Hub) forwardMcpStreamEvent(event codex.Event) {
	var values map[string]json.RawMessage
	if json.Unmarshal(event.Params, &values) != nil {
		return
	}
	var id string
	if json.Unmarshal(values["subscriptionId"], &id) != nil {
		return
	}
	var notification struct{ Method string }
	_ = json.Unmarshal(values["notification"], &notification)
	r.mu.Lock()
	resource, exists := r.resources[id]
	target := r.sessions[resource.owner]
	if exists && resource.kind == mcpStreamKind && notification.Method == "notifications/events/terminated" {
		delete(r.resources, id)
	}
	r.mu.Unlock()
	if !exists || resource.kind != mcpStreamKind || target == nil {
		return
	}
	values["subscriptionId"], _ = json.Marshal(resource.clientID)
	event.Params, _ = json.Marshal(values)
	if err := target.publish(event); err != nil {
		go r.removeSession(target)
	}
}

func (r *Hub) closeThreadMcpStreams(owner int64, threadID string) {
	r.mu.Lock()
	var removed []connectionResource
	for id, resource := range r.resources {
		if resource.owner == owner && resource.kind == mcpStreamKind && resource.threadID == threadID {
			removed = append(removed, resource)
			delete(r.resources, id)
		}
	}
	r.mu.Unlock()
	for _, resource := range removed {
		if r.upstream != nil {
			go r.cleanupResource(resource)
		}
	}
}
