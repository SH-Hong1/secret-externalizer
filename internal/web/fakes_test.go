package web_test

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"sync"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
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

// fakeProjects keeps projects in memory. A non-nil error field makes the
// matching method return that error.
type fakeProjects struct {
	createErr error
	getErr    error
	listErr   error

	mu       sync.Mutex
	projects map[project.ID]project.Project
	next     project.ID
}

func newFakeProjects() *fakeProjects {
	return &fakeProjects{projects: make(map[project.ID]project.Project)}
}

func (f *fakeProjects) CreateProject(_ context.Context, p project.Project) (project.ID, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	p.ID = f.next
	f.projects[p.ID] = p
	return p.ID, nil
}

func (f *fakeProjects) Project(_ context.Context, id project.ID) (project.Project, error) {
	if f.getErr != nil {
		return project.Project{}, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return project.Project{}, db.ErrNotFound
	}
	return p, nil
}

func (f *fakeProjects) Projects(context.Context) ([]project.Project, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]project.Project, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b project.Project) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// all returns every stored project ordered by ID.
func (f *fakeProjects) all() []project.Project {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]project.Project, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b project.Project) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// fakeScans keeps scans in memory and allows one active scan per project, as
// the database does. A non-nil error field makes the matching method return
// that error.
type fakeScans struct {
	requestErr  error
	getErr      error
	listErr     error
	findingsErr error

	mu       sync.Mutex
	scans    map[scan.ID]scan.Scan
	findings map[scan.ID][]finding.Finding
	next     scan.ID
}

func newFakeScans() *fakeScans {
	return &fakeScans{scans: make(map[scan.ID]scan.Scan), findings: make(map[scan.ID][]finding.Finding)}
}

func (f *fakeScans) RequestScan(_ context.Context, projectID project.ID, requestedBy string) (scan.ID, error) {
	if f.requestErr != nil {
		return 0, f.requestErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sc := range f.scans {
		if sc.ProjectID == projectID && sc.Status.Active() {
			return sc.ID, db.ErrScanActive
		}
	}
	f.next++
	f.scans[f.next] = scan.Scan{ID: f.next, ProjectID: projectID, Status: scan.StatusQueued, RequestedBy: requestedBy}
	return f.next, nil
}

func (f *fakeScans) Scan(_ context.Context, id scan.ID) (scan.Scan, error) {
	if f.getErr != nil {
		return scan.Scan{}, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.scans[id]
	if !ok {
		return scan.Scan{}, db.ErrNotFound
	}
	return sc, nil
}

func (f *fakeScans) ScansByProject(_ context.Context, projectID project.ID) ([]scan.Scan, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []scan.Scan
	for _, sc := range f.scans {
		if sc.ProjectID == projectID {
			out = append(out, sc)
		}
	}
	slices.SortFunc(out, func(a, b scan.Scan) int { return cmp.Compare(b.ID, a.ID) })
	return out, nil
}

func (f *fakeScans) FindingsByScan(_ context.Context, id scan.ID) ([]finding.Finding, error) {
	if f.findingsErr != nil {
		return nil, f.findingsErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.findings[id]), nil
}

// putFindings stores the findings that FindingsByScan returns for id.
func (f *fakeScans) putFindings(id scan.ID, fs ...finding.Finding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findings[id] = fs
}

// put stores sc under its ID.
func (f *fakeScans) put(sc scan.Scan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scans[sc.ID] = sc
}

// all returns every stored scan ordered by ID.
func (f *fakeScans) all() []scan.Scan {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]scan.Scan, 0, len(f.scans))
	for _, sc := range f.scans {
		out = append(out, sc)
	}
	slices.SortFunc(out, func(a, b scan.Scan) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// blockingStore is a project and scan store whose every call waits until
// its context ends. It records whether each call's context had a deadline.
type blockingStore struct {
	mu        sync.Mutex
	calls     int
	deadlines int
}

func (b *blockingStore) wait(ctx context.Context) error {
	b.mu.Lock()
	b.calls++
	if _, ok := ctx.Deadline(); ok {
		b.deadlines++
	}
	b.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// counts returns the number of calls and of calls whose context had a
// deadline.
func (b *blockingStore) counts() (calls, deadlines int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, b.deadlines
}

func (b *blockingStore) CreateProject(ctx context.Context, _ project.Project) (project.ID, error) {
	return 0, b.wait(ctx)
}

func (b *blockingStore) Project(ctx context.Context, _ project.ID) (project.Project, error) {
	return project.Project{}, b.wait(ctx)
}

func (b *blockingStore) Projects(ctx context.Context) ([]project.Project, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) RequestScan(ctx context.Context, _ project.ID, _ string) (scan.ID, error) {
	return 0, b.wait(ctx)
}

func (b *blockingStore) Scan(ctx context.Context, _ scan.ID) (scan.Scan, error) {
	return scan.Scan{}, b.wait(ctx)
}

func (b *blockingStore) ScansByProject(ctx context.Context, _ project.ID) ([]scan.Scan, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) FindingsByScan(ctx context.Context, _ scan.ID) ([]finding.Finding, error) {
	return nil, b.wait(ctx)
}
