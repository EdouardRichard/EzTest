package execution

import (
	"testing"
	"time"
)

func TestSessionFixtureValidate(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	valid := SessionFixture{
		ID: 1, VersionID: 7, SUTID: 10, EnvironmentID: 20,
		Role: "admin", StorageStateRef: "state:sha256:abc",
		CreatedAt: at, CreatedBy: 42,
	}
	cases := []struct {
		name    string
		mutate  func(*SessionFixture)
		wantErr bool
		field   string
	}{
		{name: "valid fixture", mutate: func(*SessionFixture) {}},
		{name: "missing version", mutate: func(f *SessionFixture) { f.VersionID = 0 }, wantErr: true, field: "version_id"},
		{name: "missing sut", mutate: func(f *SessionFixture) { f.SUTID = 0 }, wantErr: true, field: "sut_id"},
		{name: "missing environment", mutate: func(f *SessionFixture) { f.EnvironmentID = 0 }, wantErr: true, field: "environment_id"},
		{name: "empty role", mutate: func(f *SessionFixture) { f.Role = "" }, wantErr: true, field: "role"},
		{name: "empty storage state ref", mutate: func(f *SessionFixture) { f.StorageStateRef = "" }, wantErr: true, field: "storage_state_ref"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := valid
			tc.mutate(&fixture)
			err := fixture.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertFactField(t, err, tc.field)
			}
		})
	}
}
