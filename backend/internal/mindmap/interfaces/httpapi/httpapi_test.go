package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmaphttp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/interfaces/httpapi"
)

// The two bearer tokens the fake authenticator resolves (the identity middleware
// is the only place a user enters the request context).
const (
	tokenUser  = "ezt_user"
	tokenAdmin = "ezt_admin"
)

var (
	regularUser = identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	adminUser   = identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
)

// tokenAuth is the Authenticator fake shared by every handler test.
type tokenAuth map[string]identity.User

func (a tokenAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

// newServer mounts the mindmap handler group behind the real identity middleware,
// which is how the routes reach production: the handlers read the actor and the
// role from that context.
func newServer(t *testing.T, deps mindmaphttp.Deps) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mindmaphttp.NewHandler(deps).Register(mux)
	return identityhttp.AuthMiddleware(tokenAuth{tokenUser: regularUser, tokenAdmin: adminUser})(mux)
}

// call performs one request with an optional bearer token and JSON body.
func call(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeData decodes the {"data": ...} success envelope into a generic map.
func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode data envelope: %v (body=%s)", err, rec.Body)
	}
	return envelope.Data
}

// decodeError decodes the shared error envelope.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			RequestID string         `json:"request_id"`
			Retryable bool           `json:"retryable"`
			Details   map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (body=%s)", err, rec.Body)
	}
	return map[string]any{
		"code":      envelope.Error.Code,
		"message":   envelope.Error.Message,
		"requestID": envelope.Error.RequestID,
		"retryable": envelope.Error.Retryable,
		"details":   envelope.Error.Details,
	}
}

// reasonOf extracts error.details.reason from a decoded error envelope.
func reasonOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	details, ok := decodeError(t, rec)["details"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope has no details object: %s", rec.Body)
	}
	reason, _ := details["reason"].(string)
	return reason
}

func ptrInt64(value int64) *int64 { return &value }
func ptrInt(value int) *int       { return &value }
func ptrString(value string) *string {
	return &value
}

// --- fakes -----------------------------------------------------------------

// graphFake implements mindmaphttp.GraphService with per-test hooks. A method
// without a hook fails the test through unexpected().
type graphFake struct {
	getFn             func(context.Context, int64) (application.GraphView, error)
	createNodeFn      func(context.Context, int64, int64, application.CreateNodeCommand) (domain.CaseNode, error)
	updateNodeFn      func(context.Context, int64, int64, int64, application.UpdateNodeCommand) (domain.CaseNode, error)
	deleteNodeFn      func(context.Context, int64, int64, int64, int) error
	addStepFn         func(context.Context, int64, int64, int64, application.AddStepCommand) (domain.CaseStep, error)
	updateStepFn      func(context.Context, int64, int64, int64, application.UpdateStepCommand) (domain.CaseStep, error)
	deleteStepFn      func(context.Context, int64, int64, int64, int) error
	reorderStepsFn    func(context.Context, int64, int64, int64, application.ReorderStepsCommand) error
	addAssertionFn    func(context.Context, int64, int64, int64, application.AddAssertionCommand) (domain.CaseAssertion, error)
	updateAssertionFn func(context.Context, int64, int64, int64, application.UpdateAssertionCommand) (domain.CaseAssertion, error)
	deleteAssertionFn func(context.Context, int64, int64, int64, int) error
	addSourceFn       func(context.Context, int64, int64, int64, application.AddSourceCommand) (domain.CaseSource, error)
	deleteSourceFn    func(context.Context, int64, int64, int64) error
	reorderFn         func(context.Context, int64, int64, application.ReorderCommand) error
}

var errUnexpected = errors.New("unexpected service call")

func (f *graphFake) Get(ctx context.Context, versionID int64) (application.GraphView, error) {
	if f.getFn == nil {
		return application.GraphView{}, errUnexpected
	}
	return f.getFn(ctx, versionID)
}

func (f *graphFake) CreateNode(ctx context.Context, versionID, actor int64, cmd application.CreateNodeCommand) (domain.CaseNode, error) {
	if f.createNodeFn == nil {
		return domain.CaseNode{}, errUnexpected
	}
	return f.createNodeFn(ctx, versionID, actor, cmd)
}

func (f *graphFake) UpdateNode(ctx context.Context, versionID, nodeID, actor int64, cmd application.UpdateNodeCommand) (domain.CaseNode, error) {
	if f.updateNodeFn == nil {
		return domain.CaseNode{}, errUnexpected
	}
	return f.updateNodeFn(ctx, versionID, nodeID, actor, cmd)
}

func (f *graphFake) DeleteNode(ctx context.Context, versionID, nodeID, actor int64, revision int) error {
	if f.deleteNodeFn == nil {
		return errUnexpected
	}
	return f.deleteNodeFn(ctx, versionID, nodeID, actor, revision)
}

func (f *graphFake) AddStep(ctx context.Context, versionID, nodeID, actor int64, cmd application.AddStepCommand) (domain.CaseStep, error) {
	if f.addStepFn == nil {
		return domain.CaseStep{}, errUnexpected
	}
	return f.addStepFn(ctx, versionID, nodeID, actor, cmd)
}

func (f *graphFake) UpdateStep(ctx context.Context, versionID, stepID, actor int64, cmd application.UpdateStepCommand) (domain.CaseStep, error) {
	if f.updateStepFn == nil {
		return domain.CaseStep{}, errUnexpected
	}
	return f.updateStepFn(ctx, versionID, stepID, actor, cmd)
}

func (f *graphFake) DeleteStep(ctx context.Context, versionID, stepID, actor int64, nodeRevision int) error {
	if f.deleteStepFn == nil {
		return errUnexpected
	}
	return f.deleteStepFn(ctx, versionID, stepID, actor, nodeRevision)
}

func (f *graphFake) ReorderSteps(ctx context.Context, versionID, nodeID, actor int64, cmd application.ReorderStepsCommand) error {
	if f.reorderStepsFn == nil {
		return errUnexpected
	}
	return f.reorderStepsFn(ctx, versionID, nodeID, actor, cmd)
}

func (f *graphFake) AddAssertion(ctx context.Context, versionID, nodeID, actor int64, cmd application.AddAssertionCommand) (domain.CaseAssertion, error) {
	if f.addAssertionFn == nil {
		return domain.CaseAssertion{}, errUnexpected
	}
	return f.addAssertionFn(ctx, versionID, nodeID, actor, cmd)
}

func (f *graphFake) UpdateAssertion(ctx context.Context, versionID, assertionID, actor int64, cmd application.UpdateAssertionCommand) (domain.CaseAssertion, error) {
	if f.updateAssertionFn == nil {
		return domain.CaseAssertion{}, errUnexpected
	}
	return f.updateAssertionFn(ctx, versionID, assertionID, actor, cmd)
}

func (f *graphFake) DeleteAssertion(ctx context.Context, versionID, assertionID, actor int64, nodeRevision int) error {
	if f.deleteAssertionFn == nil {
		return errUnexpected
	}
	return f.deleteAssertionFn(ctx, versionID, assertionID, actor, nodeRevision)
}

func (f *graphFake) AddSource(ctx context.Context, versionID, nodeID, actor int64, cmd application.AddSourceCommand) (domain.CaseSource, error) {
	if f.addSourceFn == nil {
		return domain.CaseSource{}, errUnexpected
	}
	return f.addSourceFn(ctx, versionID, nodeID, actor, cmd)
}

func (f *graphFake) DeleteSource(ctx context.Context, versionID, sourceID, actor int64) error {
	if f.deleteSourceFn == nil {
		return errUnexpected
	}
	return f.deleteSourceFn(ctx, versionID, sourceID, actor)
}

func (f *graphFake) Reorder(ctx context.Context, versionID, actor int64, cmd application.ReorderCommand) error {
	if f.reorderFn == nil {
		return errUnexpected
	}
	return f.reorderFn(ctx, versionID, actor, cmd)
}

// runStarterFake implements mindmaphttp.RunStarter.
type runStarterFake struct {
	startFn func(context.Context, int64, int64, application.StartRunCommand) (domain.CaseDesignRun, error)
}

func (f *runStarterFake) Start(ctx context.Context, versionID, actor int64, cmd application.StartRunCommand) (domain.CaseDesignRun, error) {
	if f.startFn == nil {
		return domain.CaseDesignRun{}, errUnexpected
	}
	return f.startFn(ctx, versionID, actor, cmd)
}

// exploreFake implements mindmaphttp.ExploreService.
type exploreFake struct {
	startFn func(context.Context, int64, int64, application.ExploreCommand) (domain.CaseDesignRun, error)
	runFn   func(context.Context, domain.CaseDesignRun) (application.ExploreResult, error)
}

func (f *exploreFake) Start(ctx context.Context, versionID, actor int64, cmd application.ExploreCommand) (domain.CaseDesignRun, error) {
	if f.startFn == nil {
		return domain.CaseDesignRun{}, errUnexpected
	}
	return f.startFn(ctx, versionID, actor, cmd)
}

func (f *exploreFake) Run(ctx context.Context, run domain.CaseDesignRun) (application.ExploreResult, error) {
	if f.runFn == nil {
		return application.ExploreResult{}, nil
	}
	return f.runFn(ctx, run)
}

// nodeReaderFake implements mindmaphttp.NodeReader.
type nodeReaderFake struct {
	getFn  func(context.Context, int64, int64) (domain.CaseNode, error)
	listFn func(context.Context, int64) ([]domain.CaseNode, error)
}

func (f *nodeReaderFake) Get(ctx context.Context, versionID, nodeID int64) (domain.CaseNode, error) {
	if f.getFn == nil {
		return domain.CaseNode{}, errUnexpected
	}
	return f.getFn(ctx, versionID, nodeID)
}

func (f *nodeReaderFake) ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseNode, error) {
	if f.listFn == nil {
		return nil, errUnexpected
	}
	return f.listFn(ctx, versionID)
}

// runReaderFake implements mindmaphttp.RunReader.
type runReaderFake struct {
	getFn    func(context.Context, int64, int64) (domain.CaseDesignRun, error)
	listFn   func(context.Context, int64, string, string, int, string) ([]domain.CaseDesignRun, string, bool, error)
	latestFn func(context.Context, int64, string) (domain.CaseDesignRun, error)
}

func (f *runReaderFake) Get(ctx context.Context, versionID, runID int64) (domain.CaseDesignRun, error) {
	if f.getFn == nil {
		return domain.CaseDesignRun{}, errUnexpected
	}
	return f.getFn(ctx, versionID, runID)
}

func (f *runReaderFake) List(ctx context.Context, versionID int64, kind, status string, limit int, cursor string) ([]domain.CaseDesignRun, string, bool, error) {
	if f.listFn == nil {
		return nil, "", false, errUnexpected
	}
	return f.listFn(ctx, versionID, kind, status, limit, cursor)
}

func (f *runReaderFake) LatestByKind(ctx context.Context, versionID int64, kind string) (domain.CaseDesignRun, error) {
	if f.latestFn == nil {
		return domain.CaseDesignRun{}, domain.NewNotFound("run", versionID)
	}
	return f.latestFn(ctx, versionID, kind)
}

// findingFake implements mindmaphttp.FindingReader.
type findingFake struct {
	listFn func(context.Context, int64, string, string, *int64) ([]domain.CaseReviewFinding, error)
}

func (f *findingFake) List(ctx context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error) {
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx, versionID, status, severity, nodeID)
}

// anchorFake implements mindmaphttp.AnchorGateway.
type anchorFake struct {
	createFn func(context.Context, int64, int64, application.AnchorParams) (application.Anchor, error)
	listFn   func(context.Context, int64, int64) ([]application.Anchor, error)
	deleteFn func(context.Context, int64, int64) error
}

func (f *anchorFake) Create(ctx context.Context, versionID, actorID int64, params application.AnchorParams) (application.Anchor, error) {
	if f.createFn == nil {
		return application.Anchor{}, errUnexpected
	}
	return f.createFn(ctx, versionID, actorID, params)
}

func (f *anchorFake) List(ctx context.Context, versionID, nodeID int64) ([]application.Anchor, error) {
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx, versionID, nodeID)
}

func (f *anchorFake) Delete(ctx context.Context, versionID, anchorID int64) error {
	if f.deleteFn == nil {
		return errUnexpected
	}
	return f.deleteFn(ctx, versionID, anchorID)
}

// codeFactsFake implements mindmaphttp.CodeFactsReader.
type codeFactsFake struct {
	latestFn     func(context.Context, int64) (application.ImpactSnapshot, error)
	candidatesFn func(context.Context, int64) ([]application.ImpactCandidate, error)
}

func (f *codeFactsFake) GetLatestAnalysis(ctx context.Context, versionID int64) (application.ImpactSnapshot, error) {
	if f.latestFn == nil {
		return application.ImpactSnapshot{}, nil
	}
	return f.latestFn(ctx, versionID)
}

func (f *codeFactsFake) ListCandidates(ctx context.Context, analysisID int64) ([]application.ImpactCandidate, error) {
	if f.candidatesFn == nil {
		return nil, nil
	}
	return f.candidatesFn(ctx, analysisID)
}

// versionLifecycleFake implements mindmaphttp.VersionLifecycle.
type versionLifecycleFake struct {
	getFn         func(context.Context, int64) (application.VersionSnapshot, error)
	enterReviewFn func(context.Context, int64, int64) (application.VersionSnapshot, error)
}

func (f *versionLifecycleFake) GetVersion(ctx context.Context, versionID int64) (application.VersionSnapshot, error) {
	if f.getFn == nil {
		return application.VersionSnapshot{ID: versionID, Name: "v", Status: "designing"}, nil
	}
	return f.getFn(ctx, versionID)
}

func (f *versionLifecycleFake) EnterReview(ctx context.Context, versionID, actor int64) (application.VersionSnapshot, error) {
	if f.enterReviewFn == nil {
		return application.VersionSnapshot{}, errUnexpected
	}
	return f.enterReviewFn(ctx, versionID, actor)
}

// baselineFake implements mindmaphttp.BaselineReader.
type baselineFake struct {
	getFn func(context.Context, int64) (domain.CaseBaseline, error)
}

func (f *baselineFake) GetByVersion(ctx context.Context, versionID int64) (domain.CaseBaseline, error) {
	if f.getFn == nil {
		return domain.CaseBaseline{}, domain.NewNotFound("baseline", versionID)
	}
	return f.getFn(ctx, versionID)
}

// graphFixture builds the graph projection returned by the fake GraphService.
func graphFixture() application.GraphView {
	return application.GraphView{
		VersionID:     101,
		VersionStatus: "reviewing",
		Editable:      true,
		Nodes: []application.GraphNode{{
			Node: domain.CaseNode{
				ID:        900,
				VersionID: 101,
				Kind:      domain.NodeKindCase,
				Title:     "Place an order",
				Priority:  domain.PriorityP1,
				TestData:  []byte(`{"user":"qa"}`),
				Source:    domain.NodeSourceUserInput,
				Status:    domain.NodeStatusDraft,
				SortOrder: 1,
				Revision:  3,
			},
			Steps: []domain.CaseStep{{
				ID: 901, NodeID: 900, Seq: 1, Action: "open the page",
				Expected: "the page renders", Source: domain.StepSourceUserInput,
			}},
			Assertions: []domain.CaseAssertion{{
				ID: 902, NodeID: 900, Kind: domain.AssertionKindUI, Target: "/order",
				Expression: "the order number is not empty", Expected: "an order is created",
				Source: domain.NodeSourceUserInput,
			}},
			Sources: []domain.CaseSource{{
				ID: 903, VersionID: 101, NodeID: 900, RefKind: domain.RefRequirement,
				RefID: ptrInt64(7), Source: domain.NodeSourceUserInput, Confidence: domain.ConfidenceHigh,
			}},
		}},
		Coverage: domain.Coverage{
			RequirementIDs:          []int64{7, 8},
			UncoveredRequirementIDs: []int64{8},
			StaleRequirementIDs:     []int64{},
			Matrix:                  []domain.CoverageCell{{RequirementID: 7, CaseIDs: []int64{900}}},
		},
		Baseline: &domain.CaseBaseline{
			ID: 950, VersionID: 101, ReviewID: 960, NodeCount: 2, CaseCount: 1,
			GraphHash: strings.Repeat("ab", 32), FrozenAt: time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC),
		},
	}
}

// --- graph read ------------------------------------------------------------

// TestGetGraphReturnsContract pins the GET /mindmap response of spec §7.3: every
// mindmap id is a decimal string, and coverage, baseline, latestRuns, anchors,
// findings and impact cases are rendered with their documented shapes.
func TestGetGraphReturnsContract(t *testing.T) {
	deps := mindmaphttp.Deps{
		Graph: &graphFake{getFn: func(context.Context, int64) (application.GraphView, error) {
			return graphFixture(), nil
		}},
		Anchors: &anchorFake{listFn: func(context.Context, int64, int64) ([]application.Anchor, error) {
			return []application.Anchor{{
				ID: 910, VersionID: 101, SubjectID: 900, FilePath: "src/order.ts",
				StartLine: ptrInt(120), EndLine: ptrInt(140), Confidence: domain.ConfidenceHigh,
				CommitSHA: ptrString(strings.Repeat("a", 40)),
			}}, nil
		}},
		Findings: &findingFake{listFn: func(context.Context, int64, string, string, *int64) ([]domain.CaseReviewFinding, error) {
			return []domain.CaseReviewFinding{
				{ID: 920, ReviewID: 960, NodeID: ptrInt64(900), Category: domain.FindingAssertion,
					Severity: domain.SeverityBlocker, Status: domain.FindingOpen, Detail: "missing assertion"},
				{ID: 921, ReviewID: 960, NodeID: ptrInt64(900), Category: domain.FindingCoverage,
					Severity: domain.SeverityMinor, Status: domain.FindingOpen, Detail: "thin coverage"},
			}, nil
		}},
		RunStore: &runReaderFake{latestFn: func(_ context.Context, _ int64, kind string) (domain.CaseDesignRun, error) {
			if kind != domain.RunKindDesign {
				return domain.CaseDesignRun{}, domain.NewNotFound("run", 1)
			}
			finished := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
			return domain.CaseDesignRun{ID: 930, VersionID: 101, Kind: kind, Status: domain.RunDone, FinishedAt: &finished}, nil
		}},
		CodeFacts: &codeFactsFake{
			latestFn: func(context.Context, int64) (application.ImpactSnapshot, error) {
				return application.ImpactSnapshot{AnalysisID: 940, Status: "done"}, nil
			},
			candidatesFn: func(context.Context, int64) ([]application.ImpactCandidate, error) {
				return []application.ImpactCandidate{{
					ID: 941, SubjectKind: "requirement", SubjectID: 7,
					Reason: "requirement changed", Confidence: domain.ConfidenceMedium, Source: "relation",
				}}, nil
			},
		},
	}

	rec := call(t, newServer(t, deps), http.MethodGet, "/api/versions/101/mindmap", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /mindmap=%d body=%s, want 200", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("X-Graph-Truncated"); got != "false" {
		t.Fatalf("X-Graph-Truncated=%q, want false", got)
	}
	data := decodeData(t, rec)

	if got := data["versionId"]; got != "101" {
		t.Fatalf("versionId=%v, want the decimal string 101", got)
	}
	if got := data["editable"]; got != true {
		t.Fatalf("editable=%v, want true", got)
	}
	nodes, ok := data["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("nodes=%v, want one node", data["nodes"])
	}
	node, _ := nodes[0].(map[string]any)
	if got := node["id"]; got != "900" {
		t.Fatalf("node id=%v, want the decimal string 900", got)
	}
	if got := node["revision"]; got != float64(3) {
		t.Fatalf("node revision=%v, want 3", got)
	}
	steps, _ := node["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("steps=%v, want one step", node["steps"])
	}
	step, _ := steps[0].(map[string]any)
	if step["id"] != "901" || step["seq"] != float64(1) {
		t.Fatalf("step=%v, want id 901 / seq 1", step)
	}
	assertions, _ := node["assertions"].([]any)
	assertion, _ := assertions[0].(map[string]any)
	if assertion["stepId"] != nil {
		t.Fatalf("assertion stepId=%v, want null", assertion["stepId"])
	}
	sources, _ := node["sources"].([]any)
	source, _ := sources[0].(map[string]any)
	if source["refId"] != "7" || source["id"] != "903" {
		t.Fatalf("source=%v, want decimal string ids", source)
	}
	anchors, _ := node["anchors"].([]any)
	anchor, _ := anchors[0].(map[string]any)
	if anchor["id"] != "910" || anchor["startLine"] != float64(120) {
		t.Fatalf("anchor=%v, want id 910 / startLine 120", anchor)
	}
	if got := node["openBlockerCount"]; got != float64(1) {
		t.Fatalf("openBlockerCount=%v, want 1", got)
	}

	coverage, _ := data["coverage"].(map[string]any)
	if ids, _ := coverage["requirementIds"].([]any); len(ids) != 2 || ids[0] != "7" {
		t.Fatalf("coverage=%v, want decimal string requirement ids", coverage)
	}
	if uncovered, _ := coverage["uncoveredRequirementIds"].([]any); len(uncovered) != 1 || uncovered[0] != "8" {
		t.Fatalf("coverage=%v, want the uncovered requirement 8", coverage)
	}
	matrix, _ := coverage["matrix"].([]any)
	cell, _ := matrix[0].(map[string]any)
	if cell["requirementId"] != "7" {
		t.Fatalf("matrix=%v, want the requirement id as a string", matrix)
	}

	baseline, _ := data["baseline"].(map[string]any)
	if baseline["id"] != "950" || baseline["caseCount"] != float64(1) {
		t.Fatalf("baseline=%v, want id 950 / caseCount 1", baseline)
	}

	latestRuns, _ := data["latestRuns"].(map[string]any)
	design, _ := latestRuns["design"].(map[string]any)
	if design["id"] != "930" || design["status"] != "done" {
		t.Fatalf("latestRuns=%v, want the design run 930/done", latestRuns)
	}

	impact, _ := data["impactCases"].([]any)
	if len(impact) != 1 {
		t.Fatalf("impactCases=%v, want one hint", data["impactCases"])
	}
	hint, _ := impact[0].(map[string]any)
	if hint["caseNodeId"] != "900" {
		t.Fatalf("impactCases=%v, want caseNodeId 900", hint)
	}
}

// TestGetGraphIncludeOmitsAnchorsAndImpactButKeepsTheKeys pins the include rule:
// omitting a section never removes its key, and findings is only present when it
// was asked for.
func TestGetGraphIncludeOmitsAnchorsAndImpactButKeepsTheKeys(t *testing.T) {
	deps := mindmaphttp.Deps{
		Graph: &graphFake{getFn: func(context.Context, int64) (application.GraphView, error) {
			return graphFixture(), nil
		}},
		Anchors: &anchorFake{listFn: func(context.Context, int64, int64) ([]application.Anchor, error) {
			t.Fatalf("anchors must not be listed when include omits them")
			return nil, nil
		}},
		Findings: &findingFake{},
	}

	rec := call(t, newServer(t, deps), http.MethodGet, "/api/versions/101/mindmap?include=impact", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /mindmap=%d body=%s, want 200", rec.Code, rec.Body)
	}
	data := decodeData(t, rec)
	nodes, _ := data["nodes"].([]any)
	node, _ := nodes[0].(map[string]any)
	if anchors, ok := node["anchors"].([]any); !ok || len(anchors) != 0 {
		t.Fatalf("anchors=%v, want an empty array (key present)", node["anchors"])
	}
	if _, present := data["findings"]; present {
		t.Fatalf("findings must be absent when include omits it: %v", data["findings"])
	}
}

// TestGetGraphMapsNotFoundVersion proves an unknown version reaches the shared
// 404 envelope with the stable not_found reason.
func TestGetGraphMapsNotFoundVersion(t *testing.T) {
	deps := mindmaphttp.Deps{
		Graph: &graphFake{getFn: func(context.Context, int64) (application.GraphView, error) {
			return application.GraphView{}, &domain.Error{Kind: domain.KindNotFound, Reason: "not_found", Message: "version 404 not found"}
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodGet, "/api/versions/404/mindmap", tokenUser, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /mindmap unknown version=%d body=%s, want 404", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "not_found" {
		t.Fatalf("reason=%q, want not_found", got)
	}
}

// --- error mapping ---------------------------------------------------------

// TestGraphErrorReasonMapping pins the spec §7.4 HTTP mapping: the tree, freeze
// and revision reasons are 409 conflicts (even though the domain classifies them
// as invalid), while a plain invalid field is 422.
func TestGraphErrorReasonMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{"invalid_case_tree", domain.NewInvalid("graph", "invalid_case_tree", "case 900 has no steps"), http.StatusConflict, "invalid_case_tree"},
		{"baseline_frozen", domain.NewConflict("baseline_frozen", "the graph is frozen"), http.StatusConflict, "baseline_frozen"},
		{"version_state_conflict", domain.NewStateConflict("version_state_conflict", "version 101 is executing"), http.StatusConflict, "version_state_conflict"},
		{"no_cases", domain.NewInvalid("graph", "no_cases", "the graph has no case"), http.StatusConflict, "no_cases"},
		{"node_not_found", domain.NewNotFound("node", 900), http.StatusNotFound, "node_not_found"},
		{"invalid_priority", domain.NewInvalid("priority", "invalid_priority", "priority must be P0-P3"), http.StatusUnprocessableEntity, "invalid_priority"},
		{"internal", errors.New("boom"), http.StatusInternalServerError, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := mindmaphttp.Deps{
				Graph: &graphFake{updateNodeFn: func(context.Context, int64, int64, int64, application.UpdateNodeCommand) (domain.CaseNode, error) {
					return domain.CaseNode{}, tc.err
				}},
			}
			rec := call(t, newServer(t, deps), http.MethodPatch, "/api/versions/101/mindmap/nodes/900",
				tokenUser, `{"title":"x","revision":3}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s: status=%d body=%s, want %d", tc.name, rec.Code, rec.Body, tc.wantStatus)
			}
			if tc.wantReason != "" {
				if got := reasonOf(t, rec); got != tc.wantReason {
					t.Fatalf("%s: reason=%q, want %q", tc.name, got, tc.wantReason)
				}
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "boom") {
				t.Fatalf("internal error leaked its message: %s", rec.Body)
			}
		})
	}
}

// --- graph writes ----------------------------------------------------------

// TestDeleteNodeReturns204 proves the delete route maps revision from the query
// and answers 204 with no body.
func TestDeleteNodeReturns204(t *testing.T) {
	var gotRevision int
	deps := mindmaphttp.Deps{
		Graph: &graphFake{deleteNodeFn: func(_ context.Context, versionID, nodeID, actor int64, revision int) error {
			if versionID != 101 || nodeID != 900 || actor != regularUser.ID {
				t.Fatalf("delete args = %d/%d/%d, want 101/900/%d", versionID, nodeID, actor, regularUser.ID)
			}
			gotRevision = revision
			return nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodDelete, "/api/versions/101/mindmap/nodes/900?revision=4", tokenUser, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE node=%d body=%s, want 204", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("DELETE node body=%q, want empty", rec.Body)
	}
	if gotRevision != 4 {
		t.Fatalf("revision=%d, want 4", gotRevision)
	}
}

// TestDeleteNodeRequiresRevision proves a missing revision token is a 400 and
// never reaches the service.
func TestDeleteNodeRequiresRevision(t *testing.T) {
	deps := mindmaphttp.Deps{Graph: &graphFake{}}
	rec := call(t, newServer(t, deps), http.MethodDelete, "/api/versions/101/mindmap/nodes/900", tokenUser, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE node without revision=%d body=%s, want 400", rec.Code, rec.Body)
	}
}

// TestCreateNodeReturnsStringIDs proves a create answers 201 with the node view
// and decimal string ids, including the free-form testData document.
func TestCreateNodeReturnsStringIDs(t *testing.T) {
	deps := mindmaphttp.Deps{
		Graph: &graphFake{createNodeFn: func(_ context.Context, versionID, actor int64, cmd application.CreateNodeCommand) (domain.CaseNode, error) {
			if versionID != 101 || actor != regularUser.ID {
				t.Fatalf("create args = %d/%d", versionID, actor)
			}
			if cmd.Kind != domain.NodeKindCase || cmd.Title != "Place an order" {
				t.Fatalf("create command = %+v", cmd)
			}
			return domain.CaseNode{
				ID: 900, VersionID: 101, Kind: cmd.Kind, Title: cmd.Title,
				Priority: domain.PriorityP1, TestData: cmd.TestData,
				Source: domain.NodeSourceUserInput, Status: domain.NodeStatusDraft, Revision: 1,
			}, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/nodes", tokenUser,
		`{"kind":"case","title":"Place an order","priority":"P1","testData":{"user":"qa"},"requirementIds":["7"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /nodes=%d body=%s, want 201", rec.Code, rec.Body)
	}
	data := decodeData(t, rec)
	if data["id"] != "900" {
		t.Fatalf("created node id=%v, want the decimal string 900", data["id"])
	}
	if testData, _ := data["testData"].(map[string]any); testData["user"] != "qa" {
		t.Fatalf("testData=%v, want the free-form document", data["testData"])
	}
}

// TestAddStepDerivesNodeRevision proves a create-step body needs no revision
// token: the handler loads the node and passes its current revision.
func TestAddStepDerivesNodeRevision(t *testing.T) {
	var gotRevision int
	deps := mindmaphttp.Deps{
		Graph: &graphFake{addStepFn: func(_ context.Context, versionID, nodeID, actor int64, cmd application.AddStepCommand) (domain.CaseStep, error) {
			gotRevision = cmd.NodeRevision
			return domain.CaseStep{ID: 901, NodeID: nodeID, Seq: 1, Action: cmd.Action, Source: domain.StepSourceUserInput}, nil
		}},
		Nodes: &nodeReaderFake{getFn: func(_ context.Context, versionID, nodeID int64) (domain.CaseNode, error) {
			return domain.CaseNode{ID: nodeID, VersionID: versionID, Kind: domain.NodeKindCase, Revision: 7}, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/nodes/900/steps", tokenUser,
		`{"action":"open the page","expected":"the page renders"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST steps=%d body=%s, want 201", rec.Code, rec.Body)
	}
	if gotRevision != 7 {
		t.Fatalf("derived node revision=%d, want 7", gotRevision)
	}
	data := decodeData(t, rec)
	if data["id"] != "901" {
		t.Fatalf("created step id=%v, want the decimal string 901", data["id"])
	}
}

// TestAddStepUnknownNodeIs404 proves the revision derivation reports a missing
// node as 404 node_not_found instead of calling the service.
func TestAddStepUnknownNodeIs404(t *testing.T) {
	deps := mindmaphttp.Deps{
		Graph: &graphFake{},
		Nodes: &nodeReaderFake{getFn: func(_ context.Context, _, nodeID int64) (domain.CaseNode, error) {
			return domain.CaseNode{}, domain.NewNotFound("node", nodeID)
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/nodes/900/steps", tokenUser,
		`{"action":"open the page"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST steps unknown node=%d body=%s, want 404", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "node_not_found" {
		t.Fatalf("reason=%q, want node_not_found", got)
	}
}

// TestReorderRoutesReturn204 proves the two reorder routes answer 204 and pass
// the documented ids through.
func TestReorderRoutesReturn204(t *testing.T) {
	var reordered []application.ReorderCommand
	deps := mindmaphttp.Deps{
		Graph: &graphFake{
			reorderFn: func(_ context.Context, versionID, actor int64, cmd application.ReorderCommand) error {
				reordered = append(reordered, cmd)
				return nil
			},
			reorderStepsFn: func(_ context.Context, versionID, nodeID, actor int64, cmd application.ReorderStepsCommand) error {
				if nodeID != 900 || cmd.NodeRevision != 5 || len(cmd.StepIDs) != 2 {
					t.Fatalf("reorder steps = node %d cmd %+v", nodeID, cmd)
				}
				return nil
			},
		},
	}
	h := newServer(t, deps)

	rec := call(t, h, http.MethodPut, "/api/versions/101/mindmap/order", tokenUser,
		`{"parentId":"800","nodeIds":["900","901"]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT /order=%d body=%s, want 204", rec.Code, rec.Body)
	}
	if len(reordered) != 1 || reordered[0].ParentID == nil || *reordered[0].ParentID != 800 {
		t.Fatalf("reorder command=%+v, want parentId 800", reordered)
	}
	if len(reordered[0].NodeIDs) != 2 || reordered[0].NodeIDs[0] != 900 {
		t.Fatalf("reorder node ids=%v, want [900 901]", reordered[0].NodeIDs)
	}

	rec = call(t, h, http.MethodPut, "/api/versions/101/mindmap/nodes/900/steps/order", tokenUser,
		`{"stepIds":["901","902"],"nodeRevision":5}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT steps/order=%d body=%s, want 204", rec.Code, rec.Body)
	}
}

// --- fakes: runs / reviews / events ----------------------------------------

// reviewFake implements mindmaphttp.ReviewService.
type reviewFake struct {
	startReviewFn   func(context.Context, int64, int64) (domain.CaseDesignRun, error)
	approveFn       func(context.Context, int64, int64, application.ReviewCommand) (application.ApproveResult, error)
	rejectFn        func(context.Context, int64, int64, string) (application.RejectResult, error)
	listFn          func(context.Context, int64, int, string) ([]domain.CaseReview, string, bool, error)
	listFindingsFn  func(context.Context, int64, string, string, *int64) ([]domain.CaseReviewFinding, error)
	updateFindingFn func(context.Context, int64, int64, int64, string) (domain.CaseReviewFinding, error)
}

func (f *reviewFake) StartReview(ctx context.Context, versionID, actor int64) (domain.CaseDesignRun, error) {
	if f.startReviewFn == nil {
		return domain.CaseDesignRun{}, errUnexpected
	}
	return f.startReviewFn(ctx, versionID, actor)
}

func (f *reviewFake) Approve(ctx context.Context, versionID, actor int64, cmd application.ReviewCommand) (application.ApproveResult, error) {
	if f.approveFn == nil {
		return application.ApproveResult{}, errUnexpected
	}
	return f.approveFn(ctx, versionID, actor, cmd)
}

func (f *reviewFake) Reject(ctx context.Context, versionID, actor int64, comment string) (application.RejectResult, error) {
	if f.rejectFn == nil {
		return application.RejectResult{}, errUnexpected
	}
	return f.rejectFn(ctx, versionID, actor, comment)
}

func (f *reviewFake) List(ctx context.Context, versionID int64, limit int, cursor string) ([]domain.CaseReview, string, bool, error) {
	if f.listFn == nil {
		return nil, "", false, errUnexpected
	}
	return f.listFn(ctx, versionID, limit, cursor)
}

func (f *reviewFake) ListFindings(ctx context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error) {
	if f.listFindingsFn == nil {
		return nil, errUnexpected
	}
	return f.listFindingsFn(ctx, versionID, status, severity, nodeID)
}

func (f *reviewFake) UpdateFinding(ctx context.Context, versionID, findingID, actor int64, status string) (domain.CaseReviewFinding, error) {
	if f.updateFindingFn == nil {
		return domain.CaseReviewFinding{}, errUnexpected
	}
	return f.updateFindingFn(ctx, versionID, findingID, actor, status)
}

// eventFake records the published mindmap events.
type eventFake struct {
	published []application.Event
}

func (f *eventFake) Publish(_ context.Context, event application.Event) error {
	f.published = append(f.published, event)
	return nil
}

// --- runs ------------------------------------------------------------------

// TestStartRunDesignReturns202 pins the asynchronous run contract: 202 with the
// decimal string run id, and the body forwarded to the coordinator.
func TestStartRunDesignReturns202(t *testing.T) {
	var got application.StartRunCommand
	deps := mindmaphttp.Deps{
		Runs: &runStarterFake{startFn: func(_ context.Context, versionID, actor int64, cmd application.StartRunCommand) (domain.CaseDesignRun, error) {
			if versionID != 101 || actor != regularUser.ID {
				t.Fatalf("start args = %d/%d", versionID, actor)
			}
			got = cmd
			return domain.CaseDesignRun{ID: 930, VersionID: 101, Kind: cmd.Kind, Status: domain.RunQueued}, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser,
		`{"kind":"design","mode":"replace","confirmReplace":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /runs=%d body=%s, want 202", rec.Code, rec.Body)
	}
	data := decodeData(t, rec)
	if data["runId"] != "930" {
		t.Fatalf("runId=%v, want the decimal string 930", data["runId"])
	}
	if got.Kind != domain.RunKindDesign || got.Mode != "replace" || !got.ConfirmReplace {
		t.Fatalf("start command=%+v", got)
	}
}

// TestStartRunReviewDelegatesToReviewService proves a review run goes through the
// review use case (which reuses the coordinator's single-flight slot).
func TestStartRunReviewDelegatesToReviewService(t *testing.T) {
	deps := mindmaphttp.Deps{
		Reviews: &reviewFake{startReviewFn: func(_ context.Context, versionID, actor int64) (domain.CaseDesignRun, error) {
			return domain.CaseDesignRun{ID: 931, VersionID: versionID, Kind: domain.RunKindReview, Status: domain.RunQueued}, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"review"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /runs review=%d body=%s, want 202", rec.Code, rec.Body)
	}
	if data := decodeData(t, rec); data["runId"] != "931" {
		t.Fatalf("runId=%v, want 931", data["runId"])
	}
}

// TestStartRunExploreRequiresAdmin proves the exploration kind is admin-only
// (403 forbidden) and that an administrator starts it asynchronously.
func TestStartRunExploreRequiresAdmin(t *testing.T) {
	var started bool
	var ran bool
	deps := mindmaphttp.Deps{
		Explore: &exploreFake{
			startFn: func(_ context.Context, versionID, actor int64, cmd application.ExploreCommand) (domain.CaseDesignRun, error) {
				started = true
				if cmd.EnvironmentID == nil || *cmd.EnvironmentID != 55 {
					t.Fatalf("explore command=%+v", cmd)
				}
				return domain.CaseDesignRun{ID: 932, VersionID: versionID, Kind: domain.RunKindExplore, Status: domain.RunQueued}, nil
			},
			runFn: func(_ context.Context, run domain.CaseDesignRun) (application.ExploreResult, error) {
				ran = true
				return application.ExploreResult{RunID: run.ID, Status: domain.RunDone}, nil
			},
		},
	}
	h := newServer(t, deps)

	rec := call(t, h, http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser,
		`{"kind":"explore","environmentId":55}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("explore as user=%d body=%s, want 403", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "forbidden" {
		t.Fatalf("reason=%q, want forbidden", got)
	}
	if started {
		t.Fatalf("the explore service must not be called for a forbidden caller")
	}

	rec = call(t, h, http.MethodPost, "/api/versions/101/mindmap/runs", tokenAdmin,
		`{"kind":"explore","environmentId":55}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("explore as admin=%d body=%s, want 202", rec.Code, rec.Body)
	}
	// The exploration runs in a goroutine; wait for it so the test does not leak.
	deadline := time.Now().Add(2 * time.Second)
	for !ran && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !ran {
		t.Fatalf("the queued exploration was never executed")
	}
}

// TestStartRunInvalidKindIs422 proves an unknown kind is a field validation error.
func TestStartRunInvalidKindIs422(t *testing.T) {
	deps := mindmaphttp.Deps{Runs: &runStarterFake{}, Explore: &exploreFake{}}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"nope"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid kind=%d body=%s, want 422", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "invalid_run_kind" {
		t.Fatalf("reason=%q, want invalid_run_kind", got)
	}
}

// TestStartRunAIUnavailableIs409WithRunID proves a failed agent start answers 409
// ai_unavailable and exposes the failed run id (spec §8.5 note).
func TestStartRunAIUnavailableIs409WithRunID(t *testing.T) {
	deps := mindmaphttp.Deps{
		Runs: &runStarterFake{startFn: func(context.Context, int64, int64, application.StartRunCommand) (domain.CaseDesignRun, error) {
			return domain.CaseDesignRun{ID: 933, VersionID: 101, Kind: domain.RunKindDesign, Status: domain.RunFailed},
				domain.NewConflict("ai_unavailable", "the agent runtime could not start run 933")
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/runs", tokenUser, `{"kind":"design"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("ai_unavailable=%d body=%s, want 409", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "ai_unavailable" {
		t.Fatalf("reason=%q, want ai_unavailable", got)
	}
	details, _ := decodeError(t, rec)["details"].(map[string]any)
	if details["runId"] != "933" {
		t.Fatalf("details=%v, want the failed runId 933", details)
	}
}

// TestListRunsAndRunDetail pins the run DTO: string ids, the persisted reason,
// numeric P1 ids and the shared page envelope.
func TestListRunsAndRunDetail(t *testing.T) {
	started := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	run := domain.CaseDesignRun{
		ID: 930, VersionID: 101, Kind: domain.RunKindDesign, Status: domain.RunFailed,
		AgentSessionID: ptrInt64(7001), AgentRunID: ptrInt64(7002),
		Model: "deepseek", PromptVersion: "p5-design-v1",
		Errors:    []byte(`{"reason":"invalid_case_proposal","validationErrors":["case 900 has no steps"]}`),
		Stats:     []byte(`{"validationErrors":["case 900 has no steps"]}`),
		CreatedBy: regularUser.ID, CreatedAt: started, StartedAt: &started, FinishedAt: &finished,
	}
	deps := mindmaphttp.Deps{
		RunStore: &runReaderFake{
			listFn: func(_ context.Context, versionID int64, kind, status string, limit int, cursor string) ([]domain.CaseDesignRun, string, bool, error) {
				if versionID != 101 || kind != "design" || status != "failed" || limit != 5 {
					t.Fatalf("list args = %d/%q/%q/%d", versionID, kind, status, limit)
				}
				return []domain.CaseDesignRun{run}, "next-cursor", true, nil
			},
			getFn: func(_ context.Context, versionID, runID int64) (domain.CaseDesignRun, error) {
				if runID != 930 {
					return domain.CaseDesignRun{}, domain.NewNotFound("run", runID)
				}
				return run, nil
			},
		},
	}
	h := newServer(t, deps)

	rec := call(t, h, http.MethodGet, "/api/versions/101/mindmap/runs?kind=design&status=failed&limit=5", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /runs=%d body=%s, want 200", rec.Code, rec.Body)
	}
	var page struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Data) != 1 || page.NextCursor != "next-cursor" || !page.HasMore {
		t.Fatalf("page=%+v", page)
	}
	row := page.Data[0]
	if row["id"] != "930" || row["versionId"] != "101" || row["reason"] != "invalid_case_proposal" {
		t.Fatalf("run row=%v", row)
	}
	if row["agentSessionId"] != float64(7001) {
		t.Fatalf("agentSessionId=%v, want the numeric P1 id", row["agentSessionId"])
	}
	errorsList, _ := row["errors"].([]any)
	if len(errorsList) != 1 {
		t.Fatalf("errors=%v, want one validation error", row["errors"])
	}

	rec = call(t, h, http.MethodGet, "/api/versions/101/mindmap/runs/930", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /runs/930=%d body=%s, want 200", rec.Code, rec.Body)
	}
	if data := decodeData(t, rec); data["id"] != "930" {
		t.Fatalf("run detail=%v", data)
	}

	rec = call(t, h, http.MethodGet, "/api/versions/101/mindmap/runs/999", tokenUser, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /runs/999=%d body=%s, want 404", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "run_not_found" {
		t.Fatalf("reason=%q, want run_not_found", got)
	}
}

// --- submit-review / review -----------------------------------------------

// TestSubmitReviewRejectsEmptyGraph proves the submit gate reports no_cases.
func TestSubmitReviewRejectsEmptyGraph(t *testing.T) {
	deps := mindmaphttp.Deps{
		Graph: &graphFake{getFn: func(context.Context, int64) (application.GraphView, error) {
			view := graphFixture()
			view.VersionStatus = "designing"
			view.Nodes = []application.GraphNode{{
				Node: domain.CaseNode{ID: 800, VersionID: 101, Kind: domain.NodeKindScenario, Title: "scenario"},
			}}
			return view, nil
		}},
		Versions: &versionLifecycleFake{},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/submit-review", tokenUser, `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("submit-review empty graph=%d body=%s, want 409", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "no_cases" {
		t.Fatalf("reason=%q, want no_cases", got)
	}
}

// TestSubmitReviewRejectsCaseWithoutRequirementSource proves the per-case
// requirement-source gate of endpoint 20.
func TestSubmitReviewRejectsCaseWithoutRequirementSource(t *testing.T) {
	deps := mindmaphttp.Deps{
		Graph: &graphFake{getFn: func(context.Context, int64) (application.GraphView, error) {
			view := graphFixture()
			view.VersionStatus = "designing"
			view.Nodes[0].Sources = nil
			return view, nil
		}},
		Versions: &versionLifecycleFake{},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/submit-review", tokenUser, `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("submit-review uncovered case=%d body=%s, want 409", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "invalid_case_tree" {
		t.Fatalf("reason=%q, want invalid_case_tree", got)
	}
}

// TestSubmitReviewEntersReviewingAndPublishesGate proves the happy path: the
// version moves to reviewing, a gate.pending event is published and the response
// carries the version projection with a decimal string id.
func TestSubmitReviewEntersReviewingAndPublishesGate(t *testing.T) {
	events := &eventFake{}
	deps := mindmaphttp.Deps{
		Graph: &graphFake{getFn: func(context.Context, int64) (application.GraphView, error) {
			view := graphFixture()
			view.VersionStatus = "designing"
			return view, nil
		}},
		Versions: &versionLifecycleFake{enterReviewFn: func(_ context.Context, versionID, actor int64) (application.VersionSnapshot, error) {
			return application.VersionSnapshot{ID: versionID, Name: "v1", Status: "reviewing"}, nil
		}},
		Events: events,
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/submit-review", tokenUser, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit-review=%d body=%s, want 200", rec.Code, rec.Body)
	}
	data := decodeData(t, rec)
	version, _ := data["version"].(map[string]any)
	if version["id"] != "101" || version["status"] != "reviewing" {
		t.Fatalf("version=%v", version)
	}
	if len(events.published) != 1 || events.published[0].Type != domain.EventGatePending {
		t.Fatalf("events=%+v, want exactly one gate.pending", events.published)
	}
}

// TestApproveBlockedByBlockerFindings proves the blocker gate keeps its reason
// and exposes the finding ids as decimal strings.
func TestApproveBlockedByBlockerFindings(t *testing.T) {
	deps := mindmaphttp.Deps{
		Reviews: &reviewFake{approveFn: func(context.Context, int64, int64, application.ReviewCommand) (application.ApproveResult, error) {
			return application.ApproveResult{}, &application.BlockerFindingsError{
				Domain:     &domain.Error{Kind: domain.KindConflict, Reason: "blocker_findings_open", Message: "open blocker findings"},
				FindingIDs: []int64{920},
			}
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/review", tokenUser, `{"decision":"approve"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("approve blocked=%d body=%s, want 409", rec.Code, rec.Body)
	}
	if got := reasonOf(t, rec); got != "blocker_findings_open" {
		t.Fatalf("reason=%q, want blocker_findings_open", got)
	}
	details, _ := decodeError(t, rec)["details"].(map[string]any)
	ids, _ := details["findingIds"].([]any)
	if len(ids) != 1 || ids[0] != "920" {
		t.Fatalf("details=%v, want findingIds [\"920\"]", details)
	}
}

// TestApproveReturnsFrozenBaseline pins the approve response: version, baseline
// and review, all with string mindmap ids.
func TestApproveReturnsFrozenBaseline(t *testing.T) {
	frozenAt := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	deps := mindmaphttp.Deps{
		Reviews: &reviewFake{approveFn: func(context.Context, int64, int64, application.ReviewCommand) (application.ApproveResult, error) {
			return application.ApproveResult{
				Version:  application.VersionSnapshot{ID: 101, Name: "v1", Status: "executing"},
				Baseline: domain.CaseBaseline{ID: 950, VersionID: 101, ReviewID: 960, GraphHash: strings.Repeat("ab", 32), CaseCount: 1, NodeCount: 2, FrozenAt: frozenAt},
				Review:   domain.CaseReview{ID: 960, VersionID: 101, Status: domain.ReviewApproved, ReviewerID: regularUser.ID, CreatedAt: frozenAt, UpdatedAt: frozenAt},
			}, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/review", tokenUser,
		`{"decision":"approve","acknowledgeFindings":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve=%d body=%s, want 200", rec.Code, rec.Body)
	}
	data := decodeData(t, rec)
	baseline, _ := data["baseline"].(map[string]any)
	if baseline["id"] != "950" || baseline["graphHash"] != strings.Repeat("ab", 32) {
		t.Fatalf("baseline=%v", baseline)
	}
	review, _ := data["review"].(map[string]any)
	if review["id"] != "960" || review["status"] != "approved" {
		t.Fatalf("review=%v", review)
	}
}

// TestRejectReturnsDesigning proves a rejection answers 200 with a null baseline.
func TestRejectReturnsDesigning(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	deps := mindmaphttp.Deps{
		Reviews: &reviewFake{rejectFn: func(_ context.Context, versionID, actor int64, comment string) (application.RejectResult, error) {
			if comment != "needs work" {
				t.Fatalf("comment=%q", comment)
			}
			return application.RejectResult{
				Version: application.VersionSnapshot{ID: versionID, Name: "v1", Status: "designing"},
				Review:  domain.CaseReview{ID: 960, VersionID: versionID, Status: domain.ReviewRejected, ReviewerID: actor, CreatedAt: now, UpdatedAt: now},
			}, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPost, "/api/versions/101/mindmap/review", tokenUser,
		`{"decision":"reject","comment":"needs work"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject=%d body=%s, want 200", rec.Code, rec.Body)
	}
	data := decodeData(t, rec)
	if data["baseline"] != nil {
		t.Fatalf("baseline=%v, want null on rejection", data["baseline"])
	}
	version, _ := data["version"].(map[string]any)
	if version["status"] != "designing" {
		t.Fatalf("version=%v", version)
	}
}

// TestListReviewsAndFindings pins the review page envelope and the flat finding
// list of spec §7.3.
func TestListReviewsAndFindings(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	deps := mindmaphttp.Deps{
		Reviews: &reviewFake{
			listFn: func(_ context.Context, versionID int64, limit int, cursor string) ([]domain.CaseReview, string, bool, error) {
				return []domain.CaseReview{{ID: 960, VersionID: versionID, Status: domain.ReviewRejected, ReviewerID: regularUser.ID, CreatedAt: now, UpdatedAt: now}}, "", false, nil
			},
			listFindingsFn: func(_ context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error) {
				if status != "open" || severity != "blocker" || nodeID == nil || *nodeID != 900 {
					t.Fatalf("finding filter=%q/%q/%v", status, severity, nodeID)
				}
				return []domain.CaseReviewFinding{{ID: 920, ReviewID: 960, NodeID: ptrInt64(900), Category: domain.FindingAssertion, Severity: domain.SeverityBlocker, Status: domain.FindingOpen, Detail: "missing assertion"}}, nil
			},
		},
	}
	h := newServer(t, deps)

	rec := call(t, h, http.MethodGet, "/api/versions/101/mindmap/reviews?limit=10", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /reviews=%d body=%s, want 200", rec.Code, rec.Body)
	}
	var page struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0]["id"] != "960" {
		t.Fatalf("reviews page=%+v", page)
	}

	rec = call(t, h, http.MethodGet, "/api/versions/101/mindmap/findings?status=open&severity=blocker&nodeId=900", tokenUser, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /findings=%d body=%s, want 200", rec.Code, rec.Body)
	}
	var flat struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &flat); err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	if len(flat.Data) != 1 || flat.Data[0]["id"] != "920" || flat.Data[0]["nodeId"] != "900" {
		t.Fatalf("findings=%+v", flat.Data)
	}
}

// TestUpdateFindingReturnsFixed proves the human disposition route.
func TestUpdateFindingReturnsFixed(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	deps := mindmaphttp.Deps{
		Reviews: &reviewFake{updateFindingFn: func(_ context.Context, versionID, findingID, actor int64, status string) (domain.CaseReviewFinding, error) {
			if findingID != 920 || status != "fixed" || actor != regularUser.ID {
				t.Fatalf("update args=%d/%d/%q", findingID, actor, status)
			}
			return domain.CaseReviewFinding{ID: 920, ReviewID: 960, Category: domain.FindingAssertion, Severity: domain.SeverityBlocker, Status: domain.FindingFixed, Detail: "x", CreatedAt: now, UpdatedAt: now}, nil
		}},
	}
	rec := call(t, newServer(t, deps), http.MethodPatch, "/api/versions/101/mindmap/findings/920", tokenUser, `{"status":"fixed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH finding=%d body=%s, want 200", rec.Code, rec.Body)
	}
	if data := decodeData(t, rec); data["status"] != "fixed" {
		t.Fatalf("finding=%v", data)
	}
}
