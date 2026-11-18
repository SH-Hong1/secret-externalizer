// Package session stores login sessions and in-progress login attempts in
// valkey. A session holds the user and an expiry, never a token.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

// The lifetimes of the records the store keeps.
const (
	// IdleTimeout is how long a session survives without being read.
	IdleTimeout = 30 * time.Minute
	// AbsoluteTimeout is how long a session lives from sign-in, however often
	// it is read.
	AbsoluteTimeout = 8 * time.Hour
	// AttemptTimeout is how long a login attempt waits for its callback.
	AttemptTimeout = 10 * time.Minute
)

const (
	kindSession = "session"
	kindAttempt = "attempt"
	redacted    = "[redacted]"
)

// ErrNotFound means no live record exists for the ID.
var ErrNotFound = errors.New("session not found")

// ID identifies a session or a login attempt. It is a credential: every
// formatting verb and every logger prints a redacted form, and only Reveal
// returns the value.
type ID string

// NewID returns an ID made of 32 bytes from crypto/rand.
func NewID() (ID, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return ID(base64.RawURLEncoding.EncodeToString(b)), nil
}

// String returns the redacted form.
func (ID) String() string { return redacted }

// LogValue returns the redacted form for slog.
func (ID) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format prints the redacted form for every fmt verb, including %#v and %x.
func (ID) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redacted)) // a failed write to a fmt buffer has no one to report to
}

// Reveal returns the ID value. Call it only to set or read the cookie.
func (id ID) Reveal() string { return string(id) }

// Session is a signed-in user and the moment the session ends for good.
type Session struct {
	User      auth.User
	ExpiresAt time.Time
}

// Store keeps sessions and login attempts in valkey under the SHA-256 of
// their ID, so that reading valkey does not yield a usable cookie.
type Store struct {
	client valkey.Client
	now    func() time.Time
}

// NewStore returns a Store that uses client and reads the time from now.
func NewStore(client valkey.Client, now func() time.Time) *Store {
	return &Store{client: client, now: now}
}

type storedSession struct {
	Subject   string      `json:"subject"`
	Name      string      `json:"name"`
	Roles     []auth.Role `json:"roles"`
	ExpiresAt time.Time   `json:"expires_at"`
}

type storedAttempt struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	ReturnTo string `json:"return_to"`
}

func key(kind string, id ID) string {
	sum := sha256.Sum256([]byte(id))
	return kind + ":" + hex.EncodeToString(sum[:])
}

// Create stores a new session for u and returns its ID.
func (s *Store) Create(ctx context.Context, u auth.User) (ID, error) {
	id, err := NewID()
	if err != nil {
		return "", fmt.Errorf("creating session id: %w", err)
	}
	value, err := json.Marshal(storedSession{
		Subject:   u.Subject,
		Name:      u.Name,
		Roles:     u.Roles,
		ExpiresAt: s.now().Add(AbsoluteTimeout),
	})
	if err != nil {
		return "", fmt.Errorf("encoding session: %w", err)
	}
	if err := s.set(ctx, key(kindSession, id), value, IdleTimeout); err != nil {
		return "", fmt.Errorf("storing session: %w", err)
	}
	return id, nil
}

// Get returns the session for id and renews its idle lifetime, without
// extending it past ExpiresAt. It returns ErrNotFound when the session does
// not exist or has passed ExpiresAt.
func (s *Store) Get(ctx context.Context, id ID) (Session, error) {
	k := key(kindSession, id)
	value, err := s.client.Do(ctx, s.client.B().Get().Key(k).Build()).AsBytes()
	if err != nil {
		return Session{}, lookupError("reading session", err)
	}
	var st storedSession
	if err := json.Unmarshal(value, &st); err != nil {
		return Session{}, fmt.Errorf("decoding session: %w", err)
	}
	remaining := st.ExpiresAt.Sub(s.now())
	if remaining <= 0 {
		if err := s.Delete(ctx, id); err != nil {
			return Session{}, fmt.Errorf("removing expired session: %w", err)
		}
		return Session{}, ErrNotFound
	}
	ttl := min(IdleTimeout, remaining)
	if err := s.client.Do(ctx, s.client.B().Pexpire().Key(k).Milliseconds(ttl.Milliseconds()).Build()).Error(); err != nil {
		return Session{}, fmt.Errorf("renewing session: %w", err)
	}
	return Session{
		User:      auth.User{Subject: st.Subject, Name: st.Name, Roles: st.Roles},
		ExpiresAt: st.ExpiresAt,
	}, nil
}

// Delete removes the session for id. Deleting a session that does not exist
// is not an error.
func (s *Store) Delete(ctx context.Context, id ID) error {
	if err := s.client.Do(ctx, s.client.B().Del().Key(key(kindSession, id)).Build()).Error(); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

// PutAttempt stores a login attempt for AttemptTimeout and returns the ID
// that finds it again.
func (s *Store) PutAttempt(ctx context.Context, a auth.Attempt) (ID, error) {
	id, err := NewID()
	if err != nil {
		return "", fmt.Errorf("creating attempt id: %w", err)
	}
	value, err := json.Marshal(storedAttempt{
		State:    a.State,
		Nonce:    a.Nonce,
		Verifier: a.Verifier,
		ReturnTo: a.ReturnTo,
	})
	if err != nil {
		return "", fmt.Errorf("encoding attempt: %w", err)
	}
	if err := s.set(ctx, key(kindAttempt, id), value, AttemptTimeout); err != nil {
		return "", fmt.Errorf("storing attempt: %w", err)
	}
	return id, nil
}

// TakeAttempt returns the attempt for id and removes it, so a second call
// returns ErrNotFound.
func (s *Store) TakeAttempt(ctx context.Context, id ID) (auth.Attempt, error) {
	value, err := s.client.Do(ctx, s.client.B().Getdel().Key(key(kindAttempt, id)).Build()).AsBytes()
	if err != nil {
		return auth.Attempt{}, lookupError("taking attempt", err)
	}
	var st storedAttempt
	if err := json.Unmarshal(value, &st); err != nil {
		return auth.Attempt{}, fmt.Errorf("decoding attempt: %w", err)
	}
	return auth.Attempt{State: st.State, Nonce: st.Nonce, Verifier: st.Verifier, ReturnTo: st.ReturnTo}, nil
}

func (s *Store) set(ctx context.Context, k string, value []byte, ttl time.Duration) error {
	cmd := s.client.B().Set().Key(k).Value(string(value)).PxMilliseconds(ttl.Milliseconds()).Build()
	return s.client.Do(ctx, cmd).Error()
}

// lookupError maps a missing key to ErrNotFound and wraps every other
// failure, so a connection problem is never mistaken for a missing record.
func lookupError(doing string, err error) error {
	if valkey.IsValkeyNil(err) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", doing, err)
}
