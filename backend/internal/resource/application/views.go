package resourceapp

import "time"

// SUTView is the API-facing SUT read model. IDs are decimal strings.
type SUTView struct {
	ID           string
	Name         string
	Intro        string
	Family       *string
	Environments []EnvironmentView
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// EnvironmentView is one environment with its nested resources.
type EnvironmentView struct {
	ID        string
	SUTID     string
	Name      string
	BaseURL   string
	IsDefault bool
	Status    string
	Pages     []PageView
	APIs      []APIView
	Databases []DatabaseView
	Accounts  []AccountView
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PageView is one page of a SUT environment.
type PageView struct {
	ID            string
	SUTID         string
	EnvironmentID string
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

// APIView is one HTTP endpoint of a SUT environment.
type APIView struct {
	ID            string
	SUTID         string
	EnvironmentID string
	Method        string
	PathTemplate  string
	Name          string
	AuthRole      string
	Source        string
	Status        string
	SchemaRef     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// DatabaseView never carries a password, only whether one is configured.
type DatabaseView struct {
	ID            string
	SUTID         string
	EnvironmentID string
	Name          string
	DSN           string
	Username      string
	PasswordSet   bool
	ReadOnly      bool
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AccountView never carries a password, only whether one is configured.
type AccountView struct {
	ID            string
	SUTID         string
	EnvironmentID string
	Username      string
	Roles         []string
	Notes         string
	Status        string
	PasswordSet   bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
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
