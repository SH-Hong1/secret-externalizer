package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
)

// testDatabaseURL is an obviously fake database URL.
const testDatabaseURL = "postgres://se_app:dev-only-cmd-worker-test-db@postgres:5432/secret_externalizer" //nolint:gosec // G101: an obviously fake fixture value

// testSecretID is an obviously fake AppRole secret-id.
const testSecretID = "dev-only-cmd-worker-test-secret-id" //nolint:gosec // G101: an obviously fake fixture value

// validEnv returns a complete environment. The secret-id file ends with a
// newline, as a file written by an editor does. The scanner and git
// executables are fake files that are never run.
func validEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	secretIDFile := filepath.Join(dir, "secret-id")
	if err := os.WriteFile(secretIDFile, []byte(testSecretID+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", secretIDFile, err)
	}
	rules := filepath.Join(dir, "rules")
	writeFile(t, filepath.Join(rules, "betterleaks.toml"))
	writeFile(t, filepath.Join(rules, "opengrep", "java.yml"))
	return map[string]string{
		"DATABASE_URL":       testDatabaseURL,
		"WORKSPACE_ROOT":     t.TempDir(),
		"BAO_ADDR":           "http://openbao:8200",
		"BAO_ROLE_ID":        "se-worker",
		"BAO_SECRET_ID_FILE": secretIDFile,
		"BETTERLEAKS_BIN":    fakeExecutable(t, dir, "betterleaks"),
		"OPENGREP_BIN":       fakeExecutable(t, dir, "opengrep"),
		"GIT_BIN":            fakeExecutable(t, dir, "git"),
		"RULES_DIR":          rules,
		"SCAN_TEMP_DIR":      filepath.Join(dir, "scan"),
	}
}

// fakeExecutable writes an executable file named name in dir and returns its
// path.
func fakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // G306: a fake executable must be executable
		t.Fatalf("WriteFile(%q) error = %v", p, err)
	}
	return p
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
		dsn:            db.DSN(testDatabaseURL),
		root:           env["WORKSPACE_ROOT"],
		poll:           2 * time.Second,
		lease:          2 * time.Minute,
		heartbeat:      30 * time.Second,
		baoAddr:        "http://openbao:8200",
		baoCreds:       bao.Credentials{RoleID: "se-worker", SecretID: testSecretID},
		betterleaksBin: env["BETTERLEAKS_BIN"],
		opengrepBin:    env["OPENGREP_BIN"],
		gitBin:         env["GIT_BIN"],
		rulesDir:       env["RULES_DIR"],
		scanTempDir:    env["SCAN_TEMP_DIR"],
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(config{})); diff != "" {
		t.Errorf("loadConfig() mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	for _, name := range []string{
		"DATABASE_URL", "WORKSPACE_ROOT", "BAO_ADDR", "BAO_ROLE_ID", "BAO_SECRET_ID_FILE",
		"BETTERLEAKS_BIN", "OPENGREP_BIN", "RULES_DIR", "SCAN_TEMP_DIR",
	} {
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

func TestLoadConfig_BadScanSettings(t *testing.T) {
	dir := t.TempDir()
	notExecutable := filepath.Join(dir, "plain")
	writeFile(t, notExecutable)
	noToml := filepath.Join(dir, "rules-without-toml")
	writeFile(t, filepath.Join(noToml, "opengrep", "java.yml"))
	noOpengrep := filepath.Join(dir, "rules-without-opengrep")
	writeFile(t, filepath.Join(noOpengrep, "betterleaks.toml"))
	tests := []struct {
		name, value string
	}{
		{"BETTERLEAKS_BIN", "betterleaks"},
		{"BETTERLEAKS_BIN", filepath.Join(dir, "missing")},
		{"BETTERLEAKS_BIN", dir},
		{"BETTERLEAKS_BIN", notExecutable},
		{"OPENGREP_BIN", filepath.Join(dir, "missing")},
		{"GIT_BIN", filepath.Join(dir, "missing")},
		{"RULES_DIR", "rules"},
		{"RULES_DIR", filepath.Join(dir, "missing")},
		{"RULES_DIR", noToml},
		{"RULES_DIR", noOpengrep},
		{"SCAN_TEMP_DIR", "tmp/scan"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"="+filepath.Base(tt.value), func(t *testing.T) {
			env := validEnv(t)
			env[tt.name] = tt.value
			_, err := loadConfig(getenvFrom(env))
			if err == nil || !strings.Contains(err.Error(), tt.name) {
				t.Errorf("loadConfig() with %s=%q error = %v, want an error naming %s", tt.name, tt.value, err, tt.name)
			}
		})
	}
}

func TestLoadConfig_GitDefault(t *testing.T) {
	env := validEnv(t)
	delete(env, "GIT_BIN")
	got, err := loadConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadConfig() without GIT_BIN error = %v", err)
	}
	want := ""
	if info, err := os.Stat("/usr/bin/git"); err == nil && info.Mode().IsRegular() {
		want = "/usr/bin/git"
	}
	if got.gitBin != want {
		t.Errorf("loadConfig() without GIT_BIN git = %q, want %q", got.gitBin, want)
	}
}

func TestLoadConfig_BadSecretIDFile(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", empty, err)
	}
	tests := []struct {
		name string
		path string
	}{
		{"missing", filepath.Join(dir, "missing")},
		{"empty", empty},
		{"a directory", dir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv(t)
			env["BAO_SECRET_ID_FILE"] = tt.path
			_, err := loadConfig(getenvFrom(env))
			if err == nil || !strings.Contains(err.Error(), "BAO_SECRET_ID_FILE") {
				t.Errorf("loadConfig() with BAO_SECRET_ID_FILE=%q error = %v, want an error naming BAO_SECRET_ID_FILE", tt.path, err)
			}
		})
	}
}

func TestConfig_RedactsSecrets(t *testing.T) {
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
		if strings.Contains(out, testSecretID) {
			t.Errorf("output %q of config contains the secret-id", out)
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
