package designpg

import (
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

func TestDesignStatusIgnoresOldRequirementVersionStale(t *testing.T) {
	requirement := design.RequirementGraph{
		Requirement: design.RequirementRef{ID: 3001, CurrentVersionID: 3002, Status: "clarified"},
		Scenarios: []design.ScenarioGraph{
			{
				Scenario: design.Scenario{ID: 5001, RequirementVersionID: 3001},
				Cases: []design.CaseGraph{{
					Current: design.CaseVersion{Status: design.CaseStatusStale},
				}},
			},
			{
				Scenario: design.Scenario{ID: 5002, RequirementVersionID: 3002},
				Cases: []design.CaseGraph{{
					Current: design.CaseVersion{Status: design.CaseStatusConfirmed},
				}},
			},
		},
	}
	if got := designStatus(requirement, nil); got != design.CaseStatusConfirmed {
		t.Fatalf("design status = %q, want confirmed", got)
	}
}

func TestDesignStatusKeepsCurrentRequirementVersionStale(t *testing.T) {
	requirement := design.RequirementGraph{
		Requirement: design.RequirementRef{ID: 3001, CurrentVersionID: 3002, Status: "clarified"},
		Scenarios: []design.ScenarioGraph{{
			Scenario: design.Scenario{ID: 5001, RequirementVersionID: 3002},
			Cases: []design.CaseGraph{{
				Current: design.CaseVersion{Status: design.CaseStatusStale},
			}},
		}},
	}
	if got := designStatus(requirement, nil); got != design.CaseStatusStale {
		t.Fatalf("design status = %q, want stale", got)
	}
}
