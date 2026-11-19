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
}

// String returns every setting with the database URL and the secret-id
// redacted.
func (c config) String() string {
	// c.dsn and the secret-id are arguments of their own, so fmt calls their
	// Format methods.
	return fmt.Sprintf("{database_url:%v workspace_root:%s poll:%s lease:%s heartbeat:%s bao_addr:%s bao_role_id:%s bao_secret_id:%v}",
		c.dsn, c.root, c.poll, c.lease, c.heartbeat, c.baoAddr, c.baoCreds.RoleID, c.baoCreds.SecretID)
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
	return config{
		dsn:       dsn,
		root:      root,
		poll:      2 * time.Second,
		lease:     2 * time.Minute,
		heartbeat: 30 * time.Second,
		baoAddr:   baoAddr,
		baoCreds:  bao.Credentials{RoleID: roleID, SecretID: secretID},
	}, nil
}

// readSecretID reads an AppRole secret-id from the file at path, without
// surrounding white space. The errors of os.ReadFile name the path and never
// the content.
func readSecretID(path string) (bao.SecretID, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own BAO_SECRET_ID_FILE setting
	if err != nil {
		return "", err
	}
	s := bao.SecretID(strings.TrimSpace(string(b)))
	if s == "" {
		return "", fmt.Errorf("%q is empty", path)
	}
	return s, nil
}
