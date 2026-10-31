// Package versionapp implements the version use cases: create with multi-SUT
// bindings, detail aggregation through the resource catalog, cursor listing,
// title/auto-design updates and the resource delete-guard usage queries.
package versionapp

import (
	"time"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// VersionView is the detail read model returned by Create/Get/Update. Bindings
// carry nested resource summaries; passwords are never exposed by the resource
// views.
type VersionView struct {
	ID                        int64
	Title                     string
	Status                    string
	AutoDesignNewRequirements bool
	Bindings                  []BindingView
	RequirementCounts         version.RequirementCounts
	ConversationCount         int
	PendingDesignCount        int
	CreatedBy                 int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// BindingView is one SUT binding with its selected resources in input order.
type BindingView struct {
	ID          int64
	SUT         resourceapp.SUTView
	Environment resourceapp.EnvironmentView
	Repository  *resourceapp.CodeRepositoryView
	Pages       []resourceapp.PageView
	APIs        []resourceapp.APIView
	Databases   []resourceapp.DatabaseView
	Accounts    []resourceapp.AccountView
}

// VersionSummary is the list read model.
type VersionSummary struct {
	ID        int64
	Title     string
	Status    string
	SUTCount  int
	CreatedAt time.Time
	UpdatedAt time.Time
}
