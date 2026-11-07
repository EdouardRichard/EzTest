package version_test

// v0.22 W0 fix round 1 (Task 36 review Critical 1): the structured question
// card is a domain-valid conversation part. The version domain must accept,
// serialize and read it back so the runtime conversation writer can persist a
// question.ask card without the read path rejecting the message. The same file
// pins the error-part metadata the runner forwards from tools.Error.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const questionPartPayloadJSON = `{
	"question_id": "9001",
	"batch_id": "qb-9001",
	"questions": [
		{
			"id": "q1",
			"text": "Which login method should the first release support?",
			"options": [
				{"id": "o1", "label": "SMS code"},
				{"id": "o2", "label": "Password"}
			],
			"allow_free_text": true
		}
	],
	"expires_at": "2026-11-06T08:00:00Z"
}`

func questionPart() version.MessagePart {
	return version.MessagePart{
		Type:     version.MessagePartQuestion,
		Question: json.RawMessage(questionPartPayloadJSON),
	}
}

// TestQuestionPartRoundTripsThroughDomainJSON is the Critical 1 regression: the
// stored snapshot is the domain JSON, so the frontend wire shape (`type:
// "question"` with the payload fields flattened) must marshal and unmarshal
// cleanly through version.MessagePart.
func TestQuestionPartRoundTripsThroughDomainJSON(t *testing.T) {
	part := questionPart()
	if err := version.ValidateAssistantMessageParts([]version.MessagePart{part}); err != nil {
		t.Fatalf("question assistant part rejected: %v", err)
	}

	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal question part: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode question part: %v", err)
	}
	if decoded["type"] != version.MessagePartQuestion {
		t.Fatalf("type = %#v, want %q (%s)", decoded["type"], version.MessagePartQuestion, raw)
	}
	// The frontend QuestionPart contract is flat: type plus the payload fields,
	// not a nested `payload` object.
	if _, nested := decoded["payload"]; nested {
		t.Fatalf("question part must serialize the payload flat: %s", raw)
	}
	if decoded["question_id"] != "9001" || decoded["batch_id"] != "qb-9001" {
		t.Fatalf("question id/batch lost in JSON: %s", raw)
	}
	questions, ok := decoded["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("questions = %#v", decoded["questions"])
	}

	var roundTripped version.MessagePart
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("unmarshal question part: %v", err)
	}
	if roundTripped.Type != version.MessagePartQuestion {
		t.Fatalf("round trip type = %q", roundTripped.Type)
	}
	if !jsonSemanticallyEqual(t, roundTripped.Question, json.RawMessage(questionPartPayloadJSON)) {
		t.Fatalf("round trip payload = %s", roundTripped.Question)
	}
	if err := version.ValidateAssistantMessageParts([]version.MessagePart{roundTripped}); err != nil {
		t.Fatalf("round-tripped question part rejected: %v", err)
	}
}

// TestQuestionPartRejectedOnUserMessage pins that the question card is an
// assistant-only part: a user message can never inject a question batch.
func TestQuestionPartRejectedOnUserMessage(t *testing.T) {
	if err := version.ValidateUserMessageParts([]version.MessagePart{questionPart()}); err == nil {
		t.Fatal("user message accepted a question part")
	}
}

// TestQuestionPartValidationRejectsMalformedPayload keeps the domain the final
// guard for the write API: an empty/invalid card must never reach the DB.
func TestQuestionPartValidationRejectsMalformedPayload(t *testing.T) {
	cases := map[string]string{
		"missing question_id": `{"batch_id":"qb-1","questions":[{"id":"q1","text":"?"}]}`,
		"zero question_id":    `{"question_id":"0","batch_id":"qb-1","questions":[{"id":"q1","text":"?"}]}`,
		"missing batch_id":    `{"question_id":"7","questions":[{"id":"q1","text":"?"}]}`,
		"empty questions":     `{"question_id":"7","batch_id":"qb-1","questions":[]}`,
		"non-object payload":  `["not","an","object"]`,
	}
	for name, payload := range cases {
		part := version.MessagePart{Type: version.MessagePartQuestion, Question: json.RawMessage(payload)}
		if err := version.ValidateAssistantMessageParts([]version.MessagePart{part}); err == nil {
			t.Fatalf("%s: malformed question part accepted", name)
		}
	}
}

// TestErrorPartCarriesBoundedToolMetadata pins the runner error-part contract:
// the retryable/field/allowed/hint metadata of a typed tools.Error survives the
// domain JSON and stays out of a user-facing part when it is oversized.
func TestErrorPartCarriesBoundedToolMetadata(t *testing.T) {
	part := version.MessagePart{
		Type:      version.MessagePartError,
		Code:      "invalid_argument",
		Message:   "database_id is not bound to this version",
		Retryable: true,
		Field:     "cases[0].assertions[0].database_id",
		Allowed:   []string{"11", "12"},
		Hint:      "call sut.database_list first",
	}
	if err := version.ValidateAssistantMessageParts([]version.MessagePart{part}); err != nil {
		t.Fatalf("error part with metadata rejected: %v", err)
	}
	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal error part: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode error part: %v", err)
	}
	if decoded["retryable"] != true || decoded["field"] != part.Field || decoded["hint"] != part.Hint {
		t.Fatalf("error metadata lost in JSON: %s", raw)
	}
	if allowed, ok := decoded["allowed"].([]any); !ok || len(allowed) != 2 {
		t.Fatalf("allowed = %#v", decoded["allowed"])
	}
	var roundTripped version.MessagePart
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("unmarshal error part: %v", err)
	}
	if !roundTripped.Retryable || roundTripped.Field != part.Field || roundTripped.Hint != part.Hint ||
		len(roundTripped.Allowed) != 2 || roundTripped.Allowed[0] != "11" {
		t.Fatalf("round trip = %#v", roundTripped)
	}

	oversized := part
	oversized.Hint = strings.Repeat("x", version.MessageErrorMessageMax+1)
	if err := version.ValidateAssistantMessageParts([]version.MessagePart{oversized}); err == nil {
		t.Fatal("oversized error hint accepted")
	}
	tooMany := part
	for index := 0; index <= version.MessageErrorAllowedMax; index++ {
		tooMany.Allowed = append(tooMany.Allowed, "id")
	}
	if err := version.ValidateAssistantMessageParts([]version.MessagePart{tooMany}); err == nil {
		t.Fatal("unbounded error allowed list accepted")
	}
}

func jsonSemanticallyEqual(t *testing.T, left, right []byte) bool {
	t.Helper()
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		t.Fatalf("decode left %s: %v", left, err)
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		t.Fatalf("decode right %s: %v", right, err)
	}
	leftCanonical, err := json.Marshal(leftValue)
	if err != nil {
		t.Fatalf("canonicalize left: %v", err)
	}
	rightCanonical, err := json.Marshal(rightValue)
	if err != nil {
		t.Fatalf("canonicalize right: %v", err)
	}
	return bytes.Equal(leftCanonical, rightCanonical)
}
