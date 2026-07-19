package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/slovx2/tyrs-hand/internal/security"
)

var ErrLeaseLost = errors.New("任务租约已经失效")

const JobWakeupChannel = "tyrs-hand:jobs"

type ClaimedJob struct {
	domain.Job
	Capability string
	AttemptID  uuid.UUID
}

type Repository struct {
	db            *sql.DB
	leaseDuration time.Duration
}

func NewRepository(db *sql.DB, leaseDuration time.Duration) *Repository {
	return &Repository{db: db, leaseDuration: leaseDuration}
}

func (r *Repository) Claim(ctx context.Context, workerID string) (*ClaimedJob, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	leaseToken, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	capability, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}

	row := tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT j.id
			FROM job_intents j
			WHERE j.status = 'queued'
			  AND j.available_at <= now()
			  AND j.attempt_count < j.max_attempts
			  AND NOT EXISTS (
				SELECT 1 FROM job_intents active
				WHERE active.status = 'running'
				  AND active.lease_expires_at > now()
				  AND active.source_type = j.source_type
				  AND ((j.source_type = 'github_work_item' AND active.work_item_id = j.work_item_id)
					OR (j.source_type = 'discord_conversation' AND active.discord_conversation_id = j.discord_conversation_id))
			  )
			ORDER BY j.priority ASC, j.created_at ASC
			FOR UPDATE OF j SKIP LOCKED
			LIMIT 1
		)
		UPDATE job_intents j
		SET status = 'running',
			attempt_count = attempt_count + 1,
			lease_token = $2,
			lease_epoch = lease_epoch + 1,
			lease_expires_at = now() + $3::interval,
			worker_id = $1,
			updated_at = now()
		FROM candidate
		WHERE j.id = candidate.id
		RETURNING j.id, j.source_type, j.work_item_id, j.discord_conversation_id,
			COALESCE(j.discord_message_id, ''), j.repository_id, j.agent_profile_id,
			j.status, j.instruction, j.skills, j.allowed_tools, j.dangerous_actions,
			j.actor_login, j.actor_permission, j.attempt_count,
			j.lease_token, j.lease_epoch, j.lease_expires_at, j.created_at`,
		workerID, security.Digest(leaseToken), interval(r.leaseDuration))

	var job domain.Job
	var skillsJSON, toolsJSON, dangerousJSON []byte
	err = row.Scan(
		&job.ID, &job.SourceType, &job.WorkItemID, &job.DiscordConversationID,
		&job.DiscordMessageID, &job.RepositoryID, &job.AgentProfileID,
		&job.Status, &job.Instruction, &skillsJSON, &toolsJSON, &dangerousJSON,
		&job.ActorLogin, &job.ActorPermission, &job.Attempt,
		&job.LeaseToken, &job.LeaseEpoch, &job.LeaseExpiresAt, &job.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("领取任务: %w", err)
	}
	if err := json.Unmarshal(skillsJSON, &job.Skills); err != nil {
		return nil, fmt.Errorf("解析任务 Skills: %w", err)
	}
	if err := json.Unmarshal(toolsJSON, &job.AllowedTools); err != nil {
		return nil, fmt.Errorf("解析任务工具: %w", err)
	}
	if err := json.Unmarshal(dangerousJSON, &job.DangerousActions); err != nil {
		return nil, fmt.Errorf("解析任务危险动作: %w", err)
	}

	var attemptID uuid.UUID
	err = tx.QueryRowContext(ctx, `
		INSERT INTO job_attempts(job_id, attempt, worker_id, lease_epoch, capability_hash)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`, job.ID, job.Attempt, workerID, job.LeaseEpoch, security.Digest(capability)).Scan(&attemptID)
	if err != nil {
		return nil, fmt.Errorf("创建任务尝试: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	job.LeaseToken = leaseToken
	return &ClaimedJob{Job: job, Capability: capability, AttemptID: attemptID}, nil
}

func (r *Repository) Heartbeat(ctx context.Context, jobID uuid.UUID, leaseToken string, epoch int64) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE job_intents
		SET lease_expires_at = now() + $4::interval, updated_at = now()
		WHERE id = $1 AND status = 'running' AND lease_token = $2 AND lease_epoch = $3`,
		jobID, security.Digest(leaseToken), epoch, interval(r.leaseDuration))
	if err != nil {
		return err
	}
	return requireOne(result)
}

func (r *Repository) Complete(ctx context.Context, jobID uuid.UUID, leaseToken string, epoch int64) error {
	return r.finish(ctx, jobID, leaseToken, epoch, domain.JobSucceeded, "")
}

func (r *Repository) Block(ctx context.Context, jobID uuid.UUID, leaseToken string, epoch int64, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return r.finish(ctx, jobID, leaseToken, epoch, domain.JobBlocked, message)
}

func (r *Repository) Fail(ctx context.Context, jobID uuid.UUID, leaseToken string, epoch int64, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE job_intents
		SET status = CASE WHEN attempt_count < max_attempts THEN 'queued' ELSE 'failed' END,
			available_at = CASE WHEN attempt_count < max_attempts THEN now() + make_interval(secs => 60 * attempt_count) ELSE available_at END,
			last_error = $4, lease_token = NULL, lease_expires_at = NULL, worker_id = NULL, updated_at = now()
		WHERE id = $1 AND status = 'running' AND lease_token = $2 AND lease_epoch = $3`,
		jobID, security.Digest(leaseToken), epoch, message)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE job_attempts SET status = 'failed', error = $3, finished_at = now() WHERE job_id = $1 AND lease_epoch = $2 AND status = 'running'`, jobID, epoch, message)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RequeueExpired(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE job_intents
		SET status = CASE WHEN attempt_count < max_attempts THEN 'queued' ELSE 'failed' END,
			available_at = CASE WHEN attempt_count < max_attempts THEN now() + interval '1 minute' ELSE available_at END,
			lease_token = NULL, lease_expires_at = NULL, worker_id = NULL,
			last_error = 'worker lease expired', updated_at = now()
		WHERE status = 'running' AND lease_expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) finish(ctx context.Context, jobID uuid.UUID, leaseToken string, epoch int64, status domain.JobStatus, message string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE job_intents
		SET status = $4, last_error = NULLIF($5, ''), lease_token = NULL,
			lease_expires_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'running' AND lease_token = $2 AND lease_epoch = $3`,
		jobID, security.Digest(leaseToken), epoch, status, message)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE job_attempts SET status = $3, error = NULLIF($4, ''), finished_at = now()
		WHERE job_id = $1 AND lease_epoch = $2 AND status = 'running'`,
		jobID, epoch, status, message)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func requireOne(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}

func interval(value time.Duration) string {
	return fmt.Sprintf("%f seconds", value.Seconds())
}
