package design

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func validCaseVersion() CaseVersion {
	return CaseVersion{
		ID:             7002,
		CaseID:         7001,
		Revision:       2,
		Title:          "账号密码登录成功",
		Description:    "使用有效账号登录",
		SutDescription: "商城前台登录页与登录接口。",
		Preconditions:  "已有可用账号",
		CoveredACs:     []string{"AC-2", "AC-1"},
		TestData:       json.RawMessage(`{"username":"admin","password":"secret"}`),
		Priority:       "P1",
		Status:         CaseStatusDraft,
		Steps: []CaseStep{
			{Seq: 1, Action: "打开登录页", Expected: "显示登录表单", TargetDescription: "登录页账号输入框", AccountRole: "admin", Data: json.RawMessage(`{}`)},
			{Seq: 2, Action: "提交账号密码", Expected: "接口返回成功", TargetDescription: "POST /login", Data: json.RawMessage(`{"username":"admin"}`)},
		},
		Assertions: []CaseAssertion{
			{StepSeq: 2, Kind: "api", Target: "POST /login", Expected: "200"},
			{StepSeq: 1, Kind: "ui", Target: "欢迎文案", Expected: "欢迎回来"},
			{StepSeq: 2, Kind: "db", Target: "登录审计行数", Expected: "1", SUTID: int64Ptr(9001)},
		},
		Sources: []CaseSource{
			{Kind: "requirement", RefID: "3002"},
		},
	}
}

func TestComputeCaseContentHashStable(t *testing.T) {
	first, err := ComputeCaseContentHash(validCaseVersion())
	if err != nil {
		t.Fatalf("ComputeCaseContentHash: %v", err)
	}
	second, err := ComputeCaseContentHash(validCaseVersion())
	if err != nil {
		t.Fatalf("ComputeCaseContentHash: %v", err)
	}
	if first != second {
		t.Fatalf("same content produced different hashes:\n%s\n%s", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("hash length = %d, want 64 hex chars", len(first))
	}
}

// TestComputeCaseContentHashCanonicalFieldOrder pins the authoritative payload
// shape of design v1.3.1 §3.6 byte-for-byte, by hashing the expected JSON
// literal independently and comparing. Field ORDER is part of the contract
// (encoding/json emits struct fields in declaration order), so this test fails if
// anyone reorders a struct field, renames a json tag, or drops a field -- all of
// which would silently re-key every stored content_hash.
//
// SCOPE OF THE omitempty GUARD, stated precisely because it is narrower than it
// looks: `omitempty` only changes the output for a field whose value IS the zero
// value in the fixture. This fixture emits 26 keys -- 10 payload + 6 step +
// 5 assertion + 5 source -- of which exactly 4 are zero (source path, source
// line, source sha, assertion sut_id), so only those 4 are pinned here. The other
// 22 are non-zero and would NOT be caught; that is what
// TestComputeCaseContentHashCanonicalEmptyEntries below is for. Between the two
// fixtures every legally-empty field is covered. (An earlier revision of this
// comment said "nineteen"; the count is derived above so it can be re-checked.)
//
//	payload:   title, description, sut_description, preconditions, covered_acs,
//	           test_data, priority, steps, assertions, sources
//	step:      seq, action, expected, target_description, account_role, data
//	assertion: step_seq, kind, target, expected, sut_id
//	source:    kind, ref_id, path, line, sha
func TestComputeCaseContentHashCanonicalFieldOrder(t *testing.T) {
	version := CaseVersion{
		Title:          "T",
		Description:    "D",
		SutDescription: "S",
		Preconditions:  "P",
		CoveredACs:     []string{"AC-1"},
		Priority:       "P2",
		Steps: []CaseStep{
			{Seq: 1, Action: "A", Expected: "E", TargetDescription: "TD", AccountRole: "R"},
		},
		Assertions: []CaseAssertion{
			{StepSeq: 1, Kind: "ui", Target: "TG", Expected: "EX"},
		},
		Sources: []CaseSource{
			{Kind: "requirement", RefID: "3002"},
		},
	}
	wantJSON := `{"title":"T","description":"D","sut_description":"S","preconditions":"P",` +
		`"covered_acs":["AC-1"],"test_data":{},"priority":"P2",` +
		`"steps":[{"seq":1,"action":"A","expected":"E","target_description":"TD","account_role":"R","data":{}}],` +
		`"assertions":[{"step_seq":1,"kind":"ui","target":"TG","expected":"EX","sut_id":null}],` +
		`"sources":[{"kind":"requirement","ref_id":"3002","path":"","line":null,"sha":""}]}`
	sum := sha256.Sum256([]byte(wantJSON))
	want := hex.EncodeToString(sum[:])

	got, err := ComputeCaseContentHash(version)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash: %v", err)
	}
	if got != want {
		t.Fatalf("canonical payload drifted.\n got hash %s\nwant hash %s\nwant json %s", got, want, wantJSON)
	}
}

// TestComputeCaseContentHashCanonicalEmptyEntries is the other half of the
// omitempty guard, and the half that actually matters in production.
//
// `omitempty` only changes the emitted JSON for a field whose value is the zero
// value, so the fully-populated fixture above can only catch it on the four
// fields that happen to be empty there. The fields that are LEGALLY empty in real
// case versions -- payload.description, step.target_description,
// step.account_role, plus source path/sha and the nil pointers -- are the ones
// where a stray omitempty would silently drop a key from every stored hash and
// re-key the whole table with the suite still green. This fixture sets every
// optional field to its zero value and pins the exact bytes, so each of those
// keys must still be present.
func TestComputeCaseContentHashCanonicalEmptyEntries(t *testing.T) {
	// Every optional field at its zero value: no description, no step target, no
	// account role, nil test_data/step data (normalized to {}), empty priority
	// (normalized to P2), nil sut_id, empty source path/sha, nil source line.
	emptyOptionals := CaseVersion{
		Title:          "T",
		SutDescription: "S",
		Preconditions:  "P",
		CoveredACs:     []string{"AC-1"},
		Steps: []CaseStep{
			{Seq: 1, Action: "A", Expected: "E"},
		},
		Assertions: []CaseAssertion{
			{StepSeq: 1, Kind: "ui", Target: "TG", Expected: "EX"},
		},
		Sources: []CaseSource{
			{Kind: "requirement", RefID: "3002"},
		},
	}
	const wantEmptyOptionals = `{"title":"T","description":"","sut_description":"S","preconditions":"P",` +
		`"covered_acs":["AC-1"],"test_data":{},"priority":"P2",` +
		`"steps":[{"seq":1,"action":"A","expected":"E","target_description":"","account_role":"","data":{}}],` +
		`"assertions":[{"step_seq":1,"kind":"ui","target":"TG","expected":"EX","sut_id":null}],` +
		`"sources":[{"kind":"requirement","ref_id":"3002","path":"","line":null,"sha":""}]}`

	// covered_acs must stay an empty ARRAY, not vanish: a version with no covered
	// ACs is not proposable, but rollback copies and pre-contract rows still get
	// hashed, and omitempty on the slice would re-key them all.
	noCoveredACs := emptyOptionals
	noCoveredACs.CoveredACs = nil
	const wantNoCoveredACs = `{"title":"T","description":"","sut_description":"S","preconditions":"P",` +
		`"covered_acs":[],"test_data":{},"priority":"P2",` +
		`"steps":[{"seq":1,"action":"A","expected":"E","target_description":"","account_role":"","data":{}}],` +
		`"assertions":[{"step_seq":1,"kind":"ui","target":"TG","expected":"EX","sut_id":null}],` +
		`"sources":[{"kind":"requirement","ref_id":"3002","path":"","line":null,"sha":""}]}`

	for _, testCase := range []struct {
		name    string
		version CaseVersion
		want    string
	}{
		{"every optional field empty", emptyOptionals, wantEmptyOptionals},
		{"covered_acs empty stays an array", noCoveredACs, wantNoCoveredACs},
	} {
		sum := sha256.Sum256([]byte(testCase.want))
		want := hex.EncodeToString(sum[:])
		got, err := ComputeCaseContentHash(testCase.version)
		if err != nil {
			t.Fatalf("%s: ComputeCaseContentHash: %v", testCase.name, err)
		}
		if got != want {
			t.Fatalf("%s: canonical payload drifted (an omitempty on a legally-empty field would do this).\n got hash %s\nwant hash %s\nwant json %s",
				testCase.name, got, want, testCase.want)
		}
	}
}

// TestComputeCaseContentHashCarriesSUTIDTiebreak pins that the assertion sort's
// last tiebreak is sut_id (hash.go, ruling 8): two assertions identical in
// step_seq/kind/target/expected but different in sut_id must hash the same
// whichever order they are submitted in, and must differ from a version where one
// of them has no sut_id at all.
func TestComputeCaseContentHashCarriesSUTIDTiebreak(t *testing.T) {
	base := validCaseVersion()
	base.Assertions = []CaseAssertion{
		{StepSeq: 1, Kind: "db", Target: "count", Expected: "1", SUTID: int64Ptr(11)},
		{StepSeq: 1, Kind: "db", Target: "count", Expected: "1", SUTID: int64Ptr(22)},
	}
	reordered := base
	reordered.Assertions = []CaseAssertion{
		{StepSeq: 1, Kind: "db", Target: "count", Expected: "1", SUTID: int64Ptr(22)},
		{StepSeq: 1, Kind: "db", Target: "count", Expected: "1", SUTID: int64Ptr(11)},
	}
	baseHash, err := ComputeCaseContentHash(base)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash base: %v", err)
	}
	reorderedHash, err := ComputeCaseContentHash(reordered)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash reordered: %v", err)
	}
	if baseHash != reorderedHash {
		t.Fatalf("sut_id tiebreak did not make the assertion order irrelevant:\n%s\n%s", baseHash, reorderedHash)
	}

	other := base
	other.Assertions = []CaseAssertion{
		{StepSeq: 1, Kind: "db", Target: "count", Expected: "1", SUTID: int64Ptr(11)},
		{StepSeq: 1, Kind: "db", Target: "count", Expected: "1", SUTID: int64Ptr(33)},
	}
	otherHash, err := ComputeCaseContentHash(other)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash other: %v", err)
	}
	if otherHash == baseHash {
		t.Fatal("a different sut_id must change the hash")
	}
}

func TestComputeCaseContentHashNormalizesSetOrderAndJSONKeys(t *testing.T) {
	base := validCaseVersion()
	reordered := validCaseVersion()
	// covered_acs is a set: submission order must not matter.
	reordered.CoveredACs = []string{"AC-1", "AC-2"}
	// JSON object key order is normalized by canonical marshaling.
	reordered.TestData = json.RawMessage(`{"password":"secret","username":"admin"}`)
	reordered.Steps[1].Data = json.RawMessage(`{"username":"admin"}`)
	// Steps and assertions are sorted by their own keys, so submission order
	// must not matter either.
	reordered.Steps = []CaseStep{base.Steps[1], base.Steps[0]}
	reordered.Assertions = []CaseAssertion{base.Assertions[2], base.Assertions[1], base.Assertions[0]}

	baseHash, err := ComputeCaseContentHash(base)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash base: %v", err)
	}
	reorderedHash, err := ComputeCaseContentHash(reordered)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash reordered: %v", err)
	}
	if baseHash != reorderedHash {
		t.Fatalf("set order/JSON keys changed the hash:\n%s\n%s", baseHash, reorderedHash)
	}
}

// TestComputeCaseContentHashNormalizesCoveredACDuplicates pins that a repeated or
// padded id does not create a new revision: covered_acs is normalized
// sorted-unique before hashing, exactly as NormalizeACIDSet does on the write
// path.
func TestComputeCaseContentHashNormalizesCoveredACDuplicates(t *testing.T) {
	base := validCaseVersion()
	base.CoveredACs = []string{"AC-1", "AC-2"}
	duplicated := validCaseVersion()
	duplicated.CoveredACs = []string{" AC-2 ", "AC-1", "AC-1", "AC-2", ""}

	baseHash, err := ComputeCaseContentHash(base)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash base: %v", err)
	}
	duplicatedHash, err := ComputeCaseContentHash(duplicated)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash duplicated: %v", err)
	}
	if baseHash != duplicatedHash {
		t.Fatalf("duplicate/padded covered_acs changed the hash:\n%s\n%s", baseHash, duplicatedHash)
	}
}

// TestComputeCaseContentHashDetectsEveryContractField pins that each v0.2.4-B
// contract element actually participates in the hash. A field that is carried but
// not hashed would let two different cases confirm as the same content.
func TestComputeCaseContentHashDetectsEveryContractField(t *testing.T) {
	base, err := ComputeCaseContentHash(validCaseVersion())
	if err != nil {
		t.Fatalf("ComputeCaseContentHash: %v", err)
	}
	mutations := map[string]func(*CaseVersion){
		"title":             func(v *CaseVersion) { v.Title = "另一个标题" },
		"description":       func(v *CaseVersion) { v.Description = "另一个描述" },
		"sut_description":   func(v *CaseVersion) { v.SutDescription = "另一个被测系统。" },
		"preconditions":     func(v *CaseVersion) { v.Preconditions = "无" },
		"covered_acs":       func(v *CaseVersion) { v.CoveredACs = []string{"AC-10"} },
		"test_data":         func(v *CaseVersion) { v.TestData = json.RawMessage(`{"username":"other"}`) },
		"priority":          func(v *CaseVersion) { v.Priority = "P0" },
		"step.action":       func(v *CaseVersion) { v.Steps[0].Action = "关闭登录页" },
		"step.expected":     func(v *CaseVersion) { v.Steps[0].Expected = "显示注册表单" },
		"step.target_desc":  func(v *CaseVersion) { v.Steps[0].TargetDescription = "另一个目标" },
		"step.account_role": func(v *CaseVersion) { v.Steps[0].AccountRole = "guest" },
		"step.data":         func(v *CaseVersion) { v.Steps[0].Data = json.RawMessage(`{"a":1}`) },
		"assertion.kind":    func(v *CaseVersion) { v.Assertions[0].Kind = "text" },
		"assertion.target":  func(v *CaseVersion) { v.Assertions[0].Target = "另一个断言目标" },
		"assertion.expected": func(v *CaseVersion) {
			v.Assertions[0].Expected = "500"
		},
		"assertion.step_seq": func(v *CaseVersion) { v.Assertions[0].StepSeq = 1 },
		"source.ref_id":      func(v *CaseVersion) { v.Sources[0].RefID = "3003" },
	}
	for name, mutate := range mutations {
		changed := validCaseVersion()
		mutate(&changed)
		changedHash, err := ComputeCaseContentHash(changed)
		if err != nil {
			t.Fatalf("ComputeCaseContentHash %s: %v", name, err)
		}
		if changedHash == base {
			t.Fatalf("%s is not part of the content hash", name)
		}
	}
}

// TestComputeCaseContentHashIgnoresIdentityAndState pins the other half of the
// contract: identity, revision, status, timestamps and the stored hash itself are
// deliberately excluded, so re-freezing identical content as a new revision is a
// no-op rather than a new case.
func TestComputeCaseContentHashIgnoresIdentityAndState(t *testing.T) {
	base, err := ComputeCaseContentHash(validCaseVersion())
	if err != nil {
		t.Fatalf("ComputeCaseContentHash: %v", err)
	}
	other := validCaseVersion()
	other.ID = 999999
	other.CaseID = 888888
	other.Revision = 17
	other.Status = CaseStatusConfirmed
	other.ContentHash = strings.Repeat("f", 64)
	other.StepCount, other.AssertionCount, other.SourceCount = 9, 9, 9
	otherHash, err := ComputeCaseContentHash(other)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash other: %v", err)
	}
	if base != otherHash {
		t.Fatalf("identity/state leaked into the content hash:\n%s\n%s", base, otherHash)
	}
}

func TestComputeCaseContentHashRejectsInvalidTestData(t *testing.T) {
	invalid := validCaseVersion()
	invalid.TestData = json.RawMessage(`[]`)
	if _, err := ComputeCaseContentHash(invalid); err == nil {
		t.Fatal("expected error for non-object test_data")
	}
}

func TestNormalizeACIDSet(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"nil", nil, ""},
		{"empty", []string{}, ""},
		{"blanks dropped", []string{"", "  ", "\t"}, ""},
		{"trimmed and sorted", []string{" AC-2 ", "AC-10", "AC-1"}, "AC-1,AC-10,AC-2"},
		{"duplicates collapsed", []string{"AC-1", "AC-1", "AC-2"}, "AC-1,AC-2"},
	}
	for _, testCase := range cases {
		got := strings.Join(NormalizeACIDSet(testCase.in), ",")
		if got != testCase.want {
			t.Fatalf("%s: NormalizeACIDSet(%q) = %q, want %q", testCase.name, testCase.in, got, testCase.want)
		}
	}
	// The result is never nil, so callers can range and marshal it directly.
	if NormalizeACIDSet(nil) == nil {
		t.Fatal("NormalizeACIDSet(nil) = nil, want an empty non-nil slice")
	}
}
