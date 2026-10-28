package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const (
	// DefaultEvidenceMaxBytes is the per-evidence size cap of spec §5.7 (10 MiB)
	// when EZTEST_EXEC_EVIDENCE_MAX_BYTES is not configured. The composition root
	// passes the configured value with WithEvidenceMaxBytes; it should give the
	// filesystem store the same limit.
	DefaultEvidenceMaxBytes int64 = 10 << 20
	// MaxEvidencePerCaseRun is the fixed per-case-run artifact cap of spec §5.7
	// (100). The count is derived from the evidence rows of the case run.
	MaxEvidencePerCaseRun = 100
	// redactedValue replaces every sensitive header value or JSON field value.
	redactedValue = "***"
)

// CaptureInput is one artifact to persist. BatchID and CaseRunID scope the
// evidence; StepResultID is optional and adds a step_result reference. Data is
// the already-redacted artifact byte payload (spec §5.7).
type CaptureInput struct {
	BatchID      int64
	CaseRunID    int64
	StepResultID *int64
	Kind         string
	Mime         string
	Data         []byte
	Metadata     map[string]any
}

// evidenceBatchReader is the batch ownership read needed by Open. It is a
// structural subset of BatchRepository so the service does not depend on the
// unrelated batch write methods.
type evidenceBatchReader interface {
	GetBatch(ctx context.Context, versionID, batchID int64) (domain.RunBatch, error)
}

// evidenceFileRemover is the optional cleanup capability of the filesystem
// store. The shared EvidenceStore port only exposes Write/Open, but D30 allows a
// best-effort file removal after a failed metadata insert.
type evidenceFileRemover interface {
	Remove(ctx context.Context, rel string) error
}

// EvidenceService implements the evidence use cases of spec §5.7: capture an
// immutable artifact under the filesystem root with its sha256, read it back
// after verifying hash and version ownership, and list the artifacts of a case
// run. Evidence is append-only: there is no update or delete API.
type EvidenceService struct {
	evidence      EvidenceRepository
	refs          EvidenceRefRepository
	batches       evidenceBatchReader
	store         EvidenceStore
	ids           IDGenerator
	maxBytes      int64
	maxPerCaseRun int
}

// EvidenceServiceOption customises the evidence service limits.
type EvidenceServiceOption func(*EvidenceService)

// WithEvidenceMaxBytes overrides the per-evidence size limit.
func WithEvidenceMaxBytes(maxBytes int64) EvidenceServiceOption {
	return func(s *EvidenceService) {
		if maxBytes > 0 {
			s.maxBytes = maxBytes
		}
	}
}

// WithEvidenceMaxPerCaseRun overrides the per-case-run artifact count limit.
func WithEvidenceMaxPerCaseRun(max int) EvidenceServiceOption {
	return func(s *EvidenceService) {
		if max > 0 {
			s.maxPerCaseRun = max
		}
	}
}

// NewEvidenceService wires the evidence use cases to their metadata
// repositories, batch ownership reader, filesystem store and id generator. The
// limits default to the spec §5.7 constants; the composition root overrides the
// byte cap from EZTEST_EXEC_EVIDENCE_MAX_BYTES.
func NewEvidenceService(evidence EvidenceRepository, refs EvidenceRefRepository, batches evidenceBatchReader, store EvidenceStore, ids IDGenerator, opts ...EvidenceServiceOption) *EvidenceService {
	service := &EvidenceService{
		evidence:      evidence,
		refs:          refs,
		batches:       batches,
		store:         store,
		ids:           ids,
		maxBytes:      DefaultEvidenceMaxBytes,
		maxPerCaseRun: MaxEvidencePerCaseRun,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Capture validates, writes and records one immutable artifact. The bytes are
// written first (temporary file + atomic rename in the store) and the metadata
// row is the commit point (D30): a failed insert removes the file best effort.
// The case_run and optional step_result references are written after the row and
// are best effort, because the evidence row itself is the durable artifact.
func (s *EvidenceService) Capture(ctx context.Context, in CaptureInput) (domain.Evidence, error) {
	if s == nil || s.evidence == nil || s.store == nil || s.ids == nil {
		return domain.Evidence{}, errors.New("execution: evidence service is not configured")
	}
	if in.BatchID <= 0 || in.CaseRunID <= 0 || (in.StepResultID != nil && *in.StepResultID <= 0) {
		return domain.Evidence{}, invalidEvidenceError("invalid_evidence_scope",
			"batch, case run and step result ids must be positive")
	}
	if !validEvidenceKind(in.Kind) {
		return domain.Evidence{}, invalidEvidenceError("invalid_evidence_kind",
			fmt.Sprintf("evidence kind %q is not one of screenshot, http_exchange, console_log, network_log, download or db_result", in.Kind))
	}
	if len(in.Data) == 0 {
		return domain.Evidence{}, invalidEvidenceError("invalid_evidence_content",
			"evidence must not be empty")
	}
	if int64(len(in.Data)) > s.maxBytes {
		return domain.Evidence{}, invalidEvidenceError("evidence_too_large",
			fmt.Sprintf("evidence must be at most %d bytes", s.maxBytes))
	}

	existing, err := s.evidence.ListByCaseRun(ctx, in.CaseRunID)
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("execution: count case run evidence: %w", err)
	}
	if len(existing) >= s.maxPerCaseRun {
		return domain.Evidence{}, invalidEvidenceError("evidence_limit_exceeded",
			fmt.Sprintf("a case run allows at most %d evidence artifacts", s.maxPerCaseRun))
	}

	id := s.ids.Next()
	if id <= 0 {
		return domain.Evidence{}, errors.New("execution: id generator returned a non-positive evidence id")
	}
	mimeType := normalizeEvidenceMIME(in.Mime)
	rel := fmt.Sprintf("%d/%d/%d.%s", in.BatchID, in.CaseRunID, id, evidenceExtension(mimeType))
	storedRel, digest, size, err := s.store.Write(ctx, rel, in.Data)
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("execution: store evidence: %w", err)
	}

	caseRunID := in.CaseRunID
	var stepResultID *int64
	if in.StepResultID != nil {
		value := *in.StepResultID
		stepResultID = &value
	}
	metadata := in.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	evidence, err := s.evidence.Create(ctx, domain.Evidence{
		ID:           id,
		BatchID:      in.BatchID,
		CaseRunID:    &caseRunID,
		StepResultID: stepResultID,
		Kind:         in.Kind,
		Path:         storedRel,
		SHA256:       digest,
		SizeBytes:    size,
		Mime:         mimeType,
		Metadata:     metadata,
	})
	if err != nil {
		// The file has no metadata row yet; remove it best effort so it does not
		// become an orphan (P8 GC owns anything that survives this attempt).
		s.discardStored(ctx, storedRel)
		return domain.Evidence{}, fmt.Errorf("execution: create evidence: %w", err)
	}

	s.createRefs(ctx, evidence)
	return evidence, nil
}

// Open returns a reader over the verified artifact bytes plus the evidence
// metadata. It proves the evidence belongs to versionID through its batch,
// verifies the sha256 and size on read (spec §5.7), and never exposes the
// root-relative filesystem path to the caller.
func (s *EvidenceService) Open(ctx context.Context, versionID, evidenceID int64) (io.ReadCloser, domain.Evidence, error) {
	if s == nil || s.evidence == nil || s.store == nil {
		return nil, domain.Evidence{}, errors.New("execution: evidence service is not configured")
	}
	if versionID <= 0 || evidenceID <= 0 {
		return nil, domain.Evidence{}, evidenceNotFoundError(evidenceID)
	}
	evidence, err := s.evidence.Get(ctx, evidenceID)
	if err != nil {
		if errors.Is(err, ErrEvidenceNotFound) {
			return nil, domain.Evidence{}, evidenceNotFoundError(evidenceID)
		}
		return nil, domain.Evidence{}, fmt.Errorf("execution: read evidence: %w", err)
	}
	if s.batches == nil {
		return nil, domain.Evidence{}, errors.New("execution: evidence service is not configured")
	}
	if _, err := s.batches.GetBatch(ctx, versionID, evidence.BatchID); err != nil {
		// A foreign batch is indistinguishable from a missing evidence row.
		if errors.Is(err, ErrBatchNotFound) {
			return nil, domain.Evidence{}, evidenceNotFoundError(evidenceID)
		}
		return nil, domain.Evidence{}, fmt.Errorf("execution: verify evidence ownership: %w", err)
	}

	file, err := s.store.Open(ctx, evidence.Path)
	if err != nil {
		return nil, domain.Evidence{}, evidenceCorruptError(evidenceID, err)
	}
	defer file.Close()

	// Read at most the declared size plus one byte: that is enough to detect a
	// longer file, and it keeps a corrupted size_bytes row from forcing a huge
	// allocation. The read limit never drops below the default cap, so lowering
	// the configured capture limit does not make older artifacts unreadable.
	maxRead := s.maxBytes
	if maxRead < DefaultEvidenceMaxBytes {
		maxRead = DefaultEvidenceMaxBytes
	}
	if evidence.SizeBytes < 0 || evidence.SizeBytes > maxRead {
		return nil, domain.Evidence{}, evidenceCorruptError(evidenceID,
			fmt.Errorf("recorded size %d is outside the readable range", evidence.SizeBytes))
	}
	data, err := io.ReadAll(io.LimitReader(file, evidence.SizeBytes+1))
	if err != nil {
		return nil, domain.Evidence{}, evidenceCorruptError(evidenceID, err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != evidence.SizeBytes || hex.EncodeToString(sum[:]) != evidence.SHA256 {
		return nil, domain.Evidence{}, evidenceCorruptError(evidenceID,
			errors.New("stored bytes do not match the recorded sha256/size"))
	}

	// The API must not expose the filesystem layout (spec §5.7); the caller only
	// needs the metadata and the verified bytes.
	evidence.Path = ""
	return io.NopCloser(bytes.NewReader(data)), evidence, nil
}

// ListByCaseRun returns the artifacts of one case run in capture order.
func (s *EvidenceService) ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.Evidence, error) {
	if s == nil || s.evidence == nil {
		return nil, errors.New("execution: evidence service is not configured")
	}
	return s.evidence.ListByCaseRun(ctx, caseRunID)
}

// createRefs writes the case_run reference and, when present, the step_result
// reference. Both are best effort: the evidence row is already committed and the
// refs are traceability enrichment, so a failure is logged, not returned.
func (s *EvidenceService) createRefs(ctx context.Context, evidence domain.Evidence) {
	if s.refs == nil {
		return
	}
	refs := make([]domain.EvidenceRef, 0, 2)
	if evidence.CaseRunID != nil {
		refs = append(refs, domain.EvidenceRef{
			EvidenceID: evidence.ID,
			RefKind:    "case_run",
			RefID:      *evidence.CaseRunID,
		})
	}
	if evidence.StepResultID != nil {
		refs = append(refs, domain.EvidenceRef{
			EvidenceID: evidence.ID,
			RefKind:    "step_result",
			RefID:      *evidence.StepResultID,
		})
	}
	for _, ref := range refs {
		if _, err := s.refs.Create(ctx, ref); err != nil {
			slog.WarnContext(ctx, "evidence ref creation failed",
				"evidence_id", evidence.ID,
				"ref_kind", ref.RefKind,
				"ref_id", ref.RefID,
				"error", err,
			)
		}
	}
}

// discardStored removes an orphan file after a failed insert. Cleanup is best
// effort: a failure leaves the file for the P8 GC and is only logged.
func (s *EvidenceService) discardStored(ctx context.Context, rel string) {
	remover, ok := s.store.(evidenceFileRemover)
	if !ok {
		return
	}
	if err := remover.Remove(ctx, rel); err != nil {
		slog.WarnContext(ctx, "evidence file cleanup failed", "path", rel, "error", err)
	}
}

// evidenceKinds is the frozen evidence kind allow-list of the evidence DDL. The
// P7 migration adds db_result for the execution.db_assert artifact.
var evidenceKinds = map[string]struct{}{
	"screenshot":    {},
	"http_exchange": {},
	"console_log":   {},
	"network_log":   {},
	"download":      {},
	"db_result":     {},
}

// validEvidenceKind reports whether kind is one of the persisted kinds.
func validEvidenceKind(kind string) bool {
	_, ok := evidenceKinds[kind]
	return ok
}

// evidenceExtension derives the stored file extension from the canonical MIME
// type. Unknown types fall back to .bin, which keeps downloads and other opaque
// artifacts addressable without trusting a caller-supplied filename.
func evidenceExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "application/json":
		return "json"
	case "application/pdf":
		return "pdf"
	case "application/zip":
		return "zip"
	case "application/xml", "text/xml":
		return "xml"
	case "text/plain":
		return "txt"
	case "text/html":
		return "html"
	case "text/csv":
		return "csv"
	case "text/markdown":
		return "md"
	default:
		return "bin"
	}
}

// normalizeEvidenceMIME lower-cases a media type and drops its parameters.
func normalizeEvidenceMIME(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(raw); err == nil {
		return strings.ToLower(mediaType)
	}
	if idx := strings.IndexByte(raw, ';'); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

// invalidEvidenceError builds the 422 invalid_argument error of the evidence
// use cases.
func invalidEvidenceError(reason, message string) error {
	return domain.NewReasonError("invalid_argument", reason, message, false, nil)
}

// evidenceNotFoundError builds the indistinguishable 404 of a missing or foreign
// evidence row.
func evidenceNotFoundError(evidenceID int64) error {
	return domain.NewReasonError("not_found", "not_found",
		fmt.Sprintf("evidence %d not found", evidenceID), false, nil)
}

// evidenceCorruptError builds the 500 of unreadable or tampered artifact bytes.
func evidenceCorruptError(evidenceID int64, cause error) error {
	return domain.NewReasonError("internal", "evidence_corrupt",
		fmt.Sprintf("evidence %d is unreadable or corrupt: %v", evidenceID, cause), false, nil)
}

// sensitiveHeaderNames is the case-insensitive set of header values masked
// before an HTTP exchange is stored as evidence (spec §5.7).
var sensitiveHeaderNames = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"x-auth-token":        {},
}

// RedactHTTP masks sensitive header values and recursively masks sensitive JSON
// fields in body (spec §5.7). Header names are retained with their value
// cardinality; a body the redactor cannot parse as JSON is returned unchanged.
// The input map is not mutated.
func RedactHTTP(headers map[string][]string, body []byte) (map[string][]string, []byte) {
	redactedHeaders := make(map[string][]string, len(headers))
	for name, values := range headers {
		copied := make([]string, len(values))
		if _, sensitive := sensitiveHeaderNames[strings.ToLower(strings.TrimSpace(name))]; sensitive {
			for i := range copied {
				copied[i] = redactedValue
			}
			redactedHeaders[name] = copied
			continue
		}
		copy(copied, values)
		redactedHeaders[name] = copied
	}
	return redactedHeaders, RedactJSON(body)
}

// RedactJSON masks the values of recursively nested JSON keys whose normalized
// name contains password, passwd, token, secret or apikey. Non-JSON input is
// returned byte for byte. Numbers keep their literal representation.
func RedactJSON(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return body
	}
	// Reject trailing content: only a single JSON document is redacted.
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return body
	}
	encoded, err := json.Marshal(redactJSONValue(value))
	if err != nil {
		return body
	}
	return encoded
}

// redactJSONValue walks the decoded JSON value and replaces masked leaves.
func redactJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveJSONKey(key) {
				redacted[key] = redactedValue
				continue
			}
			redacted[key] = redactJSONValue(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for i, item := range typed {
			redacted[i] = redactJSONValue(item)
		}
		return redacted
	default:
		return value
	}
}

// sensitiveJSONKey reports whether a JSON key names a secret. Matching ignores
// case and separators so access_token, api-key and APIKEY are all caught.
func sensitiveJSONKey(key string) bool {
	normalized := strings.ToLower(key)
	normalized = strings.NewReplacer("_", "", "-", "", " ", "").Replace(normalized)
	for _, needle := range []string{"password", "passwd", "token", "secret", "apikey"} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}
