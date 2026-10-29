package db

import (
	"testing"
	"testing/fstest"
)

func TestReadOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/order.txt": {Data: []byte("\r\n0001_a.sql\r\n\r\n 0002_b.sql \n")},
	}
	got, err := readOrder(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0001_a.sql", "0002_b.sql"}
	if len(got) != len(want) {
		t.Fatalf("order=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestReadOrderMissingFile(t *testing.T) {
	if _, err := readOrder(fstest.MapFS{}); err == nil {
		t.Fatal("expected an error when migrations/order.txt is missing")
	}
}

func TestReadOrderRejectsDuplicateEntries(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/order.txt": {Data: []byte("0001_a.sql\n0001_a.sql\n")},
	}
	if _, err := readOrder(fsys); err == nil {
		t.Fatal("expected an error for a duplicate migration")
	}
}

func TestReadOrderRejectsPaths(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/order.txt": {Data: []byte("../0001_a.sql\n")},
	}
	if _, err := readOrder(fsys); err == nil {
		t.Fatal("expected an error for a migration name that is a path")
	}
}
