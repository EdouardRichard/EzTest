package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Catalog bounds and defaults. The session TTL range mirrors the
// systems.default_session_ttl_seconds CHECK constraint.
const (
	defaultSystemSessionTTLSeconds = 3600
	minSystemSessionTTLSeconds     = 60
	maxSystemSessionTTLSeconds     = 604800

	// maxEnvironmentAllowedHosts bounds environments.allowed_hosts (spec §7.2).
	maxEnvironmentAllowedHosts = 50
	// maxAllowedHostLength bounds one allowed host (spec §7.2).
	maxAllowedHostLength = 253

	// maxAppModelImportNodes bounds one JSON import (spec §6.6).
	maxAppModelImportNodes = 5000
	// maxAppModelDepth bounds a node's ancestor chain, the node itself included.
	maxAppModelDepth = 32

	// App model sources mirror the app_model_nodes.source CHECK constraint.
	appModelSourceManual = "manual"
	appModelSourceImport = "import"
)

// CatalogService implements the system, environment and app model use cases. It
// fixes the company to the seeded default one; requests never carry a company
// id.
type CatalogService struct {
	companies domain.CompanyRepository
	systems   domain.SystemRepository
	envs      domain.EnvironmentRepository
	appModels domain.AppModelRepository
	clock     Clock
}

// CreateSystemInput is the create-system request.
type CreateSystemInput struct {
	Key                      string
	Name                     string
	Kind                     string
	BaseURL                  string
	Notes                    string
	DefaultSessionTTLSeconds int
}

// UpdateSystemInput is the partial update-system request. A nil field is left
// unchanged.
type UpdateSystemInput struct {
	Name                     *string
	Kind                     *string
	BaseURL                  *string
	Notes                    *string
	DefaultSessionTTLSeconds *int
	Status                   *string
}

// CreateEnvironmentInput is the create-environment request.
type CreateEnvironmentInput struct {
	Key             string
	Name            string
	BaseURL         string
	Kind            string
	IsProd          bool
	AllowedHosts    []string
	LoginCheck      *domain.LoginCheck
	AllowAutoLogin  bool
	RequireApproval bool
}

// UpdateEnvironmentInput is the partial update-environment request. A nil field
// is left unchanged; ClearLoginCheck removes an existing login_check.
type UpdateEnvironmentInput struct {
	Name            *string
	BaseURL         *string
	Kind            *string
	IsProd          *bool
	AllowedHosts    *[]string
	LoginCheck      *domain.LoginCheck
	ClearLoginCheck bool
	AllowAutoLogin  *bool
	RequireApproval *bool
}

// AppModelInput is one app model node. Key and Kind are only honored on create;
// they are immutable afterwards. On create and import an empty ParentKey means a
// root node; UpdateAppModel also treats an empty ParentKey as "make it a root"
// because the HTTP layer merges omitted fields before calling it.
type AppModelInput struct {
	Key       string
	Kind      string
	Name      string
	Path      string
	Method    string
	ParentKey string
	Meta      json.RawMessage
}

// NewCatalogService wires the catalog use cases to their repositories and the
// injectable clock.
func NewCatalogService(companies domain.CompanyRepository, systems domain.SystemRepository, envs domain.EnvironmentRepository, appModels domain.AppModelRepository, clock Clock) *CatalogService {
	return &CatalogService{
		companies: companies,
		systems:   systems,
		envs:      envs,
		appModels: appModels,
		clock:     clock,
	}
}

// invalidBaseURL returns the documented 422 for a non http(s) base URL.
func invalidBaseURL() *domain.Error {
	return domain.NewInvalid("baseUrl", "invalid_base_url",
		"base_url must be an absolute http or https URL")
}

// validateBaseURL enforces an absolute http or https URL with a host.
func validateBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return invalidBaseURL()
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return invalidBaseURL()
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return invalidBaseURL()
	}
	return nil
}

// validateSystemKind enforces the systems.kind CHECK constraint.
func validateSystemKind(kind string) error {
	if kind != domain.SystemKindWeb && kind != domain.SystemKindAPI {
		return domain.NewInvalid("kind", "invalid_kind", "kind must be web or api")
	}
	return nil
}

// validateSystemStatus enforces the systems.status CHECK constraint.
func validateSystemStatus(status string) error {
	if status != domain.StatusActive && status != domain.StatusDisabled {
		return domain.NewInvalid("status", "invalid_status", "status must be active or disabled")
	}
	return nil
}

// validateDefaultSessionTTL enforces the DDL range; zero means "use the default".
func validateDefaultSessionTTL(ttl int) error {
	if ttl != 0 && (ttl < minSystemSessionTTLSeconds || ttl > maxSystemSessionTTLSeconds) {
		return domain.NewInvalid("defaultSessionTtlSeconds", "invalid_session_ttl",
			"defaultSessionTtlSeconds must be between 60 and 604800")
	}
	return nil
}

// validateEnvironmentKind enforces the environments.kind CHECK constraint.
func validateEnvironmentKind(kind string) error {
	switch kind {
	case domain.EnvironmentKindDev, domain.EnvironmentKindStaging,
		domain.EnvironmentKindProd, domain.EnvironmentKindOther:
		return nil
	default:
		return domain.NewInvalid("kind", "invalid_kind", "kind must be dev, staging, prod or other")
	}
}

// validateAllowedHosts enforces the documented count and length bounds.
func validateAllowedHosts(hosts []string) error {
	if len(hosts) > maxEnvironmentAllowedHosts {
		return domain.NewInvalid("allowedHosts", "invalid_allowed_hosts",
			"allowedHosts accepts at most 50 entries")
	}
	for _, host := range hosts {
		if len(host) > maxAllowedHostLength {
			return domain.NewInvalid("allowedHosts", "invalid_allowed_hosts",
				"allowedHosts entries must be at most 253 bytes")
		}
	}
	return nil
}

// isNotFound reports whether err is a domain not-found error.
func isNotFound(err error) bool {
	var domainErr *domain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound
}

// appModelParentError is the 422 for a parent_key that does not resolve.
func appModelParentError(parentKey string) *domain.Error {
	return domain.NewInvalid("parentKey", "invalid_app_model_parent",
		"parentKey "+parentKey+" does not reference an app model node of this system")
}

// appModelCycleError is the 422 for a parent chain that loops.
func appModelCycleError() *domain.Error {
	return domain.NewInvalid("parentKey", "app_model_cycle", "app model parent chain contains a cycle")
}

// appModelDepthError is the 422 for an ancestor chain beyond maxAppModelDepth.
func appModelDepthError() *domain.Error {
	return domain.NewInvalid("parentKey", "app_model_depth_exceeded",
		"app model parent chain must not exceed 32 nodes")
}

// effectiveAppModelMethod returns the stored method. An API node without an
// explicit method derives it from the immutable "api:<METHOD> <path>" key so the
// app_model_nodes CHECK (kind <> 'api' OR method <> ”) always holds.
func effectiveAppModelMethod(kind, key, method string) string {
	method = strings.TrimSpace(method)
	if method != "" {
		return method
	}
	if kind != domain.AppModelKindAPI {
		return ""
	}
	suffix := strings.TrimPrefix(key, "api:")
	if index := strings.IndexByte(suffix, ' '); index > 0 {
		return suffix[:index]
	}
	return ""
}

// GetSystem returns one system by id.
func (s *CatalogService) GetSystem(ctx context.Context, id int64) (*domain.System, error) {
	return s.systems.GetSystem(ctx, id)
}

// ListSystems returns one page of systems and whether more rows exist.
func (s *CatalogService) ListSystems(ctx context.Context, f domain.SystemFilter) ([]domain.System, bool, error) {
	return s.systems.ListSystems(ctx, f)
}

// CreateSystem validates and stores a system under the seeded default company.
func (s *CatalogService) CreateSystem(ctx context.Context, in CreateSystemInput) (*domain.System, error) {
	if err := domain.ValidateResourceKey(in.Key); err != nil {
		return nil, err
	}
	if err := validateBaseURL(in.BaseURL); err != nil {
		return nil, err
	}
	if err := validateSystemKind(in.Kind); err != nil {
		return nil, err
	}
	if err := validateDefaultSessionTTL(in.DefaultSessionTTLSeconds); err != nil {
		return nil, err
	}

	company, err := s.companies.GetCompanyByKey(ctx, DefaultCompanyKey)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrDefaultCompanyMissing
		}
		return nil, err
	}

	ttl := in.DefaultSessionTTLSeconds
	if ttl == 0 {
		ttl = defaultSystemSessionTTLSeconds
	}
	system := &domain.System{
		CompanyID:                company.ID,
		Key:                      in.Key,
		Name:                     in.Name,
		Kind:                     in.Kind,
		BaseURL:                  strings.TrimSpace(in.BaseURL),
		DefaultSessionTTLSeconds: ttl,
		Notes:                    in.Notes,
		Status:                   domain.StatusActive,
	}
	if err := s.systems.CreateSystem(ctx, system); err != nil {
		return nil, err
	}
	return system, nil
}

// UpdateSystem applies the provided fields to an existing system. Key and
// company are immutable.
func (s *CatalogService) UpdateSystem(ctx context.Context, id int64, in UpdateSystemInput) (*domain.System, error) {
	system, err := s.systems.GetSystem(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		system.Name = *in.Name
	}
	if in.Kind != nil {
		if err := validateSystemKind(*in.Kind); err != nil {
			return nil, err
		}
		system.Kind = *in.Kind
	}
	if in.BaseURL != nil {
		if err := validateBaseURL(*in.BaseURL); err != nil {
			return nil, err
		}
		system.BaseURL = strings.TrimSpace(*in.BaseURL)
	}
	if in.Notes != nil {
		system.Notes = *in.Notes
	}
	if in.DefaultSessionTTLSeconds != nil {
		if *in.DefaultSessionTTLSeconds == 0 {
			return nil, domain.NewInvalid("defaultSessionTtlSeconds", "invalid_session_ttl",
				"defaultSessionTtlSeconds must be between 60 and 604800")
		}
		if err := validateDefaultSessionTTL(*in.DefaultSessionTTLSeconds); err != nil {
			return nil, err
		}
		system.DefaultSessionTTLSeconds = *in.DefaultSessionTTLSeconds
	}
	if in.Status != nil {
		if err := validateSystemStatus(*in.Status); err != nil {
			return nil, err
		}
		system.Status = *in.Status
	}
	if err := s.systems.UpdateSystem(ctx, system); err != nil {
		return nil, err
	}
	return system, nil
}

// DeleteSystem deletes a system unless it still has environments or accesses.
func (s *CatalogService) DeleteSystem(ctx context.Context, id int64) error {
	environments, err := s.systems.CountEnvironments(ctx, id)
	if err != nil {
		return err
	}
	if environments > 0 {
		return domain.NewDependency("has_dependencies", "system still has environments")
	}
	accesses, err := s.systems.CountAccesses(ctx, id)
	if err != nil {
		return err
	}
	if accesses > 0 {
		return domain.NewDependency("has_dependencies", "system still has accesses")
	}
	return s.systems.DeleteSystem(ctx, id)
}

// ListEnvironments returns every environment of a system.
func (s *CatalogService) ListEnvironments(ctx context.Context, systemID int64) ([]domain.Environment, error) {
	return s.envs.ListEnvironments(ctx, systemID)
}

// GetEnvironment returns one environment by id.
func (s *CatalogService) GetEnvironment(ctx context.Context, id int64) (*domain.Environment, error) {
	return s.envs.GetEnvironment(ctx, id)
}

// CreateEnvironment validates and stores an environment of an existing system.
func (s *CatalogService) CreateEnvironment(ctx context.Context, systemID int64, in CreateEnvironmentInput) (*domain.Environment, error) {
	if err := domain.ValidateResourceKey(in.Key); err != nil {
		return nil, err
	}
	if err := validateBaseURL(in.BaseURL); err != nil {
		return nil, err
	}
	if err := validateEnvironmentKind(in.Kind); err != nil {
		return nil, err
	}
	if err := validateAllowedHosts(in.AllowedHosts); err != nil {
		return nil, err
	}
	if err := domain.ValidateLoginCheck(in.LoginCheck); err != nil {
		return nil, err
	}
	if _, err := s.systems.GetSystem(ctx, systemID); err != nil {
		return nil, err
	}

	allowedHosts := make([]string, len(in.AllowedHosts))
	copy(allowedHosts, in.AllowedHosts)
	environment := &domain.Environment{
		SystemID:        systemID,
		Key:             in.Key,
		Name:            in.Name,
		BaseURL:         strings.TrimSpace(in.BaseURL),
		Kind:            in.Kind,
		IsProd:          in.IsProd,
		AllowedHosts:    allowedHosts,
		LoginCheck:      in.LoginCheck,
		AllowAutoLogin:  in.AllowAutoLogin,
		RequireApproval: in.RequireApproval,
	}
	if err := s.envs.CreateEnvironment(ctx, environment); err != nil {
		return nil, err
	}
	return environment, nil
}

// UpdateEnvironment applies the provided fields to an existing environment. Key
// and system are immutable.
func (s *CatalogService) UpdateEnvironment(ctx context.Context, id int64, in UpdateEnvironmentInput) (*domain.Environment, error) {
	environment, err := s.envs.GetEnvironment(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		environment.Name = *in.Name
	}
	if in.BaseURL != nil {
		if err := validateBaseURL(*in.BaseURL); err != nil {
			return nil, err
		}
		environment.BaseURL = strings.TrimSpace(*in.BaseURL)
	}
	if in.Kind != nil {
		if err := validateEnvironmentKind(*in.Kind); err != nil {
			return nil, err
		}
		environment.Kind = *in.Kind
	}
	if in.IsProd != nil {
		environment.IsProd = *in.IsProd
	}
	if in.AllowedHosts != nil {
		if err := validateAllowedHosts(*in.AllowedHosts); err != nil {
			return nil, err
		}
		allowedHosts := make([]string, len(*in.AllowedHosts))
		copy(allowedHosts, *in.AllowedHosts)
		environment.AllowedHosts = allowedHosts
	}
	switch {
	case in.ClearLoginCheck:
		environment.LoginCheck = nil
	case in.LoginCheck != nil:
		if err := domain.ValidateLoginCheck(in.LoginCheck); err != nil {
			return nil, err
		}
		environment.LoginCheck = in.LoginCheck
	}
	if in.AllowAutoLogin != nil {
		environment.AllowAutoLogin = *in.AllowAutoLogin
	}
	if in.RequireApproval != nil {
		environment.RequireApproval = *in.RequireApproval
	}
	if err := s.envs.UpdateEnvironment(ctx, environment); err != nil {
		return nil, err
	}
	return environment, nil
}

// DeleteEnvironment deletes an environment unless it still has sessions.
func (s *CatalogService) DeleteEnvironment(ctx context.Context, id int64) error {
	sessions, err := s.envs.CountSessions(ctx, id)
	if err != nil {
		return err
	}
	if sessions > 0 {
		return domain.NewDependency("has_dependencies", "environment still has sessions")
	}
	return s.envs.DeleteEnvironment(ctx, id)
}

// ListAppModels returns one page of a system's app model nodes.
func (s *CatalogService) ListAppModels(ctx context.Context, f domain.AppModelFilter) ([]domain.AppModelNode, bool, error) {
	return s.appModels.ListAppModels(ctx, f)
}

// GetAppModel returns one app model node by id.
func (s *CatalogService) GetAppModel(ctx context.Context, id int64) (*domain.AppModelNode, error) {
	return s.appModels.GetAppModel(ctx, id)
}

// appModelByKey returns the system's node with key, or nil when it does not
// exist.
func (s *CatalogService) appModelByKey(ctx context.Context, systemID int64, key string) (*domain.AppModelNode, error) {
	nodes, err := s.appModels.ListAppModelsByKeys(ctx, systemID, []string{key})
	if err != nil {
		return nil, err
	}
	node, ok := nodes[key]
	if !ok {
		return nil, nil
	}
	return &node, nil
}

// parentChainDepth returns how many nodes the chain starting at nodeID contains
// (the node itself is 1). It rejects chains deeper than maxAppModelDepth and
// loops, and reports a cycle when forbiddenID is reached.
func (s *CatalogService) parentChainDepth(ctx context.Context, nodeID, forbiddenID int64) (int, error) {
	depth := 0
	current := nodeID
	seen := make(map[int64]struct{}, maxAppModelDepth)
	for current != 0 {
		if _, ok := seen[current]; ok {
			return 0, appModelCycleError()
		}
		if current == forbiddenID {
			return 0, appModelCycleError()
		}
		seen[current] = struct{}{}
		node, err := s.appModels.GetAppModel(ctx, current)
		if err != nil {
			return 0, err
		}
		depth++
		if depth > maxAppModelDepth {
			return 0, appModelDepthError()
		}
		if node.ParentID == nil {
			break
		}
		current = *node.ParentID
	}
	return depth, nil
}

// resolveAppModelParent validates parentKey for a node of systemID and returns
// the parent. forbiddenID detects moves that would create a cycle; pass 0 on
// create. A nil parent means the node is a root.
func (s *CatalogService) resolveAppModelParent(ctx context.Context, systemID int64, parentKey string, forbiddenID int64) (*domain.AppModelNode, error) {
	if parentKey == "" {
		return nil, nil
	}
	parent, err := s.appModelByKey(ctx, systemID, parentKey)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, appModelParentError(parentKey)
	}
	depth, err := s.parentChainDepth(ctx, parent.ID, forbiddenID)
	if err != nil {
		return nil, err
	}
	if depth+1 > maxAppModelDepth {
		return nil, appModelDepthError()
	}
	return parent, nil
}

// CreateAppModel validates and stores one app model node.
func (s *CatalogService) CreateAppModel(ctx context.Context, systemID int64, in AppModelInput) (*domain.AppModelNode, error) {
	if err := domain.ValidateAppModelKey(in.Kind, in.Key); err != nil {
		return nil, err
	}
	if in.ParentKey == in.Key {
		return nil, appModelCycleError()
	}
	if _, err := s.systems.GetSystem(ctx, systemID); err != nil {
		return nil, err
	}
	parent, err := s.resolveAppModelParent(ctx, systemID, in.ParentKey, 0)
	if err != nil {
		return nil, err
	}
	node := &domain.AppModelNode{
		SystemID: systemID,
		Kind:     in.Kind,
		Key:      in.Key,
		Name:     in.Name,
		Path:     in.Path,
		Method:   effectiveAppModelMethod(in.Kind, in.Key, in.Method),
		Source:   appModelSourceManual,
		Meta:     in.Meta,
	}
	if parent != nil {
		node.ParentID = &parent.ID
	}
	if err := s.appModels.CreateAppModel(ctx, node); err != nil {
		return nil, err
	}
	return node, nil
}

// parentKeyForNode returns the key of node's parent, or "" for a root.
func (s *CatalogService) parentKeyForNode(ctx context.Context, node *domain.AppModelNode) (string, error) {
	if node.ParentID == nil {
		return "", nil
	}
	parent, err := s.appModels.GetAppModel(ctx, *node.ParentID)
	if err != nil {
		return "", err
	}
	return parent.Key, nil
}

// importDepth resolves the resulting depth of key against the payload and the
// stored nodes. inputs is indexed by key; existing holds the stored nodes loaded
// for the payload keys and is extended lazily for missing ancestors.
func (s *CatalogService) importDepth(ctx context.Context, systemID int64, key string, inputs map[string]AppModelInput, existing map[string]domain.AppModelNode, cache map[string]int, visiting map[string]struct{}) (int, error) {
	if depth, ok := cache[key]; ok {
		return depth, nil
	}
	if _, ok := visiting[key]; ok {
		return 0, appModelCycleError()
	}
	visiting[key] = struct{}{}
	defer delete(visiting, key)

	var parentKey string
	if in, ok := inputs[key]; ok {
		parentKey = in.ParentKey
	} else if node, ok := existing[key]; ok {
		resolved, err := s.parentKeyForNode(ctx, &node)
		if err != nil {
			return 0, err
		}
		parentKey = resolved
	} else {
		return 0, appModelParentError(key)
	}

	depth := 1
	if parentKey != "" {
		if _, ok := inputs[parentKey]; !ok {
			if _, ok := existing[parentKey]; !ok {
				loaded, err := s.appModelByKey(ctx, systemID, parentKey)
				if err != nil {
					return 0, err
				}
				if loaded == nil {
					return 0, appModelParentError(parentKey)
				}
				existing[parentKey] = *loaded
			}
		}
		parentDepth, err := s.importDepth(ctx, systemID, parentKey, inputs, existing, cache, visiting)
		if err != nil {
			return 0, err
		}
		depth = parentDepth + 1
		if depth > maxAppModelDepth {
			return 0, appModelDepthError()
		}
	}
	cache[key] = depth
	return depth, nil
}

// ImportAppModels upserts the payload nodes by (system_id, key). Duplicate keys
// in the payload are collapsed (last value wins), missing nodes are never
// deleted, and both created and updated nodes get source='import'.
func (s *CatalogService) ImportAppModels(ctx context.Context, systemID int64, inputs []AppModelInput) (int, int, error) {
	if len(inputs) > maxAppModelImportNodes {
		return 0, 0, domain.NewInvalid("nodes", "too_many_nodes",
			"an app model import accepts at most 5000 nodes")
	}
	if _, err := s.systems.GetSystem(ctx, systemID); err != nil {
		return 0, 0, err
	}

	byKey := make(map[string]AppModelInput, len(inputs))
	orderIndex := make(map[string]int, len(inputs))
	keys := make([]string, 0, len(inputs))
	for index, in := range inputs {
		if err := domain.ValidateAppModelKey(in.Kind, in.Key); err != nil {
			return 0, 0, err
		}
		if _, seen := byKey[in.Key]; !seen {
			orderIndex[in.Key] = index
			keys = append(keys, in.Key)
		}
		byKey[in.Key] = in
	}

	existing, err := s.appModels.ListAppModelsByKeys(ctx, systemID, keys)
	if err != nil {
		return 0, 0, err
	}
	if existing == nil {
		existing = map[string]domain.AppModelNode{}
	}

	// Validate the whole resulting graph before writing anything: no cycle,
	// every parent resolves and no chain exceeds the depth limit.
	depths := make(map[string]int, len(keys))
	for _, key := range keys {
		if _, err := s.importDepth(ctx, systemID, key, byKey, existing, depths, map[string]struct{}{}); err != nil {
			return 0, 0, err
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if depths[keys[i]] != depths[keys[j]] {
			return depths[keys[i]] < depths[keys[j]]
		}
		return orderIndex[keys[i]] < orderIndex[keys[j]]
	})

	ids := make(map[string]int64, len(existing)+len(keys))
	for key, node := range existing {
		ids[key] = node.ID
	}

	var created, updated int
	for _, key := range keys {
		in := byKey[key]
		var parentID *int64
		if in.ParentKey != "" {
			value, ok := ids[in.ParentKey]
			if !ok {
				return created, updated, appModelParentError(in.ParentKey)
			}
			parentID = &value
		}
		if node, ok := existing[key]; ok {
			node.ParentID = parentID
			node.Name = in.Name
			node.Path = in.Path
			node.Method = effectiveAppModelMethod(node.Kind, node.Key, in.Method)
			node.Source = appModelSourceImport
			if len(in.Meta) > 0 {
				node.Meta = in.Meta
			}
			if err := s.appModels.UpdateAppModel(ctx, &node); err != nil {
				return created, updated, err
			}
			ids[key] = node.ID
			updated++
			continue
		}
		node := &domain.AppModelNode{
			SystemID: systemID,
			ParentID: parentID,
			Kind:     in.Kind,
			Key:      in.Key,
			Name:     in.Name,
			Path:     in.Path,
			Method:   effectiveAppModelMethod(in.Kind, in.Key, in.Method),
			Source:   appModelSourceImport,
			Meta:     in.Meta,
		}
		if err := s.appModels.CreateAppModel(ctx, node); err != nil {
			return created, updated, err
		}
		ids[key] = node.ID
		created++
	}
	return created, updated, nil
}

// UpdateAppModel updates the mutable fields of a node. Key, kind and system are
// immutable. An empty Meta keeps the stored document; an empty ParentKey makes
// the node a root.
func (s *CatalogService) UpdateAppModel(ctx context.Context, id int64, in AppModelInput) (*domain.AppModelNode, error) {
	node, err := s.appModels.GetAppModel(ctx, id)
	if err != nil {
		return nil, err
	}
	parent, err := s.resolveAppModelParent(ctx, node.SystemID, in.ParentKey, node.ID)
	if err != nil {
		return nil, err
	}
	node.Name = in.Name
	node.Path = in.Path
	node.Method = effectiveAppModelMethod(node.Kind, node.Key, in.Method)
	if len(in.Meta) > 0 {
		node.Meta = in.Meta
	}
	if parent == nil {
		node.ParentID = nil
	} else {
		node.ParentID = &parent.ID
	}
	if err := s.appModels.UpdateAppModel(ctx, node); err != nil {
		return nil, err
	}
	return node, nil
}

// DeleteAppModel deletes a node; the database cascades to its subtree.
func (s *CatalogService) DeleteAppModel(ctx context.Context, id int64) error {
	return s.appModels.DeleteAppModel(ctx, id)
}
