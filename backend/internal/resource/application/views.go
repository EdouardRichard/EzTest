package resourceapp

import "time"

// EnvironmentView is the API-facing read model of a top-level environment
// (v0.2.4-B SC-B1). IDs are decimal strings. SUTs carries the environment's SUT
// catalog with each SUT's assets; it is empty for a list projection that did not
// ask for the nested tree.
type EnvironmentView struct {
	ID          string
	Name        string
	Description string
	Status      string
	SUTs        []SUTView
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SUTView is one system under test with the assets it owns. An environment owns
// SUTs and a SUT owns its pages, APIs and target databases.
//
// Pages/APIs/Databases are RAW catalog state: every status, including proposed
// and rejected rows and disabled databases. They exist for the resource catalog
// UI (which has to show a proposed page so it can be approved) and for the
// binding counters (which report proposed alongside approved). A consumer that
// needs the assets INSIDE THE SCOPE OF A VERSION must call EffectiveAssets
// instead — see scope.go. The field names are kept as they are because the
// resource HTTP contract and the downstream contexts already read them; the
// effective projection is the method, not a renamed field.
type SUTView struct {
	ID            string
	EnvironmentID string
	Name          string
	Intro         string
	BaseURL       string
	Family        *string
	// Source and Status are the SUT's R-B4 registration vocabulary
	// (admin/ai/import and proposed/approved/rejected). They are RAW catalog
	// state: the catalog UI shows a proposed SUT so an admin can approve it. The
	// effective version scope gates on Status through EffectiveAssets — see
	// scope.go. A zero Status is the legacy default 'approved' (resource_0005).
	Source    string
	Status    string
	Pages     []PageView
	APIs      []APIView
	Databases []DatabaseView
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SUTAssets is the asset projection of one SUT: the pages, APIs and target
// databases it owns. It is what ListAssetsBySUT returns for consumers that need
// the assets without the SUT row itself.
type SUTAssets struct {
	Pages     []PageView
	APIs      []APIView
	Databases []DatabaseView
}

// SUTRegistrationView is one append-only registration fact of a SUT (v0.2.4-B
// R-B4), the read model behind GET /api/resources/suts/{id}/registrations. IDs
// are decimal strings; EvidenceRefs is the content-addressed evidence the
// registration relied on (empty for the admin path). ResolutionMethod and
// GateResult record how the SUT was resolved and the registration gate outcome.
type SUTRegistrationView struct {
	ID               string
	SUTID            string
	Source           string
	EvidenceRefs     []string
	ResolutionMethod string
	GateResult       string
	CreatedAt        time.Time
	CreatedBy        int64
}

// PageView is one page of a SUT.
type PageView struct {
	ID            string
	SUTID         string
	Name          string
	URL           string
	RouteTemplate string
	PageType      string
	AuthRole      string
	Module        string
	Status        string
	Source        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// APIView is one HTTP endpoint of a SUT.
type APIView struct {
	ID           string
	SUTID        string
	Method       string
	PathTemplate string
	Name         string
	AuthRole     string
	Source       string
	Status       string
	SchemaRef    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DatabaseView never carries a password, only whether one is configured.
type DatabaseView struct {
	ID          string
	SUTID       string
	Name        string
	DSN         string
	Username    string
	PasswordSet bool
	ReadOnly    bool
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AccountView never carries a password, only whether one is configured. SUTID is
// the optional affinity hint of the global pool (v0.2.4-B): empty means the
// account has no SUT affinity.
type AccountView struct {
	ID          string
	SUTID       string
	Username    string
	Roles       []string
	Notes       string
	Status      string
	PasswordSet bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SUTRepositoryView is one SUT-level code-repository association with the
// global repository summary, the optional role label (v0.22 DB-12) and the
// resolved code baseline of that (SUT, repository) pair (v0.2.4-B).
type SUTRepositoryView struct {
	ID                 string
	SUTID              string
	Repository         CodeRepositoryView
	Role               string
	CodeBaselineCommit string
	Enabled            bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// CodeRepositoryView is one source repository.
type CodeRepositoryView struct {
	ID        string
	Name      string
	Path      string
	Branch    string
	Include   []string
	Exclude   []string
	CreatedBy int64
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}
