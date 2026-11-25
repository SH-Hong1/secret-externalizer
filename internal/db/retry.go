package db

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// writeAttempts is how many times retried calls a write that keeps
	// failing with a transient error.
	writeAttempts = 3
	// firstRetryDelay is the wait before the second attempt; each later
	// wait is three times the one before.
	firstRetryDelay = 100 * time.Millisecond
)

// retried calls write until it succeeds, fails with an error that is not
// transient, or has been called writeAttempts times, and returns its last
// error. It stops waiting when ctx ends and then returns the last error
// joined with the context's cause. write must be a whole transaction whose
// repetition leaves the database as one run would, since a commit whose
// answer is lost is reported as a failure.
func retried(ctx context.Context, write func() error) error {
	delay := firstRetryDelay
	var timer *time.Timer
	for attempt := 1; ; attempt++ {
		err := write()
		if err == nil || attempt == writeAttempts || !transient(err) {
			return err
		}
		if ctx.Err() != nil {
			return errors.Join(err, context.Cause(ctx))
		}
		if timer == nil {
			timer = time.NewTimer(delay)
			defer timer.Stop()
		} else {
			timer.Reset(delay)
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, context.Cause(ctx))
		case <-timer.C:
		}
		delay *= 3
	}
}

// transient reports whether err is one that the same transaction may not
// meet again: a serialization failure, a deadlock, a broken connection, or
// a server that is shutting down or starting.
func transient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pgErr.Code {
		case "40001", "40P01", "57P01", "57P02", "57P03":
			return true
		}
		// Class 08 is a connection exception.
		return strings.HasPrefix(pgErr.Code, "08")
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
