package apply_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openbao/openbao/api/v2"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/manifest"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
	"github.com/SH-Hong1/secret-externalizer/internal/worktree/worktreetest"
)

// realRootToken is the root token of the openbao-test compose service, an
// in-memory dev server that never holds real values.
const realRootToken = "dev-only-openbao-test-root-token" //nolint:gosec // G101: the fake root token of a local dev server

// realWorkerPolicy is the worker policy that deploy/openbao/config.hcl
// creates; internal/bao checks that its own copy matches that file.
const realWorkerPolicy = `
path "transit/hmac/fingerprint" {
  capabilities = ["update"]
}
path "secret/data/pending/*" {
  capabilities = ["create", "update"]
}
path "secret/data/projects/*" {
  capabilities = ["create", "update", "read"]
}
path "secret/metadata/pending/*" {
  capabilities = ["list", "delete"]
}
`

// The values the integration test externalizes or judges, besides those of
// runner_test.go. Every value is obviously fake.
const (
	realMainValue = "dev-only-fake-real-main-password"
	falsePositive = "dev-only-fake-partner-password"
	realWorker    = "worker-real"
)

// realStore returns a migrated Store on a schema of its own, created for
// this test and dropped when it ends, and a pool on the same database for
// the test's own queries, with the schema's name. It skips the test when
// TEST_DATABASE_URL is not set.
func realStore(t *testing.T) (store *db.Store, admin *pgxpool.Pool, schema string) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to run against postgres")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(admin.Close)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	schema = "test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("CREATE SCHEMA %s error = %v", schema, err)
	}
	t.Cleanup(func() {
		// The test context is already cancelled during cleanup.
		if _, err := admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("DROP SCHEMA %s error = %v", schema, err)
		}
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL is not a URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store, err = db.Open(ctx, db.DSN(u.String()))
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	// The dev server outlives the schema, so project IDs start at a random
	// number to keep this test's store paths apart from earlier runs.
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	start := int64(binary.BigEndian.Uint64(b[:])>>34) + 1
	if _, err := admin.Exec(ctx, "ALTER TABLE "+schema+".project ALTER COLUMN id RESTART WITH "+strconv.FormatInt(start, 10)); err != nil {
		t.Fatalf("restarting project IDs: %v", err)
	}
	return store, admin, schema
}

// realVault prepares the openbao-test dev server the way
// deploy/openbao/config.hcl prepares the real one and returns a client with
// the worker role and a client with the root token. It skips the test when
// TEST_BAO_ADDR is not set.
func realVault(t *testing.T) (worker *bao.Client, root *api.Client) {
	t.Helper()
	addr := os.Getenv("TEST_BAO_ADDR")
	if addr == "" {
		t.Skip("set TEST_BAO_ADDR to run against OpenBao, for example TEST_BAO_ADDR=http://127.0.0.1:18200 (the compose openbao-test service)")
	}
	ctx := t.Context()
	cfg := api.NewConfig()
	cfg.Address = addr
	root, err := api.NewClient(cfg)
	if err != nil {
		t.Fatalf("api.NewClient(%q) error = %v", addr, err)
	}
	root.SetToken(realRootToken)
	mounts, err := root.Sys().ListMountsWithContext(ctx)
	if err != nil {
		t.Fatalf("listing mounts: %v", err)
	}
	if _, ok := mounts["transit/"]; !ok {
		if err := root.Sys().MountWithContext(ctx, "transit", &api.MountInput{Type: "transit"}); err != nil {
			t.Fatalf("mounting transit: %v", err)
		}
	}
	if _, ok := mounts["secret/"]; !ok {
		in := &api.MountInput{Type: "kv", Options: map[string]string{"version": "2"}}
		if err := root.Sys().MountWithContext(ctx, "secret", in); err != nil {
			t.Fatalf("mounting secret: %v", err)
		}
	}
	auths, err := root.Sys().ListAuthWithContext(ctx)
	if err != nil {
		t.Fatalf("listing auth methods: %v", err)
	}
	if _, ok := auths["approle/"]; !ok {
		if err := root.Sys().EnableAuthWithOptionsWithContext(ctx, "approle", &api.EnableAuthOptions{Type: "approle"}); err != nil {
			t.Fatalf("enabling approle: %v", err)
		}
	}
	writes := []struct {
		path string
		data map[string]any
	}{
		{"transit/keys/fingerprint", map[string]any{"type": "aes256-gcm96"}},
		{"secret/config", map[string]any{"max_versions": 100}},
		{"sys/policies/acl/worker", map[string]any{"policy": realWorkerPolicy}},
		{"auth/approle/role/worker", map[string]any{"token_policies": []string{"worker"}, "token_ttl": "1h", "token_max_ttl": "1h"}},
		{"auth/approle/role/worker/role-id", map[string]any{"role_id": "se-worker"}},
	}
	for _, w := range writes {
		if _, err := root.Logical().WriteWithContext(ctx, w.path, w.data); err != nil {
			t.Fatalf("writing %s: %v", w.path, err)
		}
	}
	secret, err := root.Logical().WriteWithContext(ctx, "auth/approle/role/worker/secret-id", nil)
	if err != nil {
		t.Fatalf("creating a secret-id: %v", err)
	}
	secretID, ok := secret.Data["secret_id"].(string)
	if !ok {
		t.Fatalf("creating a secret-id: the response holds no secret_id")
	}
	worker, err = bao.New(addr, bao.Credentials{RoleID: "se-worker", SecretID: bao.SecretID(secretID)}, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("bao.New(%q) error = %v", addr, err)
	}
	return worker, root
}

// wholeFile returns the location a scan gives the key file named file.
func (f *fixture) wholeFile(file string) finding.Location {
	f.t.Helper()
	raw := read(f.t, f.dir, file)
	l := finding.Location{StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 1, FileHash: sha(raw)}
	if tf, err := textfile.Decode(raw); err == nil && len(tf.Text) > 0 {
		_, size := utf8.DecodeLastRune(tf.Text)
		l.EndLine, l.EndCol = position(tf.Text, len(tf.Text)-size)
	}
	return l
}

// realFinding returns a finding of kind k in category c at l in file, as a
// scan stores it.
func realFinding(fingerprint, file string, c finding.Category, k finding.Kind, val string, l finding.Location) finding.Finding {
	l.Detections = []finding.Detection{{Tool: "betterleaks", Rule: "fake-rule"}}
	return finding.Finding{
		Fingerprint: fingerprint, File: file, Category: c, Kind: k,
		Preview: finding.Preview(c, k, val), Locations: []finding.Location{l},
	}
}

// claim claims the next job as realWorker and fails the test unless it is
// of kind k.
func claim(t *testing.T, store *db.Store, k job.Kind) job.Job {
	t.Helper()
	j, err := store.ClaimJob(t.Context(), realWorker, time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if j.Kind != k {
		t.Fatalf("ClaimJob() = %+v, want a job of kind %s", j, k)
	}
	return j
}

// tableText returns every row of table in schema as text, one per line.
func tableText(t *testing.T, admin *pgxpool.Pool, schema, table string) string {
	t.Helper()
	rows, err := admin.Query(t.Context(), "SELECT t::text FROM "+schema+"."+table+" t")
	if err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("reading a row of %s: %v", table, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	return strings.Join(out, "\n")
}

// storedBytes reads key of the project with the root token and returns the
// bytes it holds and its format.
func storedBytes(t *testing.T, root *api.Client, projectID project.ID, key string) (value []byte, format string) {
	t.Helper()
	path := bao.StorePath(int64(projectID), key)
	secret, err := root.Logical().ReadWithContext(t.Context(), path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if secret == nil {
		t.Errorf("%s holds nothing, want a stored value", path)
		return nil, ""
	}
	data, _ := secret.Data["data"].(map[string]any) // a missing data field fails the checks below
	encoded, _ := data["value"].(string)            // likewise
	format, _ = data["format"].(string)             // likewise
	value, err = base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Errorf("%s holds a value that is not base64", path)
	}
	return value, format
}

// TestIntegration_ApplyLegacyShop checks and applies the example project
// with the real database, the openbao-test server and git: one password
// in four files, among them an EUC-KR file with CRLF line ends and a shell
// script, a password in an ignored file, the phone number in a tracked and
// an untracked log, the key file conf/partner.pem with its PEM block, and a
// false positive whose original the scan parked. The untracked log is
// redacted only with the consent given when the apply is confirmed.
func TestIntegration_ApplyLegacyShop(t *testing.T) {
	for _, consent := range []bool{false, true} {
		t.Run("consent "+strconv.FormatBool(consent), func(t *testing.T) {
			testApplyLegacyShop(t, consent)
		})
	}
}

func testApplyLegacyShop(t *testing.T, consent bool) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	store, admin, schema := realStore(t)
	vault, root := realVault(t)
	ctx := t.Context()

	f := newShop(t, inRepository)
	f.replaceIn(ymlFile, ymlValue, realMainValue)
	f.replaceIn(xmlFile, xmlValue, realMainValue)
	f.replaceIn(euckrFile, euckrValue, realMainValue)
	f.replaceIn(shFile, shValue, realMainValue)
	f.commit()
	mainFiles := []string{euckrFile, shFile, ymlFile, xmlFile}
	untouched := []string{localFile, untrackLog, trackedLog, pemFile}
	before := map[string][]byte{}
	for _, file := range append(slices.Clone(mainFiles), untouched...) {
		before[file] = read(t, f.dir, file)
	}
	pemRaw := before[pemFile]
	pemBlock := strings.TrimSuffix(string(pemRaw), "\n")
	values := [][]byte{[]byte(realMainValue), []byte(localValue), []byte(phone), pemRaw, []byte(pemBlock), []byte(falsePositive)}
	fps, err := vault.Fingerprints(ctx, values)
	if err != nil || len(fps) != len(values) {
		t.Fatalf("Fingerprints() = %d fingerprints, %v; want %d", len(fps), err, len(values))
	}
	fpMain, fpLocal, fpPhone, fpKeyFile, fpBlock, fpFalse := fps[0], fps[1], fps[2], fps[3], fps[4], fps[5]
	// What the test searches for: every value, and the line of the key
	// rather than the whole block.
	searched := []string{realMainValue, localValue, phone, pemLine, falsePositive}

	var findings []finding.Finding
	for _, file := range mainFiles {
		findings = append(findings, realFinding(fpMain, file, finding.CategoryStorage, finding.KindPassword, realMainValue, f.locate(file, realMainValue)))
	}
	findings = append(findings,
		realFinding(fpLocal, localFile, finding.CategoryStorage, finding.KindPassword, localValue, f.locate(localFile, localValue)),
		realFinding(fpPhone, trackedLog, finding.CategoryCustomerData, finding.KindPhone, phone, f.locate(trackedLog, phone)),
		realFinding(fpPhone, untrackLog, finding.CategoryCustomerData, finding.KindPhone, phone, f.locate(untrackLog, phone)),
		realFinding(fpKeyFile, pemFile, finding.CategorySSH, finding.KindKeyFile, string(pemRaw), f.wholeFile(pemFile)),
		realFinding(fpBlock, pemFile, finding.CategorySSH, finding.KindPrivateKey, pemBlock, f.locate(pemFile, pemBlock)),
		realFinding(fpFalse, ymlFile, finding.CategoryStorage, finding.KindPassword, falsePositive, f.locate(ymlFile, falsePositive)),
	)
	findings[len(findings)-1].HasOriginal = true

	actor := audit.Actor{Subject: "fake-subject", Name: "fake-user"}
	projectID, err := store.CreateProject(ctx, project.Project{Name: "legacy-shop", Path: f.path, CreatedBy: actor.Subject})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	t.Cleanup(func() {
		// The dev server keeps what the test stored until it stops; the
		// root token removes it.
		cctx := context.WithoutCancel(ctx)
		for _, key := range []string{"ext.db.main.password", "ext.db.local.password", "ext.ssh.partner.key", "ext.ssh.partner.block"} {
			p := "secret/metadata/projects/" + strconv.FormatInt(int64(projectID), 10) + "/" + key
			if _, err := root.Logical().DeleteWithContext(cctx, p); err != nil {
				t.Errorf("deleting %s: %v", p, err)
			}
		}
	})
	if err := vault.PutPending(ctx, int64(projectID), fpFalse, []byte(falsePositive)); err != nil {
		t.Fatalf("PutPending() error = %v", err)
	}
	scanID, err := store.RequestScan(ctx, projectID, actor)
	if err != nil {
		t.Fatalf("RequestScan() error = %v", err)
	}
	sj := claim(t, store, job.KindScan)
	if err := store.StartScan(ctx, scanID); err != nil {
		t.Fatalf("StartScan(%d) error = %v", scanID, err)
	}
	head := strings.TrimSpace(worktreetest.Git(t, f.repo, "rev-parse", "HEAD"))
	result := scan.Result{
		CommitHash: head, RulesVersion: "fake-rules", ToolVersions: map[string]string{"betterleaks": "fake-1"},
		Files: 20, Findings: findings,
	}
	if err := store.SaveResult(ctx, sj.ID, realWorker, scanID, result); err != nil {
		t.Fatalf("SaveResult(%d) error = %v", scanID, err)
	}

	bundleOf := func(fingerprint, file string) triage.BundleID {
		t.Helper()
		var id triage.BundleID
		err := admin.QueryRow(ctx, "SELECT bundle_id FROM "+schema+".finding WHERE project_id = $1 AND fingerprint = $2 AND file_path = $3",
			projectID, fingerprint, file).Scan(&id)
		if err != nil {
			t.Fatalf("reading the bundle of the finding in %s: %v", file, err)
		}
		return id
	}
	decide := func(d triage.Decision, ids ...triage.BundleID) {
		t.Helper()
		if err := store.Decide(ctx, projectID, ids, d, actor); err != nil {
			t.Fatalf("Decide(%v, %+v) error = %v", ids, d, err)
		}
	}
	externalize := func(name string) triage.Decision {
		return triage.Decision{Verdict: triage.VerdictExternalize, Name: triage.Name(name)}
	}
	mainBundle := bundleOf(fpMain, ymlFile)
	for _, file := range mainFiles {
		if b := bundleOf(fpMain, file); b != mainBundle {
			t.Fatalf("the password in %s is in bundle %d, want the one bundle %d", file, b, mainBundle)
		}
	}
	decide(externalize("db.main.password"), mainBundle)
	decide(externalize("db.local.password"), bundleOf(fpLocal, localFile))
	decide(triage.Decision{Verdict: triage.VerdictRedact}, bundleOf(fpPhone, trackedLog), bundleOf(fpPhone, untrackLog))
	decide(externalize("ssh.partner.key"), bundleOf(fpKeyFile, pemFile))
	decide(externalize("ssh.partner.block"), bundleOf(fpBlock, pemFile))
	decide(triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(fpFalse, ymlFile))

	runner := apply.NewRunner(slog.New(slog.NewJSONHandler(&f.logs, nil)), store, vault,
		apply.Options{WorkerID: realWorker, Root: f.root, Git: f.gitBinary})

	id, err := store.RequestApplyCheck(ctx, projectID, actor)
	if err != nil {
		t.Fatalf("RequestApplyCheck() error = %v", err)
	}
	if err := runner.Check(ctx, claim(t, store, job.KindApplyCheck)); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	checked, err := store.ApplyRun(ctx, id)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", id, err)
	}
	if checked.State != apply.StateReady || !slices.Equal(checked.IrreversibleFiles, []string{untrackLog}) {
		t.Fatalf("ApplyRun(%d) after the check = state %q, blockers %v, irreversible %v, failure %q; want ready with %s irreversible; log:\n%s",
			id, checked.State, checked.Blockers, checked.IrreversibleFiles, checked.FailureReason, untrackLog, f.logs.String())
	}
	for file, b := range before {
		if !bytes.Equal(b, read(t, f.dir, file)) {
			t.Errorf("Check() changed %s, want it as it was", file)
		}
	}

	if err := store.ConfirmApply(ctx, id, checked.PlanDigest, consent, actor); err != nil {
		t.Fatalf("ConfirmApply(%d) error = %v", id, err)
	}
	if err := runner.Apply(ctx, claim(t, store, job.KindApply)); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	done, err := store.ApplyRun(ctx, id)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", id, err)
	}
	if done.State != apply.StateSucceeded {
		t.Fatalf("ApplyRun(%d) after the apply = state %q, failure %q; want succeeded; log:\n%s", id, done.State, done.FailureReason, f.logs.String())
	}

	// The items of the run.
	untrackedOutcome := "skipped " + string(apply.ReasonUntrackedIrreversible)
	if consent {
		untrackedOutcome = applied
	}
	want := map[string]string{
		localFile + " password": applied, trackedLog + " phone": applied, untrackLog + " phone": untrackedOutcome,
		pemFile + " key-file": applied, pemFile + " private-key": applied,
	}
	for _, file := range mainFiles {
		want[file+" password"] = applied
	}
	items, err := store.ApplyItems(ctx, id)
	if err != nil {
		t.Fatalf("ApplyItems(%d) error = %v", id, err)
	}
	got := map[string]string{}
	for _, it := range items {
		v := string(it.Status)
		if it.Reason != "" {
			v += " " + string(it.Reason)
		}
		got[it.File+" "+string(it.Kind)] = v
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("items of the run (-want +got):\n%s", diff)
	}

	// The files: only the targets changed, and nothing else in them.
	diff := strings.Fields(worktreetest.Git(t, f.repo, "diff", "--name-only"))
	wantDiff := []string{euckrFile, pemFile, shFile, trackedLog, ymlFile, xmlFile}
	slices.Sort(diff)
	slices.Sort(wantDiff)
	if !slices.Equal(diff, wantDiff) {
		t.Errorf("git diff --name-only = %v, want %v", diff, wantDiff)
	}
	for _, file := range []string{euckrFile, ymlFile, xmlFile} {
		assertOutside(t, before[file], read(t, f.dir, file), []change{{realMainValue, "${ext.db.main.password}"}})
	}
	assertOutside(t, before[shFile], read(t, f.dir, shFile), []change{{realMainValue, "${EXT_DB_MAIN_PASSWORD}"}})
	assertOutside(t, before[localFile], read(t, f.dir, localFile), []change{{localValue, "${ext.db.local.password}"}})
	assertOutside(t, before[trackedLog], read(t, f.dir, trackedLog), []change{{phone, "[REDACTED:phone]"}})
	if consent {
		assertOutside(t, before[untrackLog], read(t, f.dir, untrackLog), []change{{phone, "[REDACTED:phone]"}})
	} else if !bytes.Equal(before[untrackLog], read(t, f.dir, untrackLog)) {
		t.Errorf("Apply() without consent changed %s, want it as it was", untrackLog)
	}
	if !bytes.Contains(read(t, f.dir, euckrFile), []byte("\r\n")) {
		t.Errorf("%s lost its CRLF line ends", euckrFile)
	}
	if f.exists(pemFile) {
		t.Errorf("%s exists after the apply, want it removed", pemFile)
	}

	// OpenBao: the stored bytes are the values, and nothing else is kept.
	stored := map[string]struct {
		value  []byte
		format string
	}{
		"ext.db.main.password":  {[]byte(realMainValue), "text"},
		"ext.db.local.password": {[]byte(localValue), "text"},
		"ext.ssh.partner.key":   {pemRaw, "file"},
		"ext.ssh.partner.block": {[]byte(pemBlock), "text"},
	}
	for key, w := range stored {
		v, format := storedBytes(t, root, projectID, key)
		if !bytes.Equal(v, w.value) || format != w.format {
			t.Errorf("OpenBao %s holds %d bytes as %q, want the %d bytes of the value as %q", key, len(v), format, len(w.value), w.format)
		}
	}
	listed, err := root.Logical().ListWithContext(ctx, "secret/metadata/projects/"+strconv.FormatInt(int64(projectID), 10))
	if err != nil || listed == nil {
		t.Fatalf("listing the project's stored values: %v", err)
	}
	var keys []string
	if raw, ok := listed.Data["keys"].([]any); ok {
		for _, k := range raw {
			s, _ := k.(string) // a key that is not a string shows as empty and fails the comparison
			keys = append(keys, s)
		}
	}
	slices.Sort(keys)
	if diff := cmp.Diff([]string{"ext.db.local.password", "ext.db.main.password", "ext.ssh.partner.block", "ext.ssh.partner.key"}, keys); diff != "" {
		t.Errorf("stored values of the project (-want +got):\n%s", diff)
	}
	pending := "secret/data/pending/" + strconv.FormatInt(int64(projectID), 10) + "/" + fpFalse
	if secret, err := root.Logical().ReadWithContext(ctx, pending); err != nil || (secret != nil && secret.Data["data"] != nil) {
		t.Errorf("reading %s = %v, %v; want the false positive's original gone", pending, secret != nil, err)
	}

	// The manifest parses and names the stored values.
	m, err := manifest.Parse(read(t, f.dir, manifest.FileName))
	if err != nil {
		t.Fatalf("manifest.Parse() error = %v", err)
	}
	var names []string
	for _, e := range m.Entries {
		names = append(names, e.Name)
	}
	slices.Sort(names)
	if diff := cmp.Diff([]string{"EXT_DB_MAIN_PASSWORD", "ext.db.local.password", "ext.db.main.password", "ext.ssh.partner.block", "ext.ssh.partner.key"}, names); diff != "" {
		t.Errorf("manifest names (-want +got):\n%s", diff)
	}

	// No value outside the vault: not in a text column of the records, not
	// in the worker log, not in the manifest.
	places := map[string]string{
		"worker log": f.logs.String(),
		"manifest":   string(read(t, f.dir, manifest.FileName)),
	}
	for _, table := range []string{"apply_item", "apply_run", "apply_file", "audit_event"} {
		places["table "+table] = tableText(t, admin, schema, table)
	}
	for _, table := range []string{"apply_item", "apply_run", "apply_file", "audit_event"} {
		if places["table "+table] == "" {
			t.Errorf("%s is empty, want the rows of the run", table)
		}
	}
	for where, text := range places {
		for _, v := range searched {
			if strings.Contains(text, v) {
				t.Errorf("the %s holds a value of the example (%d bytes)", where, len(v))
			}
		}
	}
}
