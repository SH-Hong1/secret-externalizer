package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/db"
)

// config holds the settings read once at start-up. Its fields are
// unexported, and fmt prints unexported fields by reflection without calling
// db.DSN's redacting methods, so config redacts itself through String,
// Format, and LogValue.
type config struct {
	dsn       db.DSN
	root      string
	poll      time.Duration
	lease     time.Duration
	heartbeat time.Duration
}

// String returns every setting with the database URL redacted.
func (c config) String() string {
	// c.dsn is an argument of its own, so fmt calls its Format method.
	return fmt.Sprintf("{database_url:%v workspace_root:%s poll:%s lease:%s heartbeat:%s}",
		c.dsn, c.root, c.poll, c.lease, c.heartbeat)
}

// Format prints the form String returns for every fmt verb, including %#v,
// which would otherwise print every field by reflection.
func (c config) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(c.String())) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns every setting with the database URL redacted.
func (c config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("database_url", c.dsn),
		slog.String("workspace_root", c.root),
		slog.String("poll", c.poll.String()),
		slog.String("lease", c.lease.String()),
		slog.String("heartbeat", c.heartbeat.String()),
	)
}

// loadConfig builds a config from the environment. The database URL is
// converted to db.DSN as it is read, and no error returned here contains it.
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
	return config{
		dsn:       dsn,
		root:      root,
		poll:      2 * time.Second,
		lease:     2 * time.Minute,
		heartbeat: 30 * time.Second,
	}, nil
}
