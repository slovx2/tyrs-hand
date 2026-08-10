package worker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

func (p *Processor) handleManagedBrowserTool(ctx context.Context,
	request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
	if request.Namespace == nil || *request.Namespace != browserToolNamespace {
		return codex.ToolCallResult{}, errors.New("宿主只接管 browser_files 工具")
	}
	if p.hostRuntime == nil || request.ThreadID == "" {
		return codex.ToolCallResult{}, errors.New("浏览器文件工具缺少活动 Thread")
	}
	var response struct {
		Thread struct {
			CWD string `json:"cwd"`
		} `json:"thread"`
	}
	if err := p.hostRuntime.Client().Call(ctx, "thread/read", map[string]any{
		"threadId": request.ThreadID, "includeTurns": false,
	}, &response); err != nil {
		return codex.ToolCallResult{}, err
	}
	workspace, err := managedBrowserWorkspace(p.hostRuntime.WorkspaceRoot(), response.Thread.CWD)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	return executeBrowserTool(ctx, p.cfg, request.ThreadID, workspace, request)
}

func managedBrowserWorkspace(root, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", errors.New("thread 没有工作目录")
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	resolvedCWD, err := filepath.EvalSymlinks(filepath.Clean(cwd))
	if err != nil {
		return "", err
	}
	if resolvedCWD != resolvedRoot && !pathInside(resolvedRoot, resolvedCWD) {
		return "", errors.New("thread 工作目录不属于宿主 Workspace")
	}
	return resolvedCWD, nil
}
