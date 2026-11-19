package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
)

// testDatabaseURL is an obviously fake database URL.
const testDatabaseURL = "postgres://se_app:dev-only-cmd-worker-test-db@postgres:5432/secret_externalizer" //nolint:gosec // G101: an obviously fake fixture value

func validEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"DATABASE_URL":   testDatabaseURL,
		"WORKSPACE_ROOT": t.TempDir(),
	}
}

func getenvFrom(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestLoadConfig(t *testing.T) {
	env := validEnv(t)
	got, err := loadConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	want := config{
		dsn:       db.DSN(testDatabaseURL),
		root:      env["WORKSPACE_ROOT"],
		poll:      2 * time.Second,
		lease:     2 * time.Minute,
		heartbeat: 30 * time.Second,
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(config{})); diff != "" {
		t.Errorf("loadConfig() mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "WORKSPACE_ROOT"} {
		t.Run(name, func(t *testing.T) {
			env := validEnv(t)
			delete(env, name)
			_, err := loadConfig(getenvFrom(env))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("loadConfig() without %s error = %v, want an error naming it", name, err)
			}
		})
	}
}

func TestLoadConfig_BadWorkspaceRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, file)
	tests := []struct {
		name string
		root string
	}{
		{"relative", "workspace"},
		{"missing", filepath.Join(dir, "missing")},
		{"a file", file},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv(t)
			env["WORKSPACE_ROOT"] = tt.root
			_, err := loadConfig(getenvFrom(env))
			if err == nil || !strings.Contains(err.Error(), "WORKSPACE_ROOT") {
				t.Errorf("loadConfig() with WORKSPACE_ROOT=%q error = %v, want an error naming WORKSPACE_ROOT", tt.root, err)
			}
		})
	}
}

func TestConfig_RedactsDatabaseURL(t *testing.T) {
	cfg, err := loadConfig(getenvFrom(validEnv(t)))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "config", cfg)
	outputs := []string{buf.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		outputs = append(outputs, fmt.Sprintf(verb, cfg), fmt.Sprintf(verb, &cfg))
	}
	for _, out := range outputs {
		if strings.Contains(out, "dev-only-cmd-worker-test-db") {
			t.Errorf("output %q of config contains the database password", out)
		}
	}
}

func TestLoadConfig_ErrorHidesDatabaseURL(t *testing.T) {
	env := validEnv(t)
	env["WORKSPACE_ROOT"] = "relative"
	_, err := loadConfig(getenvFrom(env))
	if err == nil {
		t.Fatal("loadConfig() with a relative WORKSPACE_ROOT error = nil, want an error")
	}
	if strings.Contains(err.Error(), "dev-only-cmd-worker-test-db") {
		t.Errorf("loadConfig() error = %q, contains the database password", err)
	}
}
