package tools

// AdminCatalogRegistry is the metadata-only tool catalog of the admin profile
// editor (spec §3.3, research G-01). The strict per-phase registries only
// register a v0.22 Host tool when its production port is wired for that run, so
// an administrator could not add `question.ask` (or the other additive v0.22
// tools) to a legacy profile: validation rejected the name as unknown.
//
// This catalog is not a runtime registry. Every tool here is constructed with
// nil dependencies, so Execute always fails closed (missing dependency or an
// invalid argument); only Name/Description/Parameters are used. The backfill
// and this catalog must ship in the same release: otherwise a backfilled
// `question.ask` would still be rejected by the editor.
func AdminCatalogRegistry() *Registry {
	deps := Deps{}
	base := NewKnowledgeRegistry(deps)
	extras := []Tool{
		NewPlanUpdateTool(nil),
		NewQuestionAskTool(nil),
		NewClarificationSearchTool(nil),
		NewTaskCompleteTool(nil, nil),
		&toolResultRead{deps: deps},
		&sutDatabaseList{deps: deps},
		// sut.read (v0.2.4-F T9): the runtime P4 group registers it only when the
		// resource port implements SUTReader, so the catalog adds it explicitly
		// with nil deps (metadata only; Execute fails closed) — the admin profile
		// editor can select it (capability-list completeness gate).
		&sutRead{deps: deps},
	}
	// General-execution capability domains (v0.2.4-C T11): the runtime group
	// only registers them when the execution port implements the general
	// surface, so the catalog adds them explicitly with nil deps (metadata
	// only; Execute fails closed) — the admin profile editor can select them.
	extras = append(extras, executionDomainTools(deps)...)
	// Browser capability domain (v0.2.4-C T15): browser.cli passthrough, same
	// additive registration condition as the general-execution domains.
	extras = append(extras, browserDomainTools(deps)...)
	// Skill capability domain (v0.2.4-C T19): skill.read on-demand body reader,
	// same additive registration condition (runtime group needs both ports).
	extras = append(extras, skillDomainTools(deps)...)
	// P17 asset registration (v0.2.4-F T10): sut.asset_register, same additive
	// registration condition (runtime group needs the registration port wired).
	extras = append(extras, sutAssetRegisterDomainTools(deps)...)
	// R-B4 new-SUT registration (v0.2.4-F T11): sut.register, same additive
	// registration condition (runtime group needs the new-SUT registration port).
	extras = append(extras, sutRegisterDomainTools(deps)...)
	// P18 session fixture domain (v0.2.4-F T13): fixture.upload/fixture.download,
	// same additive registration condition (runtime group needs the fixture
	// library port wired).
	extras = append(extras, fixtureDomainTools(deps)...)
	all := make([]Tool, 0, len(base.byName)+len(extras))
	all = append(all, extras...)
	for _, name := range base.Names() {
		tool, ok := base.Get(name)
		if !ok {
			continue
		}
		all = append(all, tool)
	}
	return NewRegistry(all...)
}
