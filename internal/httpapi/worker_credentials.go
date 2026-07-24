package httpapi

import (
	"context"
	"path/filepath"

	"github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) codexRuntimeCredential(ctx context.Context) (workerprotocol.RuntimeCredential, error) {
	provider, err := s.settings.AgentProvider(ctx)
	if err != nil {
		return workerprotocol.RuntimeCredential{}, err
	}
	result := workerprotocol.RuntimeCredential{
		ModelSource: provider.ModelSource, BaseURL: provider.BaseURL,
		ProxyURL: provider.ProxyURL, ConfigSignature: provider.ConfigSignature,
		ChatGPTAuthRevision: provider.ChatGPTAuthRevision,
	}
	if provider.ModelSource == settings.ModelSourceProvider {
		key, err := s.settings.APIKey(ctx)
		if err != nil {
			return workerprotocol.RuntimeCredential{}, err
		}
		result.APIKey = string(key)
	}
	auth, revision, err := s.settings.ChatGPTAuth(ctx,
		filepath.Join(s.cfg.CodexHomeRoot, "shared"))
	if err != nil {
		return workerprotocol.RuntimeCredential{}, err
	}
	result.ChatGPTAuth, result.ChatGPTAuthRevision = auth, revision
	agents, err := s.settings.GlobalAgents(ctx)
	if err != nil {
		return workerprotocol.RuntimeCredential{}, err
	}
	result.GlobalAgents = agents.Content
	return result, nil
}
