package design

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
)

// csvHeader is the frozen P2 export column order (spec §8).
var csvHeader = []string{
	"requirement_id", "requirement_title", "requirement_revision", "requirement_status",
	"scenario_id", "scenario_name", "case_id", "case_title", "case_revision", "case_status",
	"priority", "sut_names", "page_names", "api_targets", "step_seq", "step_action",
	"step_expected", "step_target_sut", "step_page", "step_api", "account_role", "assertions",
}

// RenderCSV renders one row per step, RFC4180 quoted and UTF-8 without BOM.
// Cases without steps emit one row with empty step columns.
func RenderCSV(data ExportData) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(csvHeader); err != nil {
		return nil, fmt.Errorf("design: render CSV header: %w", err)
	}
	for _, requirement := range data.Requirements {
		if len(requirement.Scenarios) == 0 {
			row := csvRequirementBase(requirement)
			row = append(row, "-", "未设计")
			row = append(row, make([]string, 16)...)
			if err := writer.Write(row); err != nil {
				return nil, fmt.Errorf("design: render CSV: %w", err)
			}
			continue
		}
		for _, scenario := range requirement.Scenarios {
			if len(scenario.Cases) == 0 {
				row := csvRequirementBase(requirement)
				row = append(row, formatInt64(scenario.ID), scenario.Name)
				row = append(row, make([]string, 16)...)
				if err := writer.Write(row); err != nil {
					return nil, fmt.Errorf("design: render CSV: %w", err)
				}
				continue
			}
			for _, item := range scenario.Cases {
				base := csvRequirementBase(requirement)
				caseBase := []string{
					formatInt64(scenario.ID), scenario.Name,
					formatInt64(item.CaseID), item.Title, formatInt64(int64(item.Revision)),
					item.Status, item.Priority, formatIDs(item.VersionSUTIDs),
					formatIDs(item.PageIDs), formatIDs(item.APIIDs),
				}
				assertions := csvAssertions(item.Assertions)
				if len(item.Steps) == 0 {
					row := append(append(base, caseBase...), "", "", "", "", "", "", "", assertions)
					if err := writer.Write(row); err != nil {
						return nil, fmt.Errorf("design: render CSV: %w", err)
					}
					continue
				}
				for _, step := range item.Steps {
					row := append(append(base, caseBase...),
						formatInt64(int64(step.Seq)), step.Action, step.Expected,
						formatOptionalID(step.TargetVersionSUTID),
						formatOptionalID(step.TargetPageID),
						formatOptionalID(step.TargetAPIID),
						step.AccountRole, assertions)
					if err := writer.Write(row); err != nil {
						return nil, fmt.Errorf("design: render CSV: %w", err)
					}
				}
			}
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("design: render CSV: %w", err)
	}
	return buffer.Bytes(), nil
}

func csvRequirementBase(requirement ExportRequirement) []string {
	return []string{
		formatInt64(requirement.ID), requirement.Title,
		formatInt64(int64(requirement.Revision)), requirement.Status,
	}
}

func csvAssertions(assertions []CaseAssertion) string {
	parts := make([]string, 0, len(assertions))
	for _, assertion := range assertions {
		parts = append(parts, assertion.Kind+":"+assertion.Expected)
	}
	return strings.Join(parts, " | ")
}
