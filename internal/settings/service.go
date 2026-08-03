package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const (
	githubAgentInstructionsKey = "github.agent.instructions"
	maxInstructions            = 256 * 1024
)

type GitHubAgentInstructions struct {
	Content string `json:"content"`
}

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) GitHubAgentInstructions(ctx context.Context) (GitHubAgentInstructions, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM platform_settings WHERE setting_key=$1`,
		githubAgentInstructionsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubAgentInstructions{}, nil
	}
	if err != nil {
		return GitHubAgentInstructions{}, err
	}
	var result GitHubAgentInstructions
	return result, json.Unmarshal(raw, &result)
}

func (s *Service) SaveGitHubAgentInstructions(ctx context.Context,
	input GitHubAgentInstructions,
) error {
	input.Content = strings.ReplaceAll(input.Content, "\r\n", "\n")
	if len(input.Content) > maxInstructions {
		return errors.New("GitHub Agent instructions 不能超过 256 KiB")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO platform_settings(setting_key,value)
		VALUES ($1,$2) ON CONFLICT(setting_key) DO UPDATE SET value=EXCLUDED.value,
		version=platform_settings.version+1,updated_at=now()
		WHERE platform_settings.value IS DISTINCT FROM EXCLUDED.value`,
		githubAgentInstructionsKey, data)
	return err
}
