package codexcontrol

import (
	"strings"
	"unicode"
)

var nonRenderedGitDirectives = map[string]struct{}{
	"git-stage":         {},
	"git-commit":        {},
	"git-create-branch": {},
	"git-push":          {},
	"git-create-pr":     {},
}

type markdownFence struct {
	marker byte
	length int
}

func fenceAt(line string) (markdownFence, bool) {
	leadingSpaces := 0
	for leadingSpaces < len(line) && line[leadingSpaces] == ' ' {
		leadingSpaces++
	}
	if leadingSpaces > 3 || leadingSpaces == len(line) {
		return markdownFence{}, false
	}
	marker := line[leadingSpaces]
	if marker != '`' && marker != '~' {
		return markdownFence{}, false
	}
	length := 0
	for leadingSpaces+length < len(line) && line[leadingSpaces+length] == marker {
		length++
	}
	return markdownFence{marker: marker, length: length}, length >= 3
}

func closesFence(line string, open markdownFence) bool {
	candidate, ok := fenceAt(line)
	if !ok || candidate.marker != open.marker || candidate.length < open.length {
		return false
	}
	leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
	return strings.TrimSpace(line[leadingSpaces+candidate.length:]) == ""
}

func directiveName(line string) (string, bool) {
	leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
	if leadingSpaces > 3 {
		return "", false
	}
	value := strings.TrimSpace(line)
	if len(value) < 4 || value[0:2] != "::" || !directiveNameStart(value[2]) {
		return "", false
	}
	cursor := 3
	for cursor < len(value) && directiveNameCharacter(value[cursor]) {
		cursor++
	}
	name := value[2:cursor]
	if cursor >= len(value) || value[cursor] != '{' {
		return "", false
	}
	cursor++
	for cursor < len(value) {
		for cursor < len(value) && unicode.IsSpace(rune(value[cursor])) {
			cursor++
		}
		if cursor < len(value) && value[cursor] == '}' {
			return name, cursor+1 == len(value)
		}
		if cursor >= len(value) || !directiveNameStart(value[cursor]) {
			return "", false
		}
		cursor++
		for cursor < len(value) && directiveNameCharacter(value[cursor]) {
			cursor++
		}
		if cursor >= len(value) || value[cursor] != '=' {
			return "", false
		}
		cursor++
		if cursor < len(value) && value[cursor] == '"' {
			var closed bool
			cursor, closed = quotedAttributeEnd(value, cursor)
			if !closed {
				return "", false
			}
			continue
		}
		start := cursor
		for cursor < len(value) && !unicode.IsSpace(rune(value[cursor])) && value[cursor] != '}' {
			cursor++
		}
		if start == cursor {
			return "", false
		}
	}
	return "", false
}

func quotedAttributeEnd(value string, start int) (int, bool) {
	escaped := false
	for cursor := start + 1; cursor < len(value); cursor++ {
		switch {
		case escaped:
			escaped = false
		case value[cursor] == '\\':
			escaped = true
		case value[cursor] == '"':
			return cursor + 1, true
		}
	}
	return 0, false
}

func directiveNameStart(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func directiveNameCharacter(value byte) bool {
	return directiveNameStart(value) || value >= '0' && value <= '9' || value == '-' || value == '_'
}

// RenderableFinalAnswer 移除 Codex 动作指令，同时保留普通正文和代码示例。
func RenderableFinalAnswer(value string) string {
	lines := strings.Split(value, "\n")
	visible := make([]string, 0, len(lines))
	var openFence markdownFence
	inFence := false
	for _, line := range lines {
		if inFence {
			visible = append(visible, line)
			if closesFence(line, openFence) {
				inFence = false
			}
			continue
		}
		if fence, ok := fenceAt(line); ok {
			openFence, inFence = fence, true
			visible = append(visible, line)
			continue
		}
		name, ok := directiveName(line)
		if ok {
			if _, hidden := nonRenderedGitDirectives[name]; hidden {
				continue
			}
		}
		visible = append(visible, line)
	}
	return strings.TrimRightFunc(strings.Join(visible, "\n"), unicode.IsSpace)
}
