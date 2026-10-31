package executionpg

import (
	"context"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// HumanRequestRepository implements the matching executionapp repository port.
type HumanRequestRepository struct{ base }

const humanRequestColumns = `id, version_id, case_run_id, kind, payload, status, response,
	expires_at, resolved_by, resolved_at, created_by, created_at, updated_at`

// Create inserts one human request.
func (r *HumanRequestRepository) Create(ctx context.Context, request execution.HumanRequest) error {
	ctx = contextOrBackground(ctx)
	payload, err := encodeJSONObject("payload", request.Payload)
	if err != nil {
		return err
	}
	response, err := encodeJSONObject("response", request.Response)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO human_request (id, version_id, case_run_id, kind, payload, status,
			response, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb, $8, $9)
	`, r.insertID(request.ID), request.VersionID, request.CaseRunID, string(request.Kind),
		payload, string(request.Status), response, request.ExpiresAt, request.CreatedBy)
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

// GetOpenByCaseRun returns the open request of a case_run, or nil.
func (r *HumanRequestRepository) GetOpenByCaseRun(ctx context.Context, caseRunID int64) (*execution.HumanRequest, error) {
	ctx = contextOrBackground(ctx)
	item, err := scanHumanRequest(r.pool.QueryRow(ctx, `
		SELECT `+humanRequestColumns+` FROM human_request
		WHERE case_run_id = $1 AND status = 'open'
		ORDER BY id DESC LIMIT 1
	`, caseRunID))
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, wrapDB("get open human request", err)
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

// ListOpen returns every open human request (recovery sweep).
func (r *HumanRequestRepository) ListOpen(ctx context.Context) ([]execution.HumanRequest, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+humanRequestColumns+` FROM human_request WHERE status = 'open' ORDER BY id
	`)
	if err != nil {
		return nil, wrapDB("list open human requests", err)
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

// Transition applies a guarded human request status change.
func (r *HumanRequestRepository) Transition(ctx context.Context, id int64, from, to execution.HumanRequestStatus, response map[string]any, actorID int64, at time.Time) (bool, error) {
	ctx = contextOrBackground(ctx)
	encodedResponse, err := encodeJSONObject("response", response)
	if err != nil {
		return false, err
	}
	var resolvedBy *int64
	var resolvedAt *time.Time
	if to == execution.HumanResolved || to == execution.HumanCancelled {
		resolvedBy = nullableID(actorID)
		resolvedAt = &at
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE human_request SET status = $3, response = $4::jsonb,
			resolved_by = $5, resolved_at = $6, updated_at = now()
		WHERE id = $1 AND status = $2
	`, id, string(from), string(to), encodedResponse, resolvedBy, resolvedAt)
	if err != nil {
		return false, wrapDB("transition human request", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, r.existsOrNotFound(ctx, "human_request", id, "transition human request")
}

// Expire marks every open request older than now as expired and returns the
// affected rows.
func (r *HumanRequestRepository) Expire(ctx context.Context, now time.Time) ([]execution.HumanRequest, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		UPDATE human_request SET status = 'expired', updated_at = now()
		WHERE status = 'open' AND expires_at < $1
		RETURNING `+humanRequestColumns+`
	`, now)
	if err != nil {
		return nil, wrapDB("expire human requests", err)
	}
	defer rows.Close()
	return collectHumanRequests(rows)
}

func scanHumanRequest(row rowScanner) (execution.HumanRequest, error) {
	var item execution.HumanRequest
	var kind, status string
	var payloadRaw, responseRaw []byte
	err := row.Scan(&item.ID, &item.VersionID, &item.CaseRunID, &kind, &payloadRaw, &status,
		&responseRaw, &item.ExpiresAt, &item.ResolvedBy, &item.ResolvedAt, &item.CreatedBy,
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
	if len(responseRaw) > 0 {
		var response map[string]any
		if err := decodeJSONObject("response", responseRaw, &response); err != nil {
			return execution.HumanRequest{}, err
		}
		item.Response = response
	}
	return item, nil
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
