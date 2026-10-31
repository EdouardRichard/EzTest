package tools

import "testing"

func TestP4DeliveryToolNamesAreFrozen(t *testing.T) {
	want := map[string]string{
		"issue.read":               ToolIssueRead,
		"issue.propose":            ToolIssuePropose,
		"report.data_read":         ToolReportDataRead,
		"report.narrative_propose": ToolReportNarrativePropose,
	}
	for spelling, constant := range want {
		if constant != spelling {
			t.Fatalf("tool constant = %q, want %q", constant, spelling)
		}
	}
}

func TestP4DeliveryToolGroupsCaptureFactories(t *testing.T) {
	cases := []struct {
		name  string
		group func(factory func() []Tool) func(Deps) []Tool
		tool  string
	}{
		{"issue", IssueTools, ToolIssueRead},
		{"report", ReportTools, ToolReportDataRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if group := tc.group(nil); group != nil {
				t.Fatal("nil factory produced a non-nil group")
			}
			calls := 0
			group := tc.group(func() []Tool {
				calls++
				return []Tool{&p4RegistryTestTool{name: tc.tool}}
			})
			if calls != 0 {
				t.Fatal("factory was evaluated before the group was composed")
			}
			for i := 0; i < 2; i++ {
				built := group(Deps{})
				if calls != i+1 {
					t.Fatalf("factory calls = %d after group invocation %d, want %d", calls, i+1, i+1)
				}
				if len(built) != 1 || built[0].Name() != tc.tool {
					t.Fatalf("group tools = %v, want [%s]", built, tc.tool)
				}
			}
		})
	}
}
