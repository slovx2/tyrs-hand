package codexcontrol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderableFinalAnswerRemovesStandaloneGitDirectives(t *testing.T) {
	value := "部署完成。\n\n" +
		"::git-commit{cwd=\"/workspace/project\"}\n" +
		"::git-push{cwd=\"/workspace/project\" branch=\"main\"}"
	require.Equal(t, "部署完成。", RenderableFinalAnswer(value))
}

func TestRenderableFinalAnswerPreservesTextAndCodeFences(t *testing.T) {
	plain := "可手动运行 git commit。\n示例：::git-push{branch=\"main\"}"
	require.Equal(t, plain, RenderableFinalAnswer(plain))
	fenced := "```text\n::git-commit{cwd=\"/workspace/project\"}\n```"
	require.Equal(t, fenced, RenderableFinalAnswer(fenced))
	require.Equal(t, "::git-push{cwd=\"/workspace/project\"",
		RenderableFinalAnswer("::git-push{cwd=\"/workspace/project\""))
}
