package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestTransient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"connection failure", &pgconn.PgError{Code: "08006"}, true},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, true},
		{"cannot connect now", &pgconn.PgError{Code: "57P03"}, true},
		{"wrapped", fmt.Errorf("announcing file: %w", &pgconn.PgError{Code: "40001"}), true},
		{"unexpected EOF", fmt.Errorf("receiving: %w", io.ErrUnexpectedEOF), true},
		{"check violation", &pgconn.PgError{Code: "23514"}, false},
		{"query canceled", &pgconn.PgError{Code: "57014"}, false},
		{"not found", fmt.Errorf("confirming file: %w", ErrNotFound), false},
		{"superseded", fmt.Errorf("confirming file: %w", ErrSuperseded), false},
		{"context ended", errors.Join(&pgconn.PgError{Code: "08006"}, context.DeadlineExceeded), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transient(tt.err); got != tt.want {
				t.Errorf("transient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestRetried_StopsWithContext ends the context during the first wait and
// expects no second attempt.
func TestRetried_StopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	ended := errors.New("the test ended the context")
	failure := &pgconn.PgError{Code: "40001"}
	calls := 0
	err := retried(ctx, func() error {
		calls++
		cancel(ended)
		return failure
	})
	if calls != 1 {
		t.Errorf("retried() called write %d times after the context ended, want 1", calls)
	}
	if !errors.Is(err, failure) || !errors.Is(err, ended) {
		t.Errorf("retried() = %v, want the write's error joined with the context's cause", err)
	}
}
