package apply_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/betterleaks"
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
	"github.com/SH-Hong1/secret-externalizer/internal/worktree/worktreetest"
)

// update rewrites testdata/public-sample.golden from the scan of the sample
// instead of comparing with it.
var update = flag.Bool("update", false, "rewrite testdata/public-sample.golden")

// sampleGolden lists what the scan of the public sample finds, one line per
// finding: its file, category, kind, and configuration key. It holds no
// value and no preview.
const sampleGolden = "testdata/public-sample.golden"

// TestIntegration_PublicSample takes a public sample project, named by
// SAMPLE_PROJECT_DIR (make sample-check fetches one), through a scan, the
// suggested verdict for every bundle, the check, the apply, and the
// confirming scan, with the real scanners, database, OpenBao and git. It
// checks that the scan finds what testdata/public-sample.golden lists, that
// the confirming scan does not find an applied secret again, that no
// original secret is in any table or the worker log, and that the apply
// changed only files it applied a place in, and the manifest.
func TestIntegration_PublicSample(t *testing.T) {
	src := os.Getenv("SAMPLE_PROJECT_DIR")
	if src == "" {
		t.Skip("SAMPLE_PROJECT_DIR is not set; run make sample-check")
	}
	blBin := realScanner(t, "BETTERLEAKS_BIN", "betterleaks")
	ogBin := realScanner(t, "OPENGREP_BIN", "opengrep")
	store, admin, schema := realStore(t)
	worker, vault, root := realVault(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	ctx := t.Context()

	workspace := t.TempDir()
	dir := filepath.Join(workspace, "sample")
	copyWithoutGit(t, src, dir)
	worktreetest.Init(t, dir)

	rules, err := filepath.Abs(filepath.Join("..", "..", "rules"))
	if err != nil {
		t.Fatalf("Abs(rules) error = %v", err)
	}
	rulesVersion, err := ruleset.Version(rules)
	if err != nil {
		t.Fatalf("ruleset.Version(%q) error = %v", rules, err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	env := scanexec.Env{Path: "/usr/bin:/bin", Home: t.TempDir(), TmpDir: t.TempDir()}
	scanner := scan.NewRunner(logger, store, worker, worker, []scan.Scanner{
		betterleaks.New(blBin, filepath.Join(rules, "betterleaks.toml"), env),
		opengrep.New(ogBin, filepath.Join(rules, "opengrep"), nil, env),
		keyfile.New(),
	}, scan.Options{WorkerID: realWorker, Root: workspace, RulesVersion: rulesVersion, TempDir: t.TempDir(), Git: git})
	applier := apply.NewRunner(logger, store, vault, apply.Options{WorkerID: realWorker, Root: workspace, Git: git})

	actor := audit.Actor{Subject: "fake-subject", Name: "fake-user"}
	projectID, err := store.CreateProject(ctx, project.Project{Name: "public-sample", Path: "sample", CreatedBy: actor.Subject})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	t.Cleanup(func() {
		// The dev server keeps what the test stored until it stops.
		cctx := context.WithoutCancel(ctx)
		id := strconv.FormatInt(int64(projectID), 10)
		for _, key := range storedKeys(cctx, t, root, projectID) {
			if _, err := root.Logical().DeleteWithContext(cctx, "secret/metadata/projects/"+id+"/"+key); err != nil {
				t.Errorf("deleting the stored value %s: %v", key, err)
			}
		}
		pending, err := worker.ListPending(cctx, int64(projectID))
		if err != nil {
			t.Errorf("ListPending() error = %v", err)
		}
		for _, fp := range pending {
			if err := worker.DeletePending(cctx, int64(projectID), fp); err != nil {
				t.Errorf("DeletePending() error = %v", err)
			}
		}
	})

	// The scan finds what the golden file lists.
	scanID, err := store.RequestScan(ctx, projectID, actor)
	if err != nil {
		t.Fatalf("RequestScan() error = %v", err)
	}
	runScan(t, store, scanner, scanID, &logs)
	first, err := store.FindingsByScan(ctx, scanID)
	if err != nil {
		t.Fatalf("FindingsByScan(%d) error = %v", scanID, err)
	}
	compareGolden(t, first)

	// The originals of the secrets, read before the apply settles any.
	fps, err := worker.ListPending(ctx, int64(projectID))
	if err != nil {
		t.Fatalf("ListPending() error = %v", err)
	}
	secretFP := map[string]bool{}
	for _, fd := range first {
		if secretKind(fd.Kind) {
			secretFP[fd.Fingerprint] = true
		}
	}
	var originals [][]byte
	for _, fp := range fps {
		if !secretFP[fp] {
			continue
		}
		v, err := worker.ReadPending(ctx, int64(projectID), fp)
		if err != nil {
			t.Fatalf("ReadPending() error = %v", err)
		}
		originals = append(originals, v.Reveal())
	}

	// The suggested verdict for every bundle, the check, and the apply.
	for _, id := range bundleIDs(t, admin, schema, projectID) {
		b, err := store.Bundle(ctx, projectID, scanID, id)
		if err != nil {
			t.Fatalf("Bundle(%d) error = %v", id, err)
		}
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
			t.Fatalf("Decide(bundle %d) error = %v", id, err)
		}
	}
	before := projectFiles(t, dir)
	runID, err := store.RequestApplyCheck(ctx, projectID, actor)
	if err != nil {
		t.Fatalf("RequestApplyCheck() error = %v", err)
	}
	if err := applier.Check(ctx, claim(t, store, job.KindApplyCheck)); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	checked, err := store.ApplyRun(ctx, runID)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", runID, err)
	}
	if checked.State != apply.StateReady {
		t.Fatalf("ApplyRun(%d) after the check = state %q, blockers %v, failure %q; want ready", runID, checked.State, checked.Blockers, checked.FailureReason)
	}
	if err := store.ConfirmApply(ctx, runID, checked.PlanDigest, true, actor); err != nil {
		t.Fatalf("ConfirmApply(%d) error = %v", runID, err)
	}
	if err := applier.Apply(ctx, claim(t, store, job.KindApply)); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	done, err := store.ApplyRun(ctx, runID)
	if err != nil {
		t.Fatalf("ApplyRun(%d) error = %v", runID, err)
	}
	if done.State != apply.StateSucceeded || done.RescanID == 0 {
		t.Fatalf("ApplyRun(%d) after the apply = state %q, failure %q, confirming scan %d; want succeeded with a confirming scan", runID, done.State, done.FailureReason, done.RescanID)
	}
	items, err := store.ApplyItems(ctx, runID)
	if err != nil {
		t.Fatalf("ApplyItems(%d) error = %v", runID, err)
	}
	findingByID := map[finding.ID]finding.Finding{}
	for _, fd := range first {
		findingByID[fd.ID] = fd
	}
	changed := map[place]bool{}
	untouched := map[place]bool{}
	appliedFiles := map[string]bool{manifest.FileName: true}
	for _, it := range items {
		fd := findingByID[it.FindingID]
		p := place{fd.Fingerprint, fd.File, fd.Key}
		if it.Status != apply.StatusApplied {
			untouched[p] = true
			continue
		}
		changed[p] = true
		appliedFiles[it.File] = true
	}
	t.Logf("scan: %d findings; apply: %d applied, %d skipped", len(first), done.Applied, done.Skipped)

	// The apply changed only the files it applied a place in, and the
	// manifest.
	after := projectFiles(t, dir)
	for file, content := range after {
		if !bytes.Equal(before[file], content) && !appliedFiles[file] {
			t.Errorf("the apply changed %s, where it applied no place", file)
		}
	}
	for file := range before {
		if _, ok := after[file]; !ok && !appliedFiles[file] {
			t.Errorf("the apply removed %s, where it applied no place", file)
		}
	}

	// The confirming scan does not find an applied secret again.
	runScan(t, store, scanner, done.RescanID, &logs)
	second, err := store.FindingsByScan(ctx, done.RescanID)
	if err != nil {
		t.Fatalf("FindingsByScan(%d) error = %v", done.RescanID, err)
	}
	for _, fd := range second {
		p := place{fd.Fingerprint, fd.File, fd.Key}
		if secretKind(fd.Kind) && changed[p] && !untouched[p] {
			t.Errorf("the confirming scan found the %s %s in %s line %d again, which the apply changed", fd.Category, fd.Kind, fd.File, fd.Locations[0].StartLine)
		}
	}

	// No original secret in any table or the worker log.
	places := map[string]string{"worker log": logs.String()}
	for _, table := range tableNames(t, admin, schema) {
		places["table "+table] = tableText(t, admin, schema, table)
	}
	for where, text := range places {
		for _, v := range originals {
			if len(v) >= 4 && strings.Contains(text, string(v)) {
				t.Errorf("the %s holds an original secret (%d bytes)", where, len(v))
			}
		}
	}
}

// compareGolden compares the findings with testdata/public-sample.golden, or
// rewrites the file when -update is given.
func compareGolden(t *testing.T, findings []finding.Finding) {
	t.Helper()
	lines := make([]string, 0, len(findings))
	for _, fd := range findings {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", fd.File, fd.Category, fd.Kind, fd.Key))
	}
	slices.Sort(lines)
	got := strings.Join(lines, "\n") + "\n"
	if *update {
		if err := os.WriteFile(sampleGolden, []byte(got), 0o600); err != nil {
			t.Fatalf("writing %s: %v", sampleGolden, err)
		}
		return
	}
	want, err := os.ReadFile(sampleGolden)
	if err != nil {
		t.Fatalf("reading %s: %v; run make sample-check with -update to write it", sampleGolden, err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("findings of the public sample (-%s +scan):\n%s", sampleGolden, diff)
	}
}

// copyWithoutGit copies the regular files of src to dst, leaving out .git.
func copyWithoutGit(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error { //nolint:gosec // G703: src is the developer's own SAMPLE_PROJECT_DIR
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: the path is the developer's own sample copy
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o600) //nolint:gosec // G703: out is under t.TempDir
	})
	if err != nil {
		t.Fatalf("copying the sample from %s: %v", src, err)
	}
}
