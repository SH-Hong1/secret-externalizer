package web_test

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
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

	mu        sync.Mutex
	projects  map[project.ID]project.Project
	summaries map[project.ID]project.Summary
	next      project.ID
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

// ProjectSummaries returns every project ordered by name, with the summary
// that putSummary stored for it, or an empty one.
func (f *fakeProjects) ProjectSummaries(context.Context) ([]project.Summary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]project.Summary, 0, len(f.projects))
	for _, p := range f.projects {
		s := f.summaries[p.ID]
		s.Project = p
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b project.Summary) int { return cmp.Compare(a.Project.Name, b.Project.Name) })
	return out, nil
}

// putSummary stores the scan and verdict state ProjectSummaries returns for
// s.Project.ID.
func (f *fakeProjects) putSummary(s project.Summary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.summaries == nil {
		f.summaries = make(map[project.ID]project.Summary)
	}
	f.summaries[s.Project.ID] = s
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
	requestErr error
	getErr     error
	listErr    error
	countsErr  error
	aliveErr   error
	// noWorker makes WorkerAlive report that no worker is serving the queue.
	noWorker bool

	mu     sync.Mutex
	scans  map[scan.ID]scan.Scan
	counts map[scan.ID][]scan.CategoryCount
	next   scan.ID
}

func newFakeScans() *fakeScans {
	return &fakeScans{scans: make(map[scan.ID]scan.Scan), counts: make(map[scan.ID][]scan.CategoryCount)}
}

func (f *fakeScans) RequestScan(_ context.Context, projectID project.ID, by audit.Actor) (scan.ID, error) {
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
	f.scans[f.next] = scan.Scan{ID: f.next, ProjectID: projectID, Status: scan.StatusQueued, RequestedBy: by.Subject, RequesterName: by.Name}
	return f.next, nil
}

func (f *fakeScans) WorkerAlive(_ context.Context, _ time.Duration) (bool, error) {
	return !f.noWorker, f.aliveErr
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

func (f *fakeScans) CategoryCounts(_ context.Context, id scan.ID) ([]scan.CategoryCount, error) {
	if f.countsErr != nil {
		return nil, f.countsErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.counts[id]), nil
}

// putCounts stores the category counts that CategoryCounts returns for id.
func (f *fakeScans) putCounts(id scan.ID, counts ...scan.CategoryCount) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[id] = counts
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

func (b *blockingStore) Report(ctx context.Context, _ scan.ID) (report.Data, error) {
	return report.Data{}, b.wait(ctx)
}

func (b *blockingStore) CreateProject(ctx context.Context, _ project.Project) (project.ID, error) {
	return 0, b.wait(ctx)
}

func (b *blockingStore) Project(ctx context.Context, _ project.ID) (project.Project, error) {
	return project.Project{}, b.wait(ctx)
}

func (b *blockingStore) ProjectSummaries(ctx context.Context) ([]project.Summary, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) RequestScan(ctx context.Context, _ project.ID, _ audit.Actor) (scan.ID, error) {
	return 0, b.wait(ctx)
}

func (b *blockingStore) Scan(ctx context.Context, _ scan.ID) (scan.Scan, error) {
	return scan.Scan{}, b.wait(ctx)
}

func (b *blockingStore) ScansByProject(ctx context.Context, _ project.ID) ([]scan.Scan, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) CategoryCounts(ctx context.Context, _ scan.ID) ([]scan.CategoryCount, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) WorkerAlive(ctx context.Context, _ time.Duration) (bool, error) {
	return true, b.wait(ctx)
}

func (b *blockingStore) LatestScan(ctx context.Context, _ project.ID) (scan.Scan, error) {
	return scan.Scan{}, b.wait(ctx)
}

func (b *blockingStore) Entries(ctx context.Context, _ project.ID, _ scan.ID, _ triage.Filter) (triage.Page, error) {
	return triage.Page{}, b.wait(ctx)
}

func (b *blockingStore) VerdictCounts(ctx context.Context, _ project.ID, _ scan.ID) (map[triage.Verdict]int, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) Bundle(ctx context.Context, _ project.ID, _ scan.ID, _ triage.BundleID) (triage.Bundle, error) {
	return triage.Bundle{}, b.wait(ctx)
}

func (b *blockingStore) CustomerGroup(ctx context.Context, _ project.ID, _ scan.ID, _ triage.GroupKey) (triage.Group, error) {
	return triage.Group{}, b.wait(ctx)
}

func (b *blockingStore) Decide(ctx context.Context, _ project.ID, _ []triage.BundleID, _ triage.Decision, _ audit.Actor) error {
	return b.wait(ctx)
}

func (b *blockingStore) MergeBundle(ctx context.Context, _ project.ID, _, _ triage.BundleID, _ audit.Actor) error {
	return b.wait(ctx)
}

func (b *blockingStore) MoveFinding(ctx context.Context, _ project.ID, _ finding.ID, _ triage.BundleID, _ audit.Actor) (triage.BundleID, error) {
	return 0, b.wait(ctx)
}

func (b *blockingStore) AppliedBundles(ctx context.Context, _ project.ID, _ int) (triage.AppliedPage, error) {
	return triage.AppliedPage{}, b.wait(ctx)
}

func (b *blockingStore) ApplyActive(ctx context.Context, _ project.ID) (bool, error) {
	return false, b.wait(ctx)
}

func (b *blockingStore) AppliedCount(ctx context.Context, _ project.ID) (int, error) {
	return 0, b.wait(ctx)
}

func (b *blockingStore) SameValueBundles(ctx context.Context, _ project.ID, _ scan.ID, _ triage.BundleID) ([]triage.Entry, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) UsedNames(ctx context.Context, _ project.ID) ([]triage.Name, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) PastDecision(ctx context.Context, _ string, _ triage.BundleID) (triage.Decision, bool, error) {
	return triage.Decision{}, false, b.wait(ctx)
}

// fakeTriage keeps the triage state of projects in memory. Entries filters
// and pages entries the way the database does, and returns counts as given.
// A non-nil error field makes the matching method return that error.
type fakeTriage struct {
	latestErr  error
	entriesErr error
	countsErr  error
	bundleErr  error
	pastErr    error
	decideErr  error
	moveErr    error
	mergeErr   error
	activeErr  error
	// applyActive is what ApplyActive returns.
	applyActive bool
	// moveNew is the bundle MoveFinding returns for a move to a new bundle.
	moveNew triage.BundleID

	mu      sync.Mutex
	latest  map[project.ID]scan.Scan
	entries map[project.ID][]triage.Entry
	counts  map[project.ID]map[triage.Verdict]int
	bundles map[triage.BundleID]triage.Bundle
	groups  map[triage.GroupKey]triage.Group
	past    map[string]triage.Decision
	used    []triage.Name
	filters []triage.Filter
	// entriesCalls is the number of Entries calls.
	entriesCalls int
	same         map[triage.BundleID][]triage.Entry
	decides      []decideCall
	moves        []moveCall
	merges       []mergeCall
	applied      map[project.ID][]triage.Applied
}

// decideCall is the arguments of one Decide call.
type decideCall struct {
	Project  project.ID
	Bundles  []triage.BundleID
	Decision triage.Decision
	By       audit.Actor
}

// moveCall is the arguments of one MoveFinding call.
type moveCall struct {
	Project project.ID
	Finding finding.ID
	To      triage.BundleID
	By      audit.Actor
}

// mergeCall is the arguments of one MergeBundle call.
type mergeCall struct {
	Project project.ID
	From    triage.BundleID
	To      triage.BundleID
	By      audit.Actor
}

func newFakeTriage() *fakeTriage {
	return &fakeTriage{
		latest:  make(map[project.ID]scan.Scan),
		entries: make(map[project.ID][]triage.Entry),
		counts:  make(map[project.ID]map[triage.Verdict]int),
		bundles: make(map[triage.BundleID]triage.Bundle),
		groups:  make(map[triage.GroupKey]triage.Group),
		past:    make(map[string]triage.Decision),
		same:    make(map[triage.BundleID][]triage.Entry),
		applied: make(map[project.ID][]triage.Applied),
	}
}

// putApplied stores the applied bundles of project id, newest first.
func (f *fakeTriage) putApplied(id project.ID, applied ...triage.Applied) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied[id] = applied
}

func (f *fakeTriage) ApplyActive(_ context.Context, _ project.ID) (bool, error) {
	return f.applyActive, f.activeErr
}

func (f *fakeTriage) AppliedCount(_ context.Context, projectID project.ID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied[projectID]), nil
}

func (f *fakeTriage) AppliedBundles(_ context.Context, projectID project.ID, page int) (triage.AppliedPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.applied[projectID]
	p := triage.AppliedPage{Total: len(all), Pages: max(1, (len(all)+triage.AppliedPageSize-1)/triage.AppliedPageSize)}
	p.Page = min(max(page, 1), p.Pages)
	start := (p.Page - 1) * triage.AppliedPageSize
	p.Entries = slices.Clone(all[start:min(start+triage.AppliedPageSize, len(all))])
	return p, nil
}

func (f *fakeTriage) LatestScan(_ context.Context, projectID project.ID) (scan.Scan, error) {
	if f.latestErr != nil {
		return scan.Scan{}, f.latestErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.latest[projectID]
	if !ok {
		return scan.Scan{}, db.ErrNotFound
	}
	return sc, nil
}

func (f *fakeTriage) Entries(_ context.Context, projectID project.ID, _ scan.ID, filter triage.Filter) (triage.Page, error) {
	if f.entriesErr != nil {
		return triage.Page{}, f.entriesErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, filter)
	f.entriesCalls++
	var match []triage.Entry
	for _, e := range f.entries[projectID] {
		if filter.Category != "" && e.Category != filter.Category {
			continue
		}
		switch filter.Status {
		case triage.StatusAll:
		case triage.StatusUndecided:
			if e.Verdict != "" {
				continue
			}
		default:
			if string(e.Verdict) != filter.Status {
				continue
			}
		}
		match = append(match, e)
	}
	p := triage.Page{Total: len(match), Counts: map[triage.Verdict]int{}}
	for v, n := range f.counts[projectID] {
		p.Counts[v] = n
	}
	p.Pages = max(1, (p.Total+triage.PageSize-1)/triage.PageSize)
	p.Page = min(max(filter.Page, 1), p.Pages)
	start := (p.Page - 1) * triage.PageSize
	p.Entries = slices.Clone(match[start:min(start+triage.PageSize, len(match))])
	return p, nil
}

// VerdictCounts returns the counts given to setScan, as the database does,
// with a key for each verdict.
func (f *fakeTriage) VerdictCounts(_ context.Context, projectID project.ID, _ scan.ID) (map[triage.Verdict]int, error) {
	if f.countsErr != nil {
		return nil, f.countsErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[triage.Verdict]int{
		"": 0, triage.VerdictExternalize: 0, triage.VerdictRedact: 0, triage.VerdictFalsePositive: 0, triage.VerdictHold: 0,
	}
	for v, n := range f.counts[projectID] {
		out[v] = n
	}
	return out, nil
}

// entryCalls returns the number of Entries calls.
func (f *fakeTriage) entryCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entriesCalls
}

func (f *fakeTriage) Bundle(_ context.Context, projectID project.ID, _ scan.ID, id triage.BundleID) (triage.Bundle, error) {
	if f.bundleErr != nil {
		return triage.Bundle{}, f.bundleErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bundles[id]
	if !ok || b.ProjectID != projectID {
		return triage.Bundle{}, db.ErrNotFound
	}
	return b, nil
}

func (f *fakeTriage) CustomerGroup(_ context.Context, _ project.ID, _ scan.ID, key triage.GroupKey) (triage.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.groups[key]
	if !ok {
		return triage.Group{Key: key}, nil
	}
	return g, nil
}

// Decide records the call. Unless decideErr is set, it gives the bundles the
// decision and the verdict to the list entries of those bundles, and of the
// customer data groups whose bundles are all among them.
func (f *fakeTriage) Decide(_ context.Context, projectID project.ID, ids []triage.BundleID, d triage.Decision, by audit.Actor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decides = append(f.decides, decideCall{projectID, slices.Clone(ids), d, by})
	if f.decideErr != nil {
		return f.decideErr
	}
	for _, id := range ids {
		if b, ok := f.bundles[id]; ok {
			b.Decision = d
			f.bundles[id] = b
		}
	}
	entries := f.entries[projectID]
	for i, e := range entries {
		switch {
		case e.Bundle != 0 && slices.Contains(ids, e.Bundle):
			entries[i].Verdict = d.Verdict
		case e.Bundle == 0:
			g, ok := f.groups[e.Group]
			if !ok || !containsAll(ids, g.Bundles) {
				continue
			}
			delete(f.groups, e.Group)
			g.Key.Verdict = d.Verdict
			f.groups[g.Key] = g
			entries[i].Group, entries[i].Verdict = g.Key, d.Verdict
		}
	}
	return nil
}

func containsAll(set, sub []triage.BundleID) bool {
	for _, id := range sub {
		if !slices.Contains(set, id) {
			return false
		}
	}
	return true
}

// MoveFinding records the call and returns moveErr, or the bundle to, or
// moveNew for a move to a new bundle.
func (f *fakeTriage) MoveFinding(_ context.Context, projectID project.ID, id finding.ID, to triage.BundleID, by audit.Actor) (triage.BundleID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, moveCall{projectID, id, to, by})
	if f.moveErr != nil {
		return 0, f.moveErr
	}
	if to == 0 {
		return f.moveNew, nil
	}
	return to, nil
}

// MergeBundle records the call and returns mergeErr.
func (f *fakeTriage) MergeBundle(_ context.Context, projectID project.ID, from, to triage.BundleID, by audit.Actor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merges = append(f.merges, mergeCall{projectID, from, to, by})
	return f.mergeErr
}

// mergeCalls returns the MergeBundle calls so far.
func (f *fakeTriage) mergeCalls() []mergeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.merges)
}

func (f *fakeTriage) SameValueBundles(_ context.Context, _ project.ID, _ scan.ID, id triage.BundleID) ([]triage.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.same[id]), nil
}

// removeBundle forgets bundle id, as when its last member moved away.
func (f *fakeTriage) removeBundle(id triage.BundleID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.bundles, id)
}

// decideCalls returns the Decide calls so far.
func (f *fakeTriage) decideCalls() []decideCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.decides)
}

// moveCalls returns the MoveFinding calls so far.
func (f *fakeTriage) moveCalls() []moveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.moves)
}

func (f *fakeTriage) UsedNames(context.Context, project.ID) ([]triage.Name, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.used), nil
}

func (f *fakeTriage) PastDecision(_ context.Context, fingerprint string, _ triage.BundleID) (triage.Decision, bool, error) {
	if f.pastErr != nil {
		return triage.Decision{}, false, f.pastErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.past[fingerprint]
	return d, ok, nil
}

// stored returns the stored bundle id.
func (f *fakeTriage) stored(id triage.BundleID) triage.Bundle {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bundles[id]
}

// putBundle stores b, so Bundle returns it.
func (f *fakeTriage) putBundle(b triage.Bundle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bundles[b.ID] = b
}

// putGroup stores g under its key, so CustomerGroup returns it.
func (f *fakeTriage) putGroup(g triage.Group) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups[g.Key] = g
}

// setScan makes sc the latest succeeded scan of its project, with entries
// as its triage list and counts as its bundle counts.
func (f *fakeTriage) setScan(sc scan.Scan, counts map[triage.Verdict]int, entries ...triage.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest[sc.ProjectID] = sc
	f.entries[sc.ProjectID] = entries
	f.counts[sc.ProjectID] = counts
}

// lastFilter returns the filter of the last Entries call.
func (f *fakeTriage) lastFilter() (triage.Filter, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.filters) == 0 {
		return triage.Filter{}, false
	}
	return f.filters[len(f.filters)-1], true
}

// callLog records the order of calls across fakes.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (c *callLog) add(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func (c *callLog) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// revealCall is the arguments of one RecordReveal call.
type revealCall struct {
	Project project.ID
	Bundle  triage.BundleID
	By      audit.Actor
}

// fakeAudit keeps audit events in memory, newest first. RecordReveal records
// its call in log and returns revealErr.
type fakeAudit struct {
	log       *callLog
	revealErr error
	eventsErr error

	mu      sync.Mutex
	reveals []revealCall
	events  []audit.Event
	befores []int64
	limits  []int
}

func (f *fakeAudit) RecordReveal(_ context.Context, projectID project.ID, id triage.BundleID, by audit.Actor) error {
	f.log.add("RecordReveal")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reveals = append(f.reveals, revealCall{projectID, id, by})
	return f.revealErr
}

// AuditEvents returns at most limit of the stored events with an ID below
// before, or from the first when before is 0.
func (f *fakeAudit) AuditEvents(_ context.Context, before int64, limit int) ([]audit.Event, error) {
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.befores = append(f.befores, before)
	f.limits = append(f.limits, limit)
	var out []audit.Event
	for _, e := range f.events {
		if before != 0 && e.ID >= before {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeAudit) revealCalls() []revealCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reveals)
}

// fakeOriginals returns the value stored for a project and fingerprint, or
// err when it is set, or bao.ErrNoOriginal. It records each call in log.
type fakeOriginals struct {
	log *callLog
	err error

	mu     sync.Mutex
	values map[string]bao.Value
	reads  []string
}

func originalKey(projectID int64, fingerprint string) string {
	return fmt.Sprintf("%d/%s", projectID, fingerprint)
}

func (f *fakeOriginals) ReadPending(_ context.Context, projectID int64, fingerprint string) (bao.Value, error) {
	f.log.add("ReadPending")
	f.mu.Lock()
	defer f.mu.Unlock()
	key := originalKey(projectID, fingerprint)
	f.reads = append(f.reads, key)
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.values[key]
	if !ok {
		return nil, fmt.Errorf("reading: %w", bao.ErrNoOriginal)
	}
	return v, nil
}

// put stores value as the original of fingerprint in the project.
func (f *fakeOriginals) put(projectID int64, fingerprint string, value bao.Value) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.values == nil {
		f.values = make(map[string]bao.Value)
	}
	f.values[originalKey(projectID, fingerprint)] = value
}

func (f *fakeOriginals) readCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reads)
}

func (b *blockingStore) RecordReveal(ctx context.Context, _ project.ID, _ triage.BundleID, _ audit.Actor) error {
	return b.wait(ctx)
}

func (b *blockingStore) AuditEvents(ctx context.Context, _ int64, _ int) ([]audit.Event, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) ReadPending(ctx context.Context, _ int64, _ string) (bao.Value, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) ApplySummary(ctx context.Context, _ project.ID) (apply.Summary, error) {
	return apply.Summary{}, b.wait(ctx)
}

func (b *blockingStore) RequestApplyCheck(ctx context.Context, _ project.ID, _ audit.Actor) (apply.ID, error) {
	return 0, b.wait(ctx)
}

func (b *blockingStore) ConfirmApply(ctx context.Context, _ apply.ID, _ string, _ bool, _ audit.Actor) error {
	return b.wait(ctx)
}

func (b *blockingStore) ApplyRun(ctx context.Context, _ apply.ID) (apply.Run, error) {
	return apply.Run{}, b.wait(ctx)
}

func (b *blockingStore) ApplyHistory(ctx context.Context, _ project.ID, _ int) ([]apply.Run, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) ApplyItems(ctx context.Context, _ apply.ID) ([]apply.Item, error) {
	return nil, b.wait(ctx)
}

func (b *blockingStore) Reappeared(ctx context.Context, _ scan.ID) (int, error) {
	return 0, b.wait(ctx)
}

// fakeApplies keeps the apply state of projects in memory: one summary per
// project, runs and their items by ID. A non-nil error field makes the
// matching method return that error. It records the checks and
// confirmations it is asked for.
type fakeApplies struct {
	requestErr error
	confirmErr error

	mu         sync.Mutex
	summaries  map[project.ID]apply.Summary
	runs       map[apply.ID]apply.Run
	items      map[apply.ID][]apply.Item
	reappeared map[scan.ID]int
	checks     []checkCall
	confirms   []confirmCall
}

type checkCall struct {
	Project project.ID
	By      audit.Actor
}

type confirmCall struct {
	Run               apply.ID
	Digest            string
	AllowIrreversible bool
	By                audit.Actor
}

func newFakeApplies() *fakeApplies {
	return &fakeApplies{
		summaries:  make(map[project.ID]apply.Summary),
		runs:       make(map[apply.ID]apply.Run),
		items:      make(map[apply.ID][]apply.Item),
		reappeared: make(map[scan.ID]int),
	}
}

// putItems stores the items of run id.
func (f *fakeApplies) putItems(id apply.ID, items []apply.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[id] = items
}

// putReappeared sets what Reappeared returns for scan id.
func (f *fakeApplies) putReappeared(id scan.ID, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reappeared[id] = n
}

// put stores the summary of project 1, the project of the apply tests. When
// it names a check, the run and items are stored too.
func (f *fakeApplies) put(s apply.Summary, items ...apply.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaries[1] = s
	if s.Check != nil {
		f.runs[s.Check.ID] = *s.Check
		f.items[s.Check.ID] = items
	}
}

func (f *fakeApplies) putRun(r apply.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[r.ID] = r
}

func (f *fakeApplies) ApplySummary(_ context.Context, pid project.ID) (apply.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.summaries[pid]
	if !ok {
		return apply.Summary{}, db.ErrNotFound
	}
	return s, nil
}

func (f *fakeApplies) RequestApplyCheck(_ context.Context, pid project.ID, by audit.Actor) (apply.ID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks = append(f.checks, checkCall{pid, by})
	if f.requestErr != nil {
		return 0, f.requestErr
	}
	if _, ok := f.summaries[pid]; !ok {
		return 0, db.ErrNotFound
	}
	return 99, nil
}

func (f *fakeApplies) ConfirmApply(_ context.Context, id apply.ID, digest string, allow bool, by audit.Actor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirms = append(f.confirms, confirmCall{id, digest, allow, by})
	return f.confirmErr
}

func (f *fakeApplies) ApplyRun(_ context.Context, id apply.ID) (apply.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return apply.Run{}, db.ErrNotFound
	}
	return r, nil
}

// ApplyHistory returns the confirmed and active runs of pid, newest first,
// with their counts taken from their stored items, as the store does.
func (f *fakeApplies) ApplyHistory(_ context.Context, pid project.ID, limit int) ([]apply.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apply.Run
	for _, r := range f.runs {
		if r.ProjectID != pid || r.ConfirmedAt.IsZero() && !r.State.Active() {
			continue
		}
		r.Applied, r.Skipped, r.Failed = 0, 0, 0
		for _, it := range f.items[r.ID] {
			switch it.Status {
			case apply.StatusApplied:
				r.Applied++
			case apply.StatusSkipped:
				r.Skipped++
			case apply.StatusFailed:
				r.Failed++
			case apply.StatusPlanned, apply.StatusWriting:
			}
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b apply.Run) int { return cmp.Compare(b.ID, a.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeApplies) ApplyItems(_ context.Context, id apply.ID) ([]apply.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runs[id]; !ok {
		return nil, db.ErrNotFound
	}
	return f.items[id], nil
}

func (f *fakeApplies) Reappeared(_ context.Context, id scan.ID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reappeared[id], nil
}

func (f *fakeApplies) checkCalls() []checkCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.checks)
}

func (f *fakeApplies) confirmCalls() []confirmCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.confirms)
}

// fakeReports keeps the report data of scans in memory. A non-nil err makes
// Report return that error.
type fakeReports struct {
	err error

	mu   sync.Mutex
	data map[scan.ID]report.Data
}

func newFakeReports() *fakeReports {
	return &fakeReports{data: make(map[scan.ID]report.Data)}
}

func (f *fakeReports) Report(_ context.Context, id scan.ID) (report.Data, error) {
	if f.err != nil {
		return report.Data{}, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.data[id]
	if !ok {
		return report.Data{}, fmt.Errorf("reading report of scan %d: %w", id, db.ErrNotFound)
	}
	return d, nil
}

func (f *fakeReports) put(d report.Data) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[d.Scan.ID] = d
}

// fakeConverter remembers the documents it is given and whether their
// context had a deadline. It returns out, or err when that is set. When wait
// is true it waits until the context ends and returns its error wrapped in
// pdf.ErrUnavailable, as the real client does. When hold is not nil, the
// first call sends on entered and then waits until hold is closed, whatever
// its context says.
type fakeConverter struct {
	out     []byte
	err     error
	wait    bool
	hold    chan struct{}
	entered chan struct{}

	mu        sync.Mutex
	docs      []pdf.Document
	deadlines []bool
}

func (f *fakeConverter) Convert(ctx context.Context, doc pdf.Document) ([]byte, error) {
	_, hasDeadline := ctx.Deadline()
	f.mu.Lock()
	f.docs = append(f.docs, doc)
	f.deadlines = append(f.deadlines, hasDeadline)
	first := len(f.docs) == 1
	f.mu.Unlock()
	if f.hold != nil && first {
		f.entered <- struct{}{}
		<-f.hold
	}
	if f.wait {
		<-ctx.Done()
		return nil, fmt.Errorf("%w: %w", pdf.ErrUnavailable, ctx.Err())
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

// calls returns the documents Convert was given and, for each, whether its
// context had a deadline.
func (f *fakeConverter) calls() ([]pdf.Document, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.docs), slices.Clone(f.deadlines)
}

// fakeDashboards returns data, or err when that is set, and records the
// filters it is given.
type fakeDashboards struct {
	data dashboard.Data
	err  error

	mu      sync.Mutex
	filters []dashboard.Filter
}

func (f *fakeDashboards) Dashboard(_ context.Context, filter dashboard.Filter) (dashboard.Data, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, filter)
	if f.err != nil {
		return dashboard.Data{}, f.err
	}
	return f.data, nil
}

// calls returns the filters Dashboard was given.
func (f *fakeDashboards) calls() []dashboard.Filter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.filters)
}
