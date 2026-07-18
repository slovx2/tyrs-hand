package ports

import "context"

type WorkspaceSpec struct {
	RepositoryID string
	WorkItemID   string
	CloneURL     string
	BaseRef      string
	Branch       string
}

type Workspace struct {
	CachePath    string
	WorktreePath string
	Branch       string
	HeadSHA      string
}

type WorkspaceManager interface {
	Ensure(ctx context.Context, spec WorkspaceSpec, credential string) (Workspace, error)
	Status(ctx context.Context, worktreePath string) (string, error)
	Publish(ctx context.Context, worktreePath, remoteBranch, credential string) (string, error)
	Remove(ctx context.Context, repositoryID, workItemID string) error
}
