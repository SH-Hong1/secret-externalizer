package web_test

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// fakeSessions keeps sessions in memory. A non-nil error field makes the
// matching method return that error.
type fakeSessions struct {
	createErr error
	getErr    error
	deleteErr error

	mu    sync.Mutex
	users map[session.ID]auth.User
	next  int
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{users: make(map[session.ID]auth.User)}
}

func (f *fakeSessions) Create(_ context.Context, u auth.User) (session.ID, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := session.ID(fmt.Sprintf("fake-session-id-%d", f.next))
	f.users[id] = u
	return id, nil
}

func (f *fakeSessions) Get(_ context.Context, id session.ID) (session.Session, error) {
	if f.getErr != nil {
		return session.Session{}, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return session.Session{}, session.ErrNotFound
	}
	return session.Session{User: u}, nil
}

func (f *fakeSessions) Delete(_ context.Context, id session.ID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.users, id)
	return nil
}

func (f *fakeSessions) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users)
}

// fakeAttempts keeps login attempts in memory and removes each one when it is
// taken, as the real store does.
type fakeAttempts struct {
	putErr  error
	takeErr error

	mu       sync.Mutex
	attempts map[session.ID]auth.Attempt
	next     int
}

func newFakeAttempts() *fakeAttempts {
	return &fakeAttempts{attempts: make(map[session.ID]auth.Attempt)}
}

func (f *fakeAttempts) PutAttempt(_ context.Context, a auth.Attempt) (session.ID, error) {
	if f.putErr != nil {
		return "", f.putErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := session.ID(fmt.Sprintf("fake-attempt-id-%d", f.next))
	f.attempts[id] = a
	return id, nil
}

func (f *fakeAttempts) TakeAttempt(_ context.Context, id session.ID) (auth.Attempt, error) {
	if f.takeErr != nil {
		return auth.Attempt{}, f.takeErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.attempts[id]
	if !ok {
		return auth.Attempt{}, session.ErrNotFound
	}
	delete(f.attempts, id)
	return a, nil
}

func (f *fakeAttempts) get(id string) (auth.Attempt, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.attempts[session.ID(id)]
	return a, ok
}

const (
	fakeAuthURL   = "https://idp.example.test/authorize"
	fakeLogoutURL = "https://idp.example.test/logout"
)

// fakeAuthenticator returns fixed values. Exchange returns user, or err when
// it is set, and records the arguments it was given.
type fakeAuthenticator struct {
	user auth.User
	err  error

	mu           sync.Mutex
	gotCode      string
	gotVerifier  string
	gotNonce     string
	exchangeRuns int
}

// AuthCodeURL puts state in the query, as a real provider address does.
func (f *fakeAuthenticator) AuthCodeURL(state, _, _ string) string {
	return fakeAuthURL + "?" + url.Values{"state": {state}}.Encode()
}

func (f *fakeAuthenticator) Exchange(_ context.Context, code, verifier, nonce string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchangeRuns++
	f.gotCode, f.gotVerifier, f.gotNonce = code, verifier, nonce
	if f.err != nil {
		return auth.User{}, f.err
	}
	return f.user, nil
}

func (f *fakeAuthenticator) LogoutURL() string { return fakeLogoutURL }
