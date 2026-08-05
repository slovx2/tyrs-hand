package discordintegration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFallbackTitleNormalizesAndTruncates(t *testing.T) {
	require.Equal(t, "Codex 任务", fallbackTitle(" \n\t "))
	require.Equal(t, "测试 标题", fallbackTitle("  测试\n标题  "))

	long := strings.Repeat("标", titleFallbackMaxRunes+5)
	result := fallbackTitle(long)
	require.Len(t, []rune(result), titleFallbackMaxRunes)
	require.True(t, strings.HasSuffix(result, "…"))
}

func TestTitleRuneHelpers(t *testing.T) {
	require.Equal(t, "短标题", truncateRunes("短标题", 4))
	require.Equal(t, "一二三…", truncateRunes("一二三四五", 4))
	require.Equal(t, "日志 内容", sanitizeLogValue(" 日志\n内容 ", 8))
	require.Equal(t, "一二三四", sanitizeLogValue("一二三四五", 4))
}
