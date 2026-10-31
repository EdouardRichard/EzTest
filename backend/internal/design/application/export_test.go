package designapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeExportStore struct {
	records  []ExportRecord
	contents map[int64][]byte
	inputs   []ExportRecordInput
	nextTime time.Time
}

func newFakeExportStore() *fakeExportStore {
	return &fakeExportStore{
		contents: map[int64][]byte{},
		nextTime: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
}

func (s *fakeExportStore) Create(_ context.Context, in ExportRecordInput) (ExportRecord, error) {
	revision := 1
	for _, existing := range s.records {
		if existing.VersionID == in.VersionID && existing.OwnerKind == in.OwnerKind &&
			existing.OwnerID == in.OwnerID && existing.Format == in.Format {
			revision = existing.Revision + 1
		}
	}
	sum := sha256.Sum256(in.Data)
	record := ExportRecord{
		ID: in.ID, VersionID: in.VersionID, OwnerID: in.OwnerID, OwnerKind: in.OwnerKind,
		Revision: revision, Format: in.Format, ContentObjectID: hex.EncodeToString(sum[:]),
		Size: int64(len(in.Data)), MIME: in.MIME, Filename: in.Filename,
		Status: "ready", CreatedAt: s.nextTime,
	}
	s.records = append(s.records, record)
	s.contents[record.ID] = in.Data
	s.inputs = append(s.inputs, in)
	return record, nil
}

func (s *fakeExportStore) Get(_ context.Context, exportID int64) (ExportRecord, error) {
	for _, record := range s.records {
		if record.ID == exportID {
			return record, nil
		}
	}
	return ExportRecord{}, design.ErrNotFound
}

func (s *fakeExportStore) List(_ context.Context, versionID int64, format string, limit int, beforeID *int64) ([]ExportRecord, bool, error) {
	matches := make([]ExportRecord, 0)
	for index := len(s.records) - 1; index >= 0; index-- {
		record := s.records[index]
		if record.VersionID != versionID {
			continue
		}
		if format != "" && record.Format != format {
			continue
		}
		if beforeID != nil && record.ID >= *beforeID {
			continue
		}
		matches = append(matches, record)
	}
	if len(matches) > limit {
		return matches[:limit], true, nil
	}
	return matches, false, nil
}

func (s *fakeExportStore) Open(_ context.Context, exportID int64) (ExportRecord, io.ReadCloser, error) {
	record, err := s.Get(context.Background(), exportID)
	if err != nil {
		return ExportRecord{}, nil, err
	}
	return record, io.NopCloser(bytes.NewReader(s.contents[exportID])), nil
}

func newExportAppFixture(t *testing.T) (*ExportService, *fakeRepo, *fakeExportStore, *fakePublisher, *fakeAudit) {
	t.Helper()
	repo := newFakeRepo()
	repo.requirement = design.RequirementRef{
		ID: 3001, VersionID: 1001, CurrentVersionID: 3002, Revision: 2,
		Title: "登录", Status: "clarified",
	}
	repo.versionTitle = "登录版本"
	repo.scenarios = []design.Scenario{{
		ID: 5001, VersionID: 1001, RequirementID: 3001, RequirementVersionID: 3002,
		Name: "正常登录", Description: "正常路径", VersionSUTIDs: []int64{9001},
	}}
	seedFakeCase(t, repo, 7001, 5001, "账号密码登录成功", design.CaseStatusDraft)
	seedFakeCase(t, repo, 7002, 5001, "旧用例", design.CaseStatusStale)
	seedFakeCase(t, repo, 7003, 5001, "已废弃用例", design.CaseStatusSuperseded)

	store := newFakeExportStore()
	publisher := &fakePublisher{}
	audit := &fakeAudit{}
	service := NewExportService(repo, store, &fakeIDs{}, newFixedClock(), publisher, audit)
	return service, repo, store, publisher, audit
}

func formatID(value int64) string { return strconv.FormatInt(value, 10) }

func TestExportSkipsSupersededAndIncludesStale(t *testing.T) {
	service, _, store, publisher, audit := newExportAppFixture(t)
	view, err := service.Create(context.Background(), 1001, ExportInput{
		OwnerKind: "case", OwnerID: 1001, Format: design.ExportFormatMarkdown,
	}, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if view.OwnerKind != "case" || view.OwnerID != 1001 || view.Format != design.ExportFormatMarkdown {
		t.Fatalf("view = %+v", view)
	}
	if view.Filename != "case-export-"+formatID(view.ID)+".md" {
		t.Fatalf("filename = %q", view.Filename)
	}
	if view.DownloadURL != "/api/exports/"+formatID(view.ID)+"/download" {
		t.Fatalf("download url = %q", view.DownloadURL)
	}
	if len(store.inputs) != 1 {
		t.Fatalf("store writes = %d", len(store.inputs))
	}
	markdown := string(store.inputs[0].Data)
	if !strings.Contains(markdown, "账号密码登录成功") {
		t.Fatalf("draft case missing:\n%s", markdown)
	}
	if !strings.Contains(markdown, "旧用例") {
		t.Fatalf("stale case missing:\n%s", markdown)
	}
	if strings.Contains(markdown, "已废弃用例") {
		t.Fatalf("superseded case leaked:\n%s", markdown)
	}
	if !strings.Contains(markdown, "# 用例设计：登录版本") {
		t.Fatalf("version title missing:\n%s", markdown)
	}
	events := publisher.byType("design.export_ready")
	if len(events) != 1 || events[0].Payload["export_id"] != formatID(view.ID) {
		t.Fatalf("export events = %v", publisher.types())
	}
	foundAudit := false
	for _, event := range audit.events {
		if event.Action == "export.created" {
			foundAudit = true
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %+v", audit.events)
	}
}

func TestExportRejectsUnsupportedOwnerOrFormat(t *testing.T) {
	service, _, _, _, _ := newExportAppFixture(t)
	for _, input := range []ExportInput{
		{OwnerKind: "report", OwnerID: 1001, Format: design.ExportFormatMarkdown},
		{OwnerKind: "chain", OwnerID: 1001, Format: design.ExportFormatMarkdown},
		{OwnerKind: "case", OwnerID: 9999, Format: design.ExportFormatMarkdown},
		{OwnerKind: "case", OwnerID: 1001, Format: "html"},
		{OwnerKind: "case", OwnerID: 1001, Format: "pdf"},
		{OwnerKind: "case", OwnerID: 1001, Format: "excel"},
	} {
		_, err := service.Create(context.Background(), 1001, input, 1)
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
			t.Fatalf("input %+v err = %v, want invalid_argument", input, err)
		}
	}
}

func TestExportListGetAndOpen(t *testing.T) {
	service, _, store, _, _ := newExportAppFixture(t)
	first, err := service.Create(context.Background(), 1001, ExportInput{
		OwnerKind: "case", OwnerID: 1001, Format: design.ExportFormatCSV,
	}, 1)
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := service.Create(context.Background(), 1001, ExportInput{
		OwnerKind: "case", OwnerID: 1001, Format: design.ExportFormatOPML,
	}, 1)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if second.Revision != 1 {
		t.Fatalf("different formats share revisions: %d", second.Revision)
	}

	page, hasMore, err := service.List(context.Background(), 1001, "", 1, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 1 || !hasMore || page[0].ID != second.ID {
		t.Fatalf("page = %+v hasMore=%v", page, hasMore)
	}
	before := page[0].ID
	next, hasMore, err := service.List(context.Background(), 1001, "", 1, &before)
	if err != nil {
		t.Fatalf("List cursor: %v", err)
	}
	if len(next) != 1 || hasMore || next[0].ID != first.ID {
		t.Fatalf("cursor page = %+v hasMore=%v", next, hasMore)
	}
	filtered, _, err := service.List(context.Background(), 1001, design.ExportFormatCSV, 10, nil)
	if err != nil || len(filtered) != 1 || filtered[0].ID != first.ID {
		t.Fatalf("format filter = %+v, %v", filtered, err)
	}

	loaded, err := service.Get(context.Background(), first.ID)
	if err != nil || loaded.SHA256 != first.SHA256 {
		t.Fatalf("Get = %+v, %v", loaded, err)
	}
	if _, err := service.Get(context.Background(), 999999); err == nil {
		t.Fatal("missing export accepted")
	}
	opened, reader, err := service.Open(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(data, store.contents[first.ID]) {
		t.Fatalf("opened bytes = %d, %v", len(data), err)
	}
	if opened.Filename != first.Filename {
		t.Fatalf("opened view = %+v", opened)
	}
}

func TestExportAllFormatsUseCorrectMIMEAndExtension(t *testing.T) {
	service, _, store, _, _ := newExportAppFixture(t)
	expected := map[string]string{
		design.ExportFormatMarkdown: ".md",
		design.ExportFormatMermaid:  ".mmd",
		design.ExportFormatOPML:     ".opml",
		design.ExportFormatCSV:      ".csv",
	}
	for format, extension := range expected {
		view, err := service.Create(context.Background(), 1001, ExportInput{
			OwnerKind: "case", OwnerID: 1001, Format: format,
		}, 1)
		if err != nil {
			t.Fatalf("Create %s: %v", format, err)
		}
		if !strings.HasSuffix(view.Filename, extension) {
			t.Fatalf("format %s filename = %q", format, view.Filename)
		}
		if view.MIME != design.ExportMIME(format) {
			t.Fatalf("format %s mime = %q", format, view.MIME)
		}
		if len(store.inputs[len(store.inputs)-1].Data) == 0 {
			t.Fatalf("format %s rendered empty content", format)
		}
	}
}
