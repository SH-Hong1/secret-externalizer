package apply_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openbao/openbao/api/v2"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/betterleaks"
	"github.com/SH-Hong1/secret-externalizer/internal/dashboard"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/keyfile"
	"github.com/SH-Hong1/secret-externalizer/internal/manifest"
	"github.com/SH-Hong1/secret-externalizer/internal/opengrep"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/ruleset"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// realScanner returns the scanner executable named by the environment
// variable env or, failing that, the one in the repository's bin/ folder.
// It skips the test when there is none.
func realScanner(t *testing.T, env, name string) string {
	t.Helper()
	p := os.Getenv(env)
	if p == "" {
		p = filepath.Join("..", "..", "bin", name)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("Abs(%q) error = %v", p, err)
	}
	if _, err := os.Stat(abs); err != nil { //nolint:gosec // G703: the path is the developer's own setting or the repository's bin/
		t.Skipf("%s is not installed (%v); run make scanners", name, err)
	}
	return abs
}

// secretKind reports whether a value of kind k is one that must be nowhere
// outside OpenBao once it is externalized. A host, a port, an account or a
// URL may stay at a place no rule reports, and in a stored surrounding
// line.
func secretKind(k finding.Kind) bool {
	return slices.Contains([]finding.Kind{finding.KindPassword, finding.KindToken, finding.KindPrivateKey, finding.KindOther}, k)
}

// bundleIDs returns the bundles of the project, in order.
func bundleIDs(t *testing.T, admin *pgxpool.Pool, schema string, projectID project.ID) []triage.BundleID {
	t.Helper()
	rows, err := admin.Query(t.Context(), "SELECT id FROM "+schema+".bundle WHERE project_id = $1 ORDER BY id", projectID)
	if err != nil {
		t.Fatalf("reading the bundles: %v", err)
	}
	defer rows.Close()
	var ids []triage.BundleID
	for rows.Next() {
		var id triage.BundleID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("reading a bundle: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the bundles: %v", err)
	}
	return ids
}

// tableNames returns the tables of schema.
func tableNames(t *testing.T, admin *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := admin.Query(t.Context(), "SELECT table_name::text FROM information_schema.tables WHERE table_schema = $1 ORDER BY 1", schema)
	if err != nil {
		t.Fatalf("listing the tables: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("reading a table name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("listing the tables: %v", err)
	}
	return names
}

// storedKeys returns the keys OpenBao holds for the project, read with the
// root token.
func storedKeys(ctx context.Context, t *testing.T, root *api.Client, projectID project.ID) []string {
	t.Helper()
	listed, err := root.Logical().ListWithContext(ctx, "secret/metadata/projects/"+strconv.FormatInt(int64(projectID), 10))
	if err != nil {
		t.Fatalf("listing the project's stored values: %v", err)
	}
	if listed == nil {
		return nil
	}
	var keys []string
	if raw, ok := listed.Data["keys"].([]any); ok {
		for _, k := range raw {
			s, _ := k.(string) // a key that is not a string shows as empty and fails the comparisons
			keys = append(keys, s)
		}
	}
	slices.Sort(keys)
	return keys
}

// projectFiles returns the bytes of every regular file under dir by its
// slash-separated path, leaving out git's own folders.
func projectFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: the path is under t.TempDir
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatalf("reading the files of %s: %v", dir, err)
	}
	return out
}

// runScan claims the next job, which must be the job of scan id, runs it
// with scanner, and fails the test unless the scan succeeded.
func runScan(t *testing.T, store *db.Store, scanner *scan.Runner, id scan.ID, logs *bytes.Buffer) {
	t.Helper()
	ctx := t.Context()
	j := claim(t, store, job.KindScan)
	if scan.ID(j.ScanID) != id {
		t.Fatalf("ClaimJob() = the job of scan %d, want that of scan %d", j.ScanID, id)
	}
	if err := scanner.Handle(ctx, j); err != nil {
		t.Fatalf("Handle() of scan %d error = %v", id, err)
	}
	got, err := store.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.Status != scan.StatusSucceeded {
		t.Fatalf("Scan(%d) = status %q, failure %q; want succeeded; log:\n%s", id, got.Status, got.FailureReason, logs.String())
	}
}

// place names a finding by its value, file and configuration key.
type place struct{ fingerprint, file, key string }

// TestIntegration_ScanApplyRescan takes the example project through what a
// user does with it, with the real scanners, database, OpenBao and git: a
// scan, the suggested verdict for every bundle, the check, the apply, and
// the scan that confirms the apply. It checks what must hold whatever the
// rules find: the confirming scan finds none of the places the apply
// changed and nothing the first scan did not find, every stored value is
// the value its bundle was found with, no externalized secret is left in a
// file the apply could change or in any record, and the dashboard counts
// the confirming scan as the report does.
func TestIntegration_ScanApplyRescan(t *testing.T) {
	blBin := realScanner(t, "BETTERLEAKS_BIN", "betterleaks")
	ogBin := realScanner(t, "OPENGREP_BIN", "opengrep")
	store, admin, schema := realStore(t)
	vault, root := realVault(t)
	ctx := t.Context()
	f := newShop(t, inRepository)

	rules, err := filepath.Abs(filepath.Join("..", "..", "rules"))
	if err != nil {
		t.Fatalf("Abs(rules) error = %v", err)
	}
	rulesVersion, err := ruleset.Version(rules)
	if err != nil {
		t.Fatalf("ruleset.Version(%q) error = %v", rules, err)
	}
	env := scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}
	logger := slog.New(slog.NewJSONHandler(&f.logs, nil))
	scanner := scan.NewRunner(logger, store, vault, vault, []scan.Scanner{
		betterleaks.New(blBin, filepath.Join(rules, "betterleaks.toml"), env),
		opengrep.New(ogBin, filepath.Join(rules, "opengrep"), nil, env),
		keyfile.New(),
	}, scan.Options{WorkerID: realWorker, Root: f.root, RulesVersion: rulesVersion, TempDir: t.TempDir(), Git: f.gitBinary})
	applier := apply.NewRunner(logger, store, vault, apply.Options{WorkerID: realWorker, Root: f.root, Git: f.gitBinary})

	actor := audit.Actor{Subject: "fake-subject", Name: "fake-user"}
	projectID, err := store.CreateProject(ctx, project.Project{Name: "legacy-shop", Path: f.path, CreatedBy: actor.Subject})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	t.Cleanup(func() {
		// The dev server keeps what the test stored until it stops; the
		// root token removes it.
		cctx := context.WithoutCancel(ctx)
		id := strconv.FormatInt(int64(projectID), 10)
		for _, key := range storedKeys(cctx, t, root, projectID) {
			if _, err := root.Logical().DeleteWithContext(cctx, "secret/metadata/projects/"+id+"/"+key); err != nil {
				t.Errorf("deleting the stored value %s: %v", key, err)
			}
		}
		pending, err := vault.ListPending(cctx, int64(projectID))
		if err != nil {
			t.Errorf("ListPending() error = %v", err)
		}
		for _, fp := range pending {
			if err := vault.DeletePending(cctx, int64(projectID), fp); err != nil {
				t.Errorf("DeletePending() error = %v", err)
			}
		}
	})

	findingsOf := func(id scan.ID) map[place]finding.Finding {
		t.Helper()
		list, err := store.FindingsByScan(ctx, id)
		if err != nil {
			t.Fatalf("FindingsByScan(%d) error = %v", id, err)
		}
		out := map[place]finding.Finding{}
		for _, fd := range list {
			out[place{fd.Fingerprint, fd.File, fd.Key}] = fd
		}
		return out
	}

	// The first scan, and the suggested verdict for every bundle.
	scanID, err := store.RequestScan(ctx, projectID, actor)
	if err != nil {
		t.Fatalf("RequestScan() error = %v", err)
	}
	runScan(t, store, scanner, scanID, &f.logs)
	first := findingsOf(scanID)
	// A value under several keys of one file is one finding for each key.
	// The example has two such files: the port of the two JDBC URLs in each.
	keysOf := map[place][]string{}
	for pl := range first {
		k := place{pl.fingerprint, pl.file, ""}
		keysOf[k] = append(keysOf[k], pl.key)
	}
	var splits []string
	for pl, keys := range keysOf {
		if len(keys) > 1 {
			slices.Sort(keys)
			splits = append(splits, pl.file+": "+strings.Join(keys, ", "))
		}
	}
	slices.Sort(splits)
	wantSplits := []string{
		"src/main/resources/application-prod.yml: report.datasource.url, spring.datasource.url",
		"src/main/webapp/WEB-INF/spring/pool-context.xml: legacyDataSource.url, reportPool.jdbcUrl",
	}
	if diff := cmp.Diff(wantSplits, splits); diff != "" {
		t.Errorf("files holding one value under several keys (-want +got):\n%s", diff)
	}
	if len(first) == 0 {
		t.Fatalf("FindingsByScan(%d) is empty, want the findings of the example", scanID)
	}
	bundles := map[triage.BundleID]triage.Bundle{}
	for _, id := range bundleIDs(t, admin, schema, projectID) {
		b, err := store.Bundle(ctx, projectID, scanID, id)
		if err != nil {
			t.Fatalf("Bundle(%d) error = %v", id, err)
		}
		bundles[id] = b
		used, err := store.UsedNames(ctx, projectID)
		if err != nil {
			t.Fatalf("UsedNames() error = %v", err)
		}
		d := triage.Default(b, used).Decision
		switch {
		case b.Category == finding.CategoryCustomerData:
			d = triage.Decision{Verdict: triage.VerdictRedact}
		case d.Verdict == "":
			d.Verdict = triage.VerdictExternalize
		}
		if err := store.Decide(ctx, projectID, []triage.BundleID{id}, d, actor); err != nil {
			t.Fatalf("Decide(bundle %d, %+v) error = %v", id, d, err)
		}
	}

	// The check and the apply.
	before := projectFiles(t, f.dir)
	id, err := store.RequestApplyCheck(ctx, projectID, actor)
	if err != nil {
		t.Fatalf("RequestApplyCheck() error = %v", err)
	}
	if err := applier.Check(ctx, claim(t, store, job.KindApplyCheck)); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	checked, err := store.ApplyRun(ctx, id)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", id, err)
	}
	if checked.State != apply.StateReady {
		t.Fatalf("ApplyRun(%d) after the check = state %q, blockers %v, failure %q; want ready; log:\n%s",
			id, checked.State, checked.Blockers, checked.FailureReason, f.logs.String())
	}
	if diff := cmp.Diff(before, projectFiles(t, f.dir)); diff != "" {
		t.Errorf("the scan and the check changed the project (-before +after):\n%s", diff)
	}
	if err := store.ConfirmApply(ctx, id, checked.PlanDigest, true, actor); err != nil {
		t.Fatalf("ConfirmApply(%d) error = %v", id, err)
	}
	if err := applier.Apply(ctx, claim(t, store, job.KindApply)); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	done, err := store.ApplyRun(ctx, id)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", id, err)
	}
	if done.State != apply.StateSucceeded || done.RescanID == 0 {
		t.Fatalf("ApplyRun(%d) after the apply = state %q, failure %q, confirming scan %d; want succeeded with a confirming scan; log:\n%s",
			id, done.State, done.FailureReason, done.RescanID, f.logs.String())
	}
	items, err := store.ApplyItems(ctx, id)
	if err != nil {
		t.Fatalf("ApplyItems(%d) error = %v", id, err)
	}

	// The items: none failed, and a skipped one has a reason the example
	// is built to meet.
	findingByID := map[finding.ID]finding.Finding{}
	for _, fd := range first {
		findingByID[fd.ID] = fd
	}
	wantSkips := map[apply.Reason]bool{
		apply.ReasonNestedRepository: true,
		apply.ReasonKeyInSourceFile:  true,
	}
	untouched := map[place]bool{} // a location of the finding was not applied
	changed := map[place]bool{}
	skips := map[apply.Reason]int{}
	for _, it := range items {
		fd, ok := findingByID[it.FindingID]
		if !ok {
			t.Fatalf("item in %s line %d names finding %d, which the scan did not store", it.File, it.StartLine, it.FindingID)
		}
		p := place{fd.Fingerprint, fd.File, fd.Key}
		if it.Status == apply.StatusApplied {
			changed[p] = true
			continue
		}
		untouched[p] = true
		if it.Status != apply.StatusSkipped {
			t.Errorf("item in %s line %d (%s %s) = status %q, reason %q; want applied or skipped", it.File, it.StartLine, it.Category, it.Kind, it.Status, it.Reason)
			continue
		}
		skips[it.Reason]++
		if !wantSkips[it.Reason] {
			t.Errorf("item in %s line %d (%s %s) was skipped as %q, want it applied", it.File, it.StartLine, it.Category, it.Kind, it.Reason)
		}
	}
	if len(changed) == 0 {
		t.Fatalf("the apply changed nothing; items: %+v", items)
	}
	t.Logf("first scan: %d findings in %d bundles; apply: %d applied, %d skipped %v", len(first), len(bundles), done.Applied, done.Skipped, skips)

	// OpenBao: each stored value is the value its bundle was found with.
	var secrets [][]byte
	stores := map[string]triage.BundleID{}
	for _, it := range items {
		if it.Status == apply.StatusApplied && it.Action != apply.ActionRedact {
			stores[it.StorePath] = it.BundleID
		}
	}
	for path, bundleID := range stores {
		secret, err := root.Logical().ReadWithContext(ctx, path)
		if err != nil || secret == nil {
			t.Errorf("reading %s = %v, %v; want the stored value", path, secret != nil, err)
			continue
		}
		data, _ := secret.Data["data"].(map[string]any) // a missing data field fails the checks below
		encoded, _ := data["value"].(string)            // likewise
		value, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(value) == 0 {
			t.Errorf("%s holds no base64 value", path)
			continue
		}
		fps, err := vault.Fingerprints(ctx, [][]byte{value})
		if err != nil || len(fps) != 1 {
			t.Fatalf("Fingerprints() = %d fingerprints, %v; want 1", len(fps), err)
		}
		b := bundles[bundleID]
		if fps[0] != b.Fingerprint {
			t.Errorf("%s holds %d bytes that are not the value of its bundle (%s %s)", path, len(value), b.Category, b.Kind)
		}
		if secretKind(b.Kind) {
			secrets = append(secrets, value)
		}
	}
	if len(secrets) == 0 {
		t.Fatalf("the apply stored no password, token or key; stored paths: %d", len(stores))
	}

	// The files: an externalized secret is left only in a file the apply
	// skipped a place of.
	skippedFiles := map[string]bool{}
	for p := range untouched {
		skippedFiles[p.file] = true
	}
	changedFiles := map[string]bool{}
	for p := range changed {
		changedFiles[p.file] = true
	}
	after := projectFiles(t, f.dir)
	for file, content := range after {
		if skippedFiles[file] {
			continue
		}
		for _, v := range secrets {
			if bytes.Contains(content, v) {
				t.Errorf("%s still holds an externalized secret (%d bytes)", file, len(v))
			}
		}
	}
	m, err := manifest.Parse(read(t, f.dir, manifest.FileName))
	if err != nil {
		t.Fatalf("manifest.Parse() error = %v", err)
	}
	if len(m.Entries) < len(stores) {
		t.Errorf("the manifest has %d entries, want one or more for each of the %d stored values", len(m.Entries), len(stores))
	}

	// The confirming scan: the changed places are gone, and it finds
	// nothing new, so no placeholder, mask or manifest line is a finding.
	runScan(t, store, scanner, done.RescanID, &f.logs)
	second := findingsOf(done.RescanID)
	for p, fd := range second {
		if _, ok := first[p]; !ok {
			t.Errorf("the confirming scan found a new %s %s in %s line %d", fd.Category, fd.Kind, fd.File, fd.Locations[0].StartLine)
		}
		if changed[p] && !untouched[p] {
			t.Errorf("the confirming scan found the %s %s in %s line %d again, which the apply changed", fd.Category, fd.Kind, fd.File, fd.Locations[0].StartLine)
		}
	}
	for p, fd := range first {
		if _, ok := second[p]; !ok && !changedFiles[p.file] {
			t.Errorf("the confirming scan lost the %s %s in %s, a file the apply did not change", fd.Category, fd.Kind, fd.File)
		}
	}
	reappeared, err := store.Reappeared(ctx, done.RescanID)
	if err != nil {
		t.Fatalf("Reappeared(%d) error = %v", done.RescanID, err)
	}
	wantReappeared := 0
	byPlace := bundlesByPlace(t, admin, schema, projectID)
	for p := range second {
		if appliedBundle(items, byPlace[p]) {
			wantReappeared++
		}
	}
	if reappeared != wantReappeared {
		t.Errorf("Reappeared(%d) = %d, want %d: the findings of the confirming scan in a bundle the apply stored", done.RescanID, reappeared, wantReappeared)
	}

	// The records: no externalized secret in any table, the log or the
	// manifest.
	places := map[string]string{
		"worker log": f.logs.String(),
		"manifest":   string(after[manifest.FileName]),
	}
	for _, table := range tableNames(t, admin, schema) {
		places["table "+table] = tableText(t, admin, schema, table)
	}
	for where, text := range places {
		for _, v := range secrets {
			if strings.Contains(text, string(v)) {
				t.Errorf("the %s holds an externalized secret (%d bytes)", where, len(v))
			}
		}
	}

	// The dashboard counts the confirming scan as its report does.
	rep, err := store.Report(ctx, done.RescanID)
	if err != nil {
		t.Fatalf("Report(%d) error = %v", done.RescanID, err)
	}
	dash, err := store.Dashboard(ctx, dashboard.Filter{Project: projectID})
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	if len(dash.Projects) != 1 || dash.Projects[0].Basis != done.RescanID || dash.Projects[0].Previous != scanID || len(dash.Points) != 2 {
		t.Fatalf("Dashboard() = %d projects, %d points, %+v; want the project with scan %d after scan %d", len(dash.Projects), len(dash.Points), dash.Projects, done.RescanID, scanID)
	}
	if diff := cmp.Diff(rep.Totals, dash.Projects[0].Totals); diff != "" {
		t.Errorf("totals of the confirming scan (-report +dashboard):\n%s", diff)
	}
	if rep.Totals.Externalized.Bundles == 0 || rep.Totals.Redacted.Bundles == 0 {
		t.Errorf("Report(%d) totals = %+v, want externalized and redacted bundles", done.RescanID, rep.Totals)
	}

	// Once the changes are committed, a second check has nothing left to
	// apply: what is left is what an apply always skips.
	f.commit()
	again, err := store.RequestApplyCheck(ctx, projectID, actor)
	if err != nil {
		t.Fatalf("RequestApplyCheck() after the confirming scan error = %v", err)
	}
	if err := applier.Check(ctx, claim(t, store, job.KindApplyCheck)); err != nil {
		t.Fatalf("Check() after the confirming scan error = %v", err)
	}
	rechecked, err := store.ApplyRun(ctx, again)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", again, err)
	}
	if rechecked.State != apply.StateBlocked || !slices.Equal(rechecked.Blockers, []apply.Blocker{apply.BlockerNothingToApply}) {
		t.Errorf("ApplyRun(%d) after the confirming scan = state %q, blockers %v; want blocked with only %q",
			again, rechecked.State, rechecked.Blockers, apply.BlockerNothingToApply)
	}
}

// bundlesByPlace returns the bundle of every finding of the project.
func bundlesByPlace(t *testing.T, admin *pgxpool.Pool, schema string, projectID project.ID) map[place]triage.BundleID {
	t.Helper()
	rows, err := admin.Query(t.Context(), "SELECT fingerprint, file_path, key, bundle_id FROM "+schema+".finding WHERE project_id = $1", projectID)
	if err != nil {
		t.Fatalf("reading the findings: %v", err)
	}
	defer rows.Close()
	out := map[place]triage.BundleID{}
	for rows.Next() {
		var (
			p  place
			id triage.BundleID
		)
		if err := rows.Scan(&p.fingerprint, &p.file, &p.key, &id); err != nil {
			t.Fatalf("reading a finding: %v", err)
		}
		out[p] = id
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the findings: %v", err)
	}
	return out
}

// appliedBundle reports whether items has an applied replacement of bundle
// id.
func appliedBundle(items []apply.Item, id triage.BundleID) bool {
	for _, it := range items {
		if it.BundleID == id && it.Status == apply.StatusApplied && it.Action != apply.ActionRedact {
			return true
		}
	}
	return false
}
