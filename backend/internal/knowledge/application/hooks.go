package knowledgeapp

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// The 0.2.1 knowledge kinds (spec §10.2): the three low-risk kinds a success
// hook may sediment. Data-construction and defect patterns are deferred to 0.3.
// The database has no kind column (frozen P7 contract), so the kind is carried
// in the item tags as "kind:<kind>"; the structured content repeats it.
type Kind string

// The three low-risk knowledge kinds written by success hooks.
const (
	KindLoginRecipe Kind = "login_recipe"
	KindPageLocator Kind = "page_locator"
	KindAPIAuth     Kind = "api_auth"
)

// kindTagPrefix marks the knowledge kind inside Item.Tags.
const kindTagPrefix = "kind:"

// Auto-approval audit action and event type (contracts/events.md §18.1/§18.3).
const (
	// ActionAutoApproved is the audit action written when a low-risk kind is
	// approved by a success hook.
	ActionAutoApproved = "knowledge.auto_approved"
	// EventAutoApproved is the system-channel event of the same transition.
	EventAutoApproved = "knowledge.auto_approved"
)

// Valid reports whether the value is one of the three 0.2.1 knowledge kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindLoginRecipe, KindPageLocator, KindAPIAuth:
		return true
	default:
		return false
	}
}

// LowRisk reports whether the kind may be auto-approved without human review
// (spec §10.2: 低风险类型可自动批准).
func (k Kind) LowRisk() bool { return k.Valid() }

// tag is the canonical Item.Tags marker of the kind.
func (k Kind) tag() string { return kindTagPrefix + string(k) }

// SuccessFact is one sanitized success observation handed to a hook. It carries
// only structured identifiers and the "how to" recipe: credentials never enter
// a fact (spec §10.2), and a fact carrying a secret field is rejected before an
// item is created. Bindings are copied to the item's structured bindings;
// Recipe is stored as structured content.
type SuccessFact struct {
	// VersionID is the source version of the observation (provenance).
	VersionID int64
	// SystemKey selects scope=system when set; empty keeps the item global.
	SystemKey string
	// Structured SUT bindings (spec §10.2); no credential is stored here.
	SystemID      *int64
	EnvironmentID *int64
	PageKey       string
	APIKey        string
	AccountRole   string
	// Title is optional; hooks derive a deterministic title when it is empty.
	Title string
	// Recipe is the structured "how to" data (steps, selectors, field names).
	// It is validated against secret fields before it is stored.
	Recipe map[string]any
	// EvidenceIDs are logical references to execution evidence (no bytes).
	EvidenceIDs []int64
	// SourceFactID is the optional version_fact that produced the observation.
	SourceFactID *int64
}

// OnLoginSuccess creates a draft login_recipe knowledge item bound to the
// account role and page of a successful login. The item starts in draft so the
// review gate (knowledge.review_required) or AutoApprove owns the promotion.
func (s *Service) OnLoginSuccess(ctx context.Context, fact SuccessFact) (knowledge.Item, error) {
	return s.createSuccessDraft(ctx, KindLoginRecipe, fact)
}

// OnPageLocated creates a draft page_locator knowledge item bound to the located
// page.
func (s *Service) OnPageLocated(ctx context.Context, fact SuccessFact) (knowledge.Item, error) {
	return s.createSuccessDraft(ctx, KindPageLocator, fact)
}

// OnAPIAuthSuccess creates a draft api_auth knowledge item bound to the API
// endpoint and account role of a successful authenticated call.
func (s *Service) OnAPIAuthSuccess(ctx context.Context, fact SuccessFact) (knowledge.Item, error) {
	return s.createSuccessDraft(ctx, KindAPIAuth, fact)
}

// AutoApprove promotes one draft to approved when (and only when) its kind is
// one of the three low-risk kinds and the item carries no credential or secret
// field. A successful promotion writes the knowledge.auto_approved audit entry
// and publishes the same system event; both are best effort and never fail the
// caller. AutoApprove never approves an already reviewed/verified item.
func (s *Service) AutoApprove(ctx context.Context, item knowledge.Item) bool {
	kind, ok := KindOf(item)
	if !ok || !kind.LowRisk() {
		return false
	}
	if item.Status != knowledge.StatusDraft || itemHasSecrets(item) {
		return false
	}
	approved, err := s.UpdateStatus(ctx, item.ID, knowledge.StatusApproved, "")
	if err != nil {
		return false
	}

	metadata := map[string]any{
		"knowledge_id": platformid.Format(approved.ID),
		"kind":         string(kind),
		"scope":        string(approved.Scope),
	}
	if approved.SourceVersionID != nil {
		metadata["source_version_id"] = platformid.Format(*approved.SourceVersionID)
	}
	if sourceFactID := sourceFactIDFromContent(approved.Content); sourceFactID != "" {
		metadata["source_fact_id"] = sourceFactID
	}
	s.recordAudit(ctx, 0, ActionAutoApproved, approved.ID, metadata)

	payload := map[string]any{
		"knowledge_id": platformid.Format(approved.ID),
		"kind":         string(kind),
		"scope":        string(approved.Scope),
	}
	if approved.SourceVersionID != nil {
		payload["source_version_id"] = platformid.Format(*approved.SourceVersionID)
	}
	s.publish(ctx, platform.Actor{}, EventAutoApproved, payload)
	return true
}

// KindOf returns the knowledge kind carried by an item, reading the "kind:"
// tag first and falling back to the structured content kind (items proposed
// through the HTTP API keep their tags editable).
func KindOf(item knowledge.Item) (Kind, bool) {
	for _, tag := range item.Tags {
		if !strings.HasPrefix(tag, kindTagPrefix) {
			continue
		}
		kind := Kind(strings.TrimSpace(strings.TrimPrefix(tag, kindTagPrefix)))
		if kind.Valid() {
			return kind, true
		}
	}
	var payload struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(item.Content), &payload); err == nil {
		kind := Kind(strings.TrimSpace(payload.Kind))
		if kind.Valid() {
			return kind, true
		}
	}
	return "", false
}

/* -------------------------------- creation -------------------------------- */

func (s *Service) createSuccessDraft(ctx context.Context, kind Kind, fact SuccessFact) (knowledge.Item, error) {
	if err := validateSuccessFact(kind, fact); err != nil {
		return knowledge.Item{}, err
	}
	bindings, err := knowledge.Bindings{
		SystemID:      fact.SystemID,
		EnvironmentID: fact.EnvironmentID,
		PageKey:       optionalText(fact.PageKey),
		APIKey:        optionalText(fact.APIKey),
		AccountRole:   optionalText(fact.AccountRole),
	}.Normalize()
	if err != nil {
		return knowledge.Item{}, err
	}
	systemKey := optionalText(fact.SystemKey)
	content, err := successContent(kind, fact, bindings)
	if err != nil {
		return knowledge.Item{}, platform.Invalid("fact", "recipe must be JSON-encodable")
	}
	item, err := s.Create(ctx, platform.Actor{}, CreateInput{
		Title:           successTitle(kind, fact),
		Content:         content,
		SystemKey:       systemKey,
		Tags:            []string{kind.tag()},
		Bindings:        bindings,
		SourceVersionID: int64Ptr(fact.VersionID),
	})
	if err != nil {
		return knowledge.Item{}, err
	}
	return item, nil
}

func validateSuccessFact(kind Kind, fact SuccessFact) error {
	if !kind.Valid() {
		return platform.Invalid("kind", "must be login_recipe, page_locator or api_auth")
	}
	if fact.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	switch kind {
	case KindLoginRecipe:
		if strings.TrimSpace(fact.AccountRole) == "" {
			return platform.Invalid("account_role", "is required for a login recipe")
		}
	case KindPageLocator:
		if strings.TrimSpace(fact.PageKey) == "" {
			return platform.Invalid("page_key", "is required for a page locator")
		}
	case KindAPIAuth:
		if strings.TrimSpace(fact.APIKey) == "" {
			return platform.Invalid("api_key", "is required for an api auth recipe")
		}
	}
	for _, evidenceID := range fact.EvidenceIDs {
		if evidenceID <= 0 {
			return platform.Invalid("evidence_ids", "must contain positive decimal IDs")
		}
	}
	if fact.SourceFactID != nil && *fact.SourceFactID <= 0 {
		return platform.Invalid("source_fact_id", "must be a positive decimal ID")
	}
	if successFactHasSecrets(fact) {
		return platform.Invalid("fact", "credentials and secrets must not be stored in knowledge")
	}
	return nil
}

// successContent renders the structured draft (spec §10.1): kind, scope,
// structured bindings, recipe, evidence references and source provenance. It
// never contains a credential: the recipe was validated by validateSuccessFact.
func successContent(kind Kind, fact SuccessFact, bindings knowledge.Bindings) (string, error) {
	scope := knowledge.ScopeGlobal
	if strings.TrimSpace(fact.SystemKey) != "" {
		scope = knowledge.ScopeSystem
	}
	payload := map[string]any{
		"kind":              string(kind),
		"scope":             string(scope),
		"bindings":          bindings,
		"source_version_id": platformid.Format(fact.VersionID),
	}
	if fact.SourceFactID != nil {
		payload["source_fact_id"] = platformid.Format(*fact.SourceFactID)
	}
	if len(fact.EvidenceIDs) > 0 {
		refs := make([]string, 0, len(fact.EvidenceIDs))
		for _, evidenceID := range fact.EvidenceIDs {
			refs = append(refs, platformid.Format(evidenceID))
		}
		payload["evidence_refs"] = refs
	}
	if len(fact.Recipe) > 0 {
		payload["recipe"] = fact.Recipe
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func successTitle(kind Kind, fact SuccessFact) string {
	title := strings.TrimSpace(fact.Title)
	if title == "" {
		switch kind {
		case KindLoginRecipe:
			title = "登录配方：" + strings.TrimSpace(fact.AccountRole)
		case KindPageLocator:
			title = "页面定位：" + strings.TrimSpace(fact.PageKey)
		case KindAPIAuth:
			title = "API 鉴权：" + strings.TrimSpace(fact.APIKey)
		}
	}
	return truncateRunes(title, knowledge.MaxTitleRunes)
}

// sourceFactIDFromContent extracts the optional source_fact_id provenance from
// the structured content created by a success hook.
func sourceFactIDFromContent(content string) string {
	var payload struct {
		SourceFactID string `json:"source_fact_id"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.SourceFactID)
}

func optionalText(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

/* ---------------------------- secret rejection ---------------------------- */

// secretAssignmentPattern catches a credential written into free text
// ("password=hunter2", "token: ..."). Structured recipe fields are checked by
// key; this catches values that embed an assignment.
var secretAssignmentPattern = regexp.MustCompile(
	`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|authorization|cookie|credential)s?\b\s*[:=]\s*\S+`,
)

// successFactHasSecrets reports whether a hook input carries a credential or
// secret field. A template placeholder ({{token}}, <token>, ${token}) is not a
// credential and is allowed so recipes can describe the injection point.
func successFactHasSecrets(fact SuccessFact) bool {
	raw, err := json.Marshal(map[string]any{
		"title":  fact.Title,
		"recipe": fact.Recipe,
	})
	if err != nil {
		// Reject unencodable recipes: they cannot be stored safely anyway.
		return true
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return true
	}
	return payloadHasSecrets(payload)
}

// itemHasSecrets reports whether a stored item carries a credential field. It
// inspects the title, tags and the structured content; a non-JSON content is
// scanned as free text. The top-level "bindings" object is skipped: those are
// SUT identifiers validated by the binding rules, never credentials
// (spec §10.2).
func itemHasSecrets(item knowledge.Item) bool {
	if secretAssignmentPattern.MatchString(item.Title) {
		return true
	}
	for _, tag := range item.Tags {
		if secretAssignmentPattern.MatchString(tag) {
			return true
		}
	}
	var payload any
	if err := json.Unmarshal([]byte(item.Content), &payload); err == nil {
		if object, ok := payload.(map[string]any); ok {
			delete(object, "bindings")
			payload = object
		}
		return payloadHasSecrets(payload)
	}
	return secretAssignmentPattern.MatchString(item.Content)
}

func payloadHasSecrets(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if sensitiveKey(key) && !placeholderValue(item) {
				return true
			}
			if payloadHasSecrets(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if payloadHasSecrets(item) {
				return true
			}
		}
	case string:
		return secretAssignmentPattern.MatchString(typed)
	}
	return false
}

func sensitiveKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
	switch normalized {
	case "password", "passwd", "pwd", "secret", "clientsecret", "secretkey",
		"token", "accesstoken", "refreshtoken", "idtoken", "authtoken", "bearer",
		"authorization", "cookie", "cookies", "credential", "credentials",
		"privatekey", "apikey", "session", "sessionid":
		return true
	}
	for _, suffix := range []string{
		"password", "passwd", "pwd", "secret", "token", "credential",
		"apikey", "privatekey", "authorization", "cookie", "sessionid",
	} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

// placeholderValue reports whether a sensitive field value is a template
// placeholder rather than a concrete credential.
func placeholderValue(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		// A non-string under a credential key (object, number, bool) is treated
		// as a concrete secret, not a placeholder.
		return false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	if strings.Contains(trimmed, "{{") || strings.Contains(trimmed, "${") {
		return true
	}
	if strings.HasPrefix(trimmed, "<") && strings.HasSuffix(trimmed, ">") {
		return true
	}
	switch strings.ToLower(trimmed) {
	case "***", "redacted":
		return true
	default:
		return false
	}
}
