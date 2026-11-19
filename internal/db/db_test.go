package db_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
)

// testPassword is an obviously fake password.
const testPassword = "dev-only-db-test-password" //nolint:gosec // G101: an obviously fake fixture value

func TestDSN_Redacted(t *testing.T) {
	dsn := db.DSN("postgres://se_app:" + testPassword + "@localhost:5432/x")
	type holder struct {
		DSN db.DSN
	}
	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("opening", "dsn", dsn)
	jsonDSN, err := json.Marshal(dsn)
	if err != nil {
		t.Fatalf("json.Marshal(dsn) error = %v", err)
	}
	jsonHolder, err := json.Marshal(holder{DSN: dsn})
	if err != nil {
		t.Fatalf("json.Marshal(holder) error = %v", err)
	}

	outputs := map[string]string{
		"%v":             fmt.Sprintf("%v", dsn),
		"%+v":            fmt.Sprintf("%+v", dsn),
		"%#v":            fmt.Sprintf("%#v", dsn),
		"%s":             fmt.Sprintf("%s", dsn),
		"%v of a struct": fmt.Sprintf("%+v", holder{DSN: dsn}),
		"slog":           logBuf.String(),
		"json":           string(jsonDSN),
		"json struct":    string(jsonHolder),
	}
	for name, got := range outputs {
		if strings.Contains(got, testPassword) {
			t.Errorf("%s of a DSN = %q, want no password", name, got)
		}
	}
}

func TestOpen_ErrorHidesDSN(t *testing.T) {
	// Port 1 on the loopback address refuses connections.
	dsn := db.DSN("postgres://se_app:" + testPassword + "@127.0.0.1:1/x?connect_timeout=2")
	s, err := db.Open(t.Context(), dsn)
	if err == nil {
		s.Close()
		t.Fatal("Open(unreachable) error = nil, want an error")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("Open(unreachable) error = %q, want no password", err)
	}

	_, err = db.Open(t.Context(), db.DSN("postgres://se_app:"+testPassword+"@127.0.0.1:notaport/x"))
	if err == nil {
		t.Fatal("Open(malformed) error = nil, want an error")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("Open(malformed) error = %q, want no password", err)
	}
}

func TestMigrate_AppliesOnce(t *testing.T) {
	s := db.NewTestStore(t)
	ctx := t.Context()
	for i := range 2 {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() call %d error = %v", i+1, err)
		}
	}
	got, err := s.Count(ctx, "schema_migration")
	if err != nil {
		t.Fatalf("counting schema_migration: %v", err)
	}
	if want := db.MigrationCount(); got != want {
		t.Errorf("rows in schema_migration after two Migrate calls = %d, want %d", got, want)
	}
	if _, err := s.Count(ctx, "project"); err != nil {
		t.Errorf("counting project after Migrate: %v", err)
	}
}

func TestMigrate_Concurrent(t *testing.T) {
	first := db.NewTestStore(t)
	second := db.NewTestStoreLike(t, first)
	ctx := t.Context()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, s := range []*db.Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Migrate(ctx)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Migrate() on store %d error = %v, want nil", i+1, err)
		}
	}
	got, err := first.Count(ctx, "schema_migration")
	if err != nil {
		t.Fatalf("counting schema_migration: %v", err)
	}
	if want := db.MigrationCount(); got != want {
		t.Errorf("rows in schema_migration after concurrent Migrate = %d, want %d", got, want)
	}
}
