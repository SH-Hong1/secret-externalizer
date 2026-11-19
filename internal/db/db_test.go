package db_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

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

func TestOpen_ConnectTimeout(t *testing.T) {
	// The listener accepts connections and never answers, as a hung
	// postgres would.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	var conns []net.Conn
	accepting := make(chan struct{})
	go func() {
		defer close(accepting)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
		}
	}()
	// Closing the listener ends the accept loop; the cleanup waits for it
	// before it touches the connections the loop collected.
	t.Cleanup(func() {
		_ = ln.Close() // closing a listener the test only accepted on loses nothing
		<-accepting
		for _, c := range conns {
			_ = c.Close() // nothing was written to the connection
		}
	})

	dsn := db.DSN("postgres://se_app:" + testPassword + "@" + ln.Addr().String() + "/x?sslmode=disable")
	start := time.Now()
	s, err := db.Open(t.Context(), dsn)
	elapsed := time.Since(start)
	if err == nil {
		s.Close()
		t.Fatal("Open(silent server) error = nil, want an error")
	}
	// The connection check in Open gives up after 10 seconds; the connect
	// timeout must end the attempt well before that.
	if elapsed > 7*time.Second {
		t.Errorf("Open(silent server) returned after %v, want within the connect timeout", elapsed)
	}
	if strings.Contains(err.Error(), testPassword) || strings.Contains(err.Error(), dsn.Reveal()) {
		t.Errorf("Open(silent server) error = %q, want no DSN in it", err)
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

// migrations returns a file system that holds the given migration files,
// keyed by file name, under migrations/.
func migrations(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["migrations/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

// assertTables fails the test unless each table in exist can be read and
// each table in missing cannot.
func assertTables(t *testing.T, s *db.Store, exist, missing []string) {
	t.Helper()
	for _, table := range exist {
		if _, err := s.Count(t.Context(), table); err != nil {
			t.Errorf("counting %s error = %v, want the table to exist", table, err)
		}
	}
	for _, table := range missing {
		if _, err := s.Count(t.Context(), table); err == nil {
			t.Errorf("counting %s error = nil, want the table not to exist", table)
		}
	}
}

func TestMigrate_UnknownAppliedVersion(t *testing.T) {
	s := db.NewTestStore(t)
	ctx := t.Context()
	if err := s.MigrateFrom(ctx, migrations(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x int)",
		"0002_b.sql": "CREATE TABLE b (x int)",
	})); err != nil {
		t.Fatalf("MigrateFrom(0001, 0002) error = %v", err)
	}

	err := s.MigrateFrom(ctx, migrations(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x int)",
		"0003_c.sql": "CREATE TABLE c (x int)",
	}))
	if err == nil || !strings.Contains(err.Error(), "2") {
		t.Fatalf("MigrateFrom(0001, 0003) over 0001, 0002 error = %v, want one naming version 2", err)
	}
	if strings.Contains(err.Error(), "CREATE") {
		t.Errorf("MigrateFrom() error = %q, want no SQL text", err)
	}
	assertTables(t, s, []string{"a", "b"}, []string{"c"})
}

func TestMigrate_OutOfOrderFile(t *testing.T) {
	s := db.NewTestStore(t)
	ctx := t.Context()
	if err := s.MigrateFrom(ctx, migrations(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x int)",
		"0003_c.sql": "CREATE TABLE c (x int)",
	})); err != nil {
		t.Fatalf("MigrateFrom(0001, 0003) error = %v", err)
	}

	err := s.MigrateFrom(ctx, migrations(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x int)",
		"0002_b.sql": "CREATE TABLE b (x int)",
		"0003_c.sql": "CREATE TABLE c (x int)",
	}))
	if err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("MigrateFrom() with 0002 added after 0003 error = %v, want one naming 0002_b.sql", err)
	}
	assertTables(t, s, []string{"a", "c"}, []string{"b"})
}

func TestMigrate_FailureRollsBackFile(t *testing.T) {
	s := db.NewTestStore(t)
	ctx := t.Context()
	err := s.MigrateFrom(ctx, migrations(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x int)",
		"0002_b.sql": "CREATE TABLE b (x int);\nSELECT * FROM no_such_table;",
	}))
	if err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("MigrateFrom() with a failing 0002 error = %v, want one naming 0002_b.sql", err)
	}
	assertTables(t, s, []string{"a"}, []string{"b"})
	got, err := s.Count(ctx, "schema_migration")
	if err != nil {
		t.Fatalf("counting schema_migration: %v", err)
	}
	if got != 1 {
		t.Errorf("rows in schema_migration after 0002 failed = %d, want 1", got)
	}
}

func TestMigrate_InvalidFileName(t *testing.T) {
	s := db.NewTestStore(t)
	err := s.MigrateFrom(t.Context(), migrations(map[string]string{
		"0001_a.sql":    "CREATE TABLE a (x int)",
		"2_Invalid.sql": "CREATE TABLE b (x int)",
	}))
	if err == nil || !strings.Contains(err.Error(), "2_Invalid.sql") {
		t.Fatalf("MigrateFrom() with a badly named file error = %v, want one naming 2_Invalid.sql", err)
	}
	assertTables(t, s, nil, []string{"a", "b"})
}
