// Package httpapi exposes the version context's HTTP interface. Handlers stay
// thin: they decode DTOs, call the application services and render the shared
// response envelopes through the helpers in this package.
//
// P3 owns every route under /api/versions. They are mounted plain (the global
// identity middleware still requires a login); spec §6.9/§7.1 define no
// admin-only route and therefore no 403. All P3-owned ids are decimal strings in
// JSON and in path parameters, while P2 resource ids and user ids stay numbers.
package httpapi

import (
	"context"
	"io"
	"net/http"
	"time"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	eventinghttp "github.com/EdouardRichard/EzTest/backend/internal/eventing/interfaces/httpapi"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// VersionService is the version application port consumed by the HTTP layer.
// *application.VersionService implements it.
type VersionService interface {
	Create(ctx context.Context, cmd versionapp.CreateVersionCommand, actor int64) (*versionapp.Version, error)
	Get(ctx context.Context, id int64) (*versionapp.Version, error)
	List(ctx context.Context, filter versionapp.VersionFilter) ([]versionapp.Version, bool, error)
	UpdateMetadata(ctx context.Context, id int64, expected int, name, label, description string, actor int64) (*versionapp.Version, error)
	UpdateBindings(ctx context.Context, id int64, expected int, bindings versionapp.VersionBindings, actor int64) (*versionapp.Version, error)
	StartClarification(ctx context.Context, id, actor int64) (*versionapp.Version, error)
	EnterDesign(ctx context.Context, id int64, acknowledge bool, actor int64) (*versionapp.Version, versionapp.Warnings, error)
}

// VersionCountsReader returns the aggregate counters rendered on every version
// DTO (spec §7.2). *postgres.VersionRepository implements it; the version
// application service does not expose a counts pass-through, so T16 must pass
// the repository here (in addition to VersionService).
type VersionCountsReader interface {
	Counts(ctx context.Context, versionID int64) (domain.VersionCounts, error)
}

// RequirementService is the requirement application port consumed by the HTTP
// layer. *application.RequirementService implements it.
type RequirementService interface {
	Create(ctx context.Context, versionID int64, cmd versionapp.CreateRequirementCommand, actor int64) (*versionapp.Requirement, error)
	Update(ctx context.Context, versionID, reqID int64, cmd versionapp.UpdateRequirementCommand, actor int64) (*versionapp.Requirement, error)
	Delete(ctx context.Context, versionID, reqID int64, actor int64) error
	Confirm(ctx context.Context, versionID, reqID int64, actor int64) (*versionapp.Requirement, error)
	Reopen(ctx context.Context, versionID, reqID int64, actor int64) (*versionapp.Requirement, error)
	Get(ctx context.Context, versionID, reqID int64) (*versionapp.Requirement, error)
	List(ctx context.Context, versionID int64, status string, page domain.Page) ([]versionapp.Requirement, bool, error)
}

// RequirementStatsReader returns the per-requirement counters of the
// RequirementDTO (spec §7.3): open questions, relations and attachments.
// *postgres.RequirementRepository implements it; relation counts are derived
// from RelationService.List instead. T16 must pass the repository here.
type RequirementStatsReader interface {
	CountOpenQuestions(ctx context.Context, requirementID int64) (int, error)
	CountAttachments(ctx context.Context, requirementID, versionID int64) (int, int, error)
}

// RelationService is the requirement-relation application port consumed by the
// HTTP layer. *application.RelationService implements it. Update/Delete take the
// authenticated actor so relation events carry actorId (lead ruling 4).
type RelationService interface {
	Create(ctx context.Context, versionID int64, cmd versionapp.CreateRelationCommand, actor int64) (*versionapp.Relation, error)
	Update(ctx context.Context, versionID, relationID int64, relType, description *string, actor int64) (*versionapp.Relation, error)
	Delete(ctx context.Context, versionID, relationID, actor int64) error
	List(ctx context.Context, versionID int64) ([]versionapp.Relation, error)
}

// AttachmentService is the attachment application port consumed by the HTTP
// layer. *application.AttachmentService implements it. StoragePathsForRequirement
// and RemoveStoredFiles implement the requirement-delete cascade of lead
// ruling 6: capture the paths before the delete, remove the files after it.
// ReadText feeds the bounded text summaries of the context-recon run.
type AttachmentService interface {
	Upload(ctx context.Context, versionID, reqID int64, filename, declaredType string, r io.Reader, actor int64) (*versionapp.Attachment, error)
	List(ctx context.Context, versionID, reqID int64) ([]versionapp.Attachment, error)
	ReadText(ctx context.Context, versionID, attachmentID int64, maxBytes int) (string, error)
	Get(ctx context.Context, versionID, reqID, attachmentID int64) (*versionapp.Attachment, error)
	OpenContent(ctx context.Context, versionID, reqID, attachmentID int64) (*versionapp.Attachment, io.ReadSeekCloser, error)
	Delete(ctx context.Context, versionID, reqID, attachmentID, actor int64) error
	StoragePathsForRequirement(ctx context.Context, versionID, requirementID int64) ([]string, error)
	RemoveStoredFiles(ctx context.Context, paths []string) error
}

// ClarificationService is the clarification application port consumed by the
// HTTP layer (spec §7.5/§7.6). *application.ClarificationService implements it.
type ClarificationService interface {
	StartSession(ctx context.Context, versionID int64, actor int64) (*versionapp.ClarificationSession, error)
	CompleteSession(ctx context.Context, versionID, sessionID int64, actor int64) (*versionapp.ClarificationSession, error)
	SendMessage(ctx context.Context, versionID int64, message string, actor int64) (versionapp.StartRunResult, error)
	StartAnalysis(ctx context.Context, versionID int64, actor int64) (versionapp.StartRunResult, error)
	StartReconWithParts(ctx context.Context, versionID int64, extraParts []versionapp.AgentPart, actor int64) (versionapp.StartRunResult, error)
	CurrentSession(ctx context.Context, versionID int64) (*versionapp.ClarificationSession, error)
	Counts(ctx context.Context, versionID int64) (versionapp.ClarificationCounts, error)
	ListQuestions(ctx context.Context, versionID int64, status string, requirementID *int64, page domain.Page) ([]versionapp.Question, bool, error)
	UpsertQuestion(ctx context.Context, versionID int64, input versionapp.QuestionInput, actor int64, source string) (*versionapp.Question, error)
	AnswerQuestion(ctx context.Context, versionID, questionID int64, answer string, actor int64, via string) (*versionapp.Question, error)
	DismissQuestion(ctx context.Context, versionID, questionID int64, actor int64) (*versionapp.Question, error)
	ReopenQuestion(ctx context.Context, versionID, questionID int64, actor int64) (*versionapp.Question, error)
	ListDecisions(ctx context.Context, versionID int64, status string, page domain.Page) ([]versionapp.Decision, bool, error)
	CreateDecision(ctx context.Context, versionID int64, input versionapp.DecisionInput, actor int64, source string) (*versionapp.Decision, error)
	Decide(ctx context.Context, versionID, decisionID int64, accept bool, actor int64, rationale ...string) (*versionapp.Decision, error)
}

// ClarificationMessagesReader is the P1 message projection the clarification
// history reads (spec §7.5). *version/infrastructure/agent.AgentSessionsAdapter
// implements it; T16 passes that adapter here.
type ClarificationMessagesReader interface {
	Messages(ctx context.Context, agentSessionID, beforeID int64, limit int) ([]versionapp.AgentMessage, bool, error)
}

// EventSnapshotter is the eventing read use case of the version events
// endpoint. *eventing/application.Service implements it; T16 passes it here.
type EventSnapshotter interface {
	Snapshot(ctx context.Context, channel string, beforeSeq int64, limit int) ([]eventing.Event, int64, bool, error)
}

// Compile-time proof that the concrete application services T16 wires satisfy
// the consumer-side interfaces above. The aggregate readers are implemented by
// the PostgreSQL repositories and the two P1/eventing readers by their adapters;
// importing those from the HTTP layer would invert the dependency direction, so
// they are only checked at wiring time.
var (
	_ VersionService       = (*versionapp.VersionService)(nil)
	_ RequirementService   = (*versionapp.RequirementService)(nil)
	_ RelationService      = (*versionapp.RelationService)(nil)
	_ AttachmentService    = (*versionapp.AttachmentService)(nil)
	_ ClarificationService = (*versionapp.ClarificationService)(nil)
)

// Handlers groups the version context's HTTP handlers so the composition root
// wires them into the server through one dependency. A nil group is skipped by
// RegisterRoutes, which keeps a partially wired process (and most tests) from
// panicking at startup.
type Handlers struct {
	Versions      *VersionHandler
	Requirements  *RequirementHandler
	Relations     *RelationHandler
	Attachments   *AttachmentHandler
	Clarification *ClarificationHandler
	Questions     *QuestionHandler
	Decisions     *DecisionHandler
	Events        *EventHandler
	Stream        *VersionStreamHandler
}

// HandlersDeps is the dependency set of the version HTTP handlers. The service
// fields are consumer-defined interfaces so tests can inject fakes; T16 passes
// the concrete application services, the PostgreSQL repositories for the two
// aggregate readers, the P2 resource catalog adapter, the T13 P1 agent-sessions
// adapter (messages), and the eventing snapshot/stream services.
type HandlersDeps struct {
	Versions         VersionService
	VersionCounts    VersionCountsReader
	ResourceCatalog  versionapp.ResourceCatalog
	Requirements     RequirementService
	RequirementStats RequirementStatsReader
	Relations        RelationService
	Attachments      AttachmentService

	Clarification         ClarificationService
	ClarificationMessages ClarificationMessagesReader
	Events                EventSnapshotter
	EventReader           eventinghttp.EventReader
	EventSubscriber       eventinghttp.EventSubscriber
	// SSEHeartbeat overrides the inherited 20s SSE heartbeat (tests use a short
	// interval); zero means the eventing default.
	SSEHeartbeat time.Duration
}

// NewHandlers builds the handler groups whose service dependency is wired. A
// nil service leaves its group out of the returned Handlers. The clarification
// group also owns the question and decision routes; the stream group requires
// the version service for its existence check plus the two eventing ports.
func NewHandlers(deps HandlersDeps) *Handlers {
	handlers := &Handlers{}
	if deps.Versions != nil {
		handlers.Versions = &VersionHandler{
			versions:  deps.Versions,
			counts:    deps.VersionCounts,
			resources: deps.ResourceCatalog,
		}
	}
	if deps.Requirements != nil {
		handlers.Requirements = &RequirementHandler{
			requirements: deps.Requirements,
			stats:        deps.RequirementStats,
			relations:    deps.Relations,
			attachments:  deps.Attachments,
		}
	}
	if deps.Relations != nil {
		handlers.Relations = &RelationHandler{relations: deps.Relations}
	}
	if deps.Attachments != nil {
		handlers.Attachments = &AttachmentHandler{attachments: deps.Attachments}
	}
	if deps.Clarification != nil {
		handlers.Clarification = &ClarificationHandler{
			clarification: deps.Clarification,
			messages:      deps.ClarificationMessages,
			requirements:  deps.Requirements,
			attachments:   deps.Attachments,
		}
		handlers.Questions = &QuestionHandler{clarification: deps.Clarification}
		handlers.Decisions = &DecisionHandler{clarification: deps.Clarification}
	}
	if deps.Events != nil {
		handlers.Events = &EventHandler{versions: deps.Versions, events: deps.Events}
	}
	if deps.Versions != nil && deps.EventReader != nil && deps.EventSubscriber != nil {
		handlers.Stream = NewVersionStreamHandler(deps.Versions, deps.EventReader, deps.EventSubscriber, deps.SSEHeartbeat)
	}
	return handlers
}

// RegisterRoutes mounts the P3 endpoints on mux: versions, requirements,
// relations, attachments, clarification sessions/messages/questions/decisions,
// event snapshots and the version SSE stream. There is no admin guard (P3 is
// available to every authenticated user); authentication itself is the server's
// global middleware.
func RegisterRoutes(mux *http.ServeMux, handlers Handlers) {
	if mux == nil {
		return
	}
	if handlers.Versions != nil {
		handlers.Versions.RegisterRoutes(mux)
	}
	if handlers.Requirements != nil {
		handlers.Requirements.RegisterRoutes(mux)
	}
	if handlers.Relations != nil {
		handlers.Relations.RegisterRoutes(mux)
	}
	if handlers.Attachments != nil {
		handlers.Attachments.RegisterRoutes(mux)
	}
	if handlers.Clarification != nil {
		handlers.Clarification.RegisterRoutes(mux)
	}
	if handlers.Questions != nil {
		handlers.Questions.RegisterRoutes(mux)
	}
	if handlers.Decisions != nil {
		handlers.Decisions.RegisterRoutes(mux)
	}
	if handlers.Events != nil {
		handlers.Events.RegisterRoutes(mux)
	}
	if handlers.Stream != nil {
		handlers.Stream.RegisterRoutes(mux)
	}
}
