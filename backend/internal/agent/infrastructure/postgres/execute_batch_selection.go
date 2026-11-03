package agentpg

// D23 repair-scan projection: the execute.batch stage-entry job persists its
// narrowed case_ids on the 0.2.1 work_item.inputs object. The execution repair
// scan reads them here (composition-root adapter) so a restart or leader
// failover can never widen the batch to previously confirmed but unselected
// cases. This is a read-only projection; the dispatcher remains the writer.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/jackc/pgx/v5"
)

// ExecuteBatchSelection returns the case_ids frozen on the newest 0.2.1
// execute.batch work item of a version. found=false means the version never
// enqueued the stage-entry job (nothing is safe to scan); found=true with an
// empty list keeps the contract's "all current confirmed cases" meaning.
// Malformed inputs are an error: guessing would silently widen the batch.
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
	caseIDs, err := decodeWorkItemIDList(inputs, "case_ids")
	if err != nil {
		return nil, false, fmt.Errorf("execute.batch selection for version %d: %w", versionID, err)
	}
	return caseIDs, true, nil
}

// decodeWorkItemIDList decodes an optional ID array from a work_item.inputs
// object. It accepts the canonical decimal strings and JSON numbers (the same
// two spellings the agent runner accepts), rejects non-positive IDs and drops
// duplicates while preserving order.
func decodeWorkItemIDList(raw []byte, field string) ([]int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, fmt.Errorf("inputs must be a JSON object")
	}
	value, ok := object[field]
	if !ok {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(value, &entries); err != nil {
		return nil, fmt.Errorf("%s must be an array of decimal IDs", field)
	}
	ids := make([]int64, 0, len(entries))
	seen := make(map[int64]struct{}, len(entries))
	for index, entry := range entries {
		text := strings.TrimSpace(string(entry))
		if strings.HasPrefix(text, `"`) {
			var decoded string
			if err := json.Unmarshal(entry, &decoded); err != nil {
				return nil, fmt.Errorf("%s[%d] must be a decimal ID", field, index)
			}
			text = strings.TrimSpace(decoded)
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("%s[%d] must be a positive decimal ID", field, index)
		}
		if _, duplicate := seen[parsed]; duplicate {
			continue
		}
		seen[parsed] = struct{}{}
		ids = append(ids, parsed)
	}
	return ids, nil
}
