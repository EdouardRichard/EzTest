package executionpg

import (
	"context"
	"encoding/json"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// HumanRequestRepository implements the matching executionapp repository port.
type HumanRequestRepository struct{ base }

const humanRequestColumns = `id, version_id, case_run_id, conversation_id, message_id, kind,
	payload, question, status, response, expires_at, resolved_by, resolved_at, created_by,
	created_at, updated_at`

// Create inserts one human request.
func (r *HumanRequestRepository) Create(ctx context.Context, request execution.HumanRequest) error {
	ctx = contextOrBackground(ctx)
	payload, err := encodeJSONObject("payload", request.Payload)
	if err != nil {
		return err
	}
	question := []byte("{}")
	if len(request.Question.Questions) > 0 {
		question, err = encodeJSONValue("question", request.Question)
		if err != nil {
			return err
		}
	}
	response, err := encodeJSONObject("response", request.Response)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO human_request (id, version_id, case_run_id, conversation_id, message_id,
			kind, payload, question, status, response, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9, $10::jsonb, $11, $12)
	`, r.insertID(request.ID), request.VersionID, request.CaseRunID, request.ConversationID,
		request.MessageID, string(request.Kind), payload, question, string(request.Status),
		response, request.ExpiresAt, request.CreatedBy)
	return wrapDB("insert human request", err)
}

// Get loads one human request.
func (r *HumanRequestRepository) Get(ctx context.Context, id int64) (execution.HumanRequest, error) {
	ctx = contextOrBackground(ctx)
	item, err := scanHumanRequest(r.pool.QueryRow(ctx, `
		SELECT `+humanRequestColumns+` FROM human_request WHERE id = $1
	`, id))
	if err != nil {
		return execution.HumanRequest{}, wrapDB("get human request", err)
	}
	return item, nil
}

// GetOpenByCaseRun returns the pending login-takeover request of a case_run, or
// nil. v0.22 renamed the 0.21 open status to pending (DB-16).
func (r *HumanRequestRepository) GetOpenByCaseRun(ctx context.Context, caseRunID int64) (*execution.HumanRequest, error) {
	ctx = contextOrBackground(ctx)
	item, err := scanHumanRequest(r.pool.QueryRow(ctx, `
		SELECT `+humanRequestColumns+` FROM human_request
		WHERE case_run_id = $1 AND status = 'pending' AND kind = 'login_takeover'
		ORDER BY id DESC LIMIT 1
	`, caseRunID))
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, wrapDB("get pending human request", err)
	}
	return &item, nil
}

// ListByVersion returns one descending-ID page of a version's human requests.
func (r *HumanRequestRepository) ListByVersion(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.HumanRequest, bool, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+humanRequestColumns+` FROM human_request
		WHERE version_id = $1 AND ($2 = '' OR status = $2) AND ($3::BIGINT IS NULL OR id < $3)
		ORDER BY id DESC LIMIT $4
	`, versionID, status, beforeID, fetchLimit(limit))
	if err != nil {
		return nil, false, wrapDB("list human requests", err)
	}
	defer rows.Close()
	items, err := collectHumanRequests(rows)
	if err != nil {
		return nil, false, err
	}
	limit = effectiveLimit(limit)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

// ListQuestions returns one descending-ID page of the version's structured
// questions plus the v0.22 version-level decision requests (approval/
// blocked_recovery/conflict, R-W2-1), optionally filtered by status.
// Login-takeover execution requests are excluded from the question read
// surface.
func (r *HumanRequestRepository) ListQuestions(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.HumanRequest, bool, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+humanRequestColumns+` FROM human_request
		WHERE version_id = $1
		  AND kind IN ('clarification_question', 'approval', 'blocked_recovery', 'conflict')
		  AND ($2 = '' OR status = $2) AND ($3::BIGINT IS NULL OR id < $3)
		ORDER BY id DESC LIMIT $4
	`, versionID, status, beforeID, fetchLimit(limit))
	if err != nil {
		return nil, false, wrapDB("list questions", err)
	}
	defer rows.Close()
	items, err := collectHumanRequests(rows)
	if err != nil {
		return nil, false, err
	}
	limit = effectiveLimit(limit)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

// ListOpen returns every pending login-takeover human request (recovery sweep).
func (r *HumanRequestRepository) ListOpen(ctx context.Context) ([]execution.HumanRequest, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+humanRequestColumns+` FROM human_request
		WHERE status = 'pending' AND kind = 'login_takeover' ORDER BY id
	`)
	if err != nil {
		return nil, wrapDB("list pending human requests", err)
	}
	defer rows.Close()
	return collectHumanRequests(rows)
}

// CountByCaseRun counts the takeover attempts of one case_run.
func (r *HumanRequestRepository) CountByCaseRun(ctx context.Context, caseRunID int64) (int, error) {
	ctx = contextOrBackground(ctx)
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM human_request WHERE case_run_id = $1
	`, caseRunID).Scan(&count); err != nil {
		return 0, wrapDB("count human requests", err)
	}
	return count, nil
}

// Transition applies a guarded human request status change. answered/cancelled
// store the resolver; reopening to pending clears the resolver fields. A
// non-nil expiresAt extends the deadline (column and question payload) in the
// same statement.
func (r *HumanRequestRepository) Transition(ctx context.Context, id int64, from, to execution.HumanRequestStatus, response map[string]any, actorID int64, at time.Time, expiresAt *time.Time) (bool, error) {
	ctx = contextOrBackground(ctx)
	encodedResponse, err := encodeJSONObject("response", response)
	if err != nil {
		return false, err
	}
	var resolvedBy *int64
	var resolvedAt *time.Time
	if to == execution.HumanAnswered || to == execution.HumanCancelled {
		resolvedBy = nullableID(actorID)
		resolvedAt = &at
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE human_request SET status = $3, response = $4::jsonb,
			expires_at = COALESCE($7::timestamptz, expires_at),
			question = CASE
				WHEN $7::timestamptz IS NULL THEN question
				ELSE jsonb_set(question, '{expires_at}', to_jsonb($7::timestamptz))
			END,
			resolved_by = CASE WHEN $3 IN ('answered', 'cancelled') THEN $5
				WHEN $3 = 'pending' THEN NULL ELSE resolved_by END,
			resolved_at = CASE WHEN $3 IN ('answered', 'cancelled') THEN $6
				WHEN $3 = 'pending' THEN NULL ELSE resolved_at END,
			updated_at = now()
		WHERE id = $1 AND status = $2
	`, id, string(from), string(to), encodedResponse, resolvedBy, resolvedAt, expiresAt)
	if err != nil {
		return false, wrapDB("transition human request", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, r.existsOrNotFound(ctx, "human_request", id, "transition human request")
}

// AttachMessage records the conversation message that renders the question card.
func (r *HumanRequestRepository) AttachMessage(ctx context.Context, id, messageID int64) error {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		UPDATE human_request SET message_id = $2, updated_at = now() WHERE id = $1
	`, id, messageID)
	if err != nil {
		return wrapDB("attach human request message", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.existsOrNotFound(ctx, "human_request", id, "attach human request message")
}

// Expire marks every overdue pending human request (login takeover plus the
// v0.22 approval/blocked_recovery/conflict decision kinds) as expired and
// returns the affected rows. Structured questions have their own sweep so the
// question.expired event and audit stay correct.
func (r *HumanRequestRepository) Expire(ctx context.Context, now time.Time) ([]execution.HumanRequest, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		UPDATE human_request SET status = 'expired', updated_at = now()
		WHERE status = 'pending'
		  AND kind IN ('login_takeover', 'approval', 'blocked_recovery', 'conflict')
		  AND expires_at < $1
		RETURNING `+humanRequestColumns+`
	`, now)
	if err != nil {
		return nil, wrapDB("expire human requests", err)
	}
	defer rows.Close()
	return collectHumanRequests(rows)
}

// ExpireQuestions marks every overdue pending structured question as expired and
// returns the affected rows. It only touches human_request rows: question
// expiry never pauses or blocks execution (R-B3-4).
func (r *HumanRequestRepository) ExpireQuestions(ctx context.Context, now time.Time) ([]execution.HumanRequest, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		UPDATE human_request SET status = 'expired', updated_at = now()
		WHERE status = 'pending' AND kind = 'clarification_question' AND expires_at < $1
		RETURNING `+humanRequestColumns+`
	`, now)
	if err != nil {
		return nil, wrapDB("expire questions", err)
	}
	defer rows.Close()
	return collectHumanRequests(rows)
}

func scanHumanRequest(row rowScanner) (execution.HumanRequest, error) {
	var item execution.HumanRequest
	var kind, status string
	var payloadRaw, questionRaw, responseRaw []byte
	err := row.Scan(&item.ID, &item.VersionID, &item.CaseRunID, &item.ConversationID,
		&item.MessageID, &kind, &payloadRaw, &questionRaw, &status, &responseRaw,
		&item.ExpiresAt, &item.ResolvedBy, &item.ResolvedAt, &item.CreatedBy,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return execution.HumanRequest{}, err
	}
	item.Kind = execution.HumanKind(kind)
	item.Status = execution.HumanRequestStatus(status)
	if len(payloadRaw) > 0 {
		var payload map[string]any
		if err := decodeJSONObject("payload", payloadRaw, &payload); err != nil {
			return execution.HumanRequest{}, err
		}
		item.Payload = payload
	}
	if len(questionRaw) > 0 {
		var batch execution.HumanQuestionBatch
		if err := json.Unmarshal(questionRaw, &batch); err != nil {
			return execution.HumanRequest{}, wrapDB("decode human request question", err)
		}
		item.Question = batch
	}
	if len(responseRaw) > 0 {
		var response map[string]any
		if err := decodeJSONObject("response", responseRaw, &response); err != nil {
			return execution.HumanRequest{}, err
		}
		item.Response = response
	}
	return item, nil
}

func encodeJSONValue(field string, value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, wrapDB("encode "+field, err)
	}
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	return raw, nil
}

func collectHumanRequests(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]execution.HumanRequest, error) {
	items := make([]execution.HumanRequest, 0)
	for rows.Next() {
		item, err := scanHumanRequest(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
