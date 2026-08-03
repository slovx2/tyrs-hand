package codexsettings

import "strings"

func ValidReasoningEffort(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || len(value) <= 64
}
