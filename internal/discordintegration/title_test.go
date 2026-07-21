package discordintegration

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeConversationTitle(t *testing.T) {
	got := normalizeConversationTitle("  修复\n\t登录流程\x00  ")
	if got != "修复 登录流程" {
		t.Fatalf("标题规范化结果 = %q", got)
	}
	long := normalizeConversationTitle(strings.Repeat("界", 120))
	if utf8.RuneCountInString(long) != 100 {
		t.Fatalf("标题长度 = %d", utf8.RuneCountInString(long))
	}
}

func TestFallbackTitle(t *testing.T) {
	got := fallbackTitle(strings.Repeat("任", 80))
	if utf8.RuneCountInString(got) != 60 || !strings.HasSuffix(got, "…") {
		t.Fatalf("兜底标题 = %q", got)
	}
}
