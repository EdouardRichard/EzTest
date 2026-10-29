// This file is T14-owned: it pins the cross-context contract between the
// supplement agent tools (mindmap/application/agent_tool) and the T9 materializer
// (mindmap/application). The tools write the run.result document; the
// materializer parses that exact document and must project its affected-case
// codeRefs into case.revalidation_marked refs.code. It adds no production code
// and does not modify any T9 file.
package application_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	agenttool "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application/agent_tool"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// agentToolContractGraph is the GraphReader slice the affected-case tool needs:
// it serves the current graph nodes of the fixture version.
type agentToolContractGraph struct {
	fixture *runFixture
}

// Get implements agent_tool.GraphReader.
func (g agentToolContractGraph) Get(_ context.Context, versionID int64) (application.GraphView, error) {
	view := application.GraphView{VersionID: versionID}
	for _, node := range g.fixture.nodes {
		if node.VersionID != versionID {
			continue
		}
		view.Nodes = append(view.Nodes, application.GraphNode{Node: node})
	}
	return view, nil
}

// TestAgentToolCodeRefsReachMaterializedRevalidationEvent runs the real
// supplement.affected_cases_propose tool over the T9 run fixture, then runs the
// real T9 materializer on the stored run.result and asserts the tool-supplied
// codeRefs arrive in the case.revalidation_marked event refs.
func TestAgentToolCodeRefsReachMaterializedRevalidationEvent(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	fixture.version.Status = "continuing"
	fixture.nodes[42] = domain.CaseNode{
		ID: 42, VersionID: runTestVersionID, Kind: domain.NodeKindCase, Title: "Pay order",
		Priority: domain.PriorityP1, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusExecuted,
		SortOrder: 42, Revision: 3, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	run := seedSupplementRun(fixture, application.RunKindIncrementalDesign, `{}`)

	tools := agenttool.NewTools(agenttool.Deps{
		Runs:         runRunRepo{fixture},
		Graphs:       agentToolContractGraph{fixture: fixture},
		Requirements: runRequirementReader{fixture},
	})
	var affected agentdomain.Tool
	for _, tool := range tools {
		if tool.Name() == "supplement.affected_cases_propose" {
			affected = tool
			break
		}
	}
	if affected == nil {
		t.Fatalf("supplement.affected_cases_propose is not registered")
	}

	ctx := agentapp.WithToolInvocation(context.Background(), agentapp.Invocation{
		SessionID:       900,
		RunID:           901,
		VersionID:       run.VersionID,
		BatchID:         77,
		SupplementRunID: run.ID,
	})
	_, err := affected.Execute(ctx, json.RawMessage(`{"cases":[{
		"caseNodeId":"42","confidence":"high","reason":"the refund signature changed","source":"code",
		"requirementIds":["7"],
		"codeRefs":[{"path":"src/refund.ts","line":42,"reason":"changed signature"}]
	}]}`))
	if err != nil {
		t.Fatalf("affected_cases_propose: %v", err)
	}

	stored := fixture.runs[run.ID]
	if !strings.Contains(string(stored.Result), `"codeRefs"`) {
		t.Fatalf("the tool did not persist codeRefs in run.result: %s", stored.Result)
	}

	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize the tool output: %v", err)
	}
	marked := runEventOfType(t, fixture, domain.EventCaseRevalidationMarked)
	if len(marked.RefCodeRefs) != 1 || marked.RefCodeRefs[0] != "src/refund.ts" {
		t.Fatalf("revalidation refs.code = %v, want [src/refund.ts] from the tool codeRefs", marked.RefCodeRefs)
	}
	if len(marked.RefRequirementIDs) != 1 || marked.RefRequirementIDs[0] != 7 {
		t.Fatalf("revalidation refs.requirement = %v, want [7]", marked.RefRequirementIDs)
	}
}
