package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
)

// config holds the settings read once at start-up. Its fields are
// unexported, and fmt prints unexported fields by reflection without calling
// the redacting methods of db.DSN and bao.SecretID, so config redacts itself
// through String, Format, and LogValue.
type config struct {
	dsn db.DSN
	// jobs are the kinds of job this worker runs.
	jobs jobs
	// root is the read-only mount of the workspace that scans read, or
	// empty when the worker does not scan.
	root string
	// applyRoot is the read-write mount of the workspace that applies
	// change files under, or empty when applies are blocked.
	applyRoot string
	poll      time.Duration
	lease     time.Duration
	heartbeat time.Duration
	baoAddr   string
	// baoTLS is whether baoAddr is an https address.
	baoTLS bool
	// baoCA holds the PEM certificates that OpenBao's certificate must
	// chain to. Nil means the system's roots, or no TLS when baoTLS is
	// false.
	baoCA    []byte
	baoCreds bao.Credentials
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

// jobs names the kinds of job a worker runs, as WORKER_JOBS gives them.
type jobs string

// The values of WORKER_JOBS. A worker that scans runs the scanners, so it
// must not be given the read-write mount that applies change files under.
const (
	// jobsAll runs scans, apply checks, and applies.
	jobsAll jobs = "all"
	// jobsScan runs only scans.
	jobsScan jobs = "scan"
	// jobsApply runs only apply checks and applies.
	jobsApply jobs = "apply"
)

// kinds returns the kinds of job that j runs.
func (j jobs) kinds() []job.Kind {
	switch j {
	case jobsScan:
		return []job.Kind{job.KindScan}
	case jobsApply:
		return []job.Kind{job.KindApplyCheck, job.KindApply}
	case jobsAll:
		return job.Kinds()
	default:
		return job.Kinds()
	}
}

// scans reports whether j runs scans.
func (j jobs) scans() bool { return j != jobsApply }

// applies reports whether j runs apply checks and applies.
func (j jobs) applies() bool { return j != jobsScan }

// parseJobs returns the jobs that WORKER_JOBS value v names. Empty means
// every kind.
func parseJobs(v string) (jobs, error) {
	switch j := jobs(v); j {
	case "":
		return jobsAll, nil
	case jobsAll, jobsScan, jobsApply:
		return j, nil
	default:
		return "", fmt.Errorf("%q is not one of all, scan, apply", v)
	}
}

// String returns every setting with the database URL and the secret-id
// redacted. Of OpenBao's CA it says only whether the connection uses TLS.
func (c config) String() string {
	// c.dsn and the secret-id are arguments of their own, so fmt calls their
	// Format methods.
	return fmt.Sprintf("{database_url:%v jobs:%s workspace_root:%s apply_workspace_root:%s poll:%s lease:%s heartbeat:%s bao_addr:%s bao_tls:%t bao_role_id:%s bao_secret_id:%v"+
		" betterleaks_bin:%s opengrep_bin:%s rules_dir:%s git_bin:%s scan_temp_dir:%s}",
		c.dsn, c.jobs, c.root, c.applyRoot, c.poll, c.lease, c.heartbeat, c.baoAddr, c.baoTLS, c.baoCreds.RoleID, c.baoCreds.SecretID,
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
		slog.String("jobs", string(c.jobs)),
		slog.String("workspace_root", c.root),
		slog.String("apply_workspace_root", c.applyRoot),
		slog.String("poll", c.poll.String()),
		slog.String("lease", c.lease.String()),
		slog.String("heartbeat", c.heartbeat.String()),
		slog.String("bao_addr", c.baoAddr),
		slog.Bool("bao_tls", c.baoTLS),
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
//
// WORKER_JOBS chooses the kinds of job the worker runs: all (the default),
// scan, or apply. The scan settings (WORKSPACE_ROOT, the scanners, the rules,
// and SCAN_TEMP_DIR) are required when the worker scans and not read when it
// does not. APPLY_WORKSPACE_ROOT is required when the worker only applies,
// optional when it runs every kind, and refused when it only scans.
//
// BAO_CA_FILE is optional. When it is set, BAO_ADDR must be an https address
// and the worker trusts only the certificates in that file for OpenBao; when
// it is not, an https address is checked against the system's roots.
func loadConfig(getenv func(string) string) (config, error) {
	jobs, err := parseJobs(getenv("WORKER_JOBS"))
	if err != nil {
		return config{}, fmt.Errorf("checking WORKER_JOBS: %w", err)
	}
	var missing []string
	required := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	dsn := db.DSN(required("DATABASE_URL"))
	baoAddr := required("BAO_ADDR")
	roleID := required("BAO_ROLE_ID")
	secretIDFile := required("BAO_SECRET_ID_FILE")
	var root, betterleaksBin, opengrepBin, rulesDir, scanTempDir string
	if jobs.scans() {
		root = required("WORKSPACE_ROOT")
		betterleaksBin = required("BETTERLEAKS_BIN")
		opengrepBin = required("OPENGREP_BIN")
		rulesDir = required("RULES_DIR")
		scanTempDir = required("SCAN_TEMP_DIR")
	}
	applyRoot := getenv("APPLY_WORKSPACE_ROOT")
	if jobs == jobsApply {
		applyRoot = required("APPLY_WORKSPACE_ROOT")
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if jobs == jobsScan && applyRoot != "" {
		return config{}, errors.New("checking APPLY_WORKSPACE_ROOT: it is set, but WORKER_JOBS=scan runs no applies, and the scanners must not reach a read-write mount")
	}
	secretID, err := bao.ReadSecretIDFile(secretIDFile)
	if err != nil {
		return config{}, fmt.Errorf("reading BAO_SECRET_ID_FILE: %w", err)
	}
	baoTLS, baoCA, err := bao.LoadCA(baoAddr, getenv("BAO_CA_FILE"))
	if err != nil {
		return config{}, fmt.Errorf("checking BAO_ADDR and BAO_CA_FILE: %w", err)
	}
	if jobs.scans() {
		if err := checkScanSettings(root, betterleaksBin, opengrepBin, rulesDir, scanTempDir); err != nil {
			return config{}, err
		}
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
	if applyRoot != "" {
		if err := checkDir(applyRoot); err != nil {
			return config{}, fmt.Errorf("checking APPLY_WORKSPACE_ROOT: %w", err)
		}
	}
	return config{
		dsn:       dsn,
		jobs:      jobs,
		root:      root,
		applyRoot: applyRoot,
		poll:      2 * time.Second,
		lease:     2 * time.Minute,
		heartbeat: 30 * time.Second,
		baoAddr:   baoAddr,
		baoTLS:    baoTLS,
		baoCA:     baoCA,
		baoCreds:  bao.Credentials{RoleID: roleID, SecretID: secretID},

		betterleaksBin: betterleaksBin,
		opengrepBin:    opengrepBin,
		rulesDir:       rulesDir,
		gitBin:         gitBin,
		scanTempDir:    scanTempDir,
	}, nil
}

// checkScanSettings checks the settings a worker that scans needs: the
// workspace folder, the two scanner executables, the rules folder, and an
// absolute folder for the scanners' temporary files.
func checkScanSettings(root, betterleaksBin, opengrepBin, rulesDir, scanTempDir string) error {
	if err := checkDir(root); err != nil {
		return fmt.Errorf("checking WORKSPACE_ROOT: %w", err)
	}
	if err := checkExecutable(betterleaksBin); err != nil {
		return fmt.Errorf("checking BETTERLEAKS_BIN: %w", err)
	}
	if err := checkExecutable(opengrepBin); err != nil {
		return fmt.Errorf("checking OPENGREP_BIN: %w", err)
	}
	if err := checkRulesDir(rulesDir); err != nil {
		return fmt.Errorf("checking RULES_DIR: %w", err)
	}
	if !filepath.IsAbs(scanTempDir) {
		return fmt.Errorf("checking SCAN_TEMP_DIR: %q is not an absolute path", scanTempDir)
	}
	return nil
}

// checkDir fails unless p is an absolute path to a folder.
func checkDir(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%q is not an absolute path", p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("checking the folder: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", p)
	}
	return nil
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
