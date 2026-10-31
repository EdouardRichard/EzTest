package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ContentRef is the content-ledger reference of one stored evidence blob. ID is
// the P0 content_ref.id; bytes live in the content store and are never exposed
// through the execution API.
type ContentRef struct {
	ID        int64
	SHA256    string
	SizeBytes int64
	MIME      string
}

// ContentStore stores evidence bytes in the P0 content ledger and reads one
// blob back by SHA-256 for the evidence_read tool.
type ContentStore interface {
	PutEvidence(ctx context.Context, evidenceID int64, kind execution.EvidenceKind, filename string, data []byte) (ContentRef, error)
	Open(ctx context.Context, sha256 string) ([]byte, error)
}

// EvidenceInput is one evidence upload. Auto marks evidence produced by a tool
// (screenshot/console/network/http/db/download) rather than execution.evidence.
type EvidenceInput struct {
	Kind         execution.EvidenceKind
	Data         []byte
	Filename     string
	StepSeq      *int
	AssertionSeq *int
	VersionSUTID *int64
	Auto         bool
}

// AddEvidence stores immutable evidence: it re-computes SHA-256/size from the
// bytes, enforces the per-case_run cap, uploads to the content ledger, writes
// the evidence row and its references. Oversized bytes are rejected with
// evidence_too_large; evidence beyond the cap is dropped without failing the
// case.
func (s *CaseRunner) AddEvidence(ctx context.Context, scope tools.Scope, in EvidenceInput) (execution.Evidence, error) {
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return execution.Evidence{}, err
	}
	if !in.Kind.Valid() {
		return execution.Evidence{}, platform.Invalid("kind", "unsupported evidence kind")
	}
	if s.content == nil || s.evidence == nil {
		return execution.Evidence{}, errors.New("case runner has no evidence store")
	}
	maxBytes := s.intSetting("execution.evidence_max_bytes", defaultEvidenceMaxBytes)
	if int64(len(in.Data)) > maxBytes {
		return execution.Evidence{}, &platform.Error{
			Status:  413,
			Code:    "evidence_too_large",
			Message: fmt.Sprintf("evidence exceeds the %d byte limit", maxBytes),
		}
	}
	count, err := s.evidence.CountByCaseRun(ctx, caseRun.ID)
	if err != nil {
		return execution.Evidence{}, mapExecutionError(err, "evidence")
	}
	if count >= evidenceMaxPerCaseRun {
		// Overflow is dropped and counted, never failed (P3-D-21).
		return execution.Evidence{}, nil
	}
	versionSUTID := in.VersionSUTID
	if versionSUTID != nil && *versionSUTID <= 0 {
		versionSUTID = nil
	}
	if versionSUTID == nil {
		// Single-SUT runs and tools that omit version_sut_id must still produce
		// per-SUT evidence (P3 §13.2-2/4: everything carries version_sut_id).
		versionSUTID = s.defaultEvidenceVersionSUT(ctx, caseRun)
	}
	evidenceID := s.nextID()
	filename := sanitizeEvidenceFilename(in.Filename)
	ref, err := s.content.PutEvidence(ctx, evidenceID, in.Kind, filename, in.Data)
	if err != nil {
		return execution.Evidence{}, mapExecutionError(err, "content")
	}
	sum := sha256.Sum256(in.Data)
	sha := hex.EncodeToString(sum[:])
	mime := ref.MIME
	if strings.TrimSpace(mime) == "" {
		mime = in.Kind.MIME()
	}
	evidence := execution.Evidence{
		ID:             evidenceID,
		CaseRunID:      caseRun.ID,
		VersionSUTID:   versionSUTID,
		StepSeq:        in.StepSeq,
		AssertionSeq:   in.AssertionSeq,
		Kind:           in.Kind,
		ContentRefID:   ref.ID,
		SHA256:         sha,
		SizeBytes:      int64(len(in.Data)),
		MIME:           truncateRunes(mime, execution.EvidenceMIMEMax),
		Filename:       filename,
		CreatedByRunID: optionalID(scope.AgentRunID),
		CreatedAt:      s.now(),
	}
	if err := s.evidence.Insert(ctx, evidence); err != nil {
		return execution.Evidence{}, mapExecutionError(err, "evidence")
	}
	s.insertEvidenceRefs(ctx, evidence)
	s.publishEvidenceEvent(caseRun, evidence)
	return evidence, nil
}

// defaultEvidenceVersionSUT resolves the SUT attribution for evidence created
// without an explicit version_sut_id: a single-SUT intent is unambiguous, then
// the case default target is used. Cross-system runs should pass the target
// explicitly from the tool scope; nil remains allowed only if the case has no
// resolvable SUT metadata.
func (s *CaseRunner) defaultEvidenceVersionSUT(ctx context.Context, caseRun execution.CaseRun) *int64 {
	if s.intents != nil {
		if intent, err := s.intents.Get(ctx, caseRun.IntentID); err == nil && len(intent.VersionSUTIDs) == 1 && intent.VersionSUTIDs[0] > 0 {
			id := intent.VersionSUTIDs[0]
			return &id
		}
	}
	if s.cases != nil {
		if intent, err := s.intents.Get(ctx, caseRun.IntentID); err == nil {
			if snapshot, err := s.cases.GetCaseVersion(ctx, intent.VersionID, caseRun.CaseVersionID); err == nil && snapshot.DefaultVersionSUTID > 0 {
				id := snapshot.DefaultVersionSUTID
				return &id
			}
		}
	}
	return nil
}

// EvidenceText is the execution.evidence tool: it uploads text evidence.
func (s *CaseRunner) EvidenceText(ctx context.Context, scope tools.Scope, in tools.EvidenceTextInput) (any, error) {	evidence, err := s.AddEvidence(ctx, scope, EvidenceInput{
		Kind:         execution.EvidenceKind(in.Kind),
		Data:         []byte(in.Text),
		StepSeq:      in.StepSeq,
		AssertionSeq: in.AssertionSeq,
	})
	if err != nil {
		return nil, err
	}
	if evidence.ID == 0 {
		return map[string]any{"skipped": true, "reason": "evidence_cap_reached"}, nil
	}
	return map[string]any{
		"evidence_id": platformid.Format(evidence.ID),
		"kind":        string(evidence.Kind),
		"sha256":      evidence.SHA256,
		"size_bytes":  evidence.SizeBytes,
	}, nil
}

// EvidenceRead is the execution.evidence_read tool: metadata plus a bounded text
// fragment. It never returns executor paths or credentials.
func (s *CaseRunner) EvidenceRead(ctx context.Context, scope tools.Scope, in tools.EvidenceReadInput) (any, error) {
	if s.evidence == nil {
		return nil, errors.New("case runner has no evidence store")
	}
	evidence, err := s.evidence.Get(ctx, in.EvidenceID)
	if err != nil {
		return nil, mapExecutionError(err, "evidence")
	}
	caseRun, err := s.caseRuns.Get(ctx, evidence.CaseRunID)
	if err != nil {
		return nil, mapExecutionError(err, "case_run")
	}
	if scope.WaveID > 0 && caseRun.WaveID != scope.WaveID {
		return nil, platform.NotFound("evidence")
	}
	result := map[string]any{
		"evidence_id": platformid.Format(evidence.ID),
		"case_run_id": platformid.Format(evidence.CaseRunID),
		"kind":        string(evidence.Kind),
		"mime":        evidence.MIME,
		"size_bytes":  evidence.SizeBytes,
		"sha256":      evidence.SHA256,
	}
	if evidence.VersionSUTID != nil {
		result["version_sut_id"] = platformid.Format(*evidence.VersionSUTID)
	}
	if evidence.StepSeq != nil {
		result["step_seq"] = *evidence.StepSeq
	}
	if evidence.AssertionSeq != nil {
		result["assertion_seq"] = *evidence.AssertionSeq
	}
	text := ""
	if s.content != nil {
		data, readErr := s.content.Open(ctx, evidence.SHA256)
		if readErr != nil {
			return nil, mapExecutionError(readErr, "content")
		}
		text = string(data)
	}
	maxChars := in.MaxChars
	if maxChars <= 0 || maxChars > 20000 {
		maxChars = 4000
	}
	offset := in.Offset
	runes := []rune(text)
	if offset < 0 || offset > len(runes) {
		return nil, platform.Invalid("offset", "is outside the evidence text")
	}
	end := offset + maxChars
	if end > len(runes) {
		end = len(runes)
	}
	result["text"] = string(runes[offset:end])
	result["total_chars"] = len(runes)
	result["truncated"] = end < len(runes)
	return result, nil
}

func (s *CaseRunner) insertEvidenceRefs(ctx context.Context, evidence execution.Evidence) {
	_, _ = s.insertRef(ctx, evidence, execution.EvidenceOwnerCaseRun, evidence.CaseRunID)
	if evidence.StepSeq != nil {
		steps, err := s.results.ListSteps(ctx, evidence.CaseRunID)
		if err == nil {
			for _, step := range steps {
				if step.Seq == *evidence.StepSeq {
					_, _ = s.insertRef(ctx, evidence, execution.EvidenceOwnerStepResult, step.ID)
					break
				}
			}
		}
	}
	if evidence.StepSeq != nil && evidence.AssertionSeq != nil {
		assertions, err := s.results.ListAssertions(ctx, evidence.CaseRunID)
		if err == nil {
			for _, assertion := range assertions {
				if assertion.StepSeq == *evidence.StepSeq && assertion.AssertionSeq == *evidence.AssertionSeq {
					_, _ = s.insertRef(ctx, evidence, execution.EvidenceOwnerAssertionResult, assertion.ID)
					break
				}
			}
		}
	}
}

func (s *CaseRunner) insertRef(ctx context.Context, evidence execution.Evidence, ownerKind string, ownerID int64) (execution.EvidenceRef, error) {
	if ownerID <= 0 {
		return execution.EvidenceRef{}, nil
	}
	ref := execution.EvidenceRef{
		ID:         s.nextID(),
		EvidenceID: evidence.ID,
		OwnerKind:  ownerKind,
		OwnerID:    ownerID,
		Role:       string(evidence.Kind),
		CreatedAt:  s.now(),
	}
	if err := s.evidence.InsertRef(ctx, ref); err != nil {
		return execution.EvidenceRef{}, err
	}
	return ref, nil
}

func (s *CaseRunner) publishEvidenceEvent(caseRun execution.CaseRun, evidence execution.Evidence) {
	if s.events == nil {
		return
	}
	scope := map[string]any{
		"case_run_id": platformid.Format(caseRun.ID),
		"wave_id":     platformid.Format(caseRun.WaveID),
		"evidence_id": platformid.Format(evidence.ID),
		"kind":        string(evidence.Kind),
	}
	payload := map[string]any{
		"evidence_id": platformid.Format(evidence.ID),
		"case_run_id": platformid.Format(caseRun.ID),
		"kind":        string(evidence.Kind),
		"size_bytes":  evidence.SizeBytes,
	}
	publishEvent(context.Background(), s.events, EventEvidenceCreated, s.versionForCaseRun(caseRun), scope, payload, nil)
}

func sanitizeEvidenceFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if len([]rune(name)) > maxSUTFilenameLength {
		name = string([]rune(name)[:maxSUTFilenameLength])
	}
	return name
}
