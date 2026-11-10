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
	base := NewP7Registry(deps)
	extras := []Tool{
		NewPlanUpdateTool(nil),
		NewQuestionAskTool(nil),
		NewClarificationSearchTool(nil),
		NewTaskCompleteTool(nil, nil),
		&toolResultRead{deps: deps},
		&sutDatabaseList{deps: deps},
	}
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
