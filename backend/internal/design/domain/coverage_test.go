package design

import (
	"reflect"
	"testing"
)

func TestCoverageUnionAccumulatesAcrossBatches(t *testing.T) {
	confirmed := [][]string{{"AC-1", "AC-2"}}
	drafts := [][]string{{"AC-3"}, {"AC-4", "AC-5"}}
	union := CoverageUnion(confirmed, drafts)
	req := []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}
	if !CoverageComplete(union, req) {
		t.Fatalf("union %v incomplete", union)
	}
	// 第一批单独不足（模拟提案时仅 linkage，不全集门）
	if CoverageComplete(CoverageUnion(nil, [][]string{{"AC-1"}}), req) {
		t.Fatal("partial batch must be incomplete")
	}
	missing := MissingACs(CoverageUnion(confirmed, nil), req)
	if len(missing) != 3 {
		t.Fatalf("missing = %v, want 3", missing)
	}
}

func TestCoverageUnionSortedDedupNormalized(t *testing.T) {
	confirmed := [][]string{{" AC-2 ", "AC-1", "AC-2", ""}}
	drafts := [][]string{{"AC-1"}, {"AC-3", " AC-3"}}
	union := CoverageUnion(confirmed, drafts)
	want := []string{"AC-1", "AC-2", "AC-3"}
	if !reflect.DeepEqual(union, want) {
		t.Fatalf("union = %v, want %v", union, want)
	}
	// MissingACs 按规范化 id 序输出差集，重复/空白 requirement id 不产生重复缺口。
	missing := MissingACs([]string{"AC-2"}, []string{"AC-3", "AC-1", "AC-2", " AC-1 ", ""})
	wantMissing := []string{"AC-1", "AC-3"}
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Fatalf("missing = %v, want %v", missing, wantMissing)
	}
	if !CoverageComplete(nil, nil) {
		t.Fatal("empty requirement AC set must be complete")
	}
	if CoverageComplete(nil, []string{"AC-1"}) {
		t.Fatal("empty union against non-empty requirement must be incomplete")
	}
}
