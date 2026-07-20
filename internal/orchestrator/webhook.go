package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/security"
)

type WebhookService struct {
	db       *sql.DB
	provider ports.SCMProvider
	botLogin string
}

type WebhookResult struct {
	Duplicate      bool
	Jobs           int
	PermissionSync *DiscordPermissionSync `json:"-"`
}

type DiscordPermissionSync struct {
	InstallationID int64   `json:"installationId"`
	RepositoryIDs  []int64 `json:"repositoryIds,omitempty"`
}

type triggerRule struct {
	ID                 uuid.UUID
	AgentProfileID     uuid.UUID
	Action             sql.NullString
	ActorMinPermission string
	TriggerKind        string
	TriggerValue       sql.NullString
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
	result := WebhookResult{PermissionSync: discordPermissionSyncForEvent(event)}
	if err := hydrateInstallationRepositories(ctx, s.provider, &event); err != nil {
		return WebhookResult{}, err
	}
	if err := hydratePullRequest(ctx, s.provider, &event); err != nil {
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
		if err := syncInstallation(ctx, tx, event); err != nil {
			return WebhookResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return WebhookResult{}, err
		}
		result.Duplicate = true
		return result, nil
	}
	if err != nil {
		return WebhookResult{}, err
	}
	if err := syncInstallation(ctx, tx, event); err != nil {
		return WebhookResult{}, err
	}
	if event.Number == 0 || event.RepositoryID == 0 {
		return result, finishDeliveryAndCommit(ctx, tx, deliveryUUID, "ignored", "")
	}

	var repositoryID uuid.UUID
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM repositories WHERE provider = $1 AND external_id = $2 AND enabled = true`,
		event.Provider, event.RepositoryID).Scan(&repositoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, finishDeliveryAndCommit(ctx, tx, deliveryUUID, "ignored", "repository not enabled")
	}
	if err != nil {
		return WebhookResult{}, err
	}
	workItemID, err := resolveWorkItem(ctx, tx, repositoryID, event)
	if err != nil {
		return WebhookResult{}, err
	}
	if isBot(event.Actor, s.botLogin) {
		return result, finishDeliveryAndCommit(ctx, tx, deliveryUUID, "ignored", "bot reconciliation event")
	}
	rules, err := loadRules(ctx, tx, repositoryID, event)
	if err != nil {
		return WebhookResult{}, err
	}
	if len(rules) == 0 {
		return result, finishDeliveryAndCommit(ctx, tx, deliveryUUID, "ignored", "no matching trigger rule")
	}
	permission, err := s.provider.Permission(ctx, event.InstallationID, event.Owner, event.Repository, event.Actor)
	if err != nil {
		return WebhookResult{}, fmt.Errorf("读取触发者权限: %w", err)
	}

	jobs := 0
	for _, rule := range rules {
		matched, ok := matchTrigger(rule, event, s.botLogin)
		if !ok {
			continue
		}
		if permissionRank(permission) < permissionRank(rule.ActorMinPermission) {
			continue
		}
		matchedEvent := event
		matchedEvent.Body = matched.Body
		instruction := renderInstruction(rule.Instruction, matchedEvent)
		idempotencyKey := fmt.Sprintf("github:%s:%s", deliveryID, rule.ID)
		var contextVersion int64
		if err := tx.QueryRowContext(ctx, "SELECT context_version FROM work_items WHERE id = $1", workItemID).Scan(&contextVersion); err != nil {
			return WebhookResult{}, err
		}
		_, inserted, err := codexcontrol.NewRepository(s.db, 0).Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceGitHub, WorkItemID: workItemID, RepositoryID: repositoryID,
			AgentProfileID: rule.AgentProfileID, ContextVersion: contextVersion,
			WebhookDeliveryID: deliveryUUID, TriggerRuleID: rule.ID,
			TriggerEvidence: encode(matched.Evidence), IdempotencyKey: idempotencyKey,
			Instruction: instruction, Skills: rule.Skills, AllowedTools: rule.AllowedTools,
			DangerousActions: rule.DangerousActions, Priority: rule.Priority,
			ActorLogin: event.Actor, ActorPermission: permission, ReplyPolicy: "required",
			Behavior: "steer_if_active",
		})
		if err != nil {
			return WebhookResult{}, err
		}
		if inserted {
			jobs++
		}
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
	result.Jobs = jobs
	return result, nil
}

func hydratePullRequest(ctx context.Context, provider ports.SCMProvider, event *domain.NormalizedEvent) error {
	if event.Kind != domain.WorkItemPullRequest || event.Number <= 0 ||
		(event.HeadSHA != "" && event.HeadRef != "" && event.HTMLURL != "") {
		return nil
	}
	pull, err := provider.PullRequest(ctx, event.InstallationID, event.Owner, event.Repository, event.Number)
	if err != nil {
		return fmt.Errorf("补全 Pull Request 元数据: %w", err)
	}
	event.HTMLURL = pull.URL
	event.HeadSHA = pull.HeadSHA
	event.HeadRef = pull.HeadRef
	event.HeadRepository = pull.HeadRepository
	event.BaseSHA = pull.BaseSHA
	event.BaseRef = pull.BaseRef
	return nil
}

func discordPermissionSyncForEvent(event domain.NormalizedEvent) *DiscordPermissionSync {
	switch event.EventName {
	case "installation", "installation_repositories", "repository", "member", "membership", "organization", "team", "team_add":
	default:
		return nil
	}
	request := &DiscordPermissionSync{InstallationID: event.InstallationID}
	seen := make(map[int64]struct{})
	appendRepository := func(id int64) {
		if id == 0 {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		request.RepositoryIDs = append(request.RepositoryIDs, id)
	}
	appendRepository(event.RepositoryID)
	for _, repository := range event.Installation.Repositories {
		appendRepository(repository.ExternalID)
	}
	for _, id := range event.Installation.RemovedRepositoryIDs {
		appendRepository(id)
	}
	return request
}

func hydrateInstallationRepositories(ctx context.Context, provider ports.SCMProvider, event *domain.NormalizedEvent) error {
	for index := range event.Installation.Repositories {
		repository := &event.Installation.Repositories[index]
		if repository.DefaultBranch != "" && repository.CloneURL != "" {
			continue
		}
		full, err := provider.Repository(ctx, event.InstallationID, repository.Owner, repository.Name)
		if err != nil {
			return fmt.Errorf("补全仓库 %s/%s 元数据: %w", repository.Owner, repository.Name, err)
		}
		if repository.ExternalID == 0 {
			repository.ExternalID = full.ExternalID
		}
		if repository.Owner == "" {
			repository.Owner = full.Owner
		}
		if repository.Name == "" {
			repository.Name = full.Name
		}
		if repository.DefaultBranch == "" {
			repository.DefaultBranch = full.DefaultBranch
		}
		if repository.CloneURL == "" {
			repository.CloneURL = full.CloneURL
		}
		if repository.DefaultBranch == "" || repository.CloneURL == "" {
			return fmt.Errorf("GitHub 没有返回仓库 %s/%s 的默认分支或 Clone URL", repository.Owner, repository.Name)
		}
	}
	return nil
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
			head_sha = COALESCE(NULLIF($3, ''), head_sha), head_ref = COALESCE(NULLIF($4, ''), head_ref),
			head_repository = COALESCE(NULLIF($5, ''), head_repository),
			base_sha = COALESCE(NULLIF($6, ''), base_sha), base_ref = COALESCE(NULLIF($7, ''), base_ref),
			html_url = COALESCE(NULLIF($8, ''), html_url), updated_at = now() WHERE id = $1`,
			id, event.Title, event.HeadSHA, event.HeadRef, event.HeadRepository,
			event.BaseSHA, event.BaseRef, event.HTMLURL)
		if err == nil {
			err = updateWorkItemState(ctx, tx, id, event.Action)
		}
		return id, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, err
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO work_items(repository_id, kind, external_number, title, head_sha,
			head_ref, head_repository, base_sha, base_ref, html_url)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''),
			NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''))
		ON CONFLICT(repository_id, kind, external_number) DO UPDATE
		SET title = EXCLUDED.title, head_sha = COALESCE(EXCLUDED.head_sha, work_items.head_sha),
			head_ref = COALESCE(EXCLUDED.head_ref, work_items.head_ref),
			head_repository = COALESCE(EXCLUDED.head_repository, work_items.head_repository),
			base_sha = COALESCE(EXCLUDED.base_sha, work_items.base_sha),
			base_ref = COALESCE(EXCLUDED.base_ref, work_items.base_ref),
			html_url = COALESCE(EXCLUDED.html_url, work_items.html_url), updated_at = now()
		RETURNING id`, repositoryID, event.Kind, event.Number, event.Title, event.HeadSHA,
		event.HeadRef, event.HeadRepository, event.BaseSHA, event.BaseRef, event.HTMLURL).Scan(&id)
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
		SELECT id, agent_profile_id, action, actor_min_permission, trigger_kind, trigger_value,
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
			&rule.TriggerKind, &rule.TriggerValue, &rule.Instruction, &skills, &tools, &dangerous, &rule.Priority); err != nil {
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

type triggerMatch struct {
	Body     string
	Evidence map[string]any
}

func matchTrigger(rule triggerRule, event domain.NormalizedEvent, botLogin string) (triggerMatch, bool) {
	value := rule.TriggerValue.String
	matchedBody := event.Body
	source := "github_event"
	switch rule.TriggerKind {
	case "event":
	case "label":
		if !rule.TriggerValue.Valid || !strings.EqualFold(event.Label, value) {
			return triggerMatch{}, false
		}
		source = "github_label"
	case "slash_command":
		var ok bool
		matchedBody, ok = slashCommandArguments(event.Body, value)
		if !rule.TriggerValue.Valid || !ok {
			return triggerMatch{}, false
		}
		source = "comment_first_line"
	case "mention_command":
		if !firstLineMentions(event.Body, botLogin) {
			return triggerMatch{}, false
		}
		source = "comment_first_line_mention"
	case "legacy_mention":
		if !mentions(event.Body, botLogin) {
			return triggerMatch{}, false
		}
		source = "legacy_body_scan"
	default:
		return triggerMatch{}, false
	}
	return triggerMatch{
		Body: matchedBody,
		Evidence: map[string]any{
			"kind":       rule.TriggerKind,
			"value":      value,
			"source":     source,
			"event":      event.EventName,
			"action":     event.Action,
			"label":      event.Label,
			"bodySha256": security.Digest(event.Body),
		},
	}, true
}

func slashCommandArguments(body, command string) (string, bool) {
	if command == "" {
		return "", false
	}
	firstLine, remainder, hasRemainder := strings.Cut(body, "\n")
	firstLine = strings.TrimSpace(strings.TrimSuffix(firstLine, "\r"))
	prefix := "/" + command
	arguments := ""
	if firstLine == prefix {
		arguments = ""
	} else if strings.HasPrefix(firstLine, prefix) && len(firstLine) > len(prefix) && (firstLine[len(prefix)] == ' ' || firstLine[len(prefix)] == '\t') {
		arguments = strings.TrimSpace(firstLine[len(prefix):])
	} else {
		return "", false
	}
	if hasRemainder && strings.TrimSpace(remainder) != "" {
		if arguments != "" {
			arguments += "\n"
		}
		arguments += strings.TrimSpace(remainder)
	}
	return arguments, true
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
	return visibleMention(body, botLogin)
}

func firstLineMentions(body, botLogin string) bool {
	firstLine, _, _ := strings.Cut(body, "\n")
	firstLine = strings.TrimSuffix(firstLine, "\r")
	trimmed := strings.TrimLeft(firstLine, " \t")
	indent := firstLine[:len(firstLine)-len(trimmed)]
	if strings.HasPrefix(trimmed, ">") || len(indent) >= 4 || strings.Contains(indent, "\t") {
		return false
	}
	return visibleMention(firstLine, botLogin)
}

func visibleMention(body, botLogin string) bool {
	if botLogin == "" {
		return false
	}
	body = strings.ToLower(body)
	code := markdownCodeMask(body)
	needle := "@" + strings.ToLower(botLogin)
	for offset := 0; offset < len(body); {
		index := strings.Index(body[offset:], needle)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(needle)
		leftBoundary := start == 0 || !isGitHubLoginByte(body[start-1])
		rightBoundary := end == len(body) || !isGitHubLoginByte(body[end])
		if leftBoundary && rightBoundary && !code[start] && !escapedAt(body, start) && !insideURL(body, start) {
			return true
		}
		offset = start + 1
	}
	return false
}

func markdownCodeMask(body string) []bool {
	masked := make([]bool, len(body))
	fenceByte, fenceSize := byte(0), 0
	for lineStart := 0; lineStart < len(body); {
		lineEnd := strings.IndexByte(body[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(body)
		} else {
			lineEnd += lineStart
		}
		contentEnd := lineEnd
		if contentEnd > lineStart && body[contentEnd-1] == '\r' {
			contentEnd--
		}
		marker, size, closing := markdownFence(body, lineStart, contentEnd, fenceByte, fenceSize)
		if fenceByte != 0 {
			markRange(masked, lineStart, min(lineEnd+1, len(body)))
			if closing {
				fenceByte, fenceSize = 0, 0
			}
		} else if marker != 0 {
			markRange(masked, lineStart, min(lineEnd+1, len(body)))
			fenceByte, fenceSize = marker, size
		}
		if lineEnd == len(body) {
			break
		}
		lineStart = lineEnd + 1
	}

	for start := 0; start < len(body); {
		if masked[start] || body[start] != '`' {
			start++
			continue
		}
		size := byteRun(body, start, '`')
		closeStart := matchingBackticks(body, masked, start+size, size)
		if closeStart < 0 {
			start += size
			continue
		}
		markRange(masked, start, closeStart+size)
		start = closeStart + size
	}
	return masked
}

func markdownFence(body string, start, end int, openByte byte, openSize int) (byte, int, bool) {
	position := start
	for position < end && position-start < 4 && body[position] == ' ' {
		position++
	}
	if position-start > 3 || position >= end || body[position] != '`' && body[position] != '~' {
		return 0, 0, false
	}
	marker := body[position]
	size := byteRun(body[:end], position, marker)
	if openByte != 0 {
		if marker != openByte || size < openSize || strings.TrimSpace(body[position+size:end]) != "" {
			return 0, 0, false
		}
		return marker, size, true
	}
	if size < 3 || marker == '`' && strings.ContainsRune(body[position+size:end], '`') {
		return 0, 0, false
	}
	return marker, size, false
}

func matchingBackticks(body string, masked []bool, start, size int) int {
	for position := start; position < len(body); {
		if masked[position] || body[position] != '`' {
			position++
			continue
		}
		run := byteRun(body, position, '`')
		if run == size {
			return position
		}
		position += run
	}
	return -1
}

func byteRun(value string, start int, target byte) int {
	end := start
	for end < len(value) && value[end] == target {
		end++
	}
	return end - start
}

func markRange(masked []bool, start, end int) {
	for index := start; index < end; index++ {
		masked[index] = true
	}
}

func escapedAt(body string, position int) bool {
	backslashes := 0
	for position > 0 && body[position-1] == '\\' {
		backslashes++
		position--
	}
	return backslashes%2 == 1
}

func insideURL(body string, position int) bool {
	start := position
	for start > 0 && !isMarkdownSpace(body[start-1]) {
		start--
	}
	return strings.Contains(body[start:position], "://")
}

func isMarkdownSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func isGitHubLoginByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-'
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
