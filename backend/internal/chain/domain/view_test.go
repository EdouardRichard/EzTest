package chain

import "testing"

func TestClampBudget(t *testing.T) {
	tests := []struct {
		name string
		in   Budget
		want Budget
	}{
		{"zero falls back to defaults", Budget{}, Budget{MaxNodes: 300, MaxEdges: 600, MaxRequirements: 100}},
		{"negative falls back to defaults", Budget{MaxNodes: -1, MaxEdges: -5, MaxRequirements: -9}, Budget{MaxNodes: 300, MaxEdges: 600, MaxRequirements: 100}},
		{"above maximum falls back to defaults", Budget{MaxNodes: 1001, MaxEdges: 2001, MaxRequirements: 501}, Budget{MaxNodes: 300, MaxEdges: 600, MaxRequirements: 100}},
		{"minimum bounds are kept", Budget{MaxNodes: 1, MaxEdges: 1, MaxRequirements: 1}, Budget{MaxNodes: 1, MaxEdges: 1, MaxRequirements: 1}},
		{"maximum bounds are kept", Budget{MaxNodes: 1000, MaxEdges: 2000, MaxRequirements: 500}, Budget{MaxNodes: 1000, MaxEdges: 2000, MaxRequirements: 500}},
		{"fields are clamped independently", Budget{MaxNodes: 0, MaxEdges: 50, MaxRequirements: 501}, Budget{MaxNodes: 300, MaxEdges: 50, MaxRequirements: 100}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampBudget(tc.in); got != tc.want {
				t.Fatalf("ClampBudget(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func int64Ptr(value int64) *int64 { return &value }

func TestChainFilterNormalize(t *testing.T) {
	t.Run("empty filter is unchanged", func(t *testing.T) {
		if got := (ChainFilter{}).Normalize(); got != (ChainFilter{}) {
			t.Fatalf("Normalize(ChainFilter{}) = %+v, want the zero value", got)
		}
	})

	t.Run("drops non-positive IDs", func(t *testing.T) {
		got := ChainFilter{
			VersionSUTID: int64Ptr(0),
			PageID:       int64Ptr(-1),
			WaveID:       int64Ptr(77),
		}.Normalize()
		if got.VersionSUTID != nil {
			t.Errorf("VersionSUTID = %d, want nil", *got.VersionSUTID)
		}
		if got.PageID != nil {
			t.Errorf("PageID = %d, want nil", *got.PageID)
		}
		if got.WaveID == nil || *got.WaveID != 77 {
			t.Errorf("WaveID = %v, want 77", got.WaveID)
		}
	})

	t.Run("trims and lowercases enum filters", func(t *testing.T) {
		got := ChainFilter{
			Status:    "  Failed ",
			Result:    " PASS ",
			FocusNode: " case_run:12 ",
		}.Normalize()
		if got.Status != "failed" {
			t.Errorf("Status = %q, want %q", got.Status, "failed")
		}
		if got.Result != "pass" {
			t.Errorf("Result = %q, want %q", got.Result, "pass")
		}
		if got.FocusNode != "case_run:12" {
			t.Errorf("FocusNode = %q, want %q", got.FocusNode, "case_run:12")
		}
	})

	t.Run("orders the event interval", func(t *testing.T) {
		got := ChainFilter{
			EventFrom: int64Ptr(120),
			EventTo:   int64Ptr(50),
		}.Normalize()
		if got.EventFrom == nil || *got.EventFrom != 50 {
			t.Fatalf("EventFrom = %v, want 50", got.EventFrom)
		}
		if got.EventTo == nil || *got.EventTo != 120 {
			t.Fatalf("EventTo = %v, want 120", got.EventTo)
		}
	})

	t.Run("drops non-positive event bounds", func(t *testing.T) {
		got := ChainFilter{
			EventFrom: int64Ptr(0),
			EventTo:   int64Ptr(-3),
		}.Normalize()
		if got.EventFrom != nil || got.EventTo != nil {
			t.Fatalf("event bounds = %v..%v, want nil..nil", got.EventFrom, got.EventTo)
		}
	})

	t.Run("does not mutate the receiver", func(t *testing.T) {
		in := ChainFilter{
			VersionSUTID: int64Ptr(5),
			Status:       " Open ",
			EventFrom:    int64Ptr(9),
			EventTo:      int64Ptr(4),
		}
		_ = in.Normalize()
		if *in.VersionSUTID != 5 || in.Status != " Open " {
			t.Fatalf("Normalize mutated the receiver: %+v", in)
		}
		if *in.EventFrom != 9 || *in.EventTo != 4 {
			t.Fatalf("Normalize reordered the receiver event bounds: %v..%v", *in.EventFrom, *in.EventTo)
		}
	})
}

func TestChainFilterLimitNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero means unset", 0, 0},
		{"negative is invalid", -3, 0},
		{"one is kept", 1, 1},
		{"default node budget is kept", 300, 300},
		{"maximum is kept", 1000, 1000},
		{"above maximum is invalid", 1001, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (ChainFilter{Limit: tc.in}).Normalize().Limit; got != tc.want {
				t.Fatalf("Normalize(Limit: %d).Limit = %d, want %d", tc.in, got, tc.want)
			}
		})
	}

	t.Run("does not mutate the receiver limit", func(t *testing.T) {
		in := ChainFilter{Limit: 50}
		_ = in.Normalize()
		if in.Limit != 50 {
			t.Fatalf("Normalize mutated Limit to %d", in.Limit)
		}
	})
}

func TestNodeVersionSUTIDs(t *testing.T) {
	primary := int64(11)
	labels := []int64{11, 22}
	node := Node{VersionSUTID: &primary, VersionSUTIDs: labels}
	if node.VersionSUTID == nil || *node.VersionSUTID != 11 {
		t.Fatalf("VersionSUTID = %v, want 11", node.VersionSUTID)
	}
	if len(node.VersionSUTIDs) != 2 || node.VersionSUTIDs[0] != 11 || node.VersionSUTIDs[1] != 22 {
		t.Fatalf("VersionSUTIDs = %v, want [11 22]", node.VersionSUTIDs)
	}

	empty := Node{}
	if empty.VersionSUTID != nil {
		t.Fatalf("zero Node VersionSUTID = %v, want nil", empty.VersionSUTID)
	}
	if empty.VersionSUTIDs != nil {
		t.Fatalf("zero Node VersionSUTIDs = %v, want nil", empty.VersionSUTIDs)
	}
}

func TestNodeID(t *testing.T) {
	if got := NodeID(NodeRequirement, 42); got != "requirement:42" {
		t.Errorf("NodeID(requirement, 42) = %q, want %q", got, "requirement:42")
	}
	if got := NodeID(NodeClosure, 7); got != "closure:7" {
		t.Errorf("NodeID(closure, 7) = %q, want %q", got, "closure:7")
	}
}

func TestKindWireValues(t *testing.T) {
	nodes := map[NodeKind]string{
		NodeRequirement: "requirement",
		NodeScenario:    "scenario",
		NodeCaseVersion: "case_version",
		NodeWave:        "wave",
		NodeCaseRun:     "case_run",
		NodeIssue:       "issue",
		NodeReport:      "report",
		NodeClosure:     "closure",
	}
	for kind, wire := range nodes {
		if string(kind) != wire {
			t.Errorf("node kind wire value = %q, want %q", kind, wire)
		}
	}

	edges := map[EdgeKind]string{
		EdgeHasScenario: "has_scenario",
		EdgeHasCase:     "has_case",
		EdgeExecutedBy:  "executed_by",
		EdgeContains:    "contains",
		EdgeRaised:      "raised",
		EdgeReports:     "reports",
		EdgeClosedBy:    "closed_by",
		EdgeIncludedIn:  "included_in",
		EdgeMergedInto:  "merged_into",
	}
	for kind, wire := range edges {
		if string(kind) != wire {
			t.Errorf("edge kind wire value = %q, want %q", kind, wire)
		}
	}
}
