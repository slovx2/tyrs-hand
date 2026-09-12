package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

func jsonbSafe(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return jsonbOpaque(trimmed)
	}
	encoded, err := json.Marshal(sanitizeJSON(value))
	if err != nil {
		return jsonbOpaque(trimmed)
	}
	return encoded
}

func jsonbOpaque(raw []byte) json.RawMessage {
	encoded, err := json.Marshal(map[string]string{
		"encoding": "base64",
		"data":     base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		return json.RawMessage(`{"encoding":"empty"}`)
	}
	return encoded
}

func sanitizeJSON(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeJSONString(typed)
	case []any:
		for index, item := range typed {
			typed[index] = sanitizeJSON(item)
		}
		return typed
	case map[string]any:
		sanitized := make(map[string]any, len(typed))
		for key, item := range typed {
			sanitized[sanitizeJSONString(key)] = sanitizeJSON(item)
		}
		return sanitized
	default:
		return value
	}
}

func sanitizeJSONString(value string) string {
	if value == "" {
		return value
	}
	value = strings.ReplaceAll(value, "\x00", "")
	if utf8.ValidString(value) {
		return value
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}
