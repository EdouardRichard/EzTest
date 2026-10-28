package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/filestore"
)

// exportFixture wires the export service to the graph fixture's in-memory
// repositories, a real filesystem export store in a temporary directory, a
// finding repository, a code-anchor gateway, an event sink and an audit sink.
// It reuses graphFixture for the read ports of the whole graph (nodes, steps,
// assertions, sources, baseline, version, requirements, ids and clock).
type exportFixture struct {
	*graphFixture
	findings []domain.CaseReviewFinding
	exports  map[int64]domain.CaseExport
	anchors  map[int64][]application.Anchor
	audits   []application.AuditEvent
	root     string
	store    *filestore.Store
}

// newExportFixture returns an export fixture rooted in a fresh temporary export
// directory.
func newExportFixture(t *testing.T) *exportFixture {
	t.Helper()
	graph := newGraphFixture()
	graph.version = application.VersionSnapshot{ID: 1, Name: "订单版本", Status: "designing"}
	f := &exportFixture{
		graphFixture: graph,
		exports:      make(map[int64]domain.CaseExport),
		anchors:      make(map[int64][]application.Anchor),
		root:         filepath.Join(t.TempDir(), "exports"),
	}
	store, err := filestore.New(f.root)
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	f.store = store
	return f
}

// service wires the export service to the fixture ports.
func (f *exportFixture) service() *application.ExportService {
	return application.NewExportService(application.ExportDeps{
		Versions:     graphVersionLifecycle{f.graphFixture},
		Requirements: graphRequirementReader{f.graphFixture},
		Nodes:        graphNodeRepo{f.graphFixture},
		Steps:        graphStepRepo{f.graphFixture},
		Assertions:   graphAssertionRepo{f.graphFixture},
		Sources:      graphSourceRepo{f.graphFixture},
		Findings:     exportFindingRepo{f},
		Baselines:    graphBaselineRepo{f.graphFixture},
		Exports:      exportRepo{f},
		Anchors:      exportAnchorGateway{f},
		Store:        f.store,
		Events:       graphEventSink{f.graphFixture},
		IDs:          graphIDGenerator{f.graphFixture},
		Clock:        graphClock{f.graphFixture},
		Audit:        exportAuditRecorder{f},
	})
}

// addRequirement registers one clarified requirement of the version.
func (f *exportFixture) addRequirement(id int64, title string) {
	f.confirmed = append(f.confirmed, application.RequirementSnapshot{
		ID:        id,
		VersionID: f.version.ID,
		Title:     title,
		Status:    "clarified",
	})
}

// addScenario registers one scenario node with the given order.
func (f *exportFixture) addScenario(id int64, parentID *int64, title string, sortOrder int) domain.CaseNode {
	f.graphFixture.seedScenario(id, parentID)
	node := f.graphFixture.nodes[id]
	node.Title = title
	node.SortOrder = sortOrder
	f.graphFixture.nodes[id] = node
	return node
}

// addCase registers one complete case: two steps, two assertions, one
// requirement source and the given priority/test data/preconditions.
func (f *exportFixture) addCase(id int64, parentID *int64, title string, requirementID int64, sortOrder int) domain.CaseNode {
	node := domain.CaseNode{
		ID:            id,
		VersionID:     f.version.ID,
		ParentID:      parentID,
		Kind:          domain.NodeKindCase,
		Title:         title,
		Priority:      domain.PriorityP1,
		Preconditions: "用户已登录",
		TestData:      []byte(`{"user":"alice"}`),
		Source:        domain.NodeSourceUserInput,
		Status:        domain.NodeStatusDraft,
		SortOrder:     sortOrder,
		Revision:      1,
		CreatedAt:     f.now,
		UpdatedAt:     f.now,
	}
	f.graphFixture.nodes[id] = node

	first := domain.CaseStep{
		ID:        id*10 + 1,
		NodeID:    id,
		Seq:       1,
		Action:    "打开下单页",
		Expected:  "页面渲染完成",
		Data:      "sku=1001",
		Source:    domain.StepSourceUserInput,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	second := domain.CaseStep{
		ID:        id*10 + 2,
		NodeID:    id,
		Seq:       2,
		Action:    "提交订单",
		Expected:  "订单创建成功",
		Source:    domain.StepSourceUserInput,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.graphFixture.steps[first.ID] = first
	f.graphFixture.steps[second.ID] = second

	uiAssertion := domain.CaseAssertion{
		ID:         id*10 + 3,
		NodeID:     id,
		StepID:     exportInt64Ptr(first.ID),
		Kind:       domain.AssertionKindUI,
		Target:     "提交按钮",
		Expression: "订单状态为已创建",
		Expected:   "已创建",
		Source:     domain.NodeSourceUserInput,
		CreatedAt:  f.now,
		UpdatedAt:  f.now,
	}
	apiAssertion := domain.CaseAssertion{
		ID:         id*10 + 4,
		NodeID:     id,
		StepID:     exportInt64Ptr(second.ID),
		Kind:       domain.AssertionKindAPI,
		Target:     "/api/orders",
		Expression: "响应码为 201",
		Expected:   "201",
		Source:     domain.NodeSourceUserInput,
		CreatedAt:  f.now,
		UpdatedAt:  f.now,
	}
	f.graphFixture.assertions[uiAssertion.ID] = uiAssertion
	f.graphFixture.assertions[apiAssertion.ID] = apiAssertion

	ref := requirementID
	source := domain.CaseSource{
		ID:         id*10 + 5,
		VersionID:  f.version.ID,
		NodeID:     id,
		RefKind:    domain.RefRequirement,
		RefID:      &ref,
		Source:     domain.NodeSourceUserInput,
		Confidence: domain.ConfidenceHigh,
		CreatedAt:  f.now,
		UpdatedAt:  f.now,
	}
	f.graphFixture.sources[source.ID] = source
	return node
}

// addAnchor attaches one code anchor to a case node.
func (f *exportFixture) addAnchor(nodeID, anchorID int64, path string, start, end int) {
	f.anchors[nodeID] = append(f.anchors[nodeID], application.Anchor{
		ID:         anchorID,
		VersionID:  f.version.ID,
		RepoID:     1,
		SubjectID:  nodeID,
		FilePath:   path,
		StartLine:  exportIntPtr(start),
		EndLine:    exportIntPtr(end),
		Confidence: domain.ConfidenceHigh,
		CreatedBy:  7,
	})
}

// addFinding registers one review finding of the version.
func (f *exportFixture) addFinding(id int64, nodeID *int64, severity, detail string) domain.CaseReviewFinding {
	finding := domain.CaseReviewFinding{
		ID:        id,
		ReviewID:  900,
		NodeID:    nodeID,
		Category:  domain.FindingCoverage,
		Severity:  severity,
		Detail:    detail,
		Status:    domain.FindingOpen,
		Source:    domain.FindingSourceAI,
		CreatedBy: 7,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.findings = append(f.findings, finding)
	return finding
}

// readStored returns the bytes of one stored export file.
func (f *exportFixture) readStored(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read stored export %q: %v", rel, err)
	}
	return data
}

// storedNames returns the file names of one export directory below the root.
func (f *exportFixture) storedNames(t *testing.T, relDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.root, filepath.FromSlash(relDir)))
	if err != nil {
		t.Fatalf("read export dir %q: %v", relDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// exportInt64Ptr returns a pointer to value.
func exportInt64Ptr(value int64) *int64 { return &value }

// exportIntPtr returns a pointer to value.
func exportIntPtr(value int) *int { return &value }

// exportReason returns the stable details.reason of a mindmap error.
func exportReason(t *testing.T, err error) string {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *domain.Error, got %T (%v)", err, err)
	}
	return domainErr.Reason
}

// mustExport creates one export and fails the test on error.
func mustExport(t *testing.T, service *application.ExportService, format string) domain.CaseExport {
	t.Helper()
	export, err := service.Create(context.Background(), 1, 42, format)
	if err != nil {
		t.Fatalf("Create(%s): %v", format, err)
	}
	return export
}

// exportRepo is an in-memory application.ExportRepository that mirrors the
// PostgreSQL adapter's scoping, ordering and baseline uniqueness.
type exportRepo struct {
	f *exportFixture
}

// Create stores one export row; a second frozen (version, format, baseline) is
// the uk_case_export_baseline conflict.
func (r exportRepo) Create(_ context.Context, export domain.CaseExport) (domain.CaseExport, error) {
	if _, exists := r.f.exports[export.ID]; exists {
		return domain.CaseExport{}, domain.NewConflict("duplicate_key", "duplicate export id")
	}
	if export.BaselineID != nil {
		for _, existing := range r.f.exports {
			if existing.VersionID == export.VersionID && existing.Format == export.Format &&
				existing.BaselineID != nil && *existing.BaselineID == *export.BaselineID {
				return domain.CaseExport{}, domain.NewConflict("export_conflict",
					"version already has an export for this baseline")
			}
		}
	}
	r.f.exports[export.ID] = export
	return export, nil
}

// Get returns one version-scoped export.
func (r exportRepo) Get(_ context.Context, versionID, exportID int64) (domain.CaseExport, error) {
	export, ok := r.f.exports[exportID]
	if !ok || export.VersionID != versionID {
		return domain.CaseExport{}, domain.NewNotFound("export", exportID)
	}
	return export, nil
}

// List returns newest-first exports with a decimal-id keyset cursor.
func (r exportRepo) List(_ context.Context, versionID int64, format string, limit int, cursor string) ([]domain.CaseExport, string, bool, error) {
	if limit <= 0 {
		return nil, "", false, errors.New("exportRepo: limit must be positive")
	}
	ordered := make([]domain.CaseExport, 0, len(r.f.exports))
	for _, export := range r.f.exports {
		if export.VersionID != versionID || (format != "" && export.Format != format) {
			continue
		}
		ordered = append(ordered, export)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.After(ordered[j].CreatedAt)
		}
		return ordered[i].ID > ordered[j].ID
	})
	if cursor != "" {
		cursorID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", false, err
		}
		filtered := ordered[:0]
		for _, export := range ordered {
			if export.ID < cursorID {
				filtered = append(filtered, export)
			}
		}
		ordered = filtered
	}
	more := len(ordered) > limit
	next := ""
	if more {
		ordered = ordered[:limit]
		next = strconv.FormatInt(ordered[len(ordered)-1].ID, 10)
	}
	return ordered, next, more, nil
}

// FindByBaselineFormat returns the reusable export of the same (version, format,
// baseline); a nil baseline matches unfrozen exports.
func (r exportRepo) FindByBaselineFormat(_ context.Context, versionID int64, format string, baselineID *int64) (domain.CaseExport, error) {
	var found domain.CaseExport
	ok := false
	for _, export := range r.f.exports {
		if export.VersionID != versionID || export.Format != format {
			continue
		}
		switch {
		case baselineID == nil && export.BaselineID != nil:
			continue
		case baselineID != nil && (export.BaselineID == nil || *export.BaselineID != *baselineID):
			continue
		}
		if !ok || export.ID > found.ID {
			found = export
			ok = true
		}
	}
	if !ok {
		return domain.CaseExport{}, domain.NewNotFound("export", versionID)
	}
	return found, nil
}

// exportFindingRepo is an in-memory application.FindingRepository limited to the
// read/list behavior the export service needs.
type exportFindingRepo struct {
	f *exportFixture
}

// CreateBatch is not used by the export service.
func (r exportFindingRepo) CreateBatch(context.Context, []domain.CaseReviewFinding) error {
	return errors.New("exportFindingRepo: CreateBatch is not used")
}

// List filters the fixture findings by status, severity and node.
func (r exportFindingRepo) List(_ context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error) {
	findings := make([]domain.CaseReviewFinding, 0)
	for _, finding := range r.f.findings {
		if status != "" && finding.Status != status {
			continue
		}
		if severity != "" && finding.Severity != severity {
			continue
		}
		if nodeID != nil && (finding.NodeID == nil || *finding.NodeID != *nodeID) {
			continue
		}
		findings = append(findings, finding)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings, nil
}

// UpdateStatus is not used by the export service.
func (r exportFindingRepo) UpdateStatus(context.Context, int64, string, int64) (domain.CaseReviewFinding, error) {
	return domain.CaseReviewFinding{}, errors.New("exportFindingRepo: UpdateStatus is not used")
}

// DismissOpenAI is not used by the export service.
func (r exportFindingRepo) DismissOpenAI(context.Context, int64) (int, error) {
	return 0, nil
}

// CountOpenBlockers counts the open blocker findings of the fixture.
func (r exportFindingRepo) CountOpenBlockers(context.Context, int64) (int, error) {
	count := 0
	for _, finding := range r.f.findings {
		if finding.Status == domain.FindingOpen && finding.Severity == domain.SeverityBlocker {
			count++
		}
	}
	return count, nil
}

// exportAnchorGateway is an in-memory application.CodeAnchorGateway: the export
// service only reads the anchors of each case.
type exportAnchorGateway struct {
	f *exportFixture
}

// Create is not used by the export service.
func (g exportAnchorGateway) Create(context.Context, int64, int64, application.AnchorParams) (application.Anchor, error) {
	return application.Anchor{}, errors.New("exportAnchorGateway: Create is not used")
}

// List returns the anchors of one case node.
func (g exportAnchorGateway) List(_ context.Context, versionID, nodeID int64) ([]application.Anchor, error) {
	return append([]application.Anchor(nil), g.f.anchors[nodeID]...), nil
}

// Delete is not used by the export service.
func (g exportAnchorGateway) Delete(context.Context, int64, int64) error {
	return errors.New("exportAnchorGateway: Delete is not used")
}

// exportAuditRecorder records the audit entries of the export service.
type exportAuditRecorder struct {
	f *exportFixture
}

// Record appends one audit event to the fixture.
func (r exportAuditRecorder) Record(_ context.Context, event application.AuditEvent) error {
	r.f.audits = append(r.f.audits, event)
	return nil
}

// TestExportCreateMarkdown covers the Markdown structure of spec §6.10: title,
// version/baseline header, coverage matrix with the uncovered list, cases
// grouped by scenario with sources/priority/preconditions/testData/steps/
// assertions/anchors, the open findings section and the generated timestamp.
// It also pins the stored metadata (path, sha256, size, node count), the
// post-commit case.export_ready event and the audit entry.
func TestExportCreateMarkdown(t *testing.T) {
	f := newExportFixture(t)
	f.addRequirement(9001, "下单")
	f.addRequirement(9002, "退款")
	f.addScenario(8001, nil, "下单主流程", 0)
	f.addCase(7001, exportInt64Ptr(8001), "提交订单", 9001, 0)
	f.addCase(7002, exportInt64Ptr(8001), "取消订单", 9001, 1)
	f.addAnchor(7001, 6001, "src/order/submit.ts", 10, 20)
	f.addFinding(5001, exportInt64Ptr(7001), domain.SeverityMajor, "缺少异常断言")
	// A dismissed finding must not leak into the open-findings section.
	dismissed := f.addFinding(5002, nil, domain.SeverityMinor, "已修复的问题")
	dismissed.Status = domain.FindingDismissed
	f.findings[len(f.findings)-1] = dismissed

	service := f.service()
	export := mustExport(t, service, domain.ExportMarkdown)

	if export.VersionID != 1 || export.Format != domain.ExportMarkdown {
		t.Fatalf("export = %+v, want version 1 markdown", export)
	}
	if !strings.HasPrefix(export.FilePath, "versions/1/draft/markdown-") ||
		!strings.HasSuffix(export.FilePath, ".md") {
		t.Fatalf("file path = %q, want versions/1/draft/markdown-<id>.md", export.FilePath)
	}
	if filepath.IsAbs(export.FilePath) || strings.Contains(export.FilePath, f.root) {
		t.Fatalf("file path %q must be root-relative", export.FilePath)
	}
	if export.BaselineID != nil {
		t.Fatalf("baseline = %v, want nil for an unfrozen draft", *export.BaselineID)
	}
	if export.NodeCount != 3 {
		t.Fatalf("node count = %d, want 3 (one scenario and two cases)", export.NodeCount)
	}
	if export.CreatedBy != 42 {
		t.Fatalf("created by = %d, want the actor", export.CreatedBy)
	}
	if !export.CreatedAt.Equal(f.now) || !export.UpdatedAt.Equal(f.now) {
		t.Fatalf("timestamps = %v/%v, want the injected clock", export.CreatedAt, export.UpdatedAt)
	}

	content := f.readStored(t, export.FilePath)
	sum := sha256.Sum256(content)
	if export.FileSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %q, want %q", export.FileSHA256, hex.EncodeToString(sum[:]))
	}
	if export.SizeBytes != int64(len(content)) {
		t.Fatalf("size = %d, want %d", export.SizeBytes, len(content))
	}
	if !bytes.HasPrefix(content, []byte("# ")) {
		t.Fatalf("markdown must start with a title, got %q", firstLine(content))
	}
	if bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("markdown must not start with a UTF-8 BOM")
	}
	if strings.Contains(firstLine(content), "---") {
		t.Fatal("markdown must not carry YAML front matter")
	}
	text := string(content)
	for _, want := range []string{
		"订单版本",
		"（1）",
		"草稿（未冻结）",
		"生成时间：2026-10-28T10:00:00Z",
		"## 覆盖矩阵",
		"| 需求 | 覆盖用例 |",
		"R9001 下单",
		"R9002 退款",
		"### 未覆盖需求",
		"## 用例",
		"### 场景：下单主流程",
		"#### 用例：提交订单",
		"优先级：P1",
		"来源：requirement:9001",
		"前置条件：用户已登录",
		`测试数据：` + "`" + `{"user":"alice"}` + "`",
		"锚点：",
		"src/order/submit.ts:10-20（high）",
		"**步骤**",
		"打开下单页",
		"页面渲染完成",
		"sku=1001",
		"提交订单",
		"订单创建成功",
		"**断言**",
		"[ui] 提交按钮：订单状态为已创建（期望：已创建）",
		"[api] /api/orders：响应码为 201（期望：201）",
		"## 评审发现项",
		"[major] coverage：缺少异常断言",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "已修复的问题") {
		t.Fatalf("markdown must only list open findings:\n%s", text)
	}
	if strings.Index(text, "提交订单") > strings.Index(text, "取消订单") {
		t.Fatal("cases must be ordered by sortOrder (提交订单 before 取消订单)")
	}
	if !strings.Contains(text, "（用例：提交订单）") {
		t.Fatalf("finding must reference the node title:\n%s", text)
	}

	if len(f.events) != 1 {
		t.Fatalf("events = %d, want exactly one case.export_ready", len(f.events))
	}
	event := f.events[0]
	if event.Type != domain.EventCaseExportReady || event.VersionID != 1 || event.ActorID != 42 {
		t.Fatalf("event = %+v, want a case.export_ready of version 1 by the actor", event)
	}
	if !strings.Contains(event.Summary, "markdown") {
		t.Fatalf("event summary = %q, want the format", event.Summary)
	}
	if len(event.Detail) != 3 {
		t.Fatalf("event detail = %v, want exactly exportId/format/size", event.Detail)
	}
	if got, ok := event.Detail["exportId"].(int64); !ok || got != export.ID {
		t.Fatalf("event detail exportId = %v, want %d", event.Detail["exportId"], export.ID)
	}
	if got, ok := event.Detail["format"].(string); !ok || got != domain.ExportMarkdown {
		t.Fatalf("event detail format = %v, want markdown", event.Detail["format"])
	}
	if got, ok := event.Detail["sizeBytes"].(int64); !ok || got != export.SizeBytes {
		t.Fatalf("event detail sizeBytes = %v, want %d", event.Detail["sizeBytes"], export.SizeBytes)
	}
	if strings.Contains(event.Summary, "打开下单页") {
		t.Fatalf("event summary must not carry export content: %q", event.Summary)
	}

	if len(f.audits) != 1 {
		t.Fatalf("audits = %d, want one case.export.created entry", len(f.audits))
	}
	audit := f.audits[0]
	if audit.Action != "case.export.created" || audit.TargetType != "case_export" ||
		audit.TargetID != strconv.FormatInt(export.ID, 10) || audit.ActorID != 42 {
		t.Fatalf("audit = %+v, want case.export.created of the export id", audit)
	}
	if len(audit.Metadata) == 0 {
		t.Fatalf("audit metadata = %v, want format and size", audit.Metadata)
	}
	for _, value := range audit.Metadata {
		if text, ok := value.(string); ok && strings.Contains(text, "打开下单页") {
			t.Fatalf("audit metadata must not carry export content: %v", audit.Metadata)
		}
	}
}

// TestExportDraftNeverReuses covers spec §6.4.6/§6.10: without a baseline every
// POST /export creates a new file and a new metadata row, even for the same
// format.
func TestExportDraftNeverReuses(t *testing.T) {
	f := newExportFixture(t)
	f.addRequirement(9001, "下单")
	f.addCase(7001, nil, "提交订单", 9001, 0)

	service := f.service()
	first := mustExport(t, service, domain.ExportMarkdown)
	second := mustExport(t, service, domain.ExportMarkdown)
	if first.ID == second.ID {
		t.Fatalf("draft exports reused id %d; every export must be new", first.ID)
	}
	if first.FilePath == second.FilePath {
		t.Fatalf("draft exports reused file path %q", first.FilePath)
	}
	if len(f.exports) != 2 {
		t.Fatalf("export rows = %d, want 2", len(f.exports))
	}
	if names := f.storedNames(t, "versions/1/draft"); len(names) != 2 {
		t.Fatalf("stored files = %v, want two distinct drafts", names)
	}
	for _, export := range []domain.CaseExport{first, second} {
		content := f.readStored(t, export.FilePath)
		sum := sha256.Sum256(content)
		if export.FileSHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("export %d sha256 does not match its file", export.ID)
		}
	}
	if len(f.events) != 2 {
		t.Fatalf("events = %d, want one per created export", len(f.events))
	}
}

// TestExportReusesOnlyWhenBaselineBound covers spec §6.10: after a freeze the
// export is bound to the baseline and (version, format, baseline) is reused
// without writing a second file or another event.
func TestExportReusesOnlyWhenBaselineBound(t *testing.T) {
	f := newExportFixture(t)
	f.addRequirement(9001, "下单")
	f.addCase(7001, nil, "提交订单", 9001, 0)
	f.baseline = &domain.CaseBaseline{
		ID:        555,
		VersionID: f.version.ID,
		ReviewID:  900,
		NodeCount: 1,
		CaseCount: 1,
		GraphHash: strings.Repeat("a", 64),
		FrozenBy:  7,
		FrozenAt:  f.now,
	}

	service := f.service()
	first := mustExport(t, service, domain.ExportMarkdown)
	if first.BaselineID == nil || *first.BaselineID != 555 {
		t.Fatalf("baseline = %v, want 555 after freezing", first.BaselineID)
	}
	if !strings.Contains(first.FilePath, "baseline-555") {
		t.Fatalf("file path = %q, want a baseline-555 directory", first.FilePath)
	}

	reused := mustExport(t, service, domain.ExportMarkdown)
	if reused.ID != first.ID || reused.FilePath != first.FilePath {
		t.Fatalf("reused export = %d/%q, want %d/%q", reused.ID, reused.FilePath, first.ID, first.FilePath)
	}
	if len(f.exports) != 1 {
		t.Fatalf("export rows = %d, want the single reusable baseline export", len(f.exports))
	}
	if names := f.storedNames(t, "versions/1/baseline-555"); len(names) != 1 {
		t.Fatalf("stored files = %v, want a single reusable file", names)
	}
	if len(f.events) != 1 {
		t.Fatalf("events = %d, reuse must not publish a second export event", len(f.events))
	}

	// A different format is a different (version, format, baseline) key.
	opml := mustExport(t, service, domain.ExportOPML)
	if opml.ID == first.ID {
		t.Fatal("a different format must create a separate export")
	}
	if !strings.Contains(opml.FilePath, "baseline-555") || !strings.HasSuffix(opml.FilePath, ".opml") {
		t.Fatalf("opml file path = %q, want baseline-555 and .opml", opml.FilePath)
	}
	if len(f.exports) != 2 {
		t.Fatalf("export rows = %d, want markdown and opml", len(f.exports))
	}
}

// TestExportMermaid covers the Mermaid format of spec §6.10: mindmap syntax with
// the version as root, requirement groups, scenario/case leaves, 60-rune leaf
// truncation, duplicate suffixes and escaping of ()[]{}/newlines. Details stay in
// Markdown only.
func TestExportMermaid(t *testing.T) {
	f := newExportFixture(t)
	f.addRequirement(9001, "下单")
	f.addRequirement(9002, "退款")
	f.addScenario(8001, nil, "下单主流程", 0)
	f.addCase(7001, exportInt64Ptr(8001), "提交订单 (含折扣) [急速] {A}", 9001, 0)
	f.addCase(7002, exportInt64Ptr(8001), "重复用例", 9001, 1)
	f.addCase(7003, exportInt64Ptr(8001), "重复用例", 9001, 2)
	f.addCase(7004, exportInt64Ptr(8001), strings.Repeat("长", 70), 9001, 3)
	f.addCase(7005, exportInt64Ptr(8001), "第一行\n第二行", 9001, 4)

	export := mustExport(t, f.service(), domain.ExportMermaid)
	text := string(f.readStored(t, export.FilePath))

	if !strings.HasPrefix(text, "mindmap\n") {
		t.Fatalf("mermaid must start with the mindmap keyword:\n%s", text)
	}
	if !strings.Contains(text, "root((订单版本))") {
		t.Fatalf("mermaid root must be the version name:\n%s", text)
	}
	for _, want := range []string{
		"R9001 下单",
		"R9002 退款",
		"下单主流程",
		`\(含折扣\)`,
		`\[急速\]`,
		`\{A\}`,
		"第一行 第二行",
		`重复用例 \(2\)`,
		strings.Repeat("长", 60),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("mermaid is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "提交订单 (含折扣)") {
		t.Fatalf("mermaid must escape the case label parentheses:\n%s", text)
	}
	if strings.Contains(text, "第一行\n第二行") {
		t.Fatalf("mermaid must fold newlines inside a label:\n%s", text)
	}
	if strings.Contains(text, strings.Repeat("长", 61)) {
		t.Fatalf("mermaid leaf labels must be truncated to 60 runes:\n%s", text)
	}
	if strings.Count(text, "重复用例") != 2 {
		t.Fatalf("both duplicate leaves must be rendered once each:\n%s", text)
	}
	// Details are Markdown-only.
	for _, detail := range []string{"优先级", "P1", "用户已登录", "打开下单页", "订单状态为已创建", "src/order/submit.ts"} {
		if strings.Contains(text, detail) {
			t.Fatalf("mermaid must not carry the detail %q:\n%s", detail, text)
		}
	}
}

// exportOPMLDocument is the minimal OPML 2.0 shape the test decodes; it is
// deliberately independent of the production (unexported) encoding structs.
type exportOPMLDocument struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    struct {
		Title string `xml:"title"`
	} `xml:"head"`
	Body struct {
		Outlines []exportOPMLOutline `xml:"outline"`
	} `xml:"body"`
}

// exportOPMLOutline is one OPML outline of the test decoder.
type exportOPMLOutline struct {
	Text     string              `xml:"text,attr"`
	Note     string              `xml:"_note,attr"`
	Outlines []exportOPMLOutline `xml:"outline"`
}

// findOutline returns the first outline of the tree with the exact text.
func findOutline(outlines []exportOPMLOutline, text string) (exportOPMLOutline, bool) {
	for _, outline := range outlines {
		if outline.Text == text {
			return outline, true
		}
		if found, ok := findOutline(outline.Outlines, text); ok {
			return found, true
		}
	}
	return exportOPMLOutline{}, false
}

// TestExportOPML covers the OPML 2.0 format of spec §6.10 with encoding/xml:
// version→requirement→scenario→case hierarchy, the case _note details, the
// uncovered-requirement note, both a nested and a requirement-level case, and
// XML escaping.
func TestExportOPML(t *testing.T) {
	f := newExportFixture(t)
	f.addRequirement(9001, "下单")
	f.addRequirement(9002, "退款")
	f.addScenario(8001, nil, "下单主流程", 0)
	f.addCase(7001, exportInt64Ptr(8001), "提交订单 <含折扣> & 急速", 9001, 0)
	f.addCase(7002, nil, "取消订单", 9001, 1)
	f.addAnchor(7001, 6001, "src/order/submit.ts", 10, 20)

	export := mustExport(t, f.service(), domain.ExportOPML)
	if !strings.HasSuffix(export.FilePath, ".opml") {
		t.Fatalf("opml file path = %q, want an .opml extension", export.FilePath)
	}
	content := f.readStored(t, export.FilePath)
	if !bytes.HasPrefix(content, []byte(xml.Header)) {
		t.Fatalf("opml must start with the XML declaration:\n%s", firstLine(content))
	}

	var document exportOPMLDocument
	if err := xml.Unmarshal(content, &document); err != nil {
		t.Fatalf("xml.Unmarshal: %v\n%s", err, content)
	}
	if document.Version != "2.0" {
		t.Fatalf("opml version = %q, want 2.0", document.Version)
	}
	if document.Head.Title != "订单版本" {
		t.Fatalf("head title = %q, want the version name", document.Head.Title)
	}
	if len(document.Body.Outlines) != 1 {
		t.Fatalf("body outlines = %d, want the single version root", len(document.Body.Outlines))
	}
	root := document.Body.Outlines[0]
	if root.Text != "订单版本" {
		t.Fatalf("root text = %q, want the version name", root.Text)
	}
	if len(root.Outlines) != 2 {
		t.Fatalf("requirement groups = %d, want 2", len(root.Outlines))
	}

	covered := root.Outlines[0]
	if !strings.Contains(covered.Text, "9001") || !strings.Contains(covered.Text, "下单") {
		t.Fatalf("covered requirement = %q, want R9001 下单", covered.Text)
	}
	scenario, ok := findOutline(covered.Outlines, "下单主流程")
	if !ok {
		t.Fatalf("scenario outline missing under %q: %+v", covered.Text, covered.Outlines)
	}
	caseOutline, ok := findOutline(scenario.Outlines, "提交订单 <含折扣> & 急速")
	if !ok {
		t.Fatalf("case outline missing under the scenario: %+v", scenario.Outlines)
	}
	for _, want := range []string{
		"优先级：P1",
		"前置条件：用户已登录",
		`测试数据：{"user":"alice"}`,
		"步骤：",
		"打开下单页",
		"页面渲染完成",
		"订单创建成功",
		"断言：",
		"[ui] 提交按钮：订单状态为已创建（期望：已创建）",
		"[api] /api/orders：响应码为 201（期望：201）",
		"锚点：",
		"src/order/submit.ts:10-20",
	} {
		if !strings.Contains(caseOutline.Note, want) {
			t.Fatalf("case _note is missing %q:\n%s", want, caseOutline.Note)
		}
	}

	// A requirement-level case (no scenario) hangs directly off the requirement.
	direct, ok := findOutline(covered.Outlines, "取消订单")
	if !ok {
		t.Fatalf("requirement-level case outline missing: %+v", covered.Outlines)
	}
	if !strings.Contains(direct.Note, "优先级：P1") {
		t.Fatalf("requirement-level case _note = %q", direct.Note)
	}

	uncovered := root.Outlines[1]
	if !strings.Contains(uncovered.Text, "9002") {
		t.Fatalf("uncovered requirement = %q, want R9002", uncovered.Text)
	}
	if uncovered.Note != "未覆盖" {
		t.Fatalf("uncovered _note = %q, want 未覆盖", uncovered.Note)
	}

	// encoding/xml escapes the attribute text and round-trips it back.
	if !bytes.Contains(content, []byte("&lt;含折扣&gt;")) || !bytes.Contains(content, []byte("&amp; 急速")) {
		t.Fatalf("opml must XML-escape the case title:\n%s", content)
	}
}

// TestExportDownloadVerifiesSHA256 covers the download endpoint contract of spec
// §7.2: the stored file is returned with its metadata, a tampered or missing
// file is export_corrupted, an unknown or cross-version id is export_not_found
// and a metadata path outside the root is rejected.
func TestExportDownloadVerifiesSHA256(t *testing.T) {
	f := newExportFixture(t)
	f.addRequirement(9001, "下单")
	f.addCase(7001, nil, "提交订单", 9001, 0)
	service := f.service()
	export := mustExport(t, service, domain.ExportMarkdown)

	reader, meta, err := service.Download(context.Background(), 1, export.ID)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	downloaded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if meta.ID != export.ID || meta.FileSHA256 != export.FileSHA256 {
		t.Fatalf("metadata = %+v, want export %d", meta, export.ID)
	}
	if !bytes.Equal(downloaded, f.readStored(t, export.FilePath)) {
		t.Fatal("downloaded content does not match the stored file")
	}

	tamperedPath := filepath.Join(f.root, filepath.FromSlash(export.FilePath))
	if err := os.WriteFile(tamperedPath, []byte("# tampered\n"), 0o600); err != nil {
		t.Fatalf("tamper export file: %v", err)
	}
	corrupted, _, err := service.Download(context.Background(), 1, export.ID)
	if err == nil {
		t.Fatal("Download accepted a file whose sha256 does not match the metadata")
	}
	if corrupted != nil {
		t.Fatal("Download returned a reader for a corrupted export")
	}
	if reason := exportReason(t, err); reason != "export_corrupted" {
		t.Fatalf("reason = %q, want export_corrupted", reason)
	}

	if _, _, err := service.Download(context.Background(), 1, 999999); err == nil {
		t.Fatal("Download accepted an unknown export id")
	} else if reason := exportReason(t, err); reason != "export_not_found" {
		t.Fatalf("reason = %q, want export_not_found", reason)
	}
	if _, _, err := service.Download(context.Background(), 2, export.ID); err == nil {
		t.Fatal("Download accepted an export of another version")
	} else if reason := exportReason(t, err); reason != "export_not_found" {
		t.Fatalf("reason = %q, want export_not_found", reason)
	}

	// A metadata row that points outside the root is rejected by the store.
	f.exports[8888] = domain.CaseExport{
		ID: 8888, VersionID: 1, Format: domain.ExportMarkdown,
		FilePath: "../escape.md", FileSHA256: strings.Repeat("a", 64), SizeBytes: 1,
	}
	if _, _, err := service.Download(context.Background(), 1, 8888); err == nil {
		t.Fatal("Download accepted a traversal file path")
	} else if reason := exportReason(t, err); reason != "invalid_storage_path" {
		t.Fatalf("reason = %q, want invalid_storage_path", reason)
	}

	// A metadata row whose file vanished is corruption too.
	f.exports[8889] = domain.CaseExport{
		ID: 8889, VersionID: 1, Format: domain.ExportMarkdown,
		FilePath:   "versions/1/draft/markdown-8889.md",
		FileSHA256: strings.Repeat("a", 64), SizeBytes: 1,
	}
	if _, _, err := service.Download(context.Background(), 1, 8889); err == nil {
		t.Fatal("Download accepted a missing export file")
	} else if reason := exportReason(t, err); reason != "export_corrupted" {
		t.Fatalf("reason = %q, want export_corrupted", reason)
	}
}

// TestExportList covers the export history read of spec §7.2 endpoint 26:
// newest-first ordering, format filtering, a default/capped limit, keyset
// pagination and the unsupported-format error.
func TestExportList(t *testing.T) {
	f := newExportFixture(t)
	f.addRequirement(9001, "下单")
	f.addCase(7001, nil, "提交订单", 9001, 0)
	service := f.service()
	markdown := mustExport(t, service, domain.ExportMarkdown)
	opml := mustExport(t, service, domain.ExportOPML)

	all, next, more, err := service.List(context.Background(), 1, "", 0, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 || more || next != "" {
		t.Fatalf("list = %d items, next=%q, more=%v, want 2/\"\"/false", len(all), next, more)
	}
	if all[0].ID != opml.ID || all[1].ID != markdown.ID {
		t.Fatalf("list order = %d,%d, want newest first (%d,%d)", all[0].ID, all[1].ID, opml.ID, markdown.ID)
	}

	onlyMarkdown, _, _, err := service.List(context.Background(), 1, domain.ExportMarkdown, 10, "")
	if err != nil {
		t.Fatalf("List(markdown): %v", err)
	}
	if len(onlyMarkdown) != 1 || onlyMarkdown[0].Format != domain.ExportMarkdown {
		t.Fatalf("filtered list = %+v, want the single markdown export", onlyMarkdown)
	}

	page, cursor, hasMore, err := service.List(context.Background(), 1, "", 1, "")
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(page) != 1 || !hasMore || cursor == "" {
		t.Fatalf("page 1 = %d items, cursor=%q, more=%v, want 1/non-empty/true", len(page), cursor, hasMore)
	}
	rest, _, moreRest, err := service.List(context.Background(), 1, "", 1, cursor)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(rest) != 1 || moreRest || rest[0].ID == page[0].ID {
		t.Fatalf("page 2 = %+v, want the other export", rest)
	}

	if _, _, _, err := service.List(context.Background(), 1, "pdf", 10, ""); err == nil {
		t.Fatal("List accepted an unsupported format")
	} else if reason := exportReason(t, err); reason != "export_format_unsupported" {
		t.Fatalf("reason = %q, want export_format_unsupported", reason)
	}
}

// TestExportCreateRejectsUnsupportedFormat covers the POST /export format
// guard: only markdown, mermaid and opml are server-generated, and a rejected
// format writes neither a file nor a metadata row.
func TestExportCreateRejectsUnsupportedFormat(t *testing.T) {
	f := newExportFixture(t)
	f.addCase(7001, nil, "提交订单", 9001, 0)
	service := f.service()

	for _, format := range []string{"png", "PDF", "", "markdown "} {
		if _, err := service.Create(context.Background(), 1, 7, format); err == nil {
			t.Fatalf("Create accepted the unsupported format %q", format)
		} else if reason := exportReason(t, err); reason != "export_format_unsupported" {
			t.Fatalf("format %q reason = %q, want export_format_unsupported", format, reason)
		}
	}
	if len(f.exports) != 0 {
		t.Fatalf("export rows = %d, want none", len(f.exports))
	}
	if _, err := os.Stat(f.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected format must not create the export root: %v", err)
	}
	if len(f.events) != 0 {
		t.Fatalf("events = %d, want none", len(f.events))
	}
}

// TestExportCreateRejectsUnknownVersion covers the version scoping of the export
// read path.
func TestExportCreateRejectsUnknownVersion(t *testing.T) {
	f := newExportFixture(t)
	service := f.service()
	if _, err := service.Create(context.Background(), 99, 7, domain.ExportMarkdown); err == nil {
		t.Fatal("Create accepted an unknown version")
	} else if reason := exportReason(t, err); reason != "version_not_found" {
		t.Fatalf("reason = %q, want version_not_found", reason)
	}
}

// firstLine returns the first line of content for failure messages.
func firstLine(content []byte) string {
	if index := bytes.IndexByte(content, '\n'); index >= 0 {
		return string(content[:index])
	}
	return string(content)
}
