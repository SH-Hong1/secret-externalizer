// Package db opens the postgres connection pool and applies the schema
// migrations. All SQL for the application database lives in this package.
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const redacted = "[redacted]"

// DSN is the database connection URL. It contains the password, so fmt (every
// verb), slog, and encoders that use encoding.TextMarshaler print a redacted
// form. Only Reveal returns the value.
type DSN string

// String returns the redacted form.
func (DSN) String() string { return redacted }

// LogValue returns the redacted form for slog.
func (DSN) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format prints the redacted form for every fmt verb, including %#v and %x.
func (DSN) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redacted)) // a failed write to a fmt buffer has no one to report to
}

// MarshalText returns the redacted form, so encoding/json never writes the
// value.
func (DSN) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Reveal returns the connection URL. Call it only where the URL is handed to
// the database driver.
func (d DSN) Reveal() string { return string(d) }

// pingTimeout bounds the connection check in Open.
const pingTimeout = 10 * time.Second

// connectTimeout bounds each new connection to postgres when the DSN sets
// no connect_timeout, so that a database that accepts connections and does
// not answer makes calls fail instead of hang.
const connectTimeout = 3 * time.Second

// Store is the application database. It is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
}

// Open creates a connection pool for dsn and checks that the database
// answers. A new connection gives up after connectTimeout unless the DSN
// sets connect_timeout. No error it returns contains the DSN.
func Open(ctx context.Context, dsn DSN) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn.Reveal())
	if err != nil {
		// The parser's message can quote part of the URL, so it is dropped.
		return nil, errors.New("parsing database url: the value is not a valid postgres url or connection string")
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = connectTimeout
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reaching postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases every connection in the pool.
func (s *Store) Close() { s.pool.Close() }
