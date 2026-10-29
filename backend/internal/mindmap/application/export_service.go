package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// Export generation bounds and labels of spec §6.10.
const (
	// exportMermaidLeafRunes is the Mermaid leaf-label truncation of spec §6.10.
	exportMermaidLeafRunes = 60
	// exportListDefaultLimit and exportListMaxLimit bound GET /exports when the
	// caller omits or exaggerates the query limit.
	exportListDefaultLimit = 20
	exportListMaxLimit     = 100
	// exportUncoveredLeaf labels an uncovered requirement group in Mermaid.
	exportUncoveredLeaf = "（未覆盖）"
	// exportUngroupedRequirement groups cases that carry no requirement source.
	// Spec §6.2 keeps that from happening through the API, so this is a
	// defensive grouping only.
	exportUngroupedRequirement = "未关联需求"
	// exportKindInternal is the server-fault error kind of the download path:
	// a sha256 mismatch is a 500 with details.reason=export_corrupted (spec
	// §7.2 endpoint 27), not a client error.
	exportKindInternal = "internal"
)

// exportExtensions maps every server-generated export format to its filename
// extension. PNG is rendered client-side and therefore absent (D38).
var exportExtensions = map[string]string{
	domain.ExportMarkdown: "md",
	domain.ExportMermaid:  "mmd",
	domain.ExportOPML:     "opml",
}

// ExportStore is the filesystem port of the export service (D30). The
// filestore.Store adapter implements it structurally: Write returns the stored
// root-relative path, the lowercase hex sha256 and the size of the content,
// Open resolves a stored path and Remove deletes it.
type ExportStore interface {
	Write(rel string, data []byte) (path, sha256Hex string, size int64, err error)
	Open(rel string) (io.ReadCloser, error)
	Remove(rel string) error
}

// ExportDeps are the ports of the export use cases. The graph repositories,
// the version lifecycle, the requirement reader and the baseline repository are
// mandatory; Findings, Anchors, Events and Audit are optional enrichment (a nil
// port is skipped) and Logger receives best-effort warnings.
type ExportDeps struct {
	Versions     VersionLifecycle
	Requirements RequirementReader
	Nodes        NodeRepository
	Steps        StepRepository
	Assertions   AssertionRepository
	Sources      SourceRepository
	Findings     FindingRepository
	Baselines    BaselineRepository
	Exports      ExportRepository
	Anchors      CodeAnchorGateway
	Store        ExportStore
	Events       EventPublisher
	Audit        AuditRecorder
	IDs          IDGenerator
	Clock        Clock
	Logger       *slog.Logger
}

// ExportService implements the server-side exports of spec §6.10 (Markdown,
// Mermaid, OPML) with the file layout and reuse rules of D30/D38: files live on
// the filesystem, PostgreSQL only stores path + sha256 + size, and a frozen
// export is reused per (version, format, baseline) while an unfrozen draft
// always produces a new file.
type ExportService struct {
	deps ExportDeps
}

// NewExportService wires the export use cases to their ports.
func NewExportService(deps ExportDeps) *ExportService {
	return &ExportService{deps: deps}
}

// Create generates one export of format for versionID and stores its metadata.
// After a freeze the export is bound to the baseline and a second request for the
// same (version, format, baseline) returns the existing metadata without writing
// another file; without a baseline every call creates a new export (spec
// §6.4.6/§6.10). On success it publishes case.export_ready and records the
// case.export.created audit entry; both carry ids/format/size only.
func (s *ExportService) Create(ctx context.Context, versionID, actor int64, format string) (domain.CaseExport, error) {
	if versionID <= 0 {
		return domain.CaseExport{}, domain.NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	extension, ok := exportExtensions[format]
	if !ok {
		return domain.CaseExport{}, domain.NewInvalid("format", "export_format_unsupported",
			fmt.Sprintf("export format %q is not markdown, mermaid or opml", format))
	}
	if s.deps.Versions == nil || s.deps.Exports == nil || s.deps.Store == nil {
		return domain.CaseExport{}, errors.New("mindmap: export service is not configured")
	}
	if s.deps.Nodes == nil || s.deps.Steps == nil || s.deps.Assertions == nil || s.deps.Sources == nil {
		return domain.CaseExport{}, errors.New("mindmap: export graph repositories are not configured")
	}

	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return domain.CaseExport{}, err
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return domain.CaseExport{}, err
	}
	// Reuse only a baseline-bound export. An unfrozen version always writes a
	// fresh file so the export history keeps every draft snapshot (spec §6.10).
	if baseline != nil {
		existing, findErr := s.deps.Exports.FindByBaselineFormat(ctx, versionID, format, &baseline.ID)
		if findErr == nil {
			// The caller gets an existing row back instead of a new one: the
			// HTTP layer answers 200 rather than 201 (spec §7.2 endpoint 25).
			existing.Reused = true
			return existing, nil
		}
		if !exportIsNotFound(findErr) {
			return domain.CaseExport{}, findErr
		}
	}

	document, err := s.loadDocument(ctx, version, baseline)
	if err != nil {
		return domain.CaseExport{}, err
	}
	content, err := exportRenderContent(format, document)
	if err != nil {
		return domain.CaseExport{}, err
	}
	exportID, err := s.nextID()
	if err != nil {
		return domain.CaseExport{}, err
	}
	rel := exportFilePath(versionID, exportBaselineID(baseline), format, exportID, extension)
	storedPath, digest, size, err := s.deps.Store.Write(rel, content)
	if err != nil {
		return domain.CaseExport{}, err
	}
	now := s.now()
	created, err := s.deps.Exports.Create(ctx, domain.CaseExport{
		ID:         exportID,
		VersionID:  versionID,
		Format:     format,
		FilePath:   storedPath,
		FileSHA256: digest,
		SizeBytes:  size,
		BaselineID: exportBaselineID(baseline),
		NodeCount:  document.NodeCount,
		CreatedBy:  actor,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		// The metadata write failed, so the file would be an orphan: remove it
		// best-effort and surface the repository error (for example
		// export_conflict on a concurrent frozen export).
		s.removeFile(storedPath)
		return domain.CaseExport{}, err
	}
	s.publishExport(ctx, created, actor)
	s.recordExportAudit(ctx, created, actor)
	return created, nil
}

// List returns one page of the export history of versionID (spec §7.2 endpoint
// 26). An empty format matches every format; ids/keyset pagination are delegated
// to the repository.
func (s *ExportService) List(ctx context.Context, versionID int64, format string, limit int, cursor string) ([]domain.CaseExport, string, bool, error) {
	if versionID <= 0 {
		return nil, "", false, domain.NewInvalid("versionId", "invalid_id", "versionId must be a positive id")
	}
	if format != "" {
		if _, ok := exportExtensions[format]; !ok {
			return nil, "", false, domain.NewInvalid("format", "export_format_unsupported",
				fmt.Sprintf("export format %q is not markdown, mermaid or opml", format))
		}
	}
	if s.deps.Exports == nil {
		return nil, "", false, errors.New("mindmap: export repository is not configured")
	}
	if limit <= 0 {
		limit = exportListDefaultLimit
	}
	if limit > exportListMaxLimit {
		limit = exportListMaxLimit
	}
	return s.deps.Exports.List(ctx, versionID, format, limit, cursor)
}

// Download resolves one export of versionID and returns its content together
// with the stored metadata (spec §7.2 endpoint 27). The sha256 of the bytes read
// back must match case_export.file_sha256 and the recorded size; a mismatch, a
// missing file or a vanished metadata row is export_corrupted. The returned
// reader is always a verified snapshot of the file.
func (s *ExportService) Download(ctx context.Context, versionID, exportID int64) (io.ReadCloser, domain.CaseExport, error) {
	if versionID <= 0 || exportID <= 0 {
		return nil, domain.CaseExport{}, domain.NewInvalid("exportId", "invalid_id", "versionId and exportId must be positive ids")
	}
	if s.deps.Exports == nil || s.deps.Store == nil {
		return nil, domain.CaseExport{}, errors.New("mindmap: export service is not configured")
	}
	export, err := s.deps.Exports.Get(ctx, versionID, exportID)
	if err != nil {
		return nil, domain.CaseExport{}, err
	}
	reader, err := s.deps.Store.Open(export.FilePath)
	if err != nil {
		// The stored path comes from the metadata row, never from the request:
		// a missing file or a row whose path no longer resolves inside the root
		// is corrupt metadata (500 export_corrupted), not a client error.
		if errors.Is(err, fs.ErrNotExist) || exportPathInvalid(err) {
			return nil, domain.CaseExport{}, exportCorruptedError(exportID)
		}
		return nil, domain.CaseExport{}, err
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, domain.CaseExport{}, fmt.Errorf("mindmap: read export %d: %w", exportID, readErr)
	}
	if closeErr != nil {
		return nil, domain.CaseExport{}, fmt.Errorf("mindmap: close export %d: %w", exportID, closeErr)
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != export.FileSHA256 || int64(len(content)) != export.SizeBytes {
		return nil, domain.CaseExport{}, exportCorruptedError(exportID)
	}
	return io.NopCloser(bytes.NewReader(content)), export, nil
}

// exportDocument is the loaded projection rendered by one export: the version
// metadata, the ordered graph with its child collections, the code anchors, the
// coverage matrix and the open review findings.
type exportDocument struct {
	VersionID   int64
	VersionName string
	BaselineID  *int64
	GeneratedAt time.Time
	NodeCount   int
	CaseCount   int
	// Roots are the nodes without a parent (or whose parent is missing from the
	// snapshot, so content is never dropped), ordered by sortOrder then id.
	Roots    []domain.CaseNode
	Children map[int64][]domain.CaseNode

	Steps      map[int64][]domain.CaseStep
	Assertions map[int64][]domain.CaseAssertion
	Sources    map[int64][]domain.CaseSource
	Anchors    map[int64][]Anchor

	Findings          []domain.CaseReviewFinding
	Coverage          domain.Coverage
	RequirementTitles map[int64]string
	NodesByID         map[int64]domain.CaseNode
}

// loadDocument reads the whole graph, the clarified requirements, the open
// findings and (best-effort) the code anchors of version.
func (s *ExportService) loadDocument(ctx context.Context, version VersionSnapshot, baseline *domain.CaseBaseline) (exportDocument, error) {
	state, err := loadGraphState(ctx, s.deps.Nodes, s.deps.Steps, s.deps.Assertions, s.deps.Sources, version.ID)
	if err != nil {
		return exportDocument{}, err
	}

	confirmed := make(map[int64]bool)
	titles := make(map[int64]string)
	if s.deps.Requirements != nil {
		requirements, err := s.deps.Requirements.ListConfirmed(ctx, version.ID)
		if err != nil {
			return exportDocument{}, err
		}
		for _, requirement := range requirements {
			confirmed[requirement.ID] = true
			titles[requirement.ID] = requirement.Title
		}
	}

	var findings []domain.CaseReviewFinding
	if s.deps.Findings != nil {
		findings, err = s.deps.Findings.List(ctx, version.ID, domain.FindingOpen, "", nil)
		if err != nil {
			return exportDocument{}, err
		}
		sort.SliceStable(findings, func(i, j int) bool {
			left, right := exportSeverityRank(findings[i].Severity), exportSeverityRank(findings[j].Severity)
			if left != right {
				return left < right
			}
			return findings[i].ID < findings[j].ID
		})
	}

	anchors := make(map[int64][]Anchor)
	if s.deps.Anchors != nil {
		for _, node := range state.nodes {
			if node.Kind != domain.NodeKindCase {
				continue
			}
			list, err := s.deps.Anchors.List(ctx, version.ID, node.ID)
			if err != nil {
				// Anchors are read-only enrichment: a code-intel hiccup must not
				// block the human export chain (spec §6.13 rule 11).
				s.warn(ctx, "mindmap export anchor read failed",
					"version_id", version.ID, "node_id", node.ID, "error", err)
				continue
			}
			anchors[node.ID] = append([]Anchor(nil), list...)
		}
	}

	nodes := append([]domain.CaseNode(nil), state.nodes...)
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].SortOrder != nodes[j].SortOrder {
			return nodes[i].SortOrder < nodes[j].SortOrder
		}
		return nodes[i].ID < nodes[j].ID
	})
	byID := make(map[int64]domain.CaseNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	children := make(map[int64][]domain.CaseNode, len(nodes))
	roots := make([]domain.CaseNode, 0, len(nodes))
	caseCount := 0
	for _, node := range nodes {
		if node.Kind == domain.NodeKindCase {
			caseCount++
		}
		if node.ParentID == nil {
			roots = append(roots, node)
			continue
		}
		if _, ok := byID[*node.ParentID]; !ok {
			roots = append(roots, node)
			continue
		}
		children[*node.ParentID] = append(children[*node.ParentID], node)
	}

	return exportDocument{
		VersionID:         version.ID,
		VersionName:       version.Name,
		BaselineID:        exportBaselineID(baseline),
		GeneratedAt:       s.now(),
		NodeCount:         len(nodes),
		CaseCount:         caseCount,
		Roots:             roots,
		Children:          children,
		Steps:             state.steps,
		Assertions:        state.assertions,
		Sources:           groupExportSources(state.sources),
		Anchors:           anchors,
		Findings:          findings,
		Coverage:          domain.BuildCoverage(version.ID, confirmed, state.nodes, state.sources),
		RequirementTitles: titles,
		NodesByID:         byID,
	}, nil
}

// nextID mints one application-generated Snowflake id (D29).
func (s *ExportService) nextID() (int64, error) {
	if s.deps.IDs == nil {
		return 0, errors.New("mindmap: id generator is not configured")
	}
	id := s.deps.IDs.Next()
	if id <= 0 {
		return 0, errors.New("mindmap: id generator returned a non-positive id")
	}
	return id, nil
}

// now returns the injected clock time (falling back to the wall clock).
func (s *ExportService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}

// warn logs a best-effort failure through the optional logger.
func (s *ExportService) warn(ctx context.Context, msg string, args ...any) {
	if s.deps.Logger == nil {
		return
	}
	s.deps.Logger.WarnContext(ctx, msg, args...)
}

// removeFile deletes an orphaned export file best-effort.
func (s *ExportService) removeFile(rel string) {
	if err := s.deps.Store.Remove(rel); err != nil {
		s.warn(context.Background(), "mindmap export file cleanup failed", "path", rel, "error", err)
	}
}

// publishExport emits case.export_ready after the metadata commit. Spec §6.10
// requires the summary to carry format and exportId; the detail carries
// ids/format/size only, never export content (spec §6.11/D30).
func (s *ExportService) publishExport(ctx context.Context, export domain.CaseExport, actor int64) {
	if s.deps.Events == nil {
		return
	}
	event := Event{
		VersionID: export.VersionID,
		Type:      domain.EventCaseExportReady,
		Summary: fmt.Sprintf("case export ready: %s %d (%d bytes)",
			export.Format, export.ID, export.SizeBytes),
		ActorID: actor,
		Detail: map[string]any{
			"exportId":  export.ID,
			"format":    export.Format,
			"sizeBytes": export.SizeBytes,
		},
	}
	if err := s.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "mindmap event publish failed",
			"type", event.Type, "version_id", event.VersionID, "error", err)
	}
}

// recordExportAudit records the case.export.created audit entry (spec §6.12).
// The metadata carries ids/format/size only; a failure is logged, never
// returned.
func (s *ExportService) recordExportAudit(ctx context.Context, export domain.CaseExport, actor int64) {
	if s.deps.Audit == nil {
		return
	}
	metadata := map[string]any{
		"format":    export.Format,
		"sizeBytes": export.SizeBytes,
		"nodeCount": export.NodeCount,
	}
	if export.BaselineID != nil {
		metadata["baselineId"] = *export.BaselineID
	}
	if err := s.deps.Audit.Record(ctx, AuditEvent{
		Action:     "case.export.created",
		ActorID:    actor,
		TargetType: "case_export",
		TargetID:   strconv.FormatInt(export.ID, 10),
		Metadata:   metadata,
	}); err != nil {
		s.warn(ctx, "mindmap export audit failed", "export_id", export.ID, "error", err)
	}
}

// exportFilePath builds the root-relative path of spec §6.10:
// versions/{versionId}/(baseline-{baselineId}|draft)/{format}-{exportId}.{ext}.
func exportFilePath(versionID int64, baselineID *int64, format string, exportID int64, extension string) string {
	scope := "draft"
	if baselineID != nil {
		scope = "baseline-" + strconv.FormatInt(*baselineID, 10)
	}
	return fmt.Sprintf("versions/%d/%s/%s-%d.%s", versionID, scope, format, exportID, extension)
}

// baselineID returns the baseline id as an optional value.
func exportBaselineID(baseline *domain.CaseBaseline) *int64 {
	if baseline == nil {
		return nil
	}
	id := baseline.ID
	return &id
}

// exportIsNotFound reports whether err is a not-found domain error, which the
// reuse lookup uses to mean "no reusable export yet".
func exportIsNotFound(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound
}

// exportPathInvalid reports whether err is the store's invalid_storage_path
// rejection. Download only ever passes a stored metadata path to the store, so
// such a rejection means the row is corrupt (500 export_corrupted) rather than
// that the request was invalid (422 invalid_storage_path).
func exportPathInvalid(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindInvalid &&
		domainErr.Reason == "invalid_storage_path"
}

// exportCorruptedError builds the 500 export_corrupted of spec §7.2 endpoint 27.
func exportCorruptedError(exportID int64) error {
	return &domain.Error{
		Kind:    exportKindInternal,
		Reason:  "export_corrupted",
		Message: fmt.Sprintf("export %d content does not match its sha256", exportID),
	}
}

// renderExport dispatches one export document to its format renderer.
func exportRenderContent(format string, document exportDocument) ([]byte, error) {
	switch format {
	case domain.ExportMarkdown:
		return exportRenderMarkdown(document), nil
	case domain.ExportMermaid:
		return exportRenderMermaid(document), nil
	case domain.ExportOPML:
		return exportRenderOPML(document)
	default:
		return nil, domain.NewInvalid("format", "export_format_unsupported",
			fmt.Sprintf("export format %q is not markdown, mermaid or opml", format))
	}
}

// ---------- shared document helpers ----------

// childrenOf returns the ordered children of parentID (nil means the roots).
func (d exportDocument) childrenOf(parentID *int64) []domain.CaseNode {
	if parentID == nil {
		return d.Roots
	}
	return d.Children[*parentID]
}

// nodeLess orders two nodes by sortOrder then id.
func (d exportDocument) nodeLess(left, right int64) bool {
	leftNode, leftOK := d.NodesByID[left]
	rightNode, rightOK := d.NodesByID[right]
	if !leftOK || !rightOK {
		return left < right
	}
	if leftNode.SortOrder != rightNode.SortOrder {
		return leftNode.SortOrder < rightNode.SortOrder
	}
	return leftNode.ID < rightNode.ID
}

// requirementLabel renders one requirement as "R<id> <title>".
func (d exportDocument) requirementLabel(requirementID int64) string {
	label := "R" + strconv.FormatInt(requirementID, 10)
	if title := strings.TrimSpace(d.RequirementTitles[requirementID]); title != "" {
		label += " " + title
	}
	return label
}

// caseTitles resolves the titles of caseIDs, ordered like the graph.
func (d exportDocument) caseTitles(caseIDs []int64) []string {
	ordered := append([]int64(nil), caseIDs...)
	sort.SliceStable(ordered, func(i, j int) bool { return d.nodeLess(ordered[i], ordered[j]) })
	titles := make([]string, 0, len(ordered))
	for _, caseID := range ordered {
		node, ok := d.NodesByID[caseID]
		if !ok {
			titles = append(titles, "#"+strconv.FormatInt(caseID, 10))
			continue
		}
		titles = append(titles, node.Title)
	}
	return titles
}

// sourceLabels renders the sources of one case as stable short references.
func (d exportDocument) sourceLabels(nodeID int64) []string {
	sources := sortSources(d.Sources[nodeID])
	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		label := source.RefKind
		switch source.RefKind {
		case domain.RefRequirement, domain.RefAppModel, domain.RefAIRun:
			if source.RefID != nil {
				label += ":" + strconv.FormatInt(*source.RefID, 10)
			} else if source.RefKey != "" {
				label += ":" + source.RefKey
			}
		default:
			if source.RefKey != "" {
				label += ":" + source.RefKey
			}
		}
		labels = append(labels, label)
	}
	return labels
}

// groupExportSources indexes the flat source list by owning node.
func groupExportSources(sources []domain.CaseSource) map[int64][]domain.CaseSource {
	grouped := make(map[int64][]domain.CaseSource, len(sources))
	for _, source := range sources {
		grouped[source.NodeID] = append(grouped[source.NodeID], source)
	}
	return grouped
}

// anchorLabels renders the anchors of one case as "path:start-end（confidence）".
func (d exportDocument) anchorLabels(nodeID int64) []string {
	anchors := d.Anchors[nodeID]
	labels := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		label := anchor.FilePath
		switch {
		case anchor.StartLine != nil && anchor.EndLine != nil:
			label += fmt.Sprintf(":%d-%d", *anchor.StartLine, *anchor.EndLine)
		case anchor.StartLine != nil:
			label += fmt.Sprintf(":%d", *anchor.StartLine)
		}
		if anchor.Confidence != "" {
			label += "（" + anchor.Confidence + "）"
		}
		labels = append(labels, label)
	}
	return labels
}

// coveredCases maps every covered requirement to its case ids in graph order.
func (d exportDocument) coveredCases() map[int64][]int64 {
	covered := make(map[int64][]int64, len(d.Coverage.Matrix))
	for _, cell := range d.Coverage.Matrix {
		ids := append([]int64(nil), cell.CaseIDs...)
		sort.SliceStable(ids, func(i, j int) bool { return d.nodeLess(ids[i], ids[j]) })
		covered[cell.RequirementID] = ids
	}
	return covered
}

// ungroupedCases returns the cases that no coverage cell references (defensive
// only: spec §6.2 requires a requirement source on every case).
func (d exportDocument) ungroupedCases() []int64 {
	covered := make(map[int64]struct{})
	for _, cell := range d.Coverage.Matrix {
		for _, caseID := range cell.CaseIDs {
			covered[caseID] = struct{}{}
		}
	}
	ungrouped := make([]int64, 0)
	for _, node := range d.Roots {
		collectExportUngrouped(&ungrouped, d, node, covered)
	}
	return ungrouped
}

// collectExportUngrouped appends node and its descendants when they are cases outside
// the coverage matrix.
func collectExportUngrouped(ungrouped *[]int64, document exportDocument, node domain.CaseNode, covered map[int64]struct{}) {
	if node.Kind == domain.NodeKindCase {
		if _, ok := covered[node.ID]; !ok {
			*ungrouped = append(*ungrouped, node.ID)
		}
		return
	}
	for _, child := range document.childrenOf(&node.ID) {
		collectExportUngrouped(ungrouped, document, child, covered)
	}
}

// exportInline folds newlines into spaces and trims the result, keeping one
// Markdown/OPML bullet on one line.
func exportInline(value string) string {
	replacer := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")
	return strings.TrimSpace(replacer.Replace(value))
}

// exportHeading returns a Markdown heading of the given level, clamped to 6.
func exportHeading(level int) string {
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	return strings.Repeat("#", level) + " "
}

// exportMarkdownCell prepares one table cell.
func exportMarkdownCell(value string) string {
	return strings.ReplaceAll(exportInline(value), "|", `\|`)
}

// exportTextOrPlaceholder returns value or the Chinese "无" placeholder.
func exportTextOrPlaceholder(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return exportInline(trimmed)
	}
	return "（无）"
}

// exportCompactJSON normalizes a JSONB column; absent values become the "无"
// placeholder.
func exportCompactJSON(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "（无）"
	}
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, trimmed); err != nil {
		return exportInline(string(trimmed))
	}
	return buffer.String()
}

// exportAssertionLabel renders one assertion as "[kind] target：expression（期望：…）".
func exportAssertionLabel(assertion domain.CaseAssertion) string {
	label := "[" + assertion.Kind + "] "
	if target := strings.TrimSpace(assertion.Target); target != "" {
		label += exportInline(target) + "："
	}
	label += exportInline(assertion.Expression)
	if expected := strings.TrimSpace(assertion.Expected); expected != "" {
		label += "（期望：" + exportInline(expected) + "）"
	}
	return label
}

// exportSeverityRank sorts findings blocker→major→minor→info.
func exportSeverityRank(severity string) int {
	switch severity {
	case domain.SeverityBlocker:
		return 0
	case domain.SeverityMajor:
		return 1
	case domain.SeverityMinor:
		return 2
	case domain.SeverityInfo:
		return 3
	default:
		return 4
	}
}

// ---------- Markdown ----------

// exportRenderMarkdown renders the Markdown export of spec §6.10: title,
// version/baseline header, coverage matrix with the uncovered list, cases
// grouped by scenario under their sortOrder with sources/priority/preconditions/
// testData/steps/assertions/anchors, the open findings section and the generated
// timestamp. There is no YAML front matter.
func exportRenderMarkdown(document exportDocument) []byte {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# 用例脑图：%s\n\n", document.VersionName)
	fmt.Fprintf(&builder, "- 版本：%s（%d）\n", document.VersionName, document.VersionID)
	if document.BaselineID != nil {
		fmt.Fprintf(&builder, "- 基线：baseline-%d\n", *document.BaselineID)
	} else {
		builder.WriteString("- 基线：草稿（未冻结）\n")
	}
	fmt.Fprintf(&builder, "- 用例数：%d\n", document.CaseCount)
	fmt.Fprintf(&builder, "- 节点数：%d\n", document.NodeCount)
	fmt.Fprintf(&builder, "- 生成时间：%s\n\n", document.GeneratedAt.UTC().Format(time.RFC3339))

	exportWriteMarkdownCoverage(&builder, document)

	builder.WriteString("\n## 用例\n\n")
	if document.CaseCount == 0 {
		builder.WriteString("- （无用例）\n")
	} else {
		for _, root := range document.Roots {
			exportWriteMarkdownNode(&builder, document, root, 3)
		}
	}

	exportWriteMarkdownFindings(&builder, document)
	return []byte(builder.String())
}

// exportWriteMarkdownCoverage writes the coverage matrix and the uncovered list.
func exportWriteMarkdownCoverage(builder *strings.Builder, document exportDocument) {
	builder.WriteString("## 覆盖矩阵\n\n")
	builder.WriteString("| 需求 | 覆盖用例 |\n| --- | --- |\n")
	if len(document.Coverage.RequirementIDs) == 0 {
		builder.WriteString("| （无已确认需求） | — |\n")
	}
	covered := document.coveredCases()
	for _, requirementID := range document.Coverage.RequirementIDs {
		cell := "（未覆盖）"
		if titles := document.caseTitles(covered[requirementID]); len(titles) > 0 {
			cell = strings.Join(titles, "、")
		}
		fmt.Fprintf(builder, "| %s | %s |\n",
			exportMarkdownCell(document.requirementLabel(requirementID)), exportMarkdownCell(cell))
	}
	builder.WriteString("\n### 未覆盖需求\n\n")
	if len(document.Coverage.UncoveredRequirementIDs) == 0 {
		builder.WriteString("- （无）\n")
		return
	}
	for _, requirementID := range document.Coverage.UncoveredRequirementIDs {
		fmt.Fprintf(builder, "- %s\n", document.requirementLabel(requirementID))
	}
}

// exportWriteMarkdownNode writes one scenario subtree or case of the Markdown export.
func exportWriteMarkdownNode(builder *strings.Builder, document exportDocument, node domain.CaseNode, level int) {
	if level > 6 {
		level = 6
	}
	if node.Kind == domain.NodeKindScenario {
		fmt.Fprintf(builder, "%s场景：%s\n\n", exportHeading(level), exportInline(node.Title))
		for _, child := range document.childrenOf(&node.ID) {
			exportWriteMarkdownNode(builder, document, child, level+1)
		}
		return
	}
	exportWriteMarkdownCase(builder, document, node, level)
}

// exportWriteMarkdownCase writes one full case block.
func exportWriteMarkdownCase(builder *strings.Builder, document exportDocument, node domain.CaseNode, level int) {
	fmt.Fprintf(builder, "%s用例：%s\n\n", exportHeading(level), exportInline(node.Title))
	fmt.Fprintf(builder, "- 用例 ID：%d\n", node.ID)
	fmt.Fprintf(builder, "- 优先级：%s\n", node.Priority)
	if labels := document.sourceLabels(node.ID); len(labels) > 0 {
		fmt.Fprintf(builder, "- 来源：%s\n", strings.Join(labels, "、"))
	} else {
		builder.WriteString("- 来源：（无）\n")
	}
	fmt.Fprintf(builder, "- 前置条件：%s\n", exportTextOrPlaceholder(node.Preconditions))
	fmt.Fprintf(builder, "- 测试数据：%s\n", exportMarkdownTestData(node.TestData))
	if anchors := document.anchorLabels(node.ID); len(anchors) > 0 {
		builder.WriteString("- 锚点：\n")
		for _, anchor := range anchors {
			fmt.Fprintf(builder, "  - %s\n", exportInline(anchor))
		}
	} else {
		builder.WriteString("- 锚点：（无）\n")
	}

	builder.WriteString("\n**步骤**\n\n")
	steps := document.Steps[node.ID]
	if len(steps) == 0 {
		builder.WriteString("- （无）\n")
	}
	for _, step := range steps {
		fmt.Fprintf(builder, "%d. %s\n", step.Seq, exportInline(step.Action))
		if expected := strings.TrimSpace(step.Expected); expected != "" {
			fmt.Fprintf(builder, "   - 期望：%s\n", exportInline(expected))
		}
		if data := strings.TrimSpace(step.Data); data != "" {
			fmt.Fprintf(builder, "   - 数据：%s\n", exportInline(data))
		}
	}

	builder.WriteString("\n**断言**\n\n")
	assertions := document.Assertions[node.ID]
	if len(assertions) == 0 {
		builder.WriteString("- （无）\n")
	}
	for _, assertion := range assertions {
		fmt.Fprintf(builder, "- %s\n", exportAssertionLabel(assertion))
	}
	builder.WriteString("\n")
}

// exportMarkdownTestData wraps compact test data in an inline code span.
func exportMarkdownTestData(raw []byte) string {
	data := exportCompactJSON(raw)
	if data == "（无）" {
		return data
	}
	return "`" + data + "`"
}

// exportWriteMarkdownFindings writes the open findings section.
func exportWriteMarkdownFindings(builder *strings.Builder, document exportDocument) {
	builder.WriteString("\n## 评审发现项\n\n")
	if len(document.Findings) == 0 {
		builder.WriteString("- （无）\n")
		return
	}
	for _, finding := range document.Findings {
		line := fmt.Sprintf("- [%s] %s：%s", finding.Severity, finding.Category, exportInline(finding.Detail))
		if finding.NodeID != nil {
			if node, ok := document.NodesByID[*finding.NodeID]; ok {
				line += fmt.Sprintf("（用例：%s）", exportInline(node.Title))
			}
		}
		builder.WriteString(line + "\n")
	}
}

// ---------- Mermaid ----------

// exportRenderMermaid renders the Mermaid mindmap export of spec §6.10: the
// version is the root, requirement groups hold scenario→case leaves, leaf labels
// are truncated to 60 runes and de-duplicated, and ()[]{}/newlines are escaped.
// Case details stay in the Markdown export.
func exportRenderMermaid(document exportDocument) []byte {
	var builder strings.Builder
	builder.WriteString("mindmap\n")
	rootLabel := exportMermaidLabel(document.VersionName)
	if rootLabel == "" {
		rootLabel = "version"
	}
	fmt.Fprintf(&builder, "  root((%s))\n", rootLabel)

	covered := document.coveredCases()
	for _, requirementID := range document.Coverage.RequirementIDs {
		fmt.Fprintf(&builder, "    %s\n", exportMermaidLabel(document.requirementLabel(requirementID)))
		caseIDs := covered[requirementID]
		if len(caseIDs) == 0 {
			fmt.Fprintf(&builder, "      %s\n", exportUncoveredLeaf)
			continue
		}
		document.writeMermaidCases(&builder, caseIDs, 3)
	}
	if ungrouped := document.ungroupedCases(); len(ungrouped) > 0 {
		fmt.Fprintf(&builder, "    %s\n", exportUngroupedRequirement)
		document.writeMermaidCases(&builder, ungrouped, 3)
	}
	return []byte(builder.String())
}

// writeMermaidCases emits the scenario path and the case leaves of one
// requirement branch at the given indentation depth.
func (d exportDocument) writeMermaidCases(builder *strings.Builder, caseIDs []int64, depth int) {
	selected := make(map[int64]struct{}, len(caseIDs))
	ancestors := make(map[int64]struct{})
	for _, caseID := range caseIDs {
		selected[caseID] = struct{}{}
		node, ok := d.NodesByID[caseID]
		if !ok {
			continue
		}
		parent := node.ParentID
		for parent != nil {
			ancestors[*parent] = struct{}{}
			ancestor, ok := d.NodesByID[*parent]
			if !ok {
				break
			}
			parent = ancestor.ParentID
		}
	}
	seen := make(map[string]int)
	d.writeMermaidChildren(builder, nil, depth, selected, ancestors, seen)
}

// writeMermaidChildren recursively writes the selected subtree.
func (d exportDocument) writeMermaidChildren(builder *strings.Builder, parentID *int64, depth int, selected, ancestors map[int64]struct{}, seen map[string]int) {
	indent := strings.Repeat("  ", depth)
	for _, node := range d.childrenOf(parentID) {
		switch node.Kind {
		case domain.NodeKindScenario:
			if _, ok := ancestors[node.ID]; !ok {
				continue
			}
			fmt.Fprintf(builder, "%s%s\n", indent, exportMermaidLabel(node.Title))
			childID := node.ID
			d.writeMermaidChildren(builder, &childID, depth+1, selected, ancestors, seen)
		case domain.NodeKindCase:
			if _, ok := selected[node.ID]; !ok {
				continue
			}
			label := exportMermaidLabel(truncateRunes(node.Title, exportMermaidLeafRunes))
			if label == "" {
				label = "case"
			}
			// Leaf labels are unique among the leaves of one requirement branch.
			seen[label]++
			if seen[label] > 1 {
				label = fmt.Sprintf("%s \\(%d\\)", label, seen[label])
			}
			fmt.Fprintf(builder, "%s%s\n", indent, label)
		}
	}
}

// exportMermaidLabel folds newlines and escapes the Mermaid mindmap shape
// characters ()[]{} of spec §6.10.
func exportMermaidLabel(value string) string {
	escaped := exportInline(value)
	var builder strings.Builder
	builder.Grow(len(escaped))
	for _, char := range escaped {
		switch char {
		case '(', ')', '[', ']', '{', '}':
			builder.WriteByte('\\')
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// ---------- OPML ----------

// exportOPMLDocument is the OPML 2.0 document of spec §6.10.
type exportOPMLDocument struct {
	XMLName xml.Name       `xml:"opml"`
	Version string         `xml:"version,attr"`
	Head    exportOPMLHead `xml:"head"`
	Body    exportOPMLBody `xml:"body"`
}

// exportOPMLHead is the OPML head carrying the title.
type exportOPMLHead struct {
	Title string `xml:"title"`
}

// exportOPMLBody holds the version outline.
type exportOPMLBody struct {
	Outlines []exportOPMLOutline `xml:"outline"`
}

// exportOPMLOutline is one OPML outline. Note is serialized as the "_note"
// attribute (the underscore prefix keeps it inert for importers); a case leaf
// carries priority/preconditions/testData/steps/assertions/anchors there and an
// uncovered requirement carries "未覆盖".
type exportOPMLOutline struct {
	Text     string              `xml:"text,attr"`
	Note     string              `xml:"_note,attr,omitempty"`
	Outlines []exportOPMLOutline `xml:"outline"`
}

// exportRenderOPML renders the OPML 2.0 export of spec §6.10 with encoding/xml:
// the outline tree is version→requirement→scenario→case, covered and uncovered
// requirements are distinguishable, and every value is XML-escaped by the
// encoder.
func exportRenderOPML(document exportDocument) ([]byte, error) {
	root := exportOPMLOutline{
		Text:     document.VersionName,
		Outlines: make([]exportOPMLOutline, 0, len(document.Coverage.RequirementIDs)),
	}
	covered := document.coveredCases()
	for _, requirementID := range document.Coverage.RequirementIDs {
		outline := exportOPMLOutline{Text: document.requirementLabel(requirementID)}
		if caseIDs := covered[requirementID]; len(caseIDs) > 0 {
			outline.Outlines = document.opmlCaseOutlines(caseIDs)
		} else {
			outline.Note = "未覆盖"
		}
		root.Outlines = append(root.Outlines, outline)
	}
	if ungrouped := document.ungroupedCases(); len(ungrouped) > 0 {
		root.Outlines = append(root.Outlines, exportOPMLOutline{
			Text:     exportUngroupedRequirement,
			Outlines: document.opmlCaseOutlines(ungrouped),
		})
	}

	doc := exportOPMLDocument{
		Version: "2.0",
		Head:    exportOPMLHead{Title: document.VersionName},
		Body:    exportOPMLBody{Outlines: []exportOPMLOutline{root}},
	}
	encoded, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("mindmap: marshal opml export: %w", err)
	}
	return []byte(xml.Header + string(encoded) + "\n"), nil
}

// opmlCaseOutlines builds the scenario→case outline tree of the given cases.
func (d exportDocument) opmlCaseOutlines(caseIDs []int64) []exportOPMLOutline {
	selected := make(map[int64]struct{}, len(caseIDs))
	ancestors := make(map[int64]struct{})
	for _, caseID := range caseIDs {
		selected[caseID] = struct{}{}
		node, ok := d.NodesByID[caseID]
		if !ok {
			continue
		}
		parent := node.ParentID
		for parent != nil {
			ancestors[*parent] = struct{}{}
			ancestor, ok := d.NodesByID[*parent]
			if !ok {
				break
			}
			parent = ancestor.ParentID
		}
	}
	return d.opmlChildren(nil, selected, ancestors)
}

// opmlChildren recursively builds the outline list of the selected subtree.
func (d exportDocument) opmlChildren(parentID *int64, selected, ancestors map[int64]struct{}) []exportOPMLOutline {
	outlines := make([]exportOPMLOutline, 0)
	for _, node := range d.childrenOf(parentID) {
		switch node.Kind {
		case domain.NodeKindScenario:
			if _, ok := ancestors[node.ID]; !ok {
				continue
			}
			childID := node.ID
			outlines = append(outlines, exportOPMLOutline{
				Text:     node.Title,
				Outlines: d.opmlChildren(&childID, selected, ancestors),
			})
		case domain.NodeKindCase:
			if _, ok := selected[node.ID]; !ok {
				continue
			}
			outlines = append(outlines, exportOPMLOutline{
				Text: node.Title,
				Note: d.caseNote(node),
			})
		}
	}
	return outlines
}

// caseNote renders the "_note" detail of one case leaf: priority,
// preconditions, testData, steps, assertions and anchors.
func (d exportDocument) caseNote(node domain.CaseNode) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "优先级：%s\n", node.Priority)
	fmt.Fprintf(&builder, "前置条件：%s\n", exportTextOrPlaceholder(node.Preconditions))
	fmt.Fprintf(&builder, "测试数据：%s\n", exportCompactJSON(node.TestData))
	builder.WriteString("步骤：\n")
	steps := d.Steps[node.ID]
	if len(steps) == 0 {
		builder.WriteString("（无）\n")
	}
	for _, step := range steps {
		fmt.Fprintf(&builder, "%d. %s\n", step.Seq, exportInline(step.Action))
		if expected := strings.TrimSpace(step.Expected); expected != "" {
			fmt.Fprintf(&builder, "   期望：%s\n", exportInline(expected))
		}
		if data := strings.TrimSpace(step.Data); data != "" {
			fmt.Fprintf(&builder, "   数据：%s\n", exportInline(data))
		}
	}
	builder.WriteString("断言：\n")
	assertions := d.Assertions[node.ID]
	if len(assertions) == 0 {
		builder.WriteString("（无）\n")
	}
	for _, assertion := range assertions {
		fmt.Fprintf(&builder, "- %s\n", exportAssertionLabel(assertion))
	}
	if anchors := d.anchorLabels(node.ID); len(anchors) > 0 {
		builder.WriteString("锚点：\n")
		for _, anchor := range anchors {
			fmt.Fprintf(&builder, "- %s\n", exportInline(anchor))
		}
	}
	return strings.TrimSuffix(builder.String(), "\n")
}
