package design

import (
	"encoding/json"
	"testing"
)

func validCaseVersion() CaseVersion {
	return CaseVersion{
		ID:                  7002,
		CaseID:              7001,
		Revision:            2,
		Title:               "账号密码登录成功",
		Description:         "使用有效账号登录",
		Preconditions:       "已有可用账号",
		TestData:            json.RawMessage(`{"username":"admin","password":"secret"}`),
		Priority:            "P1",
		DefaultVersionSUTID: int64Ptr(9001),
		VersionSUTIDs:       []int64{9002, 9001},
		PageIDs:             []int64{6002, 6001},
		APIIDs:              []int64{4001},
		Status:              CaseStatusDraft,
		Steps: []CaseStep{
			{Seq: 1, Action: "打开登录页", Expected: "显示登录表单", TargetPageID: int64Ptr(6001), AccountRole: "admin", Data: json.RawMessage(`{}`)},
			{Seq: 2, Action: "提交账号密码", Expected: "接口返回成功", TargetVersionSUTID: int64Ptr(9002), TargetAPIID: int64Ptr(4001), Data: json.RawMessage(`{"username":"admin"}`)},
		},
		Assertions: []CaseAssertion{
			{StepSeq: 2, Kind: "api", Target: "POST /login", Expected: "200", TargetVersionSUTID: int64Ptr(9002)},
			{StepSeq: 1, Kind: "ui", Target: "欢迎文案", Expected: "欢迎回来", TargetVersionSUTID: int64Ptr(9001)},
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

func TestComputeCaseContentHashNormalizesSetOrderAndJSONKeys(t *testing.T) {
	base := validCaseVersion()
	reordered := validCaseVersion()
	reordered.VersionSUTIDs = []int64{9001, 9002}
	reordered.PageIDs = []int64{6001, 6002}
	// JSON object key order is normalized by canonical marshaling.
	reordered.TestData = json.RawMessage(`{"password":"secret","username":"admin"}`)
	reordered.Steps[1].Data = json.RawMessage(`{"username":"admin"}`)

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

func TestComputeCaseContentHashDetectsContentChange(t *testing.T) {
	base, err := ComputeCaseContentHash(validCaseVersion())
	if err != nil {
		t.Fatalf("ComputeCaseContentHash: %v", err)
	}
	changed := validCaseVersion()
	changed.Steps[0].Expected = "显示注册表单"
	changedHash, err := ComputeCaseContentHash(changed)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash changed: %v", err)
	}
	if base == changedHash {
		t.Fatal("content change did not change the hash")
	}
}

func TestComputeCaseContentHashRejectsInvalidTestData(t *testing.T) {
	invalid := validCaseVersion()
	invalid.TestData = json.RawMessage(`[]`)
	if _, err := ComputeCaseContentHash(invalid); err == nil {
		t.Fatal("expected error for non-object test_data")
	}
}
