package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"sort"
	"strings"
	"testing"
	"time"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// testNow is the fixed instant every fake repository stamps on rows.
var testNow = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

const (
	testUserToken = "version-test-token"
	testUserID    = int64(42)
)

// testAuthenticator resolves testUserToken to a regular (non-admin) user: P3
// has no admin-only route, so the same user must be able to read and write.
type testAuthenticator struct{}

// Authenticate maps the test bearer token to the seeded regular user.
func (testAuthenticator) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	if rawToken != testUserToken {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return identity.User{
		ID:       testUserID,
		Username: "tester",
		Role:     identity.RoleUser,
		Status:   identity.StatusActive,
	}, nil
}

// readSeekCloser adapts a bytes.Reader to the io.ReadSeekCloser contract of the
// attachment download service.
type readSeekCloser struct {
	*bytes.Reader
}

// Close implements io.Closer.
func (readSeekCloser) Close() error { return nil }

// fakeVersionService is an in-memory VersionService. Its behaviour mirrors the
// documented service contract closely enough for the HTTP layer: 404 for a
// missing version, revision CAS on metadata/bindings, the draft/clarifying
// status gates of the two actions and the enter-design soft gate.
type fakeVersionService struct {
	versions      map[int64]*versionapp.Version
	nextID        int64
	calls         []string
	lastActor     int64
	lastCreate    versionapp.CreateVersionCommand
	lastMetadata  fakeMetadataCall
	lastBindings  fakeBindingsCall
	listVersions  []versionapp.Version
	lastFilter    versionapp.VersionFilter
	startErr      error
	enterErr      error
	enterWarnings versionapp.Warnings
	lastEnterAck  bool
	lastEnterID   int64
}

// fakeMetadataCall records one UpdateMetadata request.
type fakeMetadataCall struct {
	versionID   int64
	revision    int
	name        string
	label       string
	description string
	actor       int64
}

// fakeBindingsCall records one UpdateBindings request.
type fakeBindingsCall struct {
	versionID int64
	revision  int
	bindings  versionapp.VersionBindings
	actor     int64
}

// newFakeVersionService returns an empty fake version service.
func newFakeVersionService() *fakeVersionService {
	return &fakeVersionService{versions: map[int64]*versionapp.Version{}}
}

// seed stores one version fixture and returns it.
func (f *fakeVersionService) seed(version versionapp.Version) *versionapp.Version {
	if version.Revision == 0 {
		version.Revision = 1
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = testNow
	}
	if version.UpdatedAt.IsZero() {
		version.UpdatedAt = testNow
	}
	f.versions[version.ID] = &version
	return &version
}

// Create implements VersionService.
func (f *fakeVersionService) Create(_ context.Context, cmd versionapp.CreateVersionCommand, actor int64) (*versionapp.Version, error) {
	f.calls = append(f.calls, "Create")
	f.lastActor = actor
	f.lastCreate = cmd
	id := f.nextID
	if id == 0 {
		id = 1
	}
	f.nextID = id + 1
	version := &versionapp.Version{
		ID:                 id,
		Name:               cmd.Name,
		Label:              cmd.Label,
		Description:        cmd.Description,
		Status:             domain.VersionDraft,
		SystemID:           cmd.SystemID,
		EnvironmentID:      cmd.EnvironmentID,
		CodeRepositoryID:   cmd.CodeRepositoryID,
		CodeBranch:         cmd.CodeBranch,
		CodeBaselineCommit: cmd.CodeBaselineCommit,
		Revision:           1,
		CreatedBy:          actor,
		UpdatedBy:          actor,
		CreatedAt:          testNow,
		UpdatedAt:          testNow,
	}
	f.versions[id] = version
	return version, nil
}

// Get implements VersionService.
func (f *fakeVersionService) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	f.calls = append(f.calls, "Get")
	version, ok := f.versions[id]
	if !ok {
		return nil, domain.NewNotFound("version", id)
	}
	return version, nil
}

// List implements VersionService.
func (f *fakeVersionService) List(_ context.Context, filter versionapp.VersionFilter) ([]versionapp.Version, bool, error) {
	f.calls = append(f.calls, "List")
	f.lastFilter = filter
	items := append([]versionapp.Version(nil), f.listVersions...)
	if filter.Page.Limit > 0 && len(items) > filter.Page.Limit {
		return items[:filter.Page.Limit], true, nil
	}
	return items, false, nil
}

// UpdateMetadata implements VersionService with revision CAS.
func (f *fakeVersionService) UpdateMetadata(_ context.Context, id int64, expected int, name, label, description string, actor int64) (*versionapp.Version, error) {
	f.calls = append(f.calls, "UpdateMetadata")
	f.lastActor = actor
	f.lastMetadata = fakeMetadataCall{versionID: id, revision: expected, name: name, label: label, description: description, actor: actor}
	version, ok := f.versions[id]
	if !ok {
		return nil, domain.NewNotFound("version", id)
	}
	if version.Revision != expected {
		return nil, domain.NewStateConflict("revision_conflict", "version revision does not match")
	}
	version.Name, version.Label, version.Description = name, label, description
	version.Revision++
	version.UpdatedBy = actor
	version.UpdatedAt = testNow
	return version, nil
}

// UpdateBindings implements VersionService with revision and status gates.
func (f *fakeVersionService) UpdateBindings(_ context.Context, id int64, expected int, bindings versionapp.VersionBindings, actor int64) (*versionapp.Version, error) {
	f.calls = append(f.calls, "UpdateBindings")
	f.lastActor = actor
	f.lastBindings = fakeBindingsCall{versionID: id, revision: expected, bindings: bindings, actor: actor}
	version, ok := f.versions[id]
	if !ok {
		return nil, domain.NewNotFound("version", id)
	}
	if version.Revision != expected {
		return nil, domain.NewStateConflict("revision_conflict", "version revision does not match")
	}
	if version.Status != domain.VersionDraft && version.Status != domain.VersionClarifying {
		return nil, domain.NewStateConflict("version_state_conflict", "bindings can only change in draft or clarifying")
	}
	version.SystemID = bindings.SystemID
	version.EnvironmentID = bindings.EnvironmentID
	version.CodeRepositoryID = bindings.CodeRepositoryID
	version.CodeBranch = bindings.CodeBranch
	version.CodeBaselineCommit = bindings.CodeBaselineCommit
	version.Revision++
	version.UpdatedBy = actor
	version.UpdatedAt = testNow
	return version, nil
}

// StartClarification implements VersionService.
func (f *fakeVersionService) StartClarification(_ context.Context, id, actor int64) (*versionapp.Version, error) {
	f.calls = append(f.calls, "StartClarification")
	f.lastActor = actor
	if f.startErr != nil {
		return nil, f.startErr
	}
	version, ok := f.versions[id]
	if !ok {
		return nil, domain.NewNotFound("version", id)
	}
	if version.Status != domain.VersionDraft {
		return nil, domain.NewStateConflict("version_state_conflict", "start-clarification requires draft")
	}
	version.Status = domain.VersionClarifying
	version.Revision++
	version.UpdatedBy = actor
	return version, nil
}

// EnterDesign implements VersionService, soft gate included.
func (f *fakeVersionService) EnterDesign(_ context.Context, id int64, acknowledge bool, actor int64) (*versionapp.Version, versionapp.Warnings, error) {
	f.calls = append(f.calls, "EnterDesign")
	f.lastActor = actor
	f.lastEnterAck = acknowledge
	f.lastEnterID = id
	if f.enterErr != nil {
		return nil, f.enterWarnings, f.enterErr
	}
	version, ok := f.versions[id]
	if !ok {
		return nil, versionapp.Warnings{}, domain.NewNotFound("version", id)
	}
	if version.Status != domain.VersionClarifying {
		return nil, versionapp.Warnings{}, domain.NewStateConflict("version_state_conflict", "enter-design requires clarifying")
	}
	version.Status = domain.VersionDesigning
	version.Revision++
	version.UpdatedBy = actor
	return version, f.enterWarnings, nil
}

// fakeVersionCounts is an in-memory VersionCountsReader.
type fakeVersionCounts struct {
	counts map[int64]domain.VersionCounts
	err    error
	calls  int
}

// Counts implements VersionCountsReader.
func (f *fakeVersionCounts) Counts(_ context.Context, versionID int64) (domain.VersionCounts, error) {
	f.calls++
	if f.err != nil {
		return domain.VersionCounts{}, f.err
	}
	return f.counts[versionID], nil
}

// fakeResourceCatalog is an in-memory P2 resource lookup port.
type fakeResourceCatalog struct {
	systems      map[int64]versionapp.ResourceRef
	environments map[int64]versionapp.ResourceRef
	repositories map[int64]versionapp.CodeRepoRef
}

// newFakeResourceCatalog returns an empty fake catalog.
func newFakeResourceCatalog() *fakeResourceCatalog {
	return &fakeResourceCatalog{
		systems:      map[int64]versionapp.ResourceRef{},
		environments: map[int64]versionapp.ResourceRef{},
		repositories: map[int64]versionapp.CodeRepoRef{},
	}
}

// GetSystem implements ResourceCatalog.
func (f *fakeResourceCatalog) GetSystem(_ context.Context, id int64) (versionapp.ResourceRef, error) {
	ref, ok := f.systems[id]
	if !ok {
		return versionapp.ResourceRef{}, &versionapp.ResourceError{Reason: "system_not_found", Message: "system not found"}
	}
	return ref, nil
}

// GetEnvironment implements ResourceCatalog.
func (f *fakeResourceCatalog) GetEnvironment(_ context.Context, id int64) (versionapp.ResourceRef, error) {
	ref, ok := f.environments[id]
	if !ok {
		return versionapp.ResourceRef{}, &versionapp.ResourceError{Reason: "environment_not_found", Message: "environment not found"}
	}
	return ref, nil
}

// GetCodeRepository implements ResourceCatalog.
func (f *fakeResourceCatalog) GetCodeRepository(_ context.Context, id int64) (versionapp.CodeRepoRef, error) {
	ref, ok := f.repositories[id]
	if !ok {
		return versionapp.CodeRepoRef{}, &versionapp.ResourceError{Reason: "code_repository_not_found", Message: "repository not found"}
	}
	return ref, nil
}

// fakeRequirementService is an in-memory RequirementService.
type fakeRequirementService struct {
	requirements  map[int64]*versionapp.Requirement
	nextID        int64
	order         *[]string
	calls         []string
	lastActor     int64
	lastCreate    versionapp.CreateRequirementCommand
	lastUpdate    versionapp.UpdateRequirementCommand
	lastListID    int64
	lastListQuery string
	lastListPage  domain.Page
	listResult    []versionapp.Requirement
	createErr     error
	updateErr     error
	deleteErr     error
	confirmErr    error
	reopenErr     error
	listErr       error
}

// newFakeRequirementService returns an empty fake requirement service that
// records its mutating calls into order when it is non-nil.
func newFakeRequirementService(order *[]string) *fakeRequirementService {
	return &fakeRequirementService{requirements: map[int64]*versionapp.Requirement{}, order: order}
}

// record appends one operation name to the shared call-order log.
func (f *fakeRequirementService) record(operation string) {
	if f.order != nil {
		*f.order = append(*f.order, operation)
	}
}

// seed stores one requirement fixture and returns it.
func (f *fakeRequirementService) seed(requirement versionapp.Requirement) *versionapp.Requirement {
	if requirement.Revision == 0 {
		requirement.Revision = 1
	}
	if requirement.CreatedAt.IsZero() {
		requirement.CreatedAt = testNow
	}
	if requirement.UpdatedAt.IsZero() {
		requirement.UpdatedAt = testNow
	}
	f.requirements[requirement.ID] = &requirement
	return &requirement
}

// Create implements RequirementService.
func (f *fakeRequirementService) Create(_ context.Context, versionID int64, cmd versionapp.CreateRequirementCommand, actor int64) (*versionapp.Requirement, error) {
	f.calls = append(f.calls, "Create")
	f.lastActor = actor
	f.lastCreate = cmd
	if f.createErr != nil {
		return nil, f.createErr
	}
	id := f.nextID
	if id == 0 {
		id = 1
	}
	f.nextID = id + 1
	priority := cmd.Priority
	if priority == "" {
		priority = domain.PriorityShould
	}
	source := cmd.Source
	if source == "" {
		source = domain.SourceUser
	}
	requirement := &versionapp.Requirement{
		ID:                 id,
		VersionID:          versionID,
		Title:              cmd.Title,
		Description:        cmd.Description,
		AcceptanceCriteria: cmd.AcceptanceCriteria,
		Priority:           priority,
		Status:             domain.RequirementDraft,
		Source:             source,
		LastUpdateSource:   source,
		Revision:           1,
		CreatedBy:          actor,
		UpdatedBy:          actor,
		CreatedAt:          testNow,
		UpdatedAt:          testNow,
	}
	f.requirements[id] = requirement
	return requirement, nil
}

// Get implements RequirementService with version scoping.
func (f *fakeRequirementService) Get(_ context.Context, versionID, reqID int64) (*versionapp.Requirement, error) {
	f.calls = append(f.calls, "Get")
	requirement, ok := f.requirements[reqID]
	if !ok || requirement.VersionID != versionID {
		return nil, domain.NewNotFound("requirement", reqID)
	}
	return requirement, nil
}

// List implements RequirementService.
func (f *fakeRequirementService) List(_ context.Context, versionID int64, status string, page domain.Page) ([]versionapp.Requirement, bool, error) {
	f.calls = append(f.calls, "List")
	f.lastListID = versionID
	f.lastListQuery = status
	f.lastListPage = page
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	items := append([]versionapp.Requirement(nil), f.listResult...)
	if items == nil {
		items = make([]versionapp.Requirement, 0, len(f.requirements))
		for _, requirement := range f.requirements {
			if requirement.VersionID != versionID {
				continue
			}
			if status != "" && requirement.Status != status {
				continue
			}
			items = append(items, *requirement)
		}
	}
	if page.Limit > 0 && len(items) > page.Limit {
		return items[:page.Limit], true, nil
	}
	return items, false, nil
}

// Update implements RequirementService with the draft gate and revision CAS.
func (f *fakeRequirementService) Update(_ context.Context, versionID, reqID int64, cmd versionapp.UpdateRequirementCommand, actor int64) (*versionapp.Requirement, error) {
	f.calls = append(f.calls, "Update")
	f.lastActor = actor
	f.lastUpdate = cmd
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	requirement, err := f.Get(context.Background(), versionID, reqID)
	if err != nil {
		return nil, err
	}
	if requirement.Status != domain.RequirementDraft {
		return nil, domain.NewStateConflict("requirement_state_conflict", "requirement is not editable")
	}
	if requirement.Revision != cmd.Revision {
		return nil, domain.NewStateConflict("revision_conflict", "requirement revision does not match")
	}
	if cmd.Title != nil {
		requirement.Title = *cmd.Title
	}
	if cmd.Description != nil {
		requirement.Description = *cmd.Description
	}
	if cmd.AcceptanceCriteria != nil {
		requirement.AcceptanceCriteria = *cmd.AcceptanceCriteria
	}
	if cmd.Priority != nil {
		requirement.Priority = *cmd.Priority
	}
	requirement.LastUpdateSource = domain.SourceUser
	requirement.UpdatedBy = actor
	requirement.Revision++
	requirement.UpdatedAt = testNow
	return requirement, nil
}

// Delete implements RequirementService, recording the cascade order.
func (f *fakeRequirementService) Delete(_ context.Context, versionID, reqID int64, actor int64) error {
	f.record("RequirementDelete")
	f.calls = append(f.calls, "Delete")
	f.lastActor = actor
	if f.deleteErr != nil {
		return f.deleteErr
	}
	requirement, err := f.Get(context.Background(), versionID, reqID)
	if err != nil {
		return err
	}
	if requirement.Status != domain.RequirementDraft {
		return domain.NewStateConflict("requirement_state_conflict", "requirement is not deletable")
	}
	delete(f.requirements, reqID)
	return nil
}

// Confirm implements RequirementService.
func (f *fakeRequirementService) Confirm(_ context.Context, versionID, reqID int64, actor int64) (*versionapp.Requirement, error) {
	f.calls = append(f.calls, "Confirm")
	f.lastActor = actor
	if f.confirmErr != nil {
		return nil, f.confirmErr
	}
	requirement, err := f.Get(context.Background(), versionID, reqID)
	if err != nil {
		return nil, err
	}
	if requirement.Status != domain.RequirementDraft {
		return nil, domain.NewStateConflict("requirement_state_conflict", "requirement is not confirmable")
	}
	confirmedAt := testNow
	requirement.Status = domain.RequirementClarified
	requirement.ConfirmedAt = &confirmedAt
	requirement.ConfirmedBy = &actor
	requirement.UpdatedBy = actor
	requirement.Revision++
	requirement.UpdatedAt = testNow
	return requirement, nil
}

// Reopen implements RequirementService.
func (f *fakeRequirementService) Reopen(_ context.Context, versionID, reqID int64, actor int64) (*versionapp.Requirement, error) {
	f.calls = append(f.calls, "Reopen")
	f.lastActor = actor
	if f.reopenErr != nil {
		return nil, f.reopenErr
	}
	requirement, err := f.Get(context.Background(), versionID, reqID)
	if err != nil {
		return nil, err
	}
	if requirement.Status != domain.RequirementClarified {
		return nil, domain.NewStateConflict("requirement_state_conflict", "requirement is not reopenable")
	}
	requirement.Status = domain.RequirementDraft
	requirement.ConfirmedAt = nil
	requirement.ConfirmedBy = nil
	requirement.UpdatedBy = actor
	requirement.Revision++
	requirement.UpdatedAt = testNow
	return requirement, nil
}

// fakeRequirementStats is an in-memory RequirementStatsReader.
type fakeRequirementStats struct {
	open        map[int64]int
	attachments map[int64]int
	err         error
}

// CountOpenQuestions implements RequirementStatsReader.
func (f *fakeRequirementStats) CountOpenQuestions(_ context.Context, requirementID int64) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.open[requirementID], nil
}

// CountAttachments implements RequirementStatsReader.
func (f *fakeRequirementStats) CountAttachments(_ context.Context, requirementID, versionID int64) (int, int, error) {
	if f.err != nil {
		return 0, 0, f.err
	}
	return f.attachments[requirementID], 0, nil
}

// fakeRelationService is an in-memory RelationService.
type fakeRelationService struct {
	relations  []versionapp.Relation
	nextID     int64
	calls      []string
	lastActor  int64
	lastCreate versionapp.CreateRelationCommand
	lastUpdate fakeRelationUpdate
	lastDelete fakeRelationDelete
	createErr  error
	updateErr  error
	deleteErr  error
	listErr    error
}

// fakeRelationUpdate records one Update call.
type fakeRelationUpdate struct {
	versionID    int64
	relationID   int64
	relationType *string
	description  *string
	actor        int64
}

// fakeRelationDelete records one Delete call.
type fakeRelationDelete struct {
	versionID  int64
	relationID int64
	actor      int64
}

// seed stores one relation fixture.
func (f *fakeRelationService) seed(relation versionapp.Relation) {
	if relation.CreatedAt.IsZero() {
		relation.CreatedAt = testNow
	}
	if relation.UpdatedAt.IsZero() {
		relation.UpdatedAt = testNow
	}
	f.relations = append(f.relations, relation)
}

// Create implements RelationService.
func (f *fakeRelationService) Create(_ context.Context, versionID int64, cmd versionapp.CreateRelationCommand, actor int64) (*versionapp.Relation, error) {
	f.calls = append(f.calls, "Create")
	f.lastActor = actor
	f.lastCreate = cmd
	if f.createErr != nil {
		return nil, f.createErr
	}
	id := f.nextID
	if id == 0 {
		id = 1
	}
	f.nextID = id + 1
	source := cmd.Source
	if source == "" {
		source = domain.SourceUser
	}
	relation := versionapp.Relation{
		ID:                id,
		VersionID:         versionID,
		FromRequirementID: cmd.FromRequirementID,
		ToRequirementID:   cmd.ToRequirementID,
		RelationType:      cmd.RelationType,
		Description:       cmd.Description,
		Source:            source,
		CreatedBy:         actor,
		CreatedAt:         testNow,
		UpdatedAt:         testNow,
	}
	f.relations = append(f.relations, relation)
	return &relation, nil
}

// Update implements RelationService with true PATCH semantics.
func (f *fakeRelationService) Update(_ context.Context, versionID, relationID int64, relationType, description *string, actor int64) (*versionapp.Relation, error) {
	f.calls = append(f.calls, "Update")
	f.lastActor = actor
	f.lastUpdate = fakeRelationUpdate{
		versionID:    versionID,
		relationID:   relationID,
		relationType: relationType,
		description:  description,
		actor:        actor,
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if relationType != nil && *relationType == "" {
		return nil, domain.NewInvalid("relationType", "invalid_relation_type", "relationType must not be empty")
	}
	for i := range f.relations {
		if f.relations[i].ID != relationID || f.relations[i].VersionID != versionID {
			continue
		}
		if relationType != nil {
			f.relations[i].RelationType = *relationType
		}
		if description != nil {
			f.relations[i].Description = *description
		}
		f.relations[i].UpdatedAt = testNow
		updated := f.relations[i]
		return &updated, nil
	}
	return nil, domain.NewNotFound("relation", relationID)
}

// Delete implements RelationService.
func (f *fakeRelationService) Delete(_ context.Context, versionID, relationID, actor int64) error {
	f.calls = append(f.calls, "Delete")
	f.lastActor = actor
	f.lastDelete = fakeRelationDelete{versionID: versionID, relationID: relationID, actor: actor}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for i := range f.relations {
		if f.relations[i].ID == relationID && f.relations[i].VersionID == versionID {
			f.relations = append(f.relations[:i], f.relations[i+1:]...)
			return nil
		}
	}
	return domain.NewNotFound("relation", relationID)
}

// List implements RelationService.
func (f *fakeRelationService) List(_ context.Context, versionID int64) ([]versionapp.Relation, error) {
	f.calls = append(f.calls, "List")
	if f.listErr != nil {
		return nil, f.listErr
	}
	relations := make([]versionapp.Relation, 0, len(f.relations))
	for _, relation := range f.relations {
		if relation.VersionID == versionID {
			relations = append(relations, relation)
		}
	}
	return relations, nil
}

// fakeAttachmentService is an in-memory AttachmentService.
type fakeAttachmentService struct {
	attachments   map[int64]*versionapp.Attachment
	contents      map[int64][]byte
	nextID        int64
	order         *[]string
	calls         []string
	lastActor     int64
	lastUpload    fakeAttachmentUpload
	readTextCalls []fakeAttachmentRead
	removedPaths  []string
	storagePaths  []string
	uploadErr     error
	listErr       error
	deleteErr     error
	pathsErr      error
	removeErr     error
	openErr       error
	readTextErr   error
}

// fakeAttachmentRead records one ReadText request of the recon part builder.
type fakeAttachmentRead struct {
	versionID    int64
	attachmentID int64
	maxBytes     int
}

// fakeAttachmentUpload records one multipart upload.
type fakeAttachmentUpload struct {
	versionID     int64
	requirementID int64
	filename      string
	declaredType  string
	data          []byte
}

// newFakeAttachmentService returns an empty fake attachment service.
func newFakeAttachmentService(order *[]string) *fakeAttachmentService {
	return &fakeAttachmentService{
		attachments: map[int64]*versionapp.Attachment{},
		contents:    map[int64][]byte{},
		order:       order,
	}
}

// record appends one operation name to the shared call-order log.
func (f *fakeAttachmentService) record(operation string) {
	if f.order != nil {
		*f.order = append(*f.order, operation)
	}
}

// seed stores one attachment fixture with content.
func (f *fakeAttachmentService) seed(attachment versionapp.Attachment, content []byte) {
	if attachment.CreatedAt.IsZero() {
		attachment.CreatedAt = testNow
	}
	f.attachments[attachment.ID] = &attachment
	f.contents[attachment.ID] = content
}

// Upload implements AttachmentService.
func (f *fakeAttachmentService) Upload(_ context.Context, versionID, reqID int64, filename, declaredType string, r io.Reader, actor int64) (*versionapp.Attachment, error) {
	f.calls = append(f.calls, "Upload")
	f.lastActor = actor
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	f.lastUpload = fakeAttachmentUpload{
		versionID:     versionID,
		requirementID: reqID,
		filename:      filename,
		declaredType:  declaredType,
		data:          data,
	}
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	id := f.nextID
	if id == 0 {
		id = 1
	}
	f.nextID = id + 1
	mimeType := declaredType
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = "text/plain"
		if strings.HasSuffix(strings.ToLower(filename), ".png") {
			mimeType = "image/png"
		}
	}
	kind := domain.AttachmentText
	if strings.HasPrefix(mimeType, "image/") {
		kind = domain.AttachmentImage
	}
	sum := sha256.Sum256(data)
	attachment := &versionapp.Attachment{
		ID:            id,
		VersionID:     versionID,
		RequirementID: reqID,
		Kind:          kind,
		Filename:      filename,
		MIMEType:      mimeType,
		SizeBytes:     int64(len(data)),
		SHA256:        hex.EncodeToString(sum[:]),
		StoragePath:   "storage/does/not/matter",
		CreatedBy:     actor,
		CreatedAt:     testNow,
	}
	f.attachments[id] = attachment
	f.contents[id] = data
	return attachment, nil
}

// List implements AttachmentService.
func (f *fakeAttachmentService) List(_ context.Context, versionID, reqID int64) ([]versionapp.Attachment, error) {
	f.calls = append(f.calls, "List")
	if f.listErr != nil {
		return nil, f.listErr
	}
	ids := make([]int64, 0, len(f.attachments))
	for id, attachment := range f.attachments {
		if attachment.VersionID == versionID && attachment.RequirementID == reqID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	attachments := make([]versionapp.Attachment, 0, len(ids))
	for _, id := range ids {
		attachments = append(attachments, *f.attachments[id])
	}
	return attachments, nil
}

// ReadText implements AttachmentService. It clamps the returned content to the
// requested byte budget like the real service clamps to 64 KiB per file.
func (f *fakeAttachmentService) ReadText(_ context.Context, versionID, attachmentID int64, maxBytes int) (string, error) {
	f.calls = append(f.calls, "ReadText")
	f.readTextCalls = append(f.readTextCalls, fakeAttachmentRead{
		versionID: versionID, attachmentID: attachmentID, maxBytes: maxBytes,
	})
	if f.readTextErr != nil {
		return "", f.readTextErr
	}
	attachment, ok := f.attachments[attachmentID]
	if !ok || attachment.VersionID != versionID {
		return "", domain.NewNotFound("attachment", attachmentID)
	}
	if attachment.Kind != domain.AttachmentText {
		return "", domain.NewInvalid("file", "unsupported_attachment_type",
			"only text attachments can be read")
	}
	content := f.contents[attachmentID]
	if maxBytes > 0 && len(content) > maxBytes {
		content = content[:maxBytes]
	}
	return string(content), nil
}

// Get implements AttachmentService.
func (f *fakeAttachmentService) Get(_ context.Context, versionID, reqID, attachmentID int64) (*versionapp.Attachment, error) {
	f.calls = append(f.calls, "Get")
	attachment, ok := f.attachments[attachmentID]
	if !ok || attachment.VersionID != versionID || attachment.RequirementID != reqID {
		return nil, domain.NewNotFound("attachment", attachmentID)
	}
	return attachment, nil
}

// OpenContent implements AttachmentService.
func (f *fakeAttachmentService) OpenContent(_ context.Context, versionID, reqID, attachmentID int64) (*versionapp.Attachment, io.ReadSeekCloser, error) {
	f.calls = append(f.calls, "OpenContent")
	if f.openErr != nil {
		return nil, nil, f.openErr
	}
	attachment, err := f.Get(context.Background(), versionID, reqID, attachmentID)
	if err != nil {
		return nil, nil, err
	}
	content := f.contents[attachmentID]
	return attachment, readSeekCloser{bytes.NewReader(content)}, nil
}

// Delete implements AttachmentService.
func (f *fakeAttachmentService) Delete(_ context.Context, versionID, reqID, attachmentID, actor int64) error {
	f.calls = append(f.calls, "Delete")
	f.lastActor = actor
	if f.deleteErr != nil {
		return f.deleteErr
	}
	attachment, err := f.Get(context.Background(), versionID, reqID, attachmentID)
	if err != nil {
		return err
	}
	delete(f.attachments, attachment.ID)
	delete(f.contents, attachment.ID)
	return nil
}

// StoragePathsForRequirement implements AttachmentService and records its call
// position in the shared order log.
func (f *fakeAttachmentService) StoragePathsForRequirement(_ context.Context, versionID, reqID int64) ([]string, error) {
	f.record("StoragePathsForRequirement")
	f.calls = append(f.calls, "StoragePathsForRequirement")
	if f.pathsErr != nil {
		return nil, f.pathsErr
	}
	return append([]string(nil), f.storagePaths...), nil
}

// RemoveStoredFiles implements AttachmentService and records its call position.
func (f *fakeAttachmentService) RemoveStoredFiles(_ context.Context, paths []string) error {
	f.record("RemoveStoredFiles")
	f.calls = append(f.calls, "RemoveStoredFiles")
	f.removedPaths = append([]string(nil), paths...)
	return f.removeErr
}

// testDeps bundles the fakes mounted behind the version routes.
type testDeps struct {
	versions         *fakeVersionService
	versionCounts    *fakeVersionCounts
	catalog          *fakeResourceCatalog
	requirements     *fakeRequirementService
	requirementStats *fakeRequirementStats
	relations        *fakeRelationService
	attachments      *fakeAttachmentService
	clarification    *fakeClarificationService
	messages         *fakeClarificationMessages
	aiReady          *fakeAIReady
	events           *fakeEventSnapshotter
	eventReader      *fakeEventReader
	eventSubscriber  *fakeEventSubscriber
	order            *[]string
}

// newTestDeps builds the default fake dependency set.
func newTestDeps() *testDeps {
	order := make([]string, 0, 4)
	return &testDeps{
		versions:         newFakeVersionService(),
		versionCounts:    &fakeVersionCounts{counts: map[int64]domain.VersionCounts{}},
		catalog:          newFakeResourceCatalog(),
		requirements:     newFakeRequirementService(&order),
		requirementStats: &fakeRequirementStats{open: map[int64]int{}, attachments: map[int64]int{}},
		relations:        &fakeRelationService{},
		attachments:      newFakeAttachmentService(&order),
		clarification:    newFakeClarificationService(),
		messages:         &fakeClarificationMessages{},
		aiReady:          &fakeAIReady{},
		events:           &fakeEventSnapshotter{},
		eventReader:      &fakeEventReader{},
		eventSubscriber:  &fakeEventSubscriber{},
		order:            &order,
	}
}

// newTestServer mounts the version routes behind the real identity middleware,
// which is the only auth layer P3 routes get (no admin guard).
func newTestServer(t *testing.T) (http.Handler, *testDeps) {
	t.Helper()
	deps := newTestDeps()
	handlers := NewHandlers(HandlersDeps{
		Versions:              deps.versions,
		VersionCounts:         deps.versionCounts,
		ResourceCatalog:       deps.catalog,
		Requirements:          deps.requirements,
		RequirementStats:      deps.requirementStats,
		Relations:             deps.relations,
		Attachments:           deps.attachments,
		Clarification:         deps.clarification,
		ClarificationMessages: deps.messages,
		AIReady:               deps.aiReady.Ready,
		Events:                deps.events,
		EventReader:           deps.eventReader,
		EventSubscriber:       deps.eventSubscriber,
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, *handlers)
	return identityhttp.AuthMiddleware(testAuthenticator{})(mux), deps
}

// seedDefaultCatalog seeds one active system, environment and repository.
func seedDefaultCatalog(deps *testDeps) {
	deps.catalog.systems[1] = versionapp.ResourceRef{ID: 1, Name: "Shop", Status: "active", Available: true}
	deps.catalog.environments[2] = versionapp.ResourceRef{
		ID: 2, Name: "staging", Status: "active", SystemID: int64Ptr(1), Available: true,
	}
	deps.catalog.repositories[3] = versionapp.CodeRepoRef{
		ID: 3, Name: "order-web", Status: "active", LocalPath: `C:\repos\order-web`, Available: true,
	}
}

// int64Ptr returns a pointer to value.
func int64Ptr(value int64) *int64 { return &value }

// stringPtr returns a pointer to value.
func stringPtr(value string) *string { return &value }

// doRequest performs one JSON request against the test server.
func doRequest(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// newAuthenticatedRequest builds a request carrying the test bearer token, for
// tests that need to set additional headers before sending it.
func newAuthenticatedRequest(method, path string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer "+testUserToken)
	return request
}

// doRequestWith serves one already built request against the test server.
func doRequestWith(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// doMultipartRequest performs one multipart upload with a single `file` part.
func doMultipartRequest(t *testing.T, handler http.Handler, path, token, filename, contentType string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create multipart part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// doMultipartWithoutFile performs one multipart request with no `file` part.
func doMultipartWithoutFile(t *testing.T, handler http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err := writer.WriteField("other", "value"); err != nil {
		t.Fatalf("write multipart field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// requireStatus asserts the HTTP status.
func requireStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status=%d want %d (body=%s)", recorder.Code, want, recorder.Body.String())
	}
}

// decodeDataMap decodes the {"data": {...}} envelope.
func decodeDataMap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode data envelope %q: %v", recorder.Body.String(), err)
	}
	if envelope.Data == nil {
		t.Fatalf("data is null: %s", recorder.Body.String())
	}
	return envelope.Data
}

// decodeArrayData decodes a {"data": [...]} envelope.
func decodeArrayData(t *testing.T, recorder *httptest.ResponseRecorder) []any {
	t.Helper()
	var envelope struct {
		Data []any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode array envelope %q: %v", recorder.Body.String(), err)
	}
	if envelope.Data == nil {
		t.Fatalf("data is null, want an array: %s", recorder.Body.String())
	}
	return envelope.Data
}

// decodePage decodes the cursor page envelope.
func decodePage(t *testing.T, recorder *httptest.ResponseRecorder) ([]map[string]any, string, bool) {
	t.Helper()
	var envelope struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode page %q: %v", recorder.Body.String(), err)
	}
	if envelope.Data == nil {
		t.Fatalf("page data is null, want an array: %s", recorder.Body.String())
	}
	return envelope.Data, envelope.NextCursor, envelope.HasMore
}

// decodeError decodes the shared error envelope.
func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) platform.APIError {
	t.Helper()
	var envelope struct {
		Error platform.APIError `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", recorder.Body.String(), err)
	}
	return envelope.Error
}

// requireError asserts the status, code and details.reason of an error response
// and returns the decoded envelope.
func requireError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code, reason string) platform.APIError {
	t.Helper()
	requireStatus(t, recorder, status)
	apiError := decodeError(t, recorder)
	if apiError.Code != code {
		t.Fatalf("error code=%q want %q (body=%s)", apiError.Code, code, recorder.Body.String())
	}
	if reason != "" {
		got, _ := apiError.Details["reason"].(string)
		if got != reason {
			t.Fatalf("details.reason=%v want %q (body=%s)", apiError.Details["reason"], reason, recorder.Body.String())
		}
	}
	return apiError
}

// requireJSONString asserts a JSON string field with an exact value. It also
// proves the value was serialized as a string and not as a number.
func requireJSONString(t *testing.T, data map[string]any, key, want string) {
	t.Helper()
	got, ok := data[key].(string)
	if !ok {
		t.Fatalf("%s=%#v want the string %q", key, data[key], want)
	}
	if got != want {
		t.Fatalf("%s=%q want %q", key, got, want)
	}
}

// requireStringField asserts a plain string field.
func requireStringField(t *testing.T, data map[string]any, key, want string) {
	t.Helper()
	requireJSONString(t, data, key, want)
}

// requireJSONNumber asserts a numeric JSON field with an exact integral value.
func requireJSONNumber(t *testing.T, data map[string]any, key string, want int64) {
	t.Helper()
	got, ok := data[key].(float64)
	if !ok {
		t.Fatalf("%s=%#v want the number %d", key, data[key], want)
	}
	if got != float64(want) {
		t.Fatalf("%s=%v want %d", key, got, want)
	}
}

// requireBoolField asserts a boolean JSON field.
func requireBoolField(t *testing.T, data map[string]any, key string, want bool) {
	t.Helper()
	got, ok := data[key].(bool)
	if !ok {
		t.Fatalf("%s=%#v want the bool %v", key, data[key], want)
	}
	if got != want {
		t.Fatalf("%s=%v want %v", key, got, want)
	}
}

// requireMapField asserts an object field and returns it.
func requireMapField(t *testing.T, data map[string]any, key string) map[string]any {
	t.Helper()
	got, ok := data[key].(map[string]any)
	if !ok {
		t.Fatalf("%s=%#v want an object", key, data[key])
	}
	return got
}

// requireArrayField asserts an array field and returns it.
func requireArrayField(t *testing.T, data map[string]any, key string) []any {
	t.Helper()
	got, ok := data[key].([]any)
	if !ok {
		t.Fatalf("%s=%#v want an array", key, data[key])
	}
	return got
}

// requireMissingField asserts a field is absent from the JSON object.
func requireMissingField(t *testing.T, data map[string]any, key string) {
	t.Helper()
	if value, ok := data[key]; ok {
		t.Fatalf("%s=%#v must not be exposed", key, value)
	}
}

// requireNullField asserts an explicit JSON null.
func requireNullField(t *testing.T, data map[string]any, key string) {
	t.Helper()
	value, ok := data[key]
	if !ok || value != nil {
		t.Fatalf("%s=%#v want null", key, value)
	}
}

// recordOrder returns the shared mutating-call order log.
func recordOrder(deps *testDeps) []string {
	return append([]string(nil), (*deps.order)...)
}

// errBoom is a plain infrastructure failure used to prove unknown errors are
// mapped to 500 without leaking their message.
var errBoom = errors.New("boom: infrastructure failure")
