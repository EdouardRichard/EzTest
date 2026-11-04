package agentpg

// D23 repair-scan projection: the execute.batch stage-entry job persists its
// narrowed case_ids on the 0.2.1 work_item.inputs object. The execution repair
// scan reads them here (composition-root adapter) so a restart or leader
// failover can never widen the batch to previously confirmed but unselected
// cases. The agent R49 recovery reads the same projection so a lost
// execute.batch job is re-enqueued with the persisted selection instead of a
// bare {} job. This is a read-only projection; the dispatcher remains the
// writer. The ID-list decoding is the shared agent runner parser
// (agentapp.ParseWorkItemIDList): one wire contract, one implementation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/jackc/pgx/v5"
)

// ExecuteBatchSelection returns the case_ids frozen on the newest 0.2.1
// execute.batch work item of a version. found=false means no 0.2.1
// execute.batch row exists for the version: a legacy 0.2.0 row (empty kind,
// type only) carries no 0.2.1 selection and is the same skip, so there is
// nothing safe to scan. found=true with an empty list is the contract's "all
// current confirmed cases" selection, which includes an inputs object without a
// case_ids field. Malformed inputs are an error: guessing would silently widen
// the batch.
func (r *WorkItemRepository) ExecuteBatchSelection(ctx context.Context, versionID int64) ([]int64, bool, error) {
	if versionID <= 0 {
		return nil, false, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	var inputs []byte
	err := r.pool.QueryRow(ctx, `
		SELECT inputs FROM work_item
		WHERE version_id = $1 AND kind = $2
		ORDER BY id DESC
		LIMIT 1
	`, versionID, agent.WorkItemKindExecuteBatch).Scan(&inputs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read execute.batch selection for version %d: %w", versionID, err)
	}
	caseIDs, err := agentapp.ParseWorkItemIDList(json.RawMessage(inputs), "case_ids")
	if err != nil {
		return nil, false, fmt.Errorf("execute.batch selection for version %d: %w", versionID, err)
	}
	return caseIDs, true, nil
}

var _ agentapp.StageJobSelectionReader = (*WorkItemRepository)(nil)
