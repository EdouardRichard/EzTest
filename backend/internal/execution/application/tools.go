package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// --- browser tools ---------------------------------------------------------

type browserToolArgs struct {
	VersionSUTID string          `json:"version_sut_id,omitempty"`
	AccountRole  string          `json:"account_role,omitempty"`
	URL          string          `json:"url,omitempty"`
	Target       json.RawMessage `json:"target,omitempty"`
	Value        *string         `json:"value,omitempty"`
	Secret       bool            `json:"secret,omitempty"`
	Key          string          `json:"key,omitempty"`
	TimeoutMS    *int            `json:"timeout_ms,omitempty"`
	FullPage     bool            `json:"full_page,omitempty"`
	Limit        *int            `json:"limit,omitempty"`
	Filename     string          `json:"filename,omitempty"`
}

// Browser executes one browser.* tool: it lazily creates the (version_sut,
// account_role) executor context, runs the action and stores automatic evidence.
func (s *CaseRunner) Browser(ctx context.Context, scope tools.Scope, action string, args json.RawMessage) (any, error) {
	if s.browser == nil {
		return nil, errors.New("case runner has no browser executor")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	var in browserToolArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, platform.Invalid("parameters", "invalid browser arguments")
	}
	versionSUTID, err := s.resolveVersionSUTID(ctx, scope, optionalToolID(in.VersionSUTID))
	if err != nil {
		return nil, err
	}
	binding, err := s.bindingFor(ctx, scope.VersionID, versionSUTID)
	if err != nil {
		return nil, err
	}
	allowedHosts := bindingHosts(binding)
	if action == "goto" {
		host, hostErr := urlHost(in.URL)
		if hostErr != nil {
			return nil, platform.Invalid("url", "must be an absolute http(s) URL")
		}
		if !containsString(allowedHosts, host) {
			return nil, platform.Invalid("url", "host does not belong to the SUT environment")
		}
	}
	contextID, err := s.ensureBrowserContext(ctx, caseRun, binding, in.AccountRole, allowedHosts)
	if err != nil {
		return nil, err
	}
	actionArgs, err := browserActionArgs(action, in)
	if err != nil {
		return nil, err
	}
	result, err := s.browser.Action(ctx, contextID, action, actionArgs)
	if err != nil {
		return nil, err
	}
	if result.Error != nil {
		if result.Error.Code == "context_not_found" {
			s.markSUTContext(ctx, caseRun.ID, versionSUTID, in.AccountRole, execution.SUTContextFailed)
		}
		return nil, &tools.Error{Code: result.Error.Code, Message: result.Error.Message}
	}
	evidenceIDs, err := s.storeBrowserEvidence(ctx, caseRun, versionSUTID, action, result)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ok": true}
	if len(result.Result) > 0 {
		var decoded any
		if decodeErr := json.Unmarshal(result.Result, &decoded); decodeErr == nil {
			out["result"] = decoded
		} else {
			out["result"] = string(result.Result)
		}
	}
	if len(evidenceIDs) > 0 {
		out["evidence_ids"] = evidenceIDs
	}
	return out, nil
}

func (s *CaseRunner) ensureBrowserContext(ctx context.Context, caseRun execution.CaseRun, binding execution.SUTBinding, role string, allowedHosts []string) (string, error) {
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRun.ID)
	if err != nil {
		return "", mapExecutionError(err, "case_run_sut_context")
	}
	var row *execution.CaseRunSUTContext
	for i := range rows {
		if rows[i].VersionSUTID == binding.VersionSUTID && rows[i].AccountRole == role {
			row = &rows[i]
			break
		}
	}
	if row != nil && row.BrowserContextID != "" && row.Status == execution.SUTContextActive {
		return row.BrowserContextID, nil
	}
	baseURL := binding.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL(binding)
	}
	if baseURL == "" {
		return "", platform.StateConflict("the version_sut has no base URL")
	}
	spec := BrowserContextSpec{
		Key:          caseContextKey(caseRun.ID, binding.VersionSUTID, role),
		AllowedHosts: allowedHosts,
		BaseURL:      baseURL,
		Metadata: map[string]any{
			"case_run_id":    platformid.Format(caseRun.ID),
			"version_sut_id": platformid.Format(binding.VersionSUTID),
			"account_role":   role,
			"dispatch_epoch": platformid.Format(caseRun.DispatchEpoch),
			"dispatch_token": caseRun.DispatchToken,
		},
	}
	contextID, err := s.browser.CreateContext(ctx, spec)
	if err != nil {
		s.markSUTContext(ctx, caseRun.ID, binding.VersionSUTID, role, execution.SUTContextFailed)
		return "", err
	}
	if row == nil {
		created := execution.CaseRunSUTContext{
			ID:               s.nextID(),
			CaseRunID:        caseRun.ID,
			VersionSUTID:     binding.VersionSUTID,
			AccountRole:      role,
			BrowserContextID: contextID,
			LeaseEpoch:       caseRun.DispatchEpoch,
			Status:           execution.SUTContextActive,
			CreatedAt:        s.now(),
			UpdatedAt:        s.now(),
		}
		if createErr := s.sutContexts.Create(ctx, created); createErr != nil {
			_ = s.browser.Close(ctx, contextID)
			return "", mapExecutionError(createErr, "case_run_sut_context")
		}
		return contextID, nil
	}
	rowID := row.ID
	ok, updateErr := s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
		target.BrowserContextID = contextID
		target.Status = execution.SUTContextActive
		target.LeaseEpoch = caseRun.DispatchEpoch
		return nil
	})
	if updateErr != nil {
		return "", mapExecutionError(updateErr, "case_run_sut_context")
	}
	if !ok {
		return "", platform.LeaseLost()
	}
	return contextID, nil
}

func (s *CaseRunner) markSUTContext(ctx context.Context, caseRunID, versionSUTID int64, role string, status execution.SUTContextStatus) {
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.VersionSUTID != versionSUTID || row.AccountRole != role {
			continue
		}
		rowID := row.ID
		_, _ = s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
			target.Status = status
			target.BrowserContextID = ""
			return nil
		})
	}
}

func browserActionArgs(action string, in browserToolArgs) (json.RawMessage, error) {
	args := map[string]any{}
	switch action {
	case "goto":
		args["url"] = in.URL
	case "snapshot", "storage_state":
	case "click", "download":
		if len(in.Target) == 0 {
			return nil, platform.Invalid("target", "target is required")
		}
		args["target"] = in.Target
		if action == "download" && in.Filename != "" {
			args["filename"] = in.Filename
		}
	case "fill":
		if len(in.Target) == 0 || in.Value == nil {
			return nil, platform.Invalid("target", "target and value are required")
		}
		args["target"] = in.Target
		args["value"] = *in.Value
		args["secret"] = in.Secret
	case "press":
		args["key"] = in.Key
		if len(in.Target) > 0 {
			args["target"] = in.Target
		}
	case "wait_for":
		if len(in.Target) > 0 {
			args["target"] = in.Target
		}
		if in.TimeoutMS != nil {
			args["timeout_ms"] = *in.TimeoutMS
		}
	case "screenshot":
		args["full_page"] = in.FullPage
	case "console", "network":
		if in.Limit != nil {
			args["limit"] = *in.Limit
		}
	default:
		return nil, platform.Invalid("action", "unsupported browser action")
	}
	return json.Marshal(args)
}

func (s *CaseRunner) storeBrowserEvidence(ctx context.Context, caseRun execution.CaseRun, versionSUTID int64, action string, result BrowserActionResult) ([]string, error) {
	ids := make([]string, 0, 1)
	versionRef := versionSUTID
	for _, artifact := range result.Artifacts {
		var kind execution.EvidenceKind
		switch artifact.Kind {
		case "screenshot":
			kind = execution.EvidenceScreenshot
		case "download":
			kind = execution.EvidenceDownload
		default:
			continue
		}
		data, err := base64.StdEncoding.DecodeString(artifact.Base64)
		if err != nil {
			return nil, errors.New("executor artifact is not valid base64")
		}
		evidence, addErr := s.AddEvidence(ctx, tools.Scope{
			VersionID:     s.versionForCaseRun(caseRun),
			WaveID:        caseRun.WaveID,
			CaseRunID:     caseRun.ID,
			IntentID:      caseRun.IntentID,
			CaseVersionID: caseRun.CaseVersionID,
			DispatchEpoch: caseRun.DispatchEpoch,
			DispatchToken: caseRun.DispatchToken,
		}, EvidenceInput{
			Kind: kind, Data: data, Filename: artifact.Filename, VersionSUTID: &versionRef, Auto: true,
		})
		if addErr != nil {
			return nil, addErr
		}
		if evidence.ID > 0 {
			ids = append(ids, platformid.Format(evidence.ID))
		}
	}
	switch action {
	case "console", "network":
		kind := execution.EvidenceConsole
		if action == "network" {
			kind = execution.EvidenceNetwork
		}
		data := result.Result
		if len(data) == 0 {
			data = json.RawMessage(`[]`)
		}
		evidence, err := s.AddEvidence(ctx, tools.Scope{
			VersionID:     s.versionForCaseRun(caseRun),
			WaveID:        caseRun.WaveID,
			CaseRunID:     caseRun.ID,
			IntentID:      caseRun.IntentID,
			CaseVersionID: caseRun.CaseVersionID,
			DispatchEpoch: caseRun.DispatchEpoch,
			DispatchToken: caseRun.DispatchToken,
		}, EvidenceInput{
			Kind: kind, Data: data, VersionSUTID: &versionRef, Auto: true,
		})
		if err != nil {
			return nil, err
		}
		if evidence.ID > 0 {
			ids = append(ids, platformid.Format(evidence.ID))
		}
	}
	return ids, nil
}

// --- http tool -------------------------------------------------------------

type httpToolArgs struct {
	VersionSUTID string            `json:"version_sut_id,omitempty"`
	APIID        string            `json:"api_id,omitempty"`
	Method       string            `json:"method,omitempty"`
	Path         string            `json:"path"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         string            `json:"body,omitempty"`
}

// sensitiveHeaderKeys are redacted from http evidence and tool responses.
var sensitiveHeaderKeys = map[string]struct{}{
	"authorization": {}, "cookie": {}, "set-cookie": {}, "x-api-key": {}, "proxy-authorization": {},
}

// HTTP executes http.request against the SUT environment only.
func (s *CaseRunner) HTTP(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if s.httpClient == nil {
		return nil, errors.New("case runner has no HTTP client")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	var in httpToolArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, platform.Invalid("parameters", "invalid http.request arguments")
	}
	versionSUTID, err := s.resolveVersionSUTID(ctx, scope, optionalToolID(in.VersionSUTID))
	if err != nil {
		return nil, err
	}
	binding, err := s.bindingFor(ctx, scope.VersionID, versionSUTID)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	path := strings.TrimSpace(in.Path)
	if in.APIID != "" {
		apiID, parseErr := parseToolID("api_id", in.APIID)
		if parseErr != nil {
			return nil, parseErr
		}
		if s.apis == nil {
			return nil, platform.Invalid("api_id", "api_id resolution is not configured")
		}
		api, apiErr := s.apis.API(ctx, scope.VersionID, versionSUTID, apiID)
		if apiErr != nil {
			return nil, mapExecutionError(apiErr, "api")
		}
		if method == "" {
			method = strings.ToUpper(api.Method)
		}
		if path == "" {
			path = api.Path
		}
	}
	if path == "" {
		return nil, platform.Invalid("path", "path or api_id is required")
	}
	targetURL, err := resolveSUTURL(binding, path)
	if err != nil {
		return nil, err
	}
	if method == "" {
		method = "GET"
	}
	headers := make(map[string]string, len(in.Headers))
	for name, value := range in.Headers {
		headers[name] = value
	}
	response, err := s.httpClient.Do(ctx, HTTPRequestData{
		Method: method, URL: targetURL, Headers: headers, Body: []byte(in.Body),
	})
	if err != nil {
		return nil, mapExecutionError(err, "http")
	}
	body := truncateBytes(response.Body, 64<<10)
	evidence := map[string]any{
		"request": map[string]any{
			"method":  method,
			"url":     targetURL,
			"headers": redactHeaders(headers),
			"body":    truncateString(in.Body, 4000),
		},
		"response": map[string]any{
			"status":         response.Status,
			"headers":        redactHeaderValues(response.Headers),
			"body":           string(body),
			"body_truncated": len(response.Body) > len(body),
		},
	}
	encoded, _ := json.Marshal(evidence)
	versionRef := versionSUTID
	stored, err := s.AddEvidence(ctx, tools.Scope{
		VersionID:     scope.VersionID,
		WaveID:        caseRun.WaveID,
		CaseRunID:     caseRun.ID,
		IntentID:      caseRun.IntentID,
		CaseVersionID: caseRun.CaseVersionID,
		DispatchEpoch: caseRun.DispatchEpoch,
		DispatchToken: caseRun.DispatchToken,
	}, EvidenceInput{Kind: execution.EvidenceHTTP, Data: encoded, Filename: "http-request.json", VersionSUTID: &versionRef, Auto: true})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"status":      response.Status,
		"duration_ms": response.DurationMS,
		"headers":     redactHeaderValues(response.Headers),
		"body":        truncateString(string(body), 4000),
	}
	if stored.ID > 0 {
		out["evidence_id"] = platformid.Format(stored.ID)
	}
	return out, nil
}

// --- db tools --------------------------------------------------------------

type dbQueryToolArgs struct {
	VersionSUTID string `json:"version_sut_id,omitempty"`
	DatabaseID   string `json:"database_id,omitempty"`
	SQL          string `json:"sql"`
	MaxRows      *int   `json:"max_rows,omitempty"`
}

// DBQuery executes one read-only SELECT/WITH on the SUT-bound PostgreSQL
// database and stores the SQL/result as db evidence (never credentials).
func (s *CaseRunner) DBQuery(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if s.database == nil || s.credentials == nil {
		return nil, errors.New("case runner has no database adapter")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	var in dbQueryToolArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, platform.Invalid("parameters", "invalid db.query arguments")
	}
	versionSUTID, err := s.resolveVersionSUTID(ctx, scope, optionalToolID(in.VersionSUTID))
	if err != nil {
		return nil, err
	}
	var databaseID *int64
	if in.DatabaseID != "" {
		id, parseErr := parseToolID("database_id", in.DatabaseID)
		if parseErr != nil {
			return nil, parseErr
		}
		databaseID = &id
	}
	if err := validateReadOnlySQL(in.SQL); err != nil {
		return nil, err
	}
	maxRows := 200
	if in.MaxRows != nil && *in.MaxRows > 0 && *in.MaxRows < maxRows {
		maxRows = *in.MaxRows
	}
	credential, err := s.credentials.Database(ctx, scope.VersionID, versionSUTID, databaseID)
	if err != nil {
		return nil, mapExecutionError(err, "target_database")
	}
	result, err := s.database.Query(ctx, credential.DSN, credential.Username, credential.Password, in.SQL, maxRows)
	if err != nil {
		return nil, mapExecutionError(err, "target_database")
	}
	evidence := map[string]any{
		"sql":       in.SQL,
		"columns":   result.Columns,
		"rows":      result.Rows,
		"row_count": result.RowCount,
		"truncated": result.Truncated,
	}
	encoded, _ := json.Marshal(evidence)
	encoded = truncateBytes(encoded, 64<<10)
	versionRef := versionSUTID
	stored, err := s.AddEvidence(ctx, tools.Scope{
		VersionID:     scope.VersionID,
		WaveID:        caseRun.WaveID,
		CaseRunID:     caseRun.ID,
		IntentID:      caseRun.IntentID,
		CaseVersionID: caseRun.CaseVersionID,
		DispatchEpoch: caseRun.DispatchEpoch,
		DispatchToken: caseRun.DispatchToken,
	}, EvidenceInput{Kind: execution.EvidenceDB, Data: encoded, Filename: "db-query.json", VersionSUTID: &versionRef, Auto: true})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"columns":   result.Columns,
		"rows":      result.Rows,
		"row_count": result.RowCount,
		"truncated": result.Truncated,
	}
	if stored.ID > 0 {
		out["evidence_id"] = platformid.Format(stored.ID)
	}
	return out, nil
}

type schemaReadToolArgs struct {
	VersionSUTID string `json:"version_sut_id,omitempty"`
	DatabaseID   string `json:"database_id,omitempty"`
	Schema       string `json:"schema,omitempty"`
}

// SchemaRead returns a bounded tables/columns summary of the target database.
func (s *CaseRunner) SchemaRead(ctx context.Context, scope tools.Scope, raw json.RawMessage) (any, error) {
	if s.database == nil || s.credentials == nil {
		return nil, errors.New("case runner has no database adapter")
	}
	var in schemaReadToolArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, platform.Invalid("parameters", "invalid database.schema_read arguments")
	}
	versionSUTID, err := s.resolveVersionSUTID(ctx, scope, optionalToolID(in.VersionSUTID))
	if err != nil {
		return nil, err
	}
	var databaseID *int64
	if in.DatabaseID != "" {
		id, parseErr := parseToolID("database_id", in.DatabaseID)
		if parseErr != nil {
			return nil, parseErr
		}
		databaseID = &id
	}
	credential, err := s.credentials.Database(ctx, scope.VersionID, versionSUTID, databaseID)
	if err != nil {
		return nil, mapExecutionError(err, "target_database")
	}
	tables, err := s.database.Schema(ctx, credential.DSN, credential.Username, credential.Password, in.Schema)
	if err != nil {
		return nil, mapExecutionError(err, "target_database")
	}
	return map[string]any{
		"version_sut_id": platformid.Format(versionSUTID),
		"tables":         tables,
		"table_count":    len(tables),
	}, nil
}

// --- read/analysis tools ---------------------------------------------------

// ResultRead returns the wave-scoped case_run/step/assertion result summary.
func (s *CaseRunner) ResultRead(ctx context.Context, scope tools.Scope, in tools.ResultReadInput) (any, error) {
	if scope.WaveID <= 0 {
		return nil, platform.Invalid("scope", "result_read requires the injected wave scope")
	}
	caseRuns, err := s.caseRuns.ListAllByWave(ctx, scope.WaveID)
	if err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	sort.SliceStable(caseRuns, func(i, j int) bool { return caseRuns[i].ID < caseRuns[j].ID })
	results := make([]map[string]any, 0, len(caseRuns))
	for _, caseRun := range caseRuns {
		if in.CaseRunID != nil && *in.CaseRunID != caseRun.ID {
			continue
		}
		steps, _ := s.results.ListSteps(ctx, caseRun.ID)
		assertions, _ := s.results.ListAssertions(ctx, caseRun.ID)
		evidenceCount, _ := s.evidence.CountByCaseRun(ctx, caseRun.ID)
		stepEntries := make([]map[string]any, 0, len(steps))
		for _, step := range steps {
			stepEntries = append(stepEntries, map[string]any{
				"seq": step.Seq, "status": string(step.Status),
				"observed": step.Observed, "error": step.Error,
			})
		}
		assertionEntries := make([]map[string]any, 0, len(assertions))
		for _, assertion := range assertions {
			assertionEntries = append(assertionEntries, map[string]any{
				"step_seq": assertion.StepSeq, "assertion_seq": assertion.AssertionSeq,
				"kind": assertion.Kind, "verdict": string(assertion.Verdict),
				"expected": assertion.Expected, "actual": assertion.Actual, "reason": assertion.Reason,
			})
		}
		results = append(results, map[string]any{
			"case_run_id":     platformid.Format(caseRun.ID),
			"case_version_id": platformid.Format(caseRun.CaseVersionID),
			"status":          string(caseRun.Status),
			"failure_class":   string(caseRun.FailureClass),
			"verdict_reason":  caseRun.Summary.VerdictReason,
			"steps":           stepEntries,
			"assertions":      assertionEntries,
			"evidence_count":  evidenceCount,
		})
	}
	return map[string]any{"wave_id": platformid.Format(scope.WaveID), "case_runs": results}, nil
}

// CoverageRead returns requirement/case execution coverage for the version.
func (s *CaseRunner) CoverageRead(ctx context.Context, scope tools.Scope) (any, error) {
	if s.coverage == nil || s.cases == nil {
		return nil, errors.New("case runner has no coverage reader")
	}
	requirements, err := s.coverage.ListRequirements(ctx, scope.VersionID)
	if err != nil {
		return nil, mapExecutionError(err, "requirement")
	}
	cases, err := s.cases.ListExecutable(ctx, scope.VersionID)
	if err != nil {
		return nil, mapCaseReadError(err)
	}
	caseIDsByRequirement := map[int64][]int64{}
	allCaseVersionIDs := make([]int64, 0, len(cases))
	for _, snapshot := range cases {
		caseIDsByRequirement[snapshot.RequirementID] = append(caseIDsByRequirement[snapshot.RequirementID], snapshot.CaseVersionID)
		allCaseVersionIDs = append(allCaseVersionIDs, snapshot.CaseVersionID)
	}
	latest, err := s.caseRuns.LatestByCaseVersions(ctx, scope.VersionID, allCaseVersionIDs)
	if err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	entries := make([]map[string]any, 0, len(requirements))
	for _, requirement := range requirements {
		caseVersionIDs := caseIDsByRequirement[requirement.RequirementID]
		entry := map[string]any{
			"requirement_id":         platformid.Format(requirement.RequirementID),
			"requirement_version_id": platformid.Format(requirement.RequirementVersionID),
			"title":                  requirement.Title,
			"status":                 requirement.Status,
			"case_version_count":     len(caseVersionIDs),
		}
		covered := false
		executed := false
		failed := false
		for _, caseVersionID := range caseVersionIDs {
			caseRun, ok := latest[caseVersionID]
			if !ok {
				continue
			}
			executed = true
			if caseRun.Status == execution.CaseRunPassed {
				covered = true
			}
			if caseRun.Status == execution.CaseRunFailed || caseRun.Status == execution.CaseRunBlocked ||
				caseRun.Status == execution.CaseRunInconclusive {
				failed = true
			}
		}
		switch {
		case covered:
			entry["coverage"] = "covered"
		case executed && failed:
			entry["coverage"] = "gap"
			entry["gap_reason"] = "execution_failed_coverage"
		case len(caseVersionIDs) > 0:
			entry["coverage"] = "gap"
			entry["gap_reason"] = "execution_not_run"
		default:
			entry["coverage"] = "no_cases"
		}
		entries = append(entries, entry)
	}
	return map[string]any{"version_id": platformid.Format(scope.VersionID), "requirements": entries}, nil
}

// FailureSubmit validates and persists one failure analysis candidate. Only
// failed(application) is a P4 issue candidate; the analyst may refine but never
// upgrade a non-application class into application.
func (s *CaseRunner) FailureSubmit(ctx context.Context, scope tools.Scope, in tools.FailureSubmitInput) (any, error) {
	if scope.WaveID <= 0 {
		return nil, platform.Invalid("scope", "failure_submit requires the injected wave scope")
	}
	caseRun, err := s.caseRuns.Get(ctx, in.CaseRunID)
	if err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	if caseRun.WaveID != scope.WaveID {
		return nil, platform.NotFound("case_run")
	}
	if caseRun.Status != execution.CaseRunFailed && caseRun.Status != execution.CaseRunBlocked &&
		caseRun.Status != execution.CaseRunInconclusive {
		return nil, platform.StateConflict("failure analysis requires a finished failed/blocked/inconclusive case_run")
	}
	class := execution.FailureClass(in.FailureClass)
	if class == execution.FailureClassApplication && caseRun.FailureClass != execution.FailureClassApplication {
		return nil, platform.Invalid("failure_class", "a non-application failure cannot be upgraded to application")
	}
	if !class.Valid() || class == "" {
		return nil, platform.Invalid("failure_class", "unsupported failure class")
	}
	updated := caseRun
	updated.FailureClass = class
	if in.Summary != "" {
		updated.Summary.LastError = truncateRunes(in.Summary, 2000)
	}
	if _, err := s.caseRuns.Update(ctx, updated); err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	dedupe := in.DedupeHint
	if dedupe == "" {
		// Align with the deterministic §8.1 fallback so P4 sees one stable
		// dedupe hint for both paths.
		steps, _ := s.results.ListSteps(ctx, caseRun.ID)
		assertions, _ := s.results.ListAssertions(ctx, caseRun.ID)
		dedupe = failureDedupeHint(scope.VersionID, caseRun, class, steps, assertions)
	}
	eventScope := map[string]any{
		"wave_id":     platformid.Format(caseRun.WaveID),
		"case_run_id": platformid.Format(caseRun.ID),
	}
	publishEvent(context.Background(), s.events, EventFailureCandidate, s.versionForCaseRun(caseRun), eventScope, map[string]any{
		"case_run_id":     platformid.Format(caseRun.ID),
		"case_version_id": platformid.Format(caseRun.CaseVersionID),
		"failure_class":   string(class),
		"category":        in.Category,
		"severity":        in.Severity,
		"summary":         truncateRunes(in.Summary, 2000),
		"dedupe_hint":     dedupe,
		"issue_candidate": class.IsIssueCandidate(),
	}, nil)
	if s.teamRoom != nil && class.IsIssueCandidate() {
		_ = s.teamRoom.Post(context.Background(), TeamPost{
			VersionID: s.versionForCaseRun(caseRun),
			Team:      "execution",
			Kind:      "finding",
			Content:   truncateRunes(in.Summary, 2000),
			Refs:      []TeamPostRef{{Kind: "case_run", ID: platformid.Format(caseRun.ID)}},
		})
	}
	return map[string]any{
		"case_run_id":   platformid.Format(caseRun.ID),
		"failure_class": string(class),
		"dedupe_hint":   dedupe,
	}, nil
}

// CoverageSubmit validates one coverage gap candidate and ensures design.gap.
func (s *CaseRunner) CoverageSubmit(ctx context.Context, scope tools.Scope, in tools.CoverageSubmitInput) (any, error) {
	if scope.VersionID <= 0 {
		return nil, platform.Invalid("scope", "coverage_submit requires the injected version scope")
	}
	if s.coverage == nil || s.gaps == nil {
		return nil, errors.New("case runner has no coverage analyzer")
	}
	requirements, err := s.coverage.ListRequirements(ctx, scope.VersionID)
	if err != nil {
		return nil, mapExecutionError(err, "requirement")
	}
	found := false
	for _, requirement := range requirements {
		if requirement.RequirementID == in.RequirementID {
			found = true
			break
		}
	}
	if !found {
		return nil, platform.NotFound("requirement")
	}
	switch in.Reason {
	case "execution_not_run", "execution_failed_coverage":
	default:
		return nil, platform.Invalid("reason", "unsupported coverage gap reason")
	}
	for _, caseRunID := range in.EvidenceCaseRunIDs {
		caseRun, getErr := s.caseRuns.Get(ctx, caseRunID)
		if getErr != nil {
			return nil, mapExecutionError(getErr, "case_run")
		}
		if scope.WaveID > 0 && caseRun.WaveID != scope.WaveID {
			return nil, platform.Invalid("evidence_case_run_ids", "evidence case_run is outside the wave")
		}
	}
	if err := s.gaps.EnsureGap(ctx, scope.VersionID, in.RequirementID, 0, in.Reason); err != nil {
		return nil, mapExecutionError(err, "design_gap")
	}
	publishEvent(context.Background(), s.events, EventCoverageGap, scope.VersionID, map[string]any{
		"requirement_id": platformid.Format(in.RequirementID),
		"reason":         in.Reason,
	}, map[string]any{
		"requirement_id": platformid.Format(in.RequirementID),
		"reason":         in.Reason,
	}, nil)
	if s.teamRoom != nil {
		_ = s.teamRoom.Post(context.Background(), TeamPost{
			VersionID: scope.VersionID,
			Team:      "execution",
			Kind:      "blocker",
			Content:   "执行覆盖缺口: " + in.Reason,
			Refs:      []TeamPostRef{{Kind: "requirement", ID: platformid.Format(in.RequirementID)}},
		})
	}
	return map[string]any{
		"requirement_id": platformid.Format(in.RequirementID),
		"reason":         in.Reason,
		"ensured":        true,
	}, nil
}

// HumanRequest delegates the login-takeover pause to the Task 9 human service.
func (s *CaseRunner) HumanRequest(ctx context.Context, scope tools.Scope, in tools.HumanRequestInput) (any, error) {
	if s.human == nil {
		return nil, errors.New("case runner has no human request service")
	}
	humanRequestID, err := s.human.CreateFromTool(ctx, scope, in)
	if err != nil {
		return nil, mapExecutionError(err, "human_request")
	}
	return map[string]any{
		"human_request_id": platformid.Format(humanRequestID),
		"status":           "open",
		"note":             "case_run is paused until the request is resolved; stop this run",
	}, nil
}

// CaseFinish is the execution.case_finish tool candidate wrapper.
func (s *CaseRunner) CaseFinish(ctx context.Context, scope tools.Scope, in tools.CaseFinishInput) (any, error) {
	finished, err := s.Finish(ctx, scope, execution.FinishCandidate{
		Verdict:      execution.CaseRunStatus(in.Verdict),
		FailureClass: execution.FailureClass(in.FailureClass),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"case_run_id":    platformid.Format(finished.ID),
		"status":         string(finished.Status),
		"failure_class":  string(finished.FailureClass),
		"verdict_reason": finished.Summary.VerdictReason,
	}, nil
}

// --- helpers ---------------------------------------------------------------

func optionalToolID(value string) *int64 {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return nil
	}
	return &parsed
}

func parseToolID(field, value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, platform.Invalid(field, "must be a positive decimal ID")
	}
	return parsed, nil
}

func bindingHosts(binding execution.SUTBinding) []string {
	hosts := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(raw string) {
		if raw == "" {
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" {
			return
		}
		host := strings.ToLower(parsed.Hostname())
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	add(binding.BaseURL)
	for _, page := range binding.PageURLs {
		add(page)
	}
	if len(hosts) == 0 {
		hosts = append(hosts, "127.0.0.1")
	}
	return hosts
}

func defaultBaseURL(binding execution.SUTBinding) string {
	if len(binding.PageURLs) > 0 {
		if parsed, err := url.Parse(binding.PageURLs[0]); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return parsed.Scheme + "://" + parsed.Host
		}
	}
	return ""
}

func urlHost(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("invalid url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("invalid scheme")
	}
	return strings.ToLower(parsed.Hostname()), nil
}

func resolveSUTURL(binding execution.SUTBinding, path string) (string, error) {
	base := binding.BaseURL
	if base == "" {
		base = defaultBaseURL(binding)
	}
	if base == "" {
		return "", platform.StateConflict("the version_sut has no base URL")
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		host, err := urlHost(path)
		if err != nil {
			return "", platform.Invalid("path", "must be an absolute http(s) URL")
		}
		if !containsString(bindingHosts(binding), host) {
			return "", platform.Invalid("path", "URL host does not belong to the SUT environment")
		}
		return path, nil
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", platform.StateConflict("the version_sut base URL is invalid")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(base, "/") + path, nil
}

func validateReadOnlySQL(sql string) error {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return platform.Invalid("sql", "sql is required")
	}
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return platform.Invalid("sql", "only a single SELECT/WITH statement is allowed")
	}
	if strings.Contains(trimmed, ";") {
		return platform.Invalid("sql", "multiple statements are not allowed")
	}
	return nil
}

func redactHeaders(headers map[string]string) map[string]string {
	redacted := make(map[string]string, len(headers))
	for name, value := range headers {
		if _, sensitive := sensitiveHeaderKeys[strings.ToLower(strings.TrimSpace(name))]; sensitive {
			redacted[name] = "[redacted]"
			continue
		}
		redacted[name] = truncateString(value, 500)
	}
	return redacted
}

func redactHeaderValues(headers map[string][]string) map[string][]string {
	redacted := make(map[string][]string, len(headers))
	for name, values := range headers {
		if _, sensitive := sensitiveHeaderKeys[strings.ToLower(strings.TrimSpace(name))]; sensitive {
			redacted[name] = []string{"[redacted]"}
			continue
		}
		copied := make([]string, 0, len(values))
		for _, value := range values {
			copied = append(copied, truncateString(value, 500))
		}
		redacted[name] = copied
	}
	return redacted
}

// failureDedupeHint implements the deterministic P3 §8.1 candidate key:
// sha256(version_id|case_version_id|step_seq|assertion_seq|failure_class)[:32].
// The first failed assertion (or failed step when no assertion failed) is used.
func failureDedupeHint(versionID int64, caseRun execution.CaseRun, class execution.FailureClass, steps []execution.StepResult, assertions []execution.AssertionResult) string {
	stepSeq, assertionSeq := 0, 0
	for _, assertion := range assertions {
		if assertion.Verdict == execution.AssertionFailed {
			stepSeq, assertionSeq = assertion.StepSeq, assertion.AssertionSeq
			break
		}
	}
	if stepSeq == 0 {
		for _, step := range steps {
			if step.Status == execution.StepFailed {
				stepSeq = step.Seq
				break
			}
		}
	}
	payload := strconv.FormatInt(versionID, 10) + "|" +
		strconv.FormatInt(caseRun.CaseVersionID, 10) + "|" +
		strconv.Itoa(stepSeq) + "|" + strconv.Itoa(assertionSeq) + "|" + string(class)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:32]
}

func truncateBytes(value []byte, limit int) []byte {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func truncateString(value string, limit int) string {
	return truncateRunes(value, limit)
}
