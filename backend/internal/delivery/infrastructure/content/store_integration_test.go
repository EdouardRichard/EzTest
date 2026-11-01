//go:build integration

package content_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/content"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	storagepg "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// contentTestSettings serves the storage data root setting from a temp dir.
type contentTestSettings struct{ root string }

func (s contentTestSettings) String(context.Context, string) (string, error) { return s.root, nil }

type contentFixture struct {
	store *content.Store
	pool  *pgxpool.Pool
	root  string
}

// newContentFixture wires the real storage service (PostgreSQL ledger + file
// store) behind the delivery report content adapter.
func newContentFixture(t *testing.T) *contentFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	root := t.TempDir()
	ids, err := id.NewSnowflake(0)
	if err != nil {
		t.Fatalf("new snowflake: %v", err)
	}
	svc := storageapp.New(
		storagepg.New(pool),
		storagepg.NewFileStore(),
		contentTestSettings{root: root},
		ids,
		nil,
		nil,
	)
	store := content.New(svc, deliverypg.NewContentRefReader(pool))
	return &contentFixture{store: store, pool: pool, root: root}
}

func contentSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var sampleReportHTML = []byte(`<!doctype html><html lang="zh-CN"><body><h1>报告</h1></body></html>`)

// TestContentPutReportHTMLWritesLedger covers the content_object + content_ref
// write contract: owner_kind=report_revision, owner_id=revision.id,
// role=report_html and the returned ref/sha/size.
func TestContentPutReportHTMLWritesLedger(t *testing.T) {
	fixture := newContentFixture(t)
	ctx := context.Background()

	refID, sha, size, err := fixture.store.PutReportHTML(ctx, 9001, sampleReportHTML)
	if err != nil {
		t.Fatalf("PutReportHTML: %v", err)
	}
	if refID <= 0 {
		t.Fatalf("content ref ID = %d", refID)
	}
	if want := contentSHA(sampleReportHTML); sha != want {
		t.Fatalf("sha = %q, want %q", sha, want)
	}
	if size != int64(len(sampleReportHTML)) {
		t.Fatalf("size = %d, want %d", size, len(sampleReportHTML))
	}

	var (
		storedSHA, mime, ownerKind, role string
		storedSize, ownerID              int64
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT o.sha256, o.size, o.mime, r.owner_kind, r.owner_id, r.role
		FROM content_ref r
		JOIN content_object o ON o.sha256 = r.object_id
		WHERE r.id = $1
	`, refID).Scan(&storedSHA, &storedSize, &mime, &ownerKind, &ownerID, &role); err != nil {
		t.Fatalf("read content ledger: %v", err)
	}
	if storedSHA != sha || storedSize != size {
		t.Fatalf("ledger object = %s/%d, want %s/%d", storedSHA, storedSize, sha, size)
	}
	if mime != "text/html; charset=utf-8" {
		t.Fatalf("mime = %q, want text/html; charset=utf-8", mime)
	}
	if ownerKind != "report_revision" || ownerID != 9001 || role != "report_html" {
		t.Fatalf("content_ref = owner_kind:%s owner_id:%d role:%s", ownerKind, ownerID, role)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE content_ref SET deleted_at = now() WHERE id = $1
	`, refID); err != nil {
		t.Fatalf("soft delete ref: %v", err)
	}
}

// contentFixturePublishRevision makes the content ref eligible for the §6.4
// read path: a published delivery_report_revision must point html_ref at it.
func contentFixturePublishRevision(t *testing.T, fixture *contentFixture, revisionID, refID int64) {
	t.Helper()
	ctx := context.Background()
	reportID := revisionID + 1
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO delivery_report (id, version_id, kind, status)
		VALUES ($1, 1, 'version', 'published')
	`, reportID); err != nil {
		t.Fatalf("insert report: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO delivery_report_revision (
			id, report_id, revision, status, html_ref, input_fingerprint,
			as_of, render_status
		)
		VALUES ($1, $2, 1, 'published', $3, repeat('a', 64), now(), 'done')
	`, revisionID, reportID, refID); err != nil {
		t.Fatalf("insert report revision: %v", err)
	}
}

// contentFixturePublishPDFRevision inserts the published revision that owns a
// stored PDF through pdf_ref (P7). html_ref is deliberately absent: the report
// HTML lives elsewhere and the PDF read path must validate pdf_ref ownership.
func contentFixturePublishPDFRevision(t *testing.T, fixture *contentFixture, revisionID, pdfRefID int64) {
	t.Helper()
	ctx := context.Background()
	reportID := revisionID + 1
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO delivery_report (id, version_id, kind, status)
		VALUES ($1, 1, 'version', 'published')
	`, reportID); err != nil {
		t.Fatalf("insert report: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO delivery_report_revision (
			id, report_id, revision, status, html_ref, pdf_ref, input_fingerprint,
			as_of, render_status, pdf_status
		)
		VALUES ($1, $2, 1, 'published', NULL, $3, repeat('a', 64), now(), 'done', 'done')
	`, revisionID, reportID, pdfRefID); err != nil {
		t.Fatalf("insert report PDF revision: %v", err)
	}
}

// TestContentOpenReportHTMLValidatesOwnershipAndBytes covers the read contract:
// owner/role/sha are re-validated and every mismatch fails closed.
func TestContentOpenReportHTMLValidatesOwnershipAndBytes(t *testing.T) {
	fixture := newContentFixture(t)
	ctx := context.Background()
	refID, sha, size, err := fixture.store.PutReportHTML(ctx, 9002, sampleReportHTML)
	if err != nil {
		t.Fatalf("PutReportHTML: %v", err)
	}
	contentFixturePublishRevision(t, fixture, 9002, refID)

	opened, openedSHA, openedSize, err := fixture.store.OpenReportHTML(ctx, refID)
	if err != nil {
		t.Fatalf("OpenReportHTML: %v", err)
	}
	if !bytes.Equal(opened, sampleReportHTML) || openedSHA != sha || openedSize != size {
		t.Fatalf("OpenReportHTML = %q/%s/%d", opened, openedSHA, openedSize)
	}

	if _, _, _, err := fixture.store.OpenReportHTML(ctx, 999999); !errors.Is(err, deliveryapp.ErrReportContentNotFound) {
		t.Fatalf("missing ref error = %v, want ErrReportContentNotFound", err)
	}

	for _, tc := range []struct {
		name  string
		query string
		arg   any
	}{
		{"owner kind", `UPDATE content_ref SET owner_kind = 'evidence' WHERE id = $1`, refID},
		{"owner id", `UPDATE content_ref SET owner_id = 7777 WHERE id = $1`, refID},
		{"role", `UPDATE content_ref SET role = 'attachment' WHERE id = $1`, refID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Undo previous tampering first, then break exactly one column.
			if _, err := fixture.pool.Exec(ctx, `
				UPDATE content_ref
				SET owner_kind = 'report_revision', owner_id = 9002, role = 'report_html'
				WHERE id = $1
			`, refID); err != nil {
				t.Fatalf("reset ref: %v", err)
			}
			if _, err := fixture.pool.Exec(ctx, tc.query, tc.arg); err != nil {
				t.Fatalf("tamper ref: %v", err)
			}
			if _, _, _, err := fixture.store.OpenReportHTML(ctx, refID); !errors.Is(err, deliveryapp.ErrReportContentCorrupt) {
				t.Fatalf("OpenReportHTML error = %v, want ErrReportContentCorrupt", err)
			}
		})
	}

	// Restore a valid ref, then corrupt the stored bytes: the storage service
	// detects the hash/size mismatch and the adapter reports it as corrupt.
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE content_ref
		SET owner_kind = 'report_revision', owner_id = 9002, role = 'report_html'
		WHERE id = $1
	`, refID); err != nil {
		t.Fatalf("restore ref: %v", err)
	}
	var relativePath string
	if err := fixture.pool.QueryRow(ctx, `SELECT relative_path FROM content_object WHERE sha256 = $1`, sha).Scan(&relativePath); err != nil {
		t.Fatalf("read relative path: %v", err)
	}
	path := filepath.Join(fixture.root, filepath.FromSlash(relativePath))
	if err := os.WriteFile(path, []byte("tampered report bytes"), 0o600); err != nil {
		t.Fatalf("tamper report file: %v", err)
	}
	if _, _, _, err := fixture.store.OpenReportHTML(ctx, refID); !errors.Is(err, deliveryapp.ErrReportContentCorrupt) {
		t.Fatalf("tampered bytes error = %v, want ErrReportContentCorrupt", err)
	}
	if _, _, _, err := fixture.store.OpenReportHTML(ctx, refID); !errors.Is(err, deliveryapp.ErrReportContentCorrupt) {
		t.Fatalf("second tampered read error = %v, want ErrReportContentCorrupt", err)
	}
}

// TestContentPutReportHTMLValidatesInput covers the pre-write argument checks.
func TestContentPutReportHTMLValidatesInput(t *testing.T) {
	fixture := newContentFixture(t)
	ctx := context.Background()
	if _, _, _, err := fixture.store.PutReportHTML(ctx, 0, sampleReportHTML); err == nil {
		t.Fatal("zero revision ID must be rejected")
	}
	if _, _, _, err := fixture.store.PutReportHTML(ctx, 9003, nil); err == nil {
		t.Fatal("empty HTML must be rejected")
	}
	var count int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM content_ref`).Scan(&count); err != nil {
		t.Fatalf("count refs: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid PutReportHTML wrote %d refs", count)
	}
	// A ledger lookup error is classified as not found, never as success.
	if _, _, _, err := fixture.store.OpenReportHTML(ctx, 0); err == nil {
		t.Fatal("zero ref ID must be rejected")
	}
}

/* ----------------------------- P7 report PDF ------------------------------ */

var sampleReportPDF = []byte("%PDF-1.7\n" + "renderer bytes\n")

// TestContentPutReportPDFWritesLedger covers the P7 PDF write contract:
// owner_kind=report_revision, owner_id=revision.id, role=report_pdf, MIME
// application/pdf and the returned ref/sha/size.
func TestContentPutReportPDFWritesLedger(t *testing.T) {
	fixture := newContentFixture(t)
	ctx := context.Background()

	ref, err := fixture.store.PutReportPDF(ctx, 9101, sampleReportPDF)
	if err != nil {
		t.Fatalf("PutReportPDF: %v", err)
	}
	if ref.RefID <= 0 || ref.SHA256 != contentSHA(sampleReportPDF) || ref.Size != int64(len(sampleReportPDF)) {
		t.Fatalf("PutReportPDF = %+v", ref)
	}

	var (
		storedSHA, mime, ownerKind, role string
		storedSize, ownerID              int64
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT o.sha256, o.size, o.mime, r.owner_kind, r.owner_id, r.role
		FROM content_ref r
		JOIN content_object o ON o.sha256 = r.object_id
		WHERE r.id = $1
	`, ref.RefID).Scan(&storedSHA, &storedSize, &mime, &ownerKind, &ownerID, &role); err != nil {
		t.Fatalf("read PDF content ledger: %v", err)
	}
	if storedSHA != ref.SHA256 || storedSize != ref.Size {
		t.Fatalf("ledger object = %s/%d, want %s/%d", storedSHA, storedSize, ref.SHA256, ref.Size)
	}
	if mime != "application/pdf" {
		t.Fatalf("mime = %q, want application/pdf", mime)
	}
	if ownerKind != "report_revision" || ownerID != 9101 || role != "report_pdf" {
		t.Fatalf("content_ref = owner_kind:%s owner_id:%d role:%s", ownerKind, ownerID, role)
	}
}

// TestContentOpenReportPDFValidatesOwnershipAndBytes covers the P7 PDF read
// contract: pdf_ref ownership/role/sha/size are re-validated and every mismatch
// fails closed.
func TestContentOpenReportPDFValidatesOwnershipAndBytes(t *testing.T) {
	fixture := newContentFixture(t)
	ctx := context.Background()
	ref, err := fixture.store.PutReportPDF(ctx, 9102, sampleReportPDF)
	if err != nil {
		t.Fatalf("PutReportPDF: %v", err)
	}
	contentFixturePublishPDFRevision(t, fixture, 9102, ref.RefID)

	opened, openedSHA, openedSize, err := fixture.store.OpenReportPDF(ctx, ref.RefID)
	if err != nil {
		t.Fatalf("OpenReportPDF: %v", err)
	}
	if !bytes.Equal(opened, sampleReportPDF) || openedSHA != ref.SHA256 || openedSize != ref.Size {
		t.Fatalf("OpenReportPDF = %q/%s/%d", opened, openedSHA, openedSize)
	}
	if _, _, _, err := fixture.store.OpenReportPDF(ctx, 999999); !errors.Is(err, deliveryapp.ErrReportContentNotFound) {
		t.Fatalf("missing ref error = %v, want ErrReportContentNotFound", err)
	}

	for _, tc := range []struct {
		name  string
		query string
		arg   any
	}{
		{"owner kind", `UPDATE content_ref SET owner_kind = 'evidence' WHERE id = $1`, ref.RefID},
		{"owner id", `UPDATE content_ref SET owner_id = 7777 WHERE id = $1`, ref.RefID},
		{"role", `UPDATE content_ref SET role = 'report_html' WHERE id = $1`, ref.RefID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fixture.pool.Exec(ctx, `
				UPDATE content_ref
				SET owner_kind = 'report_revision', owner_id = 9102, role = 'report_pdf'
				WHERE id = $1
			`, ref.RefID); err != nil {
				t.Fatalf("reset ref: %v", err)
			}
			if _, err := fixture.pool.Exec(ctx, tc.query, tc.arg); err != nil {
				t.Fatalf("tamper ref: %v", err)
			}
			if _, _, _, err := fixture.store.OpenReportPDF(ctx, ref.RefID); !errors.Is(err, deliveryapp.ErrReportContentCorrupt) {
				t.Fatalf("OpenReportPDF error = %v, want ErrReportContentCorrupt", err)
			}
		})
	}

	// The HTML reader must reject a PDF ref (role mismatch) and vice versa.
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE content_ref SET role = 'report_pdf' WHERE id = $1
	`, ref.RefID); err != nil {
		t.Fatalf("restore PDF ref: %v", err)
	}
	if _, _, _, err := fixture.store.OpenReportHTML(ctx, ref.RefID); !errors.Is(err, deliveryapp.ErrReportContentCorrupt) {
		t.Fatalf("OpenReportHTML on PDF ref error = %v, want ErrReportContentCorrupt", err)
	}

	// Corrupted stored bytes are detected by the storage service.
	var relativePath string
	if err := fixture.pool.QueryRow(ctx, `SELECT relative_path FROM content_object WHERE sha256 = $1`, ref.SHA256).Scan(&relativePath); err != nil {
		t.Fatalf("read relative path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, filepath.FromSlash(relativePath)), []byte("tampered pdf bytes"), 0o600); err != nil {
		t.Fatalf("tamper PDF file: %v", err)
	}
	if _, _, _, err := fixture.store.OpenReportPDF(ctx, ref.RefID); !errors.Is(err, deliveryapp.ErrReportContentCorrupt) {
		t.Fatalf("tampered PDF error = %v, want ErrReportContentCorrupt", err)
	}
}

func TestContentPutReportPDFValidatesInput(t *testing.T) {
	fixture := newContentFixture(t)
	ctx := context.Background()
	if _, err := fixture.store.PutReportPDF(ctx, 0, sampleReportPDF); err == nil {
		t.Fatal("zero revision ID must be rejected")
	}
	if _, err := fixture.store.PutReportPDF(ctx, 9103, nil); err == nil {
		t.Fatal("empty PDF must be rejected")
	}
	var count int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM content_ref`).Scan(&count); err != nil {
		t.Fatalf("count refs: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid PutReportPDF wrote %d refs", count)
	}
}

// TestContentGetReportHTML covers the worker's frozen-HTML read helper.
func TestContentGetReportHTML(t *testing.T) {
	fixture := newContentFixture(t)
	ctx := context.Background()
	refID, _, _, err := fixture.store.PutReportHTML(ctx, 9104, sampleReportHTML)
	if err != nil {
		t.Fatalf("PutReportHTML: %v", err)
	}
	contentFixturePublishRevision(t, fixture, 9104, refID)
	html, err := fixture.store.GetReportHTML(ctx, refID)
	if err != nil {
		t.Fatalf("GetReportHTML: %v", err)
	}
	if !bytes.Equal(html, sampleReportHTML) {
		t.Fatalf("GetReportHTML = %q", html)
	}
	if _, err := fixture.store.GetReportHTML(ctx, 999999); !errors.Is(err, deliveryapp.ErrReportContentNotFound) {
		t.Fatalf("missing GetReportHTML error = %v, want ErrReportContentNotFound", err)
	}
}
