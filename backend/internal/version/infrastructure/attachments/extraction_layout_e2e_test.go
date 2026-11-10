package attachments_test

// C-03 end-to-end coverage: the in-memory DOCX extraction-layout fixture must
// survive the real parser -> segmenter -> deterministic processor path. The
// parser test alone cannot prove the processor reads the table columns and the
// FR body BDD acceptance criteria instead of the flattened Markdown text.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type layoutExtractionSource struct {
	segments []versionapp.Segment
}

func (s layoutExtractionSource) ExtractionSegments(_ context.Context, _, _ int64, _ string) ([]versionapp.Segment, error) {
	return s.segments, nil
}

type layoutExtractionSink struct {
	requirements []versionapp.ExtractionRequirement
}

func (s *layoutExtractionSink) EmitExtractedRequirements(_ context.Context, _ versionapp.ExtractionSource, requirements []versionapp.ExtractionRequirement) (versionapp.ExtractionEmitCounts, error) {
	s.requirements = append(s.requirements, requirements...)
	return versionapp.ExtractionEmitCounts{Created: len(requirements)}, nil
}

// TestDOCXExtractionLayoutThroughProcessor drives the fixture through the
// production deterministic processor: the FR body yields its BDD AC, the 6.1
// catalog row keeps a clean column title, and the 6.4 planned row keeps the
// planned category without a numeric AC.
func TestDOCXExtractionLayoutThroughProcessor(t *testing.T) {
	result, err := extractResult(t, "requirements.docx", "", docxExtractionLayoutFixture())
	if err != nil {
		t.Fatalf("docx: %v", err)
	}
	segments, err := versionapp.SegmentExtraction(result, versionapp.SegmentOptions{SourcePath: "requirements.docx"})
	if err != nil {
		t.Fatalf("segment: %v", err)
	}
	sum := sha256.Sum256([]byte(result.Text))
	inputSHA := hex.EncodeToString(sum[:])
	sink := &layoutExtractionSink{}
	processor := versionapp.NewDeterministicExtractionProcessor(
		layoutExtractionSource{segments: segments}, sink)
	run := versionapp.ExtractionRun{
		ID: 9001, VersionID: 7, AttachmentID: 3, InputSHA256: inputSHA,
	}
	for _, segment := range segments {
		if _, err := processor.ProcessSegment(context.Background(), run, versionapp.ExtractionSegment{
			Seq: segment.Seq, TextSHA256: segment.TextSHA256,
		}); err != nil {
			t.Fatalf("ProcessSegment(%d): %v", segment.Seq, err)
		}
	}

	byID := make(map[string]versionapp.ExtractionRequirement, len(sink.requirements))
	for _, requirement := range sink.requirements {
		byID[requirement.ExternalID] = requirement
	}
	body, ok := byID["FR-101"]
	if !ok {
		t.Fatalf("FR-101 body requirement missing: %#v", sink.requirements)
	}
	if body.Title != "账号密码登录" {
		t.Fatalf("FR-101 title = %q", body.Title)
	}
	if strings.Contains(body.Title, "|") || strings.Contains(body.Description, "|") {
		t.Fatalf("FR-101 leaked a table row: %#v", body)
	}
	joined := strings.Join(body.AcceptanceCriteria, "\n")
	for _, want := range []string{"场景 1", "场景 2", "Given", "Then"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("FR-101 BDD AC missing %q: %#v", want, body.AcceptanceCriteria)
		}
	}

	list, ok := byID["FR-001"]
	if !ok {
		t.Fatalf("FR-001 6.1 row missing: %#v", sink.requirements)
	}
	if list.Title != "账号密码登录" || strings.Contains(list.Title, "|") {
		t.Fatalf("FR-001 title = %q, want the 需求名称 cell", list.Title)
	}
	if list.Description != "用户使用账号密码登录" {
		t.Fatalf("FR-001 description = %q, want the 需求描述 cell", list.Description)
	}

	planned, ok := byID["FR-131"]
	if !ok {
		t.Fatalf("FR-131 6.4 row missing: %#v", sink.requirements)
	}
	if planned.Category != "planned" || planned.Priority != "P2" {
		t.Fatalf("FR-131 = %#v, want planned/P2", planned)
	}
	if len(planned.AcceptanceCriteria) != 0 {
		t.Fatalf("numeric 验收场景数 became AC: %#v", planned.AcceptanceCriteria)
	}
}
