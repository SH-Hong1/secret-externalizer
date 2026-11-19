package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
)

// config holds the settings read once at start-up. Its fields are
// unexported, and fmt prints unexported fields by reflection without calling
// the redacting methods of db.DSN and bao.SecretID, so config redacts itself
// through String, Format, and LogValue.
type config struct {
	dsn       db.DSN
	root      string
	poll      time.Duration
	lease     time.Duration
	heartbeat time.Duration
	baoAddr   string
	baoCreds  bao.Credentials
	// betterleaksBin and opengrepBin are the scanner executables, and
	// rulesDir holds betterleaks.toml and the opengrep rule folder.
	betterleaksBin string
	opengrepBin    string
	rulesDir       string
	// gitBin is the git executable, or empty when there is none and commit
	// hashes are not read.
	gitBin string
	// scanTempDir is the folder under which the worker keeps the scanners'
	// home and temporary folders and the per-scan copies of EUC-KR files.
	scanTempDir string
}

// defaultGitBin is where the worker image has git.
const defaultGitBin = "/usr/bin/git"

// String returns every setting with the database URL and the secret-id
// redacted.
func (c config) String() string {
	// c.dsn and the secret-id are arguments of their own, so fmt calls their
	// Format methods.
	return fmt.Sprintf("{database_url:%v workspace_root:%s poll:%s lease:%s heartbeat:%s bao_addr:%s bao_role_id:%s bao_secret_id:%v"+
		" betterleaks_bin:%s opengrep_bin:%s rules_dir:%s git_bin:%s scan_temp_dir:%s}",
		c.dsn, c.root, c.poll, c.lease, c.heartbeat, c.baoAddr, c.baoCreds.RoleID, c.baoCreds.SecretID,
		c.betterleaksBin, c.opengrepBin, c.rulesDir, c.gitBin, c.scanTempDir)
}

// Format prints the form String returns for every fmt verb, including %#v,
// which would otherwise print every field by reflection.
func (c config) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(c.String())) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns every setting with the database URL and the secret-id
// redacted.
func (c config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("database_url", c.dsn),
		slog.String("workspace_root", c.root),
		slog.String("poll", c.poll.String()),
		slog.String("lease", c.lease.String()),
		slog.String("heartbeat", c.heartbeat.String()),
		slog.String("bao_addr", c.baoAddr),
		slog.String("bao_role_id", c.baoCreds.RoleID),
		slog.Any("bao_secret_id", c.baoCreds.SecretID),
		slog.String("betterleaks_bin", c.betterleaksBin),
		slog.String("opengrep_bin", c.opengrepBin),
		slog.String("rules_dir", c.rulesDir),
		slog.String("git_bin", c.gitBin),
		slog.String("scan_temp_dir", c.scanTempDir),
	)
}

// loadConfig builds a config from the environment. The database URL is
// converted to db.DSN as it is read, and the secret-id to bao.SecretID as it
// is read from BAO_SECRET_ID_FILE. No error returned here contains either.
func loadConfig(getenv func(string) string) (config, error) {
	var missing []string
	required := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	dsn := db.DSN(required("DATABASE_URL"))
	root := required("WORKSPACE_ROOT")
	baoAddr := required("BAO_ADDR")
	roleID := required("BAO_ROLE_ID")
	secretIDFile := required("BAO_SECRET_ID_FILE")
	betterleaksBin := required("BETTERLEAKS_BIN")
	opengrepBin := required("OPENGREP_BIN")
	rulesDir := required("RULES_DIR")
	scanTempDir := required("SCAN_TEMP_DIR")
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if !filepath.IsAbs(root) {
		return config{}, fmt.Errorf("checking WORKSPACE_ROOT: %q is not an absolute path", root)
	}
	info, err := os.Stat(root)
	if err != nil {
		return config{}, fmt.Errorf("checking WORKSPACE_ROOT: %w", err)
	}
	if !info.IsDir() {
		return config{}, fmt.Errorf("checking WORKSPACE_ROOT: %q is not a directory", root)
	}
	secretID, err := readSecretID(secretIDFile)
	if err != nil {
		return config{}, fmt.Errorf("reading BAO_SECRET_ID_FILE: %w", err)
	}
	if err := checkExecutable(betterleaksBin); err != nil {
		return config{}, fmt.Errorf("checking BETTERLEAKS_BIN: %w", err)
	}
	if err := checkExecutable(opengrepBin); err != nil {
		return config{}, fmt.Errorf("checking OPENGREP_BIN: %w", err)
	}
	gitBin := getenv("GIT_BIN")
	switch {
	case gitBin != "":
		if err := checkExecutable(gitBin); err != nil {
			return config{}, fmt.Errorf("checking GIT_BIN: %w", err)
		}
	case checkExecutable(defaultGitBin) == nil:
		gitBin = defaultGitBin
	}
	if err := checkRulesDir(rulesDir); err != nil {
		return config{}, fmt.Errorf("checking RULES_DIR: %w", err)
	}
	if !filepath.IsAbs(scanTempDir) {
		return config{}, fmt.Errorf("checking SCAN_TEMP_DIR: %q is not an absolute path", scanTempDir)
	}
	return config{
		dsn:       dsn,
		root:      root,
		poll:      2 * time.Second,
		lease:     2 * time.Minute,
		heartbeat: 30 * time.Second,
		baoAddr:   baoAddr,
		baoCreds:  bao.Credentials{RoleID: roleID, SecretID: secretID},

		betterleaksBin: betterleaksBin,
		opengrepBin:    opengrepBin,
		rulesDir:       rulesDir,
		gitBin:         gitBin,
		scanTempDir:    scanTempDir,
	}, nil
}

// checkExecutable fails unless p is an absolute path to a regular file that
// someone may execute.
func checkExecutable(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%q is not an absolute path", p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("checking the file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%q is not an executable file", p)
	}
	return nil
}

// checkRulesDir fails unless dir is an absolute path to a folder that holds
// the betterleaks rule file betterleaks.toml and the opengrep rule folder
// opengrep.
func checkRulesDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%q is not an absolute path", dir)
	}
	toml, err := os.Stat(filepath.Join(dir, "betterleaks.toml"))
	if err != nil {
		return fmt.Errorf("checking the betterleaks rule file: %w", err)
	}
	if !toml.Mode().IsRegular() {
		return fmt.Errorf("%q holds no betterleaks.toml file", dir)
	}
	og, err := os.Stat(filepath.Join(dir, "opengrep"))
	if err != nil {
		return fmt.Errorf("checking the opengrep rule folder: %w", err)
	}
	if !og.IsDir() {
		return fmt.Errorf("%q holds no opengrep folder", dir)
	}
	return nil
}

// readSecretID reads an AppRole secret-id from the file at path, without
// surrounding white space. The errors of os.ReadFile name the path and never
// the content.
func readSecretID(path string) (bao.SecretID, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own BAO_SECRET_ID_FILE setting
	if err != nil {
		return "", fmt.Errorf("reading the secret-id file: %w", err)
	}
	s := bao.SecretID(strings.TrimSpace(string(b)))
	if s == "" {
		return "", fmt.Errorf("%q is empty", path)
	}
	return s, nil
}
