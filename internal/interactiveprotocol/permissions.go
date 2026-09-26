package interactiveprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

type permissionProfile struct {
	Network    *networkPermission    `json:"network,omitempty"`
	FileSystem *fileSystemPermission `json:"fileSystem,omitempty"`
}

type networkPermission struct {
	Enabled *bool `json:"enabled"`
}

type fileSystemPermission struct {
	Read             []string                    `json:"read"`
	Write            []string                    `json:"write"`
	GlobScanMaxDepth *uint64                     `json:"globScanMaxDepth,omitempty"`
	Entries          []fileSystemPermissionEntry `json:"entries,omitempty"`
}

type fileSystemPermissionEntry struct {
	Path   json.RawMessage `json:"path"`
	Access string          `json:"access"`
}

type permissionAnswer struct {
	Permissions      permissionProfile `json:"permissions"`
	Scope            string            `json:"scope"`
	StrictAutoReview *bool             `json:"strictAutoReview,omitempty"`
}

func permissionQuestions(params json.RawMessage) (json.RawMessage, error) {
	profile, err := permissionRequestProfile(params)
	if err != nil {
		return nil, err
	}
	var details struct{ CWD, Reason string }
	if err := json.Unmarshal(params, &details); err != nil {
		return nil, err
	}
	permissions, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return nil, err
	}
	return json.Marshal([]map[string]any{{
		"id": "approval", "header": "权限审批",
		"question": fmt.Sprintf("目录：%s\n原因：%s\n请求权限：\n%s", details.CWD, details.Reason, permissions),
		"options": []map[string]string{
			{"label": "允许本轮", "description": "只批准所列权限，当前回合结束后失效"},
			{"label": "拒绝", "description": "不授予任何新增权限"},
			{"label": "允许本会话", "description": "明确允许所列权限在本会话后续回合继续使用"},
		},
	}})
}

func decodeStrictObject(raw json.RawMessage, target any) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return errors.New("交互参数必须是 JSON 对象")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("交互参数必须只有一个 JSON 对象")
	}
	return nil
}

func permissionRequestProfile(params json.RawMessage) (permissionProfile, error) {
	var request struct {
		Permissions json.RawMessage `json:"permissions"`
	}
	var profile permissionProfile
	if err := json.Unmarshal(params, &request); err != nil {
		return profile, err
	}
	if err := decodeStrictObject(request.Permissions, &profile); err != nil {
		return profile, fmt.Errorf("原生权限提案无效: %w", err)
	}
	if !validPermissionEntries(profile) {
		return profile, errors.New("原生权限提案包含无效路径")
	}
	return profile, nil
}

func normalizePermissionAnswer(params, answer json.RawMessage) (json.RawMessage, error) {
	requested, err := permissionRequestProfile(params)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(answer, &fields) != nil || fields == nil {
		return nil, errors.New("权限答案必须是对象")
	}
	var granted permissionAnswer
	if _, choices := fields["answers"]; choices {
		var selected struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		}
		if decodeStrictObject(answer, &selected) != nil || len(selected.Answers) != 1 || len(selected.Answers["approval"].Answers) != 1 {
			return nil, errors.New("权限审批必须选择一个明确选项")
		}
		switch selected.Answers["approval"].Answers[0] {
		case "允许本轮":
			granted = permissionAnswer{Permissions: requested, Scope: "turn"}
		case "允许本会话":
			granted = permissionAnswer{Permissions: requested, Scope: "session"}
		case "拒绝":
			granted.Scope = "turn"
		default:
			return nil, errors.New("权限审批选项无效")
		}
	} else {
		if err := decodeStrictObject(answer, &granted); err != nil {
			return nil, err
		}
		if err := decodeStrictObject(fields["permissions"], &permissionProfile{}); err != nil {
			return nil, errors.New("权限答案缺少明确的权限对象")
		}
	}
	if granted.Scope != "turn" && granted.Scope != "session" {
		return nil, errors.New("权限作用域只能是 turn 或 session")
	}
	if !permissionSubset(requested, granted.Permissions) {
		return nil, errors.New("权限答案不能扩大原生请求的授权范围")
	}
	return json.Marshal(granted)
}

func permissionSubset(requested, granted permissionProfile) bool {
	if !validPermissionEntries(granted) {
		return false
	}
	if granted.Network != nil && granted.Network.Enabled != nil && *granted.Network.Enabled {
		if requested.Network == nil || requested.Network.Enabled == nil || !*requested.Network.Enabled {
			return false
		}
	}
	if granted.FileSystem == nil {
		return true
	}
	if requested.FileSystem == nil {
		return len(granted.FileSystem.Read) == 0 && len(granted.FileSystem.Write) == 0 && len(granted.FileSystem.Entries) == 0
	}
	proposal, approved := requested.FileSystem, granted.FileSystem
	for _, path := range approved.Read {
		if !slices.Contains(proposal.Read, path) && !slices.Contains(proposal.Write, path) {
			return false
		}
	}
	for _, path := range approved.Write {
		if !slices.Contains(proposal.Write, path) {
			return false
		}
	}
	for _, entry := range approved.Entries {
		if entry.Access == "deny" {
			continue
		}
		if entry.Access != "read" && entry.Access != "write" {
			return false
		}
		found := false
		for _, offered := range proposal.Entries {
			if equalJSON(entry.Path, offered.Path) && (entry.Access == offered.Access || (entry.Access == "read" && offered.Access == "write")) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	// 不能通过丢弃拒绝条目，把被批准的目录重新扩大到受保护子目录。
	if len(approved.Read)+len(approved.Write)+len(approved.Entries) > 0 {
		for _, offered := range proposal.Entries {
			if offered.Access != "deny" {
				continue
			}
			found := false
			for _, entry := range approved.Entries {
				found = found || (entry.Access == "deny" && equalJSON(entry.Path, offered.Path))
			}
			if !found {
				return false
			}
		}
	}
	if proposal.GlobScanMaxDepth != nil && (approved.GlobScanMaxDepth == nil || *approved.GlobScanMaxDepth > *proposal.GlobScanMaxDepth) {
		return false
	}
	return true
}
