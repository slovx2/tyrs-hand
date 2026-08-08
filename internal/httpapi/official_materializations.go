package httpapi

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

func (s *Server) prepareOfficialMaterializations(ctx context.Context,
	workspaceID uuid.UUID,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT mapping.submission_id,mapping.attachment_id,
		attachment.original_filename,attachment.media_type,attachment.size_bytes,
		attachment.sha256,attachment.storage_key,submission.workspace_id,workspace.worker_id
		FROM official_submission_attachments mapping
		JOIN official_turn_submissions submission ON submission.id=mapping.submission_id
		JOIN discord_attachments attachment ON attachment.id=mapping.attachment_id
		JOIN worker_workspaces workspace ON workspace.id=submission.workspace_id
		WHERE submission.workspace_id=$1 AND submission.status IN ('queued','ambiguous')
		  AND mapping.materialization_id IS NULL
		ORDER BY submission.source_order,mapping.ordinal
		FOR UPDATE OF mapping SKIP LOCKED`, workspaceID)
	if err != nil {
		return err
	}
	type pending struct {
		submissionID, attachmentID, workspaceID, workerID uuid.UUID
		filename, mediaType, digest, storageKey           string
		size                                              int64
	}
	items := make([]pending, 0)
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.submissionID, &item.attachmentID, &item.filename,
			&item.mediaType, &item.size, &item.digest, &item.storageKey,
			&item.workspaceID, &item.workerID); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		var materializationID uuid.UUID
		err = tx.QueryRowContext(ctx, `INSERT INTO client_materializations(
			device_id,workspace_id,worker_id,source_type,source_key,client_id,
			original_filename,media_type,size_bytes,sha256,storage_key)
			VALUES(NULL,$1,$2,'discord',$3,NULL,$4,$5,$6,$7,$8)
			ON CONFLICT(source_type,source_key) DO UPDATE SET source_key=EXCLUDED.source_key
			RETURNING id`, item.workspaceID, item.workerID, item.attachmentID.String(),
			item.filename, item.mediaType, item.size, item.digest, item.storageKey).
			Scan(&materializationID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE official_submission_attachments
			SET materialization_id=$3 WHERE submission_id=$1 AND attachment_id=$2
			AND materialization_id IS NULL`, item.submissionID, item.attachmentID,
			materializationID)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE official_turn_submissions submission
		SET status='failed',last_error='附件 materialization 失败',updated_at=now()
		WHERE submission.workspace_id=$1 AND submission.status IN ('queued','ambiguous')
		AND EXISTS(SELECT 1 FROM official_submission_attachments mapping
			JOIN client_materializations materialization ON materialization.id=mapping.materialization_id
			WHERE mapping.submission_id=submission.id AND materialization.status='failed')`,
		workspaceID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) officialSubmissionInputs(ctx context.Context,
	submission officialapp.Submission,
) ([]officialapp.UserInput, error) {
	inputs := append([]officialapp.UserInput(nil), submission.Input...)
	rows, err := s.db.QueryContext(ctx, `SELECT attachment.kind,
		attachment.original_filename,materialization.remote_path
		FROM official_submission_attachments mapping
		JOIN discord_attachments attachment ON attachment.id=mapping.attachment_id
		JOIN client_materializations materialization ON materialization.id=mapping.materialization_id
		WHERE mapping.submission_id=$1 AND materialization.status='completed'
		ORDER BY mapping.ordinal`, submission.ID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, filename string
		var path sql.NullString
		if err = rows.Scan(&kind, &filename, &path); err != nil {
			return nil, err
		}
		if !path.Valid || path.String == "" {
			return nil, errors.New("附件 materialization 缺少远端路径")
		}
		if kind == "image" {
			inputs = append(inputs, officialapp.UserInput{Type: "localImage", Path: path.String})
		} else {
			inputs = append(inputs, officialapp.UserInput{Type: "mention", Name: filename,
				Path: path.String})
		}
	}
	return inputs, rows.Err()
}
