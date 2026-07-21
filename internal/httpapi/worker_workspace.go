package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerWorkspaceState(c *gin.Context) {
	var request workerprotocol.WorkspaceState
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID,
		request.RunLeaseRequest)
	if err != nil {
		remoteRunError(c, "校验 Worktree 状态请求失败", err)
		return
	}
	if claimed.SourceType != codexcontrol.SourceGitHub || request.CachePath == "" ||
		request.WorktreePath == "" {
		badRequest(c, errors.New("worktree 状态只允许用于 GitHub Run，且路径不能为空"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Worktree 状态失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var cacheID string
	err = tx.QueryRowContext(c.Request.Context(), `INSERT INTO repo_caches
		(repository_id, execution_node_id, path, status, last_fetch_at, last_used_at, error)
		VALUES ($1,$2,$3,$4,now(),now(),NULLIF($5,''))
		ON CONFLICT (execution_node_id, repository_id) WHERE execution_node_id IS NOT NULL
		DO UPDATE SET path = EXCLUDED.path, status = EXCLUDED.status,
		last_fetch_at = now(), last_used_at = now(), error = EXCLUDED.error RETURNING id`,
		claimed.RepositoryID, node.ID, request.CachePath, request.Status, request.Error).Scan(&cacheID)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO worktrees
			(work_item_id, repo_cache_id, execution_node_id, path, branch, base_sha, head_sha,
			 status, dirty, last_used_at, error)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now(),NULLIF($10,''))
			ON CONFLICT(work_item_id) DO UPDATE SET repo_cache_id = EXCLUDED.repo_cache_id,
			execution_node_id = EXCLUDED.execution_node_id, path = EXCLUDED.path,
			branch = EXCLUDED.branch,
			base_sha = COALESCE(NULLIF(EXCLUDED.base_sha,''), worktrees.base_sha),
			head_sha = COALESCE(NULLIF(EXCLUDED.head_sha,''), worktrees.head_sha),
			status = EXCLUDED.status, dirty = EXCLUDED.dirty, last_used_at = now(),
			error = EXCLUDED.error`, claimed.WorkItemID, cacheID, node.ID, request.WorktreePath,
			request.Branch, request.BaseSHA, request.HeadSHA, request.Status, request.Dirty,
			request.Error)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Worktree 状态失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Worktree 状态失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
