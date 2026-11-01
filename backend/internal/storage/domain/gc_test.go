package storage_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

func validSHA() string { return strings.Repeat("ab", 32) }

func TestParseGCMode(t *testing.T) {
	cases := []struct {
		raw  string
		want storage.GCMode
	}{
		{"dry_run", storage.GCModeDryRun},
		{" apply ", storage.GCModeApply},
	}
	for _, tc := range cases {
		got, err := storage.ParseGCMode(tc.raw)
		if err != nil {
			t.Fatalf("ParseGCMode(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseGCMode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	for _, raw := range []string{"", "APPLY", "dryrun", "delete"} {
		if _, err := storage.ParseGCMode(raw); err == nil {
			t.Fatalf("ParseGCMode(%q) did not fail", raw)
		} else {
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_argument" {
				t.Fatalf("ParseGCMode(%q) err = %#v", raw, err)
			}
		}
	}
}

func TestGCModeConfirmation(t *testing.T) {
	if storage.GCModeDryRun.RequiresConfirmation() {
		t.Fatal("dry_run must not require confirmation")
	}
	if !storage.GCModeApply.RequiresConfirmation() {
		t.Fatal("apply must require confirmation")
	}
	if !storage.ValidGCMode(storage.GCModeApply) || storage.ValidGCMode("destroy") {
		t.Fatal("ValidGCMode returned the wrong verdict")
	}
}

func TestValidGCCandidateClass(t *testing.T) {
	for _, class := range []string{
		storage.GCCandidateUnreferenced,
		storage.GCCandidateTemp,
		storage.GCCandidateSupersededExport,
		storage.GCCandidateOrphanFile,
	} {
		if !storage.ValidGCCandidateClass(class) {
			t.Fatalf("class %q should be valid", class)
		}
	}
	for _, class := range []string{"", "orphan_ref", "SUPERSEDED_EXPORT"} {
		if storage.ValidGCCandidateClass(class) {
			t.Fatalf("class %q should be invalid", class)
		}
	}
}

func TestGCCandidateValidate(t *testing.T) {
	base := storage.GCCandidate{
		ObjectSHA:      validSHA(),
		Size:           10,
		MIME:           "text/plain",
		RelativePath:   "objects/ab/ab/" + validSHA(),
		Class:          storage.GCCandidateUnreferenced,
		AgeSeconds:     3600,
		DeleteObject:   true,
		DeletionReason: storage.GCCandidateUnreferenced,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid candidate: %v", err)
	}

	orphan := base
	orphan.Class = storage.GCCandidateOrphanFile
	orphan.RefID = 7
	orphan.OwnerKind = "evidence"
	orphan.OwnerID = 9
	if err := orphan.Validate(); err != nil {
		t.Fatalf("valid orphan candidate: %v", err)
	}

	cases := map[string]func(*storage.GCCandidate){
		"bad sha":           func(c *storage.GCCandidate) { c.ObjectSHA = "nope" },
		"negative size":     func(c *storage.GCCandidate) { c.Size = -1 },
		"negative age":      func(c *storage.GCCandidate) { c.AgeSeconds = -1 },
		"empty relative":    func(c *storage.GCCandidate) { c.RelativePath = "" },
		"empty class":       func(c *storage.GCCandidate) { c.Class = "" },
		"unknown class":     func(c *storage.GCCandidate) { c.Class = "orphan_ref" },
		"unreferenced ref":  func(c *storage.GCCandidate) { c.RefID = 3 },
		"temp ref":          func(c *storage.GCCandidate) { c.Class = storage.GCCandidateTemp; c.RefID = 3 },
		"orphan no ref":     func(c *storage.GCCandidate) { c.Class = storage.GCCandidateOrphanFile },
		"superseded no ref": func(c *storage.GCCandidate) { c.Class = storage.GCCandidateSupersededExport; c.ExportArtifactID = 4 },
		"superseded no art": func(c *storage.GCCandidate) { c.Class = storage.GCCandidateSupersededExport; c.RefID = 4 },
	}
	for name, mutate := range cases {
		candidate := base
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("%s: candidate should be invalid", name)
		}
	}
}

func TestGCRunValidate(t *testing.T) {
	actor := int64(5)
	run := storage.GCRun{
		ID:        11,
		Mode:      storage.GCModeApply,
		Status:    storage.GCRunRunning,
		ActorID:   &actor,
		ActorKind: storage.GCActorUser,
		StartedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Report:    json.RawMessage(`{}`),
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("valid run: %v", err)
	}
	if run.IsTerminal() {
		t.Fatal("running run reported terminal")
	}
	run.Status = storage.GCRunCompleted
	if !run.IsTerminal() {
		t.Fatal("completed run not terminal")
	}

	cases := map[string]func(*storage.GCRun){
		"zero id":          func(r *storage.GCRun) { r.ID = 0 },
		"bad mode":         func(r *storage.GCRun) { r.Mode = "gc" },
		"bad status":       func(r *storage.GCRun) { r.Status = "done" },
		"bad actor kind":   func(r *storage.GCRun) { r.ActorKind = "robot" },
		"negative scanned": func(r *storage.GCRun) { r.ScannedObjects = -1 },
		"negative deleted": func(r *storage.GCRun) { r.DeletedBytes = -1 },
	}
	for name, mutate := range cases {
		bad := run
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatalf("%s: run should be invalid", name)
		}
	}

	if storage.ValidGCRunStatus(storage.GCRunFailed) != true || storage.ValidGCRunStatus("running ") {
		t.Fatal("ValidGCRunStatus returned the wrong verdict")
	}
}

func TestGCTombstoneValidate(t *testing.T) {
	tombstone := storage.GCTombstone{
		ID:           1,
		RunID:        2,
		ObjectSHA:    validSHA(),
		Size:         12,
		MIME:         "text/plain",
		RelativePath: "objects/ab/ab/" + validSHA(),
		Reason:       storage.GCCandidateOrphanFile,
		DeletedAt:    time.Date(2026, 5, 1, 1, 0, 0, 0, time.UTC),
	}
	if err := tombstone.Validate(); err != nil {
		t.Fatalf("valid tombstone: %v", err)
	}
	cases := map[string]func(*storage.GCTombstone){
		"zero run":     func(t *storage.GCTombstone) { t.RunID = 0 },
		"bad sha":      func(t *storage.GCTombstone) { t.ObjectSHA = "zz" },
		"negative":     func(t *storage.GCTombstone) { t.Size = -3 },
		"empty path":   func(t *storage.GCTombstone) { t.RelativePath = "" },
		"empty mime":   func(t *storage.GCTombstone) { t.MIME = "" },
		"bad reason":   func(t *storage.GCTombstone) { t.Reason = "orphan_ref" },
		"zero deleted": func(t *storage.GCTombstone) { t.DeletedAt = time.Time{} },
	}
	for name, mutate := range cases {
		bad := tombstone
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatalf("%s: tombstone should be invalid", name)
		}
	}
}
