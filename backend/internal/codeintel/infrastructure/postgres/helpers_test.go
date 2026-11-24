package codeintelpg

import (
	"errors"
	"strings"
	"testing"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWrapDBUniqueViolationIsConflict(t *testing.T) {
	err := wrapDB("create code index run", &pgconn.PgError{Code: "23505"})
	if !errors.Is(err, codeintel.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestWrapDBOtherPgErrorStaysOpaque(t *testing.T) {
	cause := &pgconn.PgError{Code: "23503", Message: "foreign key violation"}
	err := wrapDB("create code anchor", cause)
	if errors.Is(err, codeintel.ErrConflict) {
		t.Fatalf("err = %v, must not be ErrConflict", err)
	}
	if errors.Is(err, codeintel.ErrNotFound) {
		t.Fatalf("err = %v, must not be ErrNotFound", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want wrapped cause", err)
	}
	if !strings.Contains(err.Error(), "create code anchor") {
		t.Fatalf("err = %v, want action context", err)
	}
}

func TestWrapDBNoRowsIsNotFound(t *testing.T) {
	if err := wrapDB("get code index run", pgx.ErrNoRows); !errors.Is(err, codeintel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
