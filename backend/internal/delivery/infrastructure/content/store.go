// Package content adapts the P0 content ledger to the delivery report content
// port. Report HTML bytes live in the ledger; report_revision rows only carry
// the content_ref ID, never a filesystem path.
package content

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagedomain "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

const (
	// OwnerKindReportRevision is the content_ref owner kind of report HTML/PDF.
	OwnerKindReportRevision = "report_revision"
	// RoleReportHTML is the content_ref role of report HTML.
	RoleReportHTML = "report_html"
	// ReportHTMLMIME is the declared report HTML content type.
	ReportHTMLMIME = "text/html; charset=utf-8"
	// RoleReportPDF is the content_ref role of a rendered report PDF (P7).
	RoleReportPDF = "report_pdf"
	// ReportPDFMIME is the declared report PDF content type.
	ReportPDFMIME = "application/pdf"
)

// Ref is one content_ref row resolved by ID for the report read path.
// OwnedByRevision reports that a delivery_report_revision whose id equals
// OwnerID points its html_ref at this exact ref, which is how the adapter
// validates the owner_id half of the §6.4 ownership contract.
type Ref struct {
	ObjectID        string
	OwnerKind       string
	OwnerID         int64
	Role            string
	OwnedByRevision bool
}

// RefReader resolves content_ref rows by ID. deliverypg implements it; the
// storage application service exposes no by-ID ref lookup.
type RefReader interface {
	GetContentRef(ctx context.Context, refID int64) (Ref, error)
}

// ContentService is the P0 content-ledger surface consumed by this adapter
// (*storageapp.Service satisfies it).
type ContentService interface {
	Upload(ctx context.Context, filename, declaredMIME string, r io.Reader, maxBytes int64, actorID int64) (storagedomain.Object, error)
	AddRef(ctx context.Context, objectID, ownerKind string, ownerID int64, role string) (storagedomain.Ref, error)
	Open(ctx context.Context, sha string) (storagedomain.Object, io.ReadCloser, error)
}

// Store implements deliveryapp.ContentStore on top of the content ledger.
type Store struct {
	storage ContentService
	refs    RefReader
}

var _ deliveryapp.ContentStore = (*Store)(nil)

// New wires the report content adapter.
func New(storage ContentService, refs RefReader) *Store {
	return &Store{storage: storage, refs: refs}
}

// PutReportHTML uploads the report bytes (content-addressed, de-duplicated by
// SHA-256) and records content_ref(owner_kind='report_revision',
// owner_id=reportRevisionID, role='report_html'). It returns the ref ID, hash
// and size stored in the ledger.
func (s *Store) PutReportHTML(ctx context.Context, reportRevisionID int64, html []byte) (int64, string, int64, error) {
	if reportRevisionID <= 0 {
		return 0, "", 0, platform.Invalid("report_revision_id", "must be a positive decimal ID")
	}
	if len(html) == 0 {
		return 0, "", 0, platform.Invalid("html", "report HTML is required")
	}
	if s == nil || s.storage == nil {
		return 0, "", 0, platform.NewError(http.StatusInternalServerError, "internal", "content service is not configured")
	}
	filename := fmt.Sprintf("report-%d.html", reportRevisionID)
	object, err := s.storage.Upload(ctx, filename, ReportHTMLMIME, bytes.NewReader(html), storageapp.MaxUploadBytes, 0)
	if err != nil {
		return 0, "", 0, err
	}
	ref, err := s.storage.AddRef(ctx, object.SHA256, OwnerKindReportRevision, reportRevisionID, RoleReportHTML)
	if err != nil {
		return 0, "", 0, err
	}
	return ref.ID, object.SHA256, object.Size, nil
}

// OpenReportHTML loads one report content_ref, re-validates that it is owned
// by report_revision/report_html, and reads the verified content bytes. A
// missing ledger row is ErrReportContentNotFound; an owner/role mismatch or a
// SHA-256/size mismatch is ErrReportContentCorrupt (mapped to 409 by the
// service). Storage paths never leave the ledger.
func (s *Store) OpenReportHTML(ctx context.Context, contentRefID int64) ([]byte, string, int64, error) {
	return s.openReportContent(ctx, contentRefID, RoleReportHTML)
}

// GetReportHTML loads the verified HTML bytes of one frozen report revision for
// the PDF worker (RenderContent). It applies the same ledger validation as
// OpenReportHTML.
func (s *Store) GetReportHTML(ctx context.Context, contentRefID int64) ([]byte, error) {
	data, _, _, err := s.OpenReportHTML(ctx, contentRefID)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// PutReportPDF uploads the rendered PDF (content-addressed) and records
// content_ref(owner_kind='report_revision', owner_id=reportRevisionID,
// role='report_pdf'). The authoritative HTML is untouched.
func (s *Store) PutReportPDF(ctx context.Context, reportRevisionID int64, pdf []byte) (deliveryapp.ReportContentRef, error) {
	if reportRevisionID <= 0 {
		return deliveryapp.ReportContentRef{}, platform.Invalid("report_revision_id", "must be a positive decimal ID")
	}
	if len(pdf) == 0 {
		return deliveryapp.ReportContentRef{}, platform.Invalid("pdf", "report PDF is required")
	}
	if s == nil || s.storage == nil {
		return deliveryapp.ReportContentRef{}, platform.NewError(http.StatusInternalServerError, "internal", "content service is not configured")
	}
	filename := fmt.Sprintf("report-%d.pdf", reportRevisionID)
	object, err := s.storage.Upload(ctx, filename, ReportPDFMIME, bytes.NewReader(pdf), storageapp.MaxUploadBytes, 0)
	if err != nil {
		return deliveryapp.ReportContentRef{}, err
	}
	ref, err := s.storage.AddRef(ctx, object.SHA256, OwnerKindReportRevision, reportRevisionID, RoleReportPDF)
	if err != nil {
		return deliveryapp.ReportContentRef{}, err
	}
	return deliveryapp.ReportContentRef{RefID: ref.ID, SHA256: object.SHA256, Size: object.Size}, nil
}

// OpenReportPDF loads one stored PDF content_ref, re-validates that it is owned
// by report_revision/report_pdf and reads the verified bytes. The service
// additionally validates the %PDF- magic.
func (s *Store) OpenReportPDF(ctx context.Context, contentRefID int64) ([]byte, string, int64, error) {
	return s.openReportContent(ctx, contentRefID, RoleReportPDF)
}

// openReportContent is the shared verified read of one report content role.
func (s *Store) openReportContent(ctx context.Context, contentRefID int64, role string) ([]byte, string, int64, error) {
	if contentRefID <= 0 {
		return nil, "", 0, platform.Invalid("content_ref_id", "must be a positive decimal ID")
	}
	if s == nil || s.refs == nil || s.storage == nil {
		return nil, "", 0, platform.NewError(http.StatusInternalServerError, "internal", "content ledger is not configured")
	}
	ref, err := s.refs.GetContentRef(ctx, contentRefID)
	if err != nil {
		if errors.Is(err, deliveryapp.ErrReportContentNotFound) {
			return nil, "", 0, deliveryapp.ErrReportContentNotFound
		}
		return nil, "", 0, err
	}
	if ref.OwnerKind != OwnerKindReportRevision || ref.OwnerID <= 0 || ref.Role != role || !ref.OwnedByRevision {
		return nil, "", 0, fmt.Errorf("%w: content_ref %d owner/role mismatch", deliveryapp.ErrReportContentCorrupt, contentRefID)
	}
	object, reader, err := s.storage.Open(ctx, ref.ObjectID)
	if err != nil {
		return nil, "", 0, mapStorageError(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, storageapp.MaxUploadBytes+1))
	if err != nil {
		return nil, "", 0, fmt.Errorf("%w: read report content: %v", deliveryapp.ErrReportContentCorrupt, err)
	}
	if int64(len(data)) != object.Size || object.SHA256 != ref.ObjectID {
		return nil, "", 0, fmt.Errorf("%w: content_ref %d sha/size mismatch", deliveryapp.ErrReportContentCorrupt, contentRefID)
	}
	return data, object.SHA256, object.Size, nil
}

// mapStorageError translates the content ledger's integrity/not-found errors
// into the report content sentinels consumed by the service.
func mapStorageError(err error) error {
	if errors.Is(err, storagedomain.ErrCorrupt) {
		return fmt.Errorf("%w: %v", deliveryapp.ErrReportContentCorrupt, err)
	}
	if apiErr := platform.AsError(err); apiErr.Status == http.StatusNotFound {
		return deliveryapp.ErrReportContentNotFound
	}
	return err
}
