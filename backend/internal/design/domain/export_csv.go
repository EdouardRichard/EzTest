package design

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
)

// csvHeader is the export column order. v0.2.4-B (§3.5 "design export"): the
// sut_names/page_names/api_targets and step_target_sut/step_page/step_api
// columns are replaced by the natural-language contract -- one sut_description
// and one covered_acs column per case, one target_description per step -- so the
// header is 19 columns wide instead of 22.
var csvHeader = []string{
	"requirement_id", "requirement_title", "requirement_revision", "requirement_status",
	"scenario_id", "scenario_name", "case_id", "case_title", "case_revision", "case_status",
	"priority", "sut_description", "covered_acs", "step_seq", "step_action",
	"step_expected", "step_target_description", "account_role", "assertions",
}

// RenderCSV renders one row per step, RFC4180 quoted and UTF-8 without BOM.
// Cases without steps emit one row with empty step columns.
func RenderCSV(data ExportData) ([]byte, error) {
	// An "undesigned" filler row is the 4-column requirement base plus a
	// 2-column scenario placeholder, padded to the full header width.
	fillerColumns := len(csvHeader) - 6
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(csvHeader); err != nil {
		return nil, fmt.Errorf("design: render CSV header: %w", err)
	}
	for _, requirement := range data.Requirements {
		if len(requirement.Scenarios) == 0 {
			row := csvRequirementBase(requirement)
			row = append(row, "-", "未设计")
			row = append(row, make([]string, fillerColumns)...)
			if err := writer.Write(row); err != nil {
				return nil, fmt.Errorf("design: render CSV: %w", err)
			}
			continue
		}
		for _, scenario := range requirement.Scenarios {
			if len(scenario.Cases) == 0 {
				row := csvRequirementBase(requirement)
				row = append(row, formatInt64(scenario.ID), scenario.Name)
				row = append(row, make([]string, fillerColumns)...)
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
					item.Status, item.Priority, item.SutDescription,
					formatACList(item.CoveredACs),
				}
				assertions := csvAssertions(item.Assertions)
				if len(item.Steps) == 0 {
					row := append(append(base, caseBase...), "", "", "", "", "", assertions)
					if err := writer.Write(row); err != nil {
						return nil, fmt.Errorf("design: render CSV: %w", err)
					}
					continue
				}
				for _, step := range item.Steps {
					row := append(append(base, caseBase...),
						formatInt64(int64(step.Seq)), step.Action, step.Expected,
						step.TargetDescription,
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
