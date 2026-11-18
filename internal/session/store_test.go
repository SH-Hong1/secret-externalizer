package session_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/valkey-io/valkey-go"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// testClock is a clock the test moves by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
}

func valkeyClient(t *testing.T) valkey.Client {
	t.Helper()
	addr := os.Getenv("TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("set TEST_VALKEY_ADDR to run against valkey")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}})
	if err != nil {
		t.Fatalf("valkey.NewClient(%q) error = %v", addr, err)
	}
	t.Cleanup(c.Close)
	return c
}

func testUser() auth.User {
	return auth.User{
		Subject: "sub-123",
		Name:    "Test User",
		Roles:   []auth.Role{auth.RoleUser, auth.RoleAdmin},
	}
}

func TestStore_CreateThenGet(t *testing.T) {
	clock := newClock()
	s := session.NewStore(valkeyClient(t), clock.Now)

	id, err := s.Create(t.Context(), testUser())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	want := session.Session{
		User:      testUser(),
		ExpiresAt: clock.Now().Add(session.AbsoluteTimeout),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Get() mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_GetUnknown(t *testing.T) {
	s := session.NewStore(valkeyClient(t), newClock().Now)
	id, err := session.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if _, err := s.Get(t.Context(), id); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestStore_Delete(t *testing.T) {
	s := session.NewStore(valkeyClient(t), newClock().Now)
	id, err := s.Create(t.Context(), testUser())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := s.Delete(t.Context(), id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := s.Get(t.Context(), id); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("Get(deleted) error = %v, want ErrNotFound", err)
	}
	if err := s.Delete(t.Context(), id); err != nil {
		t.Errorf("Delete(deleted) error = %v, want nil", err)
	}
}

func TestStore_AbsoluteTimeout(t *testing.T) {
	clock := newClock()
	c := valkeyClient(t)
	s := session.NewStore(c, clock.Now)
	id, err := s.Create(t.Context(), testUser())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clock.Advance(session.AbsoluteTimeout + time.Minute)
	if _, err := s.Get(t.Context(), id); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("Get(after absolute timeout) error = %v, want ErrNotFound", err)
	}
	key := session.KeyForTest("session", id)
	n, err := c.Do(t.Context(), c.B().Exists().Key(key).Build()).AsInt64()
	if err != nil {
		t.Fatalf("EXISTS %s error = %v", key, err)
	}
	if n != 0 {
		t.Errorf("session key after the absolute timeout exists, want it deleted")
	}
}

func keyTTL(t *testing.T, c valkey.Client, key string) time.Duration {
	t.Helper()
	ms, err := c.Do(t.Context(), c.B().Pttl().Key(key).Build()).AsInt64()
	if err != nil {
		t.Fatalf("PTTL %s error = %v", key, err)
	}
	return time.Duration(ms) * time.Millisecond
}

func TestStore_IdleTTL(t *testing.T) {
	clock := newClock()
	c := valkeyClient(t)
	s := session.NewStore(c, clock.Now)
	id, err := s.Create(t.Context(), testUser())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	key := session.KeyForTest("session", id)

	if ttl := keyTTL(t, c, key); ttl > session.IdleTimeout || ttl < 29*time.Minute {
		t.Errorf("TTL after Create = %v, want between 29m and 30m", ttl)
	}

	clock.Advance(7*time.Hour + 50*time.Minute)
	if _, err := s.Get(t.Context(), id); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if ttl := keyTTL(t, c, key); ttl > 10*time.Minute || ttl <= 0 {
		t.Errorf("TTL near the absolute limit = %v, want at most 10m", ttl)
	}
}

func TestStore_GetRenewsIdleTTL(t *testing.T) {
	c := valkeyClient(t)
	s := session.NewStore(c, newClock().Now)
	id, err := s.Create(t.Context(), testUser())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	key := session.KeyForTest("session", id)
	if err := c.Do(t.Context(), c.B().Pexpire().Key(key).Milliseconds(time.Minute.Milliseconds()).Build()).Error(); err != nil {
		t.Fatalf("PEXPIRE %s error = %v", key, err)
	}

	if _, err := s.Get(t.Context(), id); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if ttl := keyTTL(t, c, key); ttl > session.IdleTimeout || ttl < session.IdleTimeout-time.Minute {
		t.Errorf("TTL after Get on a key with 1m left = %v, want it renewed to about %v", ttl, session.IdleTimeout)
	}
}

func TestStore_AttemptTTL(t *testing.T) {
	c := valkeyClient(t)
	s := session.NewStore(c, newClock().Now)
	id, err := s.PutAttempt(t.Context(), auth.Attempt{State: "st", Nonce: "no", Verifier: "ve", ReturnTo: "/"})
	if err != nil {
		t.Fatalf("PutAttempt() error = %v", err)
	}
	if ttl := keyTTL(t, c, session.KeyForTest("attempt", id)); ttl > session.AttemptTimeout || ttl < session.AttemptTimeout-time.Minute {
		t.Errorf("attempt TTL = %v, want at most %v and close to it", ttl, session.AttemptTimeout)
	}
}

func TestStore_KeyIsNotTheID(t *testing.T) {
	c := valkeyClient(t)
	s := session.NewStore(c, newClock().Now)
	id, err := s.Create(t.Context(), testUser())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	n, err := c.Do(t.Context(), c.B().Exists().Key("session:"+id.Reveal()).Build()).AsInt64()
	if err != nil {
		t.Fatalf("EXISTS error = %v", err)
	}
	if n != 0 {
		t.Errorf("key session:<ID> exists, want only the hashed key")
	}
}

func TestStore_AttemptIsTakenOnce(t *testing.T) {
	s := session.NewStore(valkeyClient(t), newClock().Now)
	want := auth.Attempt{State: "st", Nonce: "no", Verifier: "ve", ReturnTo: "/projects"}
	id, err := s.PutAttempt(t.Context(), want)
	if err != nil {
		t.Fatalf("PutAttempt() error = %v", err)
	}
	got, err := s.TakeAttempt(t.Context(), id)
	if err != nil {
		t.Fatalf("TakeAttempt() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("TakeAttempt() mismatch (-want +got):\n%s", diff)
	}
	if _, err := s.TakeAttempt(t.Context(), id); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("second TakeAttempt() error = %v, want ErrNotFound", err)
	}
}

// serveHandshake accepts connections on ln and answers every command with a
// simple string reply, which is enough for the client to finish connecting in RESP2.
// The returned function closes the listener and every connection.
func serveHandshake(t *testing.T, ln net.Listener) (stop func()) {
	t.Helper()
	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					// Commands arrive as arrays of bulk strings; reply once per command.
					if !strings.HasPrefix(line, "*") {
						continue
					}
					var n int
					if _, err := fmt.Sscanf(line, "*%d", &n); err != nil {
						return
					}
					name := ""
					for i := range n {
						if _, err := r.ReadString('\n'); err != nil {
							return
						}
						arg, err := r.ReadString('\n')
						if err != nil {
							return
						}
						if i == 0 {
							name = strings.ToUpper(strings.TrimSpace(arg))
						}
					}
					reply := "+OK\r\n"
					if name == "PING" {
						reply = "+PONG\r\n"
					}
					if _, err := io.WriteString(conn, reply); err != nil {
						return
					}
				}
			}()
		}
	}()
	return func() {
		if err := ln.Close(); err != nil {
			t.Errorf("closing listener: %v", err)
		}
		mu.Lock()
		for _, c := range conns {
			_ = c.Close() // the peer may have closed it already
		}
		mu.Unlock()
		wg.Wait()
	}
}

func TestStore_Unreachable(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	stop := serveHandshake(t, ln)
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:       []string{ln.Addr().String()},
		ForceSingleClient: true,
		DisableCache:      true,
		DisableRetry:      true,
		AlwaysRESP2:       true,
	})
	if err != nil {
		stop()
		t.Fatalf("valkey.NewClient() error = %v", err)
	}
	t.Cleanup(c.Close)
	stop() // the server goes away after the client has connected

	s := session.NewStore(c, newClock().Now)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	id, err := session.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	_, err = s.Get(ctx, id)
	if err == nil {
		t.Fatal("Get() error = nil, want a connection error")
	}
	if errors.Is(err, session.ErrNotFound) {
		t.Errorf("Get() error = %v, must not be ErrNotFound", err)
	}
}

func TestID_Redacted(t *testing.T) {
	id, err := session.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	raw := id.Reveal()
	if len(raw) < 43 {
		t.Errorf("Reveal() = %d characters, want at least 43", len(raw))
	}
	var logged strings.Builder
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	logger.Info("session", "id", id)
	logger = slog.New(slog.NewJSONHandler(&logged, nil))
	logger.Info("session", "id", id)

	outputs := []string{
		fmt.Sprint(id), fmt.Sprintf("%v", id), fmt.Sprintf("%+v", id),
		fmt.Sprintf("%#v", id), fmt.Sprintf("%s", id), fmt.Sprintf("%q", id),
		fmt.Sprintf("%x", id), id.String(), logged.String(),
	}
	for _, out := range outputs {
		if strings.Contains(out, raw) {
			t.Errorf("output %q contains the session ID", out)
		}
	}
}

func TestID_RedactedInsideStructs(t *testing.T) {
	id, err := session.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	raw := id.Reveal()
	wrapper := struct{ ID session.ID }{id}

	var logged strings.Builder
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("session", "v", wrapper)
	slog.New(slog.NewTextHandler(&logged, nil)).Info("session", "v", wrapper)
	encoded, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatalf("json.Marshal(struct with ID) error = %v", err)
	}
	outputs := []string{
		logged.String(), string(encoded),
		fmt.Sprintf("%v", wrapper), fmt.Sprintf("%+v", wrapper), fmt.Sprintf("%#v", wrapper),
	}
	for _, out := range outputs {
		if strings.Contains(out, raw) {
			t.Errorf("output %q of a struct with an ID field contains the session ID", out)
		}
	}
}
