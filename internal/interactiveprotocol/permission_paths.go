package interactiveprotocol

import "encoding/json"

func validPermissionEntries(profile permissionProfile) bool {
	if profile.FileSystem == nil {
		return true
	}
	for _, entry := range profile.FileSystem.Entries {
		if entry.Access != "read" && entry.Access != "write" && entry.Access != "deny" {
			return false
		}
		if !validPermissionPath(entry.Path) {
			return false
		}
	}
	return true
}

func validPermissionPath(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	var kind string
	if json.Unmarshal(fields["type"], &kind) != nil {
		return false
	}
	switch kind {
	case "path":
		return len(fields) == 2 && jsonString(fields["path"], false)
	case "glob_pattern":
		return len(fields) == 2 && jsonString(fields["pattern"], false)
	case "special":
		return len(fields) == 2 && validSpecialPermissionPath(fields["value"])
	default:
		return false
	}
}

func validSpecialPermissionPath(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	var kind string
	if json.Unmarshal(fields["kind"], &kind) != nil {
		return false
	}
	switch kind {
	case "root", "minimal", "tmpdir", "slash_tmp":
		return len(fields) == 1
	case "project_roots":
		return len(fields) == 2 && jsonString(fields["subpath"], true)
	case "unknown":
		return len(fields) == 3 && jsonString(fields["path"], false) && jsonString(fields["subpath"], true)
	default:
		return false
	}
}

func jsonString(raw json.RawMessage, nullable bool) bool {
	if string(raw) == "null" {
		return nullable
	}
	var value string
	return json.Unmarshal(raw, &value) == nil
}
