package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/slovx2/tyrs-hand/internal/ports"
)

type WebhookService struct {
	db       *sql.DB
	provider ports.SCMProvider
	botLogin string
}

type WebhookResult struct {
	Duplicate bool
	Jobs      int
}

type triggerRule struct {
	ID                 uuid.UUID
	AgentProfileID     uuid.UUID
	Action             sql.NullString
	ActorMinPermission string
	MentionRequired    bool
	Instruction        string
	Skills             []string
	AllowedTools       []string
	DangerousActions   []string
	Priority           int
}

func NewWebhookService(db *sql.DB, provider ports.SCMProvider, botLogin string) *WebhookService {
	return &WebhookService{db: db, provider: provider, botLogin: strings.TrimSuffix(strings.ToLower(botLogin), "[bot]")}
}

func (s *WebhookService) Process(ctx context.Context, signature, deliveryID, eventName string, payload []byte) (WebhookResult, error) {
	if !s.provider.VerifyWebhook(signature, payload) {
		return WebhookResult{}, errors.New("收到的 GitHub Webhook 签名无效")
	}
	event, err := s.provider.NormalizeWebhook(deliveryID, eventName, payload)
	if err != nil {
		return WebhookResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WebhookResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var deliveryUUID uuid.UUID
	err = tx.QueryRowContext(ctx, `
		INSERT INTO webhook_deliveries(provider, delivery_id, event_name, action, signature_valid, payload)
		VALUES ($1, $2, $3, NULLIF($4, ''), true, $5)
		ON CONFLICT(provider, delivery_id) DO NOTHING
		RETURNING id`, event.Provider, event.DeliveryID, event.EventName, event.Action, event.Raw).Scan(&deliveryUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookResult{Duplicate: true}, nil
	}
	if err != nil {
		return WebhookResult{}, err
	}
	if err := syncInstallation(ctx, tx, event); err != nil {
		return WebhookResult{}, err
	}
	if event.Number == 0 || event.RepositoryID == 0 {
		return WebhookResult{}, finishDeliveryAndCommit(ctx, tx, deliveryUUID, "ignored", "")
	}

	var repositoryID uuid.UUID
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM repositories WHERE provider = $1 AND external_id = $2 AND enabled = true`,
		event.Provider, event.RepositoryID).Scan(&repositoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookResult{}, finishDeliveryAndCommit(ctx, tx, deliveryUUID, "ignored", "repository not enabled")
	}
	if err != nil {
		return WebhookResult{}, err
	}
	workItemID, err := resolveWorkItem(ctx, tx, repositoryID, event)
	if err != nil {
		return WebhookResult{}, err
	}
	if isBot(event.Actor, s.botLogin) {
		return WebhookResult{}, finishDeliveryAndCommit(ctx, tx, deliveryUUID, "ignored", "bot reconciliation event")
	}
	rules, err := loadRules(ctx, tx, repositoryID, event)
	if err != nil {
		return WebhookResult{}, err
	}
	if len(rules) == 0 {
		return WebhookResult{}, finishDeliveryAndCommit(ctx, tx, deliveryUUID, "ignored", "no matching trigger rule")
	}
	permission, err := s.provider.Permission(ctx, event.InstallationID, event.Owner, event.Repository, event.Actor)
	if err != nil {
		return WebhookResult{}, fmt.Errorf("读取触发者权限: %w", err)
	}

	jobs := 0
	for _, rule := range rules {
		if rule.MentionRequired && !mentions(event.Body, s.botLogin) {
			continue
		}
		if permissionRank(permission) < permissionRank(rule.ActorMinPermission) {
			continue
		}
		instruction := renderInstruction(rule.Instruction, event)
		idempotencyKey := fmt.Sprintf("github:%s:%s", deliveryID, rule.ID)
		result, err := tx.ExecContext(ctx, `
			INSERT INTO job_intents(
				work_item_id, repository_id, agent_profile_id, webhook_delivery_id,
				idempotency_key, instruction, skills, allowed_tools, dangerous_actions,
				priority, actor_login, actor_permission)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT(idempotency_key) DO NOTHING`,
			workItemID, repositoryID, rule.AgentProfileID, deliveryUUID, idempotencyKey,
			instruction, encode(rule.Skills), encode(rule.AllowedTools), encode(rule.DangerousActions),
			rule.Priority, event.Actor, permission)
		if err != nil {
			return WebhookResult{}, err
		}
		count, _ := result.RowsAffected()
		jobs += int(count)
	}
	status := "processed"
	message := ""
	if jobs == 0 {
		status, message = "ignored", "actor or mention policy did not match"
	}
	if err := finishDelivery(ctx, tx, deliveryUUID, status, message); err != nil {
		return WebhookResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return WebhookResult{}, err
	}
	return WebhookResult{Jobs: jobs}, nil
}

func syncInstallation(ctx context.Context, tx *sql.Tx, event domain.NormalizedEvent) error {
	if event.InstallationID == 0 || event.Installation.AccountLogin == "" {
		return nil
	}
	accountType := event.Installation.AccountType
	if accountType == "" {
		accountType = "Organization"
	}
	var installationID uuid.UUID
	err := tx.QueryRowContext(ctx, `
		INSERT INTO scm_installations(provider, external_id, account_login, account_type)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT(provider, external_id) DO UPDATE SET account_login = EXCLUDED.account_login,
			account_type = EXCLUDED.account_type, suspended_at = CASE
				WHEN $5 = 'suspended' THEN now() WHEN $5 = 'unsuspended' THEN NULL
				ELSE scm_installations.suspended_at END, updated_at = now()
		RETURNING id`, event.Provider, event.InstallationID, event.Installation.AccountLogin, accountType, event.Action).Scan(&installationID)
	if err != nil {
		return err
	}
	if event.Action == "deleted" {
		_, err := tx.ExecContext(ctx, "UPDATE scm_installations SET suspended_at = now(), updated_at = now() WHERE id = $1", installationID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE repositories SET enabled = false, updated_at = now() WHERE installation_id = $1", installationID)
		return err
	}
	for _, repository := range event.Installation.Repositories {
		if repository.ExternalID == 0 || repository.Owner == "" || repository.Name == "" {
			continue
		}
		var repositoryID uuid.UUID
		err := tx.QueryRowContext(ctx, `
			INSERT INTO repositories(installation_id, provider, external_id, owner, name, default_branch, clone_url)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(provider, external_id) DO UPDATE SET installation_id = EXCLUDED.installation_id,
				owner = EXCLUDED.owner, name = EXCLUDED.name,
				default_branch = COALESCE(NULLIF(EXCLUDED.default_branch,''), repositories.default_branch),
				clone_url = COALESCE(NULLIF(EXCLUDED.clone_url,''), repositories.clone_url),
				enabled = true, updated_at = now()
			RETURNING id`, installationID, event.Provider, repository.ExternalID, repository.Owner, repository.Name, repository.DefaultBranch, repository.CloneURL).Scan(&repositoryID)
		if err != nil {
			return err
		}
		if err := SeedRepositoryRules(ctx, tx, repositoryID); err != nil {
			return err
		}
	}
	for _, externalID := range event.Installation.RemovedRepositoryIDs {
		if _, err := tx.ExecContext(ctx, "UPDATE repositories SET enabled = false, updated_at = now() WHERE provider = $1 AND external_id = $2", event.Provider, externalID); err != nil {
			return err
		}
	}
	return nil
}

func resolveWorkItem(ctx context.Context, tx *sql.Tx, repositoryID uuid.UUID, event domain.NormalizedEvent) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRowContext(ctx, `
		SELECT w.id FROM work_item_channels c
		JOIN work_items w ON w.id = c.work_item_id
		WHERE w.repository_id = $1 AND c.channel_type = $2 AND c.external_number = $3
		LIMIT 1`, repositoryID, event.Kind, event.Number).Scan(&id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE work_items SET title = $2,
			head_sha = COALESCE(NULLIF($3, ''), head_sha), updated_at = now() WHERE id = $1`, id, event.Title, event.HeadSHA)
		if err == nil {
			err = updateWorkItemState(ctx, tx, id, event.Action)
		}
		return id, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, err
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO work_items(repository_id, kind, external_number, title, head_sha)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT(repository_id, kind, external_number) DO UPDATE
		SET title = EXCLUDED.title, head_sha = COALESCE(EXCLUDED.head_sha, work_items.head_sha), updated_at = now()
		RETURNING id`, repositoryID, event.Kind, event.Number, event.Title, event.HeadSHA).Scan(&id)
	if err == nil {
		err = updateWorkItemState(ctx, tx, id, event.Action)
	}
	return id, err
}

func updateWorkItemState(ctx context.Context, tx *sql.Tx, id uuid.UUID, action string) error {
	switch action {
	case "closed":
		_, err := tx.ExecContext(ctx, "UPDATE work_items SET state = 'closed', closed_at = now(), updated_at = now() WHERE id = $1", id)
		return err
	case "reopened":
		_, err := tx.ExecContext(ctx, "UPDATE work_items SET state = 'open', closed_at = NULL, updated_at = now() WHERE id = $1", id)
		return err
	default:
		return nil
	}
}

func loadRules(ctx context.Context, tx *sql.Tx, repositoryID uuid.UUID, event domain.NormalizedEvent) ([]triggerRule, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, agent_profile_id, action, actor_min_permission, mention_required,
			instruction_template, skills, allowed_tools, dangerous_actions, priority
		FROM trigger_rules
		WHERE repository_id = $1 AND enabled = true AND event_name = $2
		  AND (action IS NULL OR action = '' OR action = $3)
		ORDER BY priority ASC`, repositoryID, event.EventName, event.Action)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var rules []triggerRule
	for rows.Next() {
		var rule triggerRule
		var skills, tools, dangerous []byte
		if err := rows.Scan(&rule.ID, &rule.AgentProfileID, &rule.Action, &rule.ActorMinPermission,
			&rule.MentionRequired, &rule.Instruction, &skills, &tools, &dangerous, &rule.Priority); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(skills, &rule.Skills); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(tools, &rule.AllowedTools); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(dangerous, &rule.DangerousActions); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func finishDelivery(ctx context.Context, tx *sql.Tx, id uuid.UUID, status, message string) error {
	_, err := tx.ExecContext(ctx, `UPDATE webhook_deliveries SET status = $2, error = NULLIF($3, ''), processed_at = now() WHERE id = $1`, id, status, message)
	return err
}

func finishDeliveryAndCommit(ctx context.Context, tx *sql.Tx, id uuid.UUID, status, message string) error {
	if err := finishDelivery(ctx, tx, id, status, message); err != nil {
		return err
	}
	return tx.Commit()
}

func renderInstruction(template string, event domain.NormalizedEvent) string {
	return strings.NewReplacer(
		"{{owner}}", event.Owner, "{{repository}}", event.Repository,
		"{{number}}", fmt.Sprintf("%d", event.Number), "{{actor}}", event.Actor,
		"{{body}}", event.Body, "{{event}}", event.EventName, "{{action}}", event.Action,
	).Replace(template)
}

func mentions(body, botLogin string) bool {
	return botLogin != "" && strings.Contains(strings.ToLower(body), "@"+botLogin)
}

func isBot(actor, botLogin string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(actor), "[bot]")
	return normalized != "" && normalized == botLogin
}

func permissionRank(value string) int {
	switch strings.ToLower(value) {
	case "admin":
		return 6
	case "maintain":
		return 5
	case "write", "push":
		return 4
	case "triage":
		return 3
	case "read", "pull":
		return 2
	default:
		return 0
	}
}

func encode(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
