package db_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/bao"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// hashOf returns a fake SHA-256 made of the hex digit c.
func hashOf(c string) string { return strings.Repeat(c, 64) }

// tempOf returns the temporary file name an apply would use for file.
func tempOf(file string) string { return file + ".se-apply-0123456789abcdef.tmp" }

// replaceItem is the item that externalizes the password of decidedProject
// in file at line 3.
func replaceItem(t *testing.T, s *db.Store, pid project.ID, bundle triage.BundleID, file string) apply.Item {
	t.Helper()
	path := bao.StorePath(int64(pid), "ext.db.main.password")
	return apply.Item{
		BundleID: bundle, FindingID: findingID(t, s, pid, fpA, file), File: file, StartLine: 3, StartCol: 1, Line: 3,
		Action: apply.ActionReplace, Category: finding.CategoryStorage, Kind: finding.KindPassword,
		Name: "db.main.password", StorePath: path, StoreVersion: 1, Tracked: true, Status: apply.StatusPlanned,
	}
}

// redactItem is the item that redacts the phone number of decidedProject in
// logs/x.log.
func redactItem(t *testing.T, s *db.Store, pid project.ID, bundle triage.BundleID) apply.Item {
	t.Helper()
	return apply.Item{
		BundleID: bundle, FindingID: findingID(t, s, pid, fpC, "logs/x.log"), File: "logs/x.log", StartLine: 7, StartCol: 1,
		Line: 7, Action: apply.ActionRedact, Category: finding.CategoryCustomerData, Kind: finding.KindPhone,
		Tracked: true, Status: apply.StatusPlanned,
	}
}

// writeFile announces and confirms a change of file by run id.
func writeFile(t *testing.T, s *db.Store, id apply.ID, file, after string, items ...apply.Item) {
	t.Helper()
	temp := beginFile(t, s, id, file, after, items...)
	if err := s.EndFile(t.Context(), id, file, after, temp); err != nil {
		t.Fatalf("EndFile(%d, %q) error = %v", id, file, err)
	}
}

// beginFile announces a change of file by run id to the hash after and
// returns the temporary file name it announced.
func beginFile(t *testing.T, s *db.Store, id apply.ID, file, after string, items ...apply.Item) string {
	t.Helper()
	temp := ""
	if after != "" {
		temp = tempOf(file)
	}
	if err := s.BeginFile(t.Context(), id, file, fileHash, after, temp, items); err != nil {
		t.Fatalf("BeginFile(%d, %q) error = %v", id, file, err)
	}
	return temp
}

func applyItems(t *testing.T, s *db.Store, id apply.ID) []apply.Item {
	t.Helper()
	items, err := s.ApplyItems(t.Context(), id)
	if err != nil {
		t.Fatalf("ApplyItems(%d) error = %v", id, err)
	}
	return items
}

func openFiles(t *testing.T, s *db.Store, pid project.ID) []apply.OpenFile {
	t.Helper()
	files, err := s.OpenFiles(t.Context(), pid)
	if err != nil {
		t.Fatalf("OpenFiles(%d) error = %v", pid, err)
	}
	return files
}

func writtenFiles(t *testing.T, s *db.Store, pid project.ID) map[string][]apply.Written {
	t.Helper()
	files, err := s.WrittenFiles(t.Context(), pid)
	if err != nil {
		t.Fatalf("WrittenFiles(%d) error = %v", pid, err)
	}
	return files
}

func applyInput(t *testing.T, s *db.Store, id apply.ID) apply.Input {
	t.Helper()
	in, err := s.ApplyInput(t.Context(), id)
	if err != nil {
		t.Fatalf("ApplyInput(%d) error = %v", id, err)
	}
	return in
}

// withStatus returns items with status and reason set.
func withStatus(items []apply.Item, status apply.Status, reason apply.Reason) []apply.Item {
	out := make([]apply.Item, len(items))
	for i, it := range items {
		it.Status, it.Reason = status, reason
		out[i] = it
	}
	return out
}

// claimApply confirms a ready run of the project and claims its apply job.
func claimApply(t *testing.T, s *db.Store, pid project.ID) (apply.ID, job.Job) {
	t.Helper()
	r := readyRun(t, s, pid)
	if err := s.ConfirmApply(t.Context(), r.ID, r.PlanDigest, false, tester); err != nil {
		t.Fatalf("ConfirmApply(%d) error = %v", r.ID, err)
	}
	j, err := s.ClaimJob(t.Context(), "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if j.Kind != job.KindApply || j.ApplyID != int64(r.ID) {
		t.Fatalf("ClaimJob() = %+v, want the apply job of run %d", j, r.ID)
	}
	if err := s.StartApply(t.Context(), r.ID, job.KindApply); err != nil {
		t.Fatalf("StartApply(%d, apply) error = %v", r.ID, err)
	}
	return r.ID, j
}

func TestApplyInput(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, pw, ph := decidedProject(t, s, "shop")
	earlier := applyRunIn(t, s, pid, sid, apply.StateRunning)
	replaced := replaceItem(t, s, pid, pw, "conf/a.yml")
	writeFile(t, s, earlier, "conf/a.yml", hashOf("1"), replaced)
	writeFile(t, s, earlier, "logs/x.log", hashOf("2"), redactItem(t, s, pid, ph))
	if err := s.SetApplyState(ctx, earlier, apply.StateSucceeded); err != nil {
		t.Fatalf("SetApplyState(%d) error = %v", earlier, err)
	}
	id := requestCheck(t, s, pid)
	newest, err := s.InsertRawScanJob(ctx, pid, "failed", "failed")
	if err != nil {
		t.Fatalf("InsertRawScanJob(failed) error = %v", err)
	}

	in := applyInput(t, s, id)
	if in.Run.ID != id || in.Run.State != apply.StateChecking {
		t.Errorf("ApplyInput(%d).Run = %+v, want the checking run", id, in.Run)
	}
	if in.Project.ID != pid || in.Project.Name != "shop" {
		t.Errorf("ApplyInput(%d).Project = %+v, want project %d", id, in.Project, pid)
	}
	if in.Scan.ID != sid || in.NewestScan != newest {
		t.Errorf("ApplyInput(%d) scan, newest = %d, %d, want %d, %d", id, in.Scan.ID, in.NewestScan, sid, newest)
	}
	want := []triage.Bundle{readBundle(t, s, pid, sid, pw), readBundle(t, s, pid, sid, ph)}
	if diff := cmp.Diff(want, in.Bundles); diff != "" {
		t.Errorf("ApplyInput(%d).Bundles (-want +got):\n%s", id, diff)
	}
	if diff := cmp.Diff([]string{"logs/x.log", "logs/y.log"}, in.CustomerDataFiles); diff != "" {
		t.Errorf("ApplyInput(%d).CustomerDataFiles (-want +got):\n%s", id, diff)
	}
	if diff := cmp.Diff(phoneSpans("logs/x.log", "logs/y.log"), in.CustomerDataSpans); diff != "" {
		t.Errorf("ApplyInput(%d).CustomerDataSpans (-want +got):\n%s", id, diff)
	}
	if got, want := apply.NewPlan(in).Digest, readRun(t, s, id).PlanDigest; got != want {
		t.Errorf("NewPlan(ApplyInput(%d)).Digest = %s, want the run's digest %s", id, got, want)
	}
	replaced.Status = apply.StatusApplied
	if diff := cmp.Diff([]apply.Item{replaced}, in.Unlisted); diff != "" {
		t.Errorf("ApplyInput(%d).Unlisted before MarkInManifest (-want +got):\n%s", id, diff)
	}

	if err := s.MarkInManifest(ctx, pid); err != nil {
		t.Fatalf("MarkInManifest(%d) error = %v", pid, err)
	}
	if got := applyInput(t, s, id).Unlisted; len(got) != 0 {
		t.Errorf("ApplyInput(%d).Unlisted after MarkInManifest = %+v, want none", id, got)
	}

	if _, err := s.ApplyInput(ctx, id+100); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("ApplyInput(no run) error = %v, want ErrNotFound", err)
	}
}

func TestApplyInput_CustomerDataSpans(t *testing.T) {
	s := newMigratedStore(t)
	pid := newProject(t, s, "shop")
	email := fnd(fpD, "conf/db.properties", finding.CategoryCustomerData, finding.KindEmail, 5)
	email.Locations[0].StartCol, email.Locations[0].EndCol = 30, 45
	second := email.Locations[0]
	second.StartLine, second.EndLine = 2, 2
	email.Locations = append(email.Locations, second)
	sid := save(t, s, pid, append(mixed(), email)...)
	decide(t, s, pid, externalizeDB, bundleOf(t, s, pid, fpA, "conf/a.yml"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm}, bundleOf(t, s, pid, fpD, "conf/db.properties"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictRedact}, bundleOf(t, s, pid, fpC, "logs/x.log"))
	decide(t, s, pid, triage.Decision{Verdict: triage.VerdictFalsePositive}, bundleOf(t, s, pid, fpC, "logs/y.log"))

	in := applyInput(t, s, requestCheck(t, s, pid))
	want := []apply.CustomerSpan{
		{File: "conf/db.properties", StartLine: 2, StartCol: 30, EndLine: 2, EndCol: 45},
		{File: "conf/db.properties", StartLine: 5, StartCol: 30, EndLine: 5, EndCol: 45},
		{File: "logs/x.log", StartLine: 7, StartCol: 1, EndLine: 7, EndCol: 4},
	}
	if in.Scan.ID != sid {
		t.Fatalf("ApplyInput().Scan.ID = %d, want %d", in.Scan.ID, sid)
	}
	if diff := cmp.Diff(want, in.CustomerDataSpans); diff != "" {
		t.Errorf("ApplyInput().CustomerDataSpans with hold, redact and false positive (-want +got):\n%s", diff)
	}
}

func TestApplyItem_HoldsCustomerData(t *testing.T) {
	s := newMigratedStore(t)
	pid, _, pw, _ := decidedProject(t, s, "shop")
	id := requestCheck(t, s, pid)
	it := replaceItem(t, s, pid, pw, "conf/a.yml")
	it.Status, it.Reason = apply.StatusSkipped, apply.ReasonHoldsCustomerData
	if err := s.InsertApplyItem(t.Context(), id, it); err != nil {
		t.Fatalf("InsertApplyItem(%d, skipped as %s) error = %v", id, it.Reason, err)
	}
	if diff := cmp.Diff([]apply.Item{it}, applyItems(t, s, id)); diff != "" {
		t.Errorf("ApplyItems(%d) (-want +got):\n%s", id, diff)
	}
}

func TestStartApply(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, _, _ := decidedProject(t, s, "shop")

	for _, c := range []struct {
		state apply.State
		phase job.Kind
		want  apply.State // empty: ErrNotFound
	}{
		{apply.StateChecking, job.KindApplyCheck, apply.StateChecking},
		{apply.StateQueued, job.KindApply, apply.StateRunning},
		{apply.StateRunning, job.KindApply, apply.StateRunning},
		{apply.StateChecking, job.KindApply, ""},
		{apply.StateQueued, job.KindApplyCheck, ""},
		{apply.StateReady, job.KindApply, ""},
		{apply.StateReady, job.KindApplyCheck, ""},
		{apply.StateSucceeded, job.KindApply, ""},
		{apply.StateFailed, job.KindApply, ""},
		{apply.StateQueued, job.KindScan, ""},
	} {
		t.Run(fmt.Sprintf("%s %s", c.phase, c.state), func(t *testing.T) {
			id := applyRunIn(t, s, pid, sid, c.state)
			t.Cleanup(func() {
				// Leave no active run behind for the next case.
				if err := s.SetApplyState(ctx, id, apply.StateFailed); err != nil {
					t.Errorf("SetApplyState(%d) error = %v", id, err)
				}
			})
			err := s.StartApply(ctx, id, c.phase)
			r := readRun(t, s, id)
			if c.want == "" {
				if err == nil {
					t.Errorf("StartApply(%d in %s, %s) error = nil, want an error", id, c.state, c.phase)
				}
				if c.phase != job.KindScan && !errors.Is(err, db.ErrNotFound) {
					t.Errorf("StartApply(%d in %s, %s) error = %v, want ErrNotFound", id, c.state, c.phase, err)
				}
				if r.State != c.state {
					t.Errorf("ApplyRun(%d) after a refused start = %s, want %s", id, r.State, c.state)
				}
				return
			}
			if err != nil {
				t.Fatalf("StartApply(%d in %s, %s) error = %v", id, c.state, c.phase, err)
			}
			if r.State != c.want {
				t.Errorf("ApplyRun(%d) after start = %s, want %s", id, r.State, c.want)
			}
			if started := !r.StartedAt.IsZero(); started != (c.phase == job.KindApply) {
				t.Errorf("ApplyRun(%d).StartedAt = %v after %s start, want set only for apply", id, r.StartedAt, c.phase)
			}
		})
	}
}

func TestSaveCheck(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, pw, ph := decidedProject(t, s, "shop")
	id := requestCheck(t, s, pid)
	j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if err := s.StartApply(ctx, id, job.KindApplyCheck); err != nil {
		t.Fatalf("StartApply(%d, apply-check) error = %v", id, err)
	}
	planned := replaceItem(t, s, pid, pw, "conf/a.yml")
	skipped := redactItem(t, s, pid, ph)
	skipped.Status, skipped.Reason = apply.StatusSkipped, apply.ReasonUntrackedIrreversible
	items := []apply.Item{planned, skipped}
	// A row an earlier attempt could have left for the same location.
	if err := s.InsertApplyItem(ctx, id, planned); err != nil {
		t.Fatalf("InsertApplyItem(%d) error = %v", id, err)
	}
	var dirty []string
	for i := range 60 {
		dirty = append(dirty, fmt.Sprintf("src/f%02d.java", i))
	}
	irreversible := []string{"logs/x.log"}

	if err := s.SaveCheck(ctx, j.ID, "worker-b", id, nil, dirty, irreversible, items); !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("SaveCheck(worker-b) error = %v, want ErrLeaseLost", err)
	}
	if r := readRun(t, s, id); r.State != apply.StateChecking || r.DirtyCount != 0 || r.IrreversibleFiles != nil {
		t.Errorf("ApplyRun(%d) after a refused save = %+v, want unchanged", id, r)
	}
	if got := applyItems(t, s, id); len(got) != 1 {
		t.Errorf("ApplyItems(%d) after a refused save = %d items, want the 1 left before", id, len(got))
	}

	if err := s.SaveCheck(ctx, j.ID, "worker-a", id, nil, dirty, irreversible, items); err != nil {
		t.Fatalf("SaveCheck(worker-a) error = %v", err)
	}
	r := readRun(t, s, id)
	if r.State != apply.StateReady || r.CheckedAt.IsZero() || r.Blockers != nil {
		t.Errorf("ApplyRun(%d) after the check = %+v, want ready with a check time and no blockers", id, r)
	}
	if r.DirtyCount != 60 || len(r.DirtyFiles) != apply.MaxDirtyShown || r.DirtyFiles[0] != dirty[0] ||
		r.DirtyFiles[apply.MaxDirtyShown-1] != dirty[apply.MaxDirtyShown-1] {
		t.Errorf("ApplyRun(%d) dirty = %d, %d files, want 60 and the first %d", id, r.DirtyCount, len(r.DirtyFiles), apply.MaxDirtyShown)
	}
	if diff := cmp.Diff(irreversible, r.IrreversibleFiles); diff != "" {
		t.Errorf("ApplyRun(%d).IrreversibleFiles (-want +got):\n%s", id, diff)
	}
	if diff := cmp.Diff(items, applyItems(t, s, id)); diff != "" {
		t.Errorf("ApplyItems(%d) after the check (-want +got):\n%s", id, diff)
	}
	if jr, err := s.ApplyJob(ctx, int64(id), "apply-check"); err != nil || jr.State != "done" {
		t.Errorf("ApplyJob(%d, apply-check) = %+v, %v, want done", id, jr, err)
	}

	if err := s.SaveCheck(ctx, j.ID, "worker-a", id, nil, dirty, irreversible, items); !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("SaveCheck(again) error = %v, want ErrLeaseLost", err)
	}
	if got := applyItems(t, s, id); len(got) != len(items) {
		t.Errorf("ApplyItems(%d) after a second save = %d items, want %d", id, len(got), len(items))
	}

	t.Run("not checking", func(t *testing.T) {
		pid, _, _, _ := decidedProject(t, s, "moved")
		id := requestCheck(t, s, pid)
		j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
		if err != nil {
			t.Fatalf("ClaimJob() error = %v", err)
		}
		if err := s.MoveApplyRun(ctx, id, apply.StateReady); err != nil {
			t.Fatalf("MoveApplyRun(%d, ready) error = %v", id, err)
		}
		if err := s.SaveCheck(ctx, j.ID, "worker-a", id, nil, dirty, irreversible, nil); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("SaveCheck(ready run) error = %v, want ErrNotFound", err)
		}
		r := readRun(t, s, id)
		if r.State != apply.StateReady || r.IrreversibleFiles != nil || r.DirtyCount != 0 || !r.CheckedAt.IsZero() {
			t.Errorf("ApplyRun(%d) after saving a check on a ready run = %+v, want unchanged", id, r)
		}
		if jr, err := s.ApplyJob(ctx, int64(id), "apply-check"); err != nil || jr.State != "running" {
			t.Errorf("ApplyJob(%d, apply-check) = %+v, %v, want still running", id, jr, err)
		}
	})
	t.Run("same location twice", func(t *testing.T) {
		pid, _, pw, _ := decidedProject(t, s, "twice")
		id := requestCheck(t, s, pid)
		j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
		if err != nil {
			t.Fatalf("ClaimJob() error = %v", err)
		}
		it := replaceItem(t, s, pid, pw, "conf/a.yml")
		if err := s.SaveCheck(ctx, j.ID, "worker-a", id, nil, nil, nil, []apply.Item{it, it}); err == nil {
			t.Errorf("SaveCheck(two items at one location) error = nil, want an error")
		}
		if r := readRun(t, s, id); r.State != apply.StateChecking {
			t.Errorf("ApplyRun(%d) after a refused save = %s, want checking", id, r.State)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		pid, _, _, _ := decidedProject(t, s, "blocked")
		id := requestCheck(t, s, pid)
		j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
		if err != nil {
			t.Fatalf("ClaimJob() error = %v", err)
		}
		blockers := []apply.Blocker{apply.BlockerDirty, apply.BlockerScanNotLatest}
		if err := s.SaveCheck(ctx, j.ID, "worker-a", id, blockers, []string{"src/a.java"}, nil, nil); err != nil {
			t.Fatalf("SaveCheck(blocked) error = %v", err)
		}
		r := readRun(t, s, id)
		if r.State != apply.StateBlocked || r.DirtyCount != 1 {
			t.Errorf("ApplyRun(%d) = %+v, want blocked with 1 changed file", id, r)
		}
		if diff := cmp.Diff(blockers, r.Blockers); diff != "" {
			t.Errorf("ApplyRun(%d).Blockers (-want +got):\n%s", id, diff)
		}
	})
}

func TestBeginEndFile(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, pw, _ := decidedProject(t, s, "shop")
	id := applyRunIn(t, s, pid, sid, apply.StateRunning)
	it := replaceItem(t, s, pid, pw, "conf/a.yml")

	beginFile(t, s, id, "conf/a.yml", hashOf("1"), it)
	if got := applyItems(t, s, id); len(got) != 1 || got[0].Status != apply.StatusWriting {
		t.Errorf("ApplyItems(%d) after BeginFile = %+v, want one writing item", id, got)
	}
	wantOpen := []apply.OpenFile{{Run: id, File: "conf/a.yml", AfterHash: hashOf("1"), TempName: tempOf("conf/a.yml")}}
	if diff := cmp.Diff(wantOpen, openFiles(t, s, pid)); diff != "" {
		t.Errorf("OpenFiles(%d) after BeginFile (-want +got):\n%s", pid, diff)
	}

	// Announcing the same file again replaces the record.
	if err := s.BeginFile(ctx, id, "conf/a.yml", fileHash, hashOf("2"), "conf/a.yml.se-apply-fedcba9876543210.tmp",
		[]apply.Item{it}); err != nil {
		t.Fatalf("BeginFile(again) error = %v", err)
	}
	wantOpen = []apply.OpenFile{{Run: id, File: "conf/a.yml", AfterHash: hashOf("2"), TempName: "conf/a.yml.se-apply-fedcba9876543210.tmp"}}
	if diff := cmp.Diff(wantOpen, openFiles(t, s, pid)); diff != "" {
		t.Errorf("OpenFiles(%d) after a second BeginFile (-want +got):\n%s", pid, diff)
	}
	if got := applyItems(t, s, id); len(got) != 1 {
		t.Errorf("ApplyItems(%d) after a second BeginFile = %d items, want 1", id, len(got))
	}

	if err := s.EndFile(ctx, id, "conf/a.yml", hashOf("1"), tempOf("conf/a.yml")); !errors.Is(err, db.ErrSuperseded) {
		t.Fatalf("EndFile(%d) of the first announcement error = %v, want ErrSuperseded", id, err)
	}
	if got := openFiles(t, s, pid); len(got) != 1 {
		t.Fatalf("OpenFiles(%d) after a superseded EndFile = %+v, want the second announcement still open", pid, got)
	}
	if err := s.EndFile(ctx, id, "conf/a.yml", hashOf("2"), "conf/a.yml.se-apply-fedcba9876543210.tmp"); err != nil {
		t.Fatalf("EndFile(%d) error = %v", id, err)
	}
	it.Status = apply.StatusApplied
	if diff := cmp.Diff([]apply.Item{it}, applyItems(t, s, id)); diff != "" {
		t.Errorf("ApplyItems(%d) after EndFile (-want +got):\n%s", id, diff)
	}
	if got := openFiles(t, s, pid); len(got) != 0 {
		t.Errorf("OpenFiles(%d) after EndFile = %+v, want none", pid, got)
	}
	if diff := cmp.Diff(map[string][]apply.Written{"conf/a.yml": {{AfterHash: hashOf("2")}}}, writtenFiles(t, s, pid)); diff != "" {
		t.Errorf("WrittenFiles(%d) after EndFile (-want +got):\n%s", pid, diff)
	}
	if b := readBundle(t, s, pid, sid, pw); b.AppliedAt.IsZero() || b.StorePath != it.StorePath {
		t.Errorf("Bundle(%d) after EndFile applied at %v, store path %q, want a time and %q", pw, b.AppliedAt, b.StorePath, it.StorePath)
	}
	if err := s.EndFile(ctx, id, "conf/a.yml", hashOf("2"), "conf/a.yml.se-apply-fedcba9876543210.tmp"); err != nil {
		t.Errorf("EndFile(%d) of a written file error = %v, want nil", id, err)
	}

	t.Run("manifest", func(t *testing.T) {
		if err := s.BeginFile(ctx, id, "secrets-manifest.yml", "", hashOf("3"), tempOf("secrets-manifest.yml"), nil); err != nil {
			t.Fatalf("BeginFile(manifest) error = %v", err)
		}
		if err := s.EndFile(ctx, id, "secrets-manifest.yml", hashOf("3"), tempOf("secrets-manifest.yml")); err != nil {
			t.Fatalf("EndFile(manifest) error = %v", err)
		}
		if got := writtenFiles(t, s, pid)["secrets-manifest.yml"]; len(got) != 1 || got[0].AfterHash != hashOf("3") {
			t.Errorf("WrittenFiles(%d)[manifest] = %+v, want the new hash", pid, got)
		}
	})
	t.Run("refused", func(t *testing.T) {
		if err := s.EndFile(ctx, id, "conf/none.yml", hashOf("4"), tempOf("conf/none.yml")); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("EndFile(not announced) error = %v, want ErrNotFound", err)
		}
		other := it
		other.File = "conf/b.xml"
		if err := s.BeginFile(ctx, id, "conf/c.yml", fileHash, hashOf("4"), tempOf("conf/c.yml"), []apply.Item{other}); err == nil {
			t.Errorf("BeginFile(conf/c.yml) with an item of conf/b.xml error = nil, want an error")
		}
		done := applyRunIn(t, s, pid, sid, apply.StateSucceeded)
		if err := s.BeginFile(ctx, done, "conf/c.yml", fileHash, hashOf("4"), tempOf("conf/c.yml"), nil); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("BeginFile(succeeded run) error = %v, want ErrNotFound", err)
		}
	})
}

func TestResolveFile(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, pw, ph := decidedProject(t, s, "shop")
	old := applyRunIn(t, s, pid, sid, apply.StateRunning)
	replaced := replaceItem(t, s, pid, pw, "conf/a.yml")
	redacted := redactItem(t, s, pid, ph)
	beginFile(t, s, old, "conf/a.yml", hashOf("1"), replaced)
	beginFile(t, s, old, "logs/x.log", hashOf("2"), redacted)
	if err := s.SetApplyState(ctx, old, apply.StateFailed); err != nil {
		t.Fatalf("SetApplyState(%d) error = %v", old, err)
	}

	open := openFiles(t, s, pid)
	if len(open) != 2 || open[0].File != "conf/a.yml" || open[1].File != "logs/x.log" {
		t.Fatalf("OpenFiles(%d) = %+v, want conf/a.yml and logs/x.log", pid, open)
	}
	if err := s.ResolveFile(ctx, open[0], true); err != nil {
		t.Fatalf("ResolveFile(%q, done) error = %v", open[0].File, err)
	}
	if err := s.ResolveFile(ctx, open[1], false); err != nil {
		t.Fatalf("ResolveFile(%q, not done) error = %v", open[1].File, err)
	}

	replaced.Status = apply.StatusApplied
	redacted.Status, redacted.Reason = apply.StatusFailed, apply.ReasonInterrupted
	if diff := cmp.Diff([]apply.Item{replaced, redacted}, applyItems(t, s, old)); diff != "" {
		t.Errorf("ApplyItems(%d) after resolving (-want +got):\n%s", old, diff)
	}
	if got := openFiles(t, s, pid); len(got) != 0 {
		t.Errorf("OpenFiles(%d) after resolving = %+v, want none", pid, got)
	}
	if diff := cmp.Diff(map[string][]apply.Written{"conf/a.yml": {{AfterHash: hashOf("1")}}}, writtenFiles(t, s, pid)); diff != "" {
		t.Errorf("WrittenFiles(%d) after resolving (-want +got):\n%s", pid, diff)
	}
	if b := readBundle(t, s, pid, sid, pw); b.AppliedAt.IsZero() || b.StorePath != replaced.StorePath {
		t.Errorf("Bundle(%d) after resolving done applied at %v, store path %q, want a time and %q", pw, b.AppliedAt, b.StorePath, replaced.StorePath)
	}
	if b := readBundle(t, s, pid, sid, ph); !b.AppliedAt.IsZero() || b.StorePath != "" {
		t.Errorf("Bundle(%d) after resolving not done applied at %v, store path %q, want neither", ph, b.AppliedAt, b.StorePath)
	}
	if diff := cmp.Diff([]apply.Item{replaced}, applyInput(t, s, requestCheck(t, s, pid)).Unlisted); diff != "" {
		t.Errorf("ApplyInput().Unlisted after resolving (-want +got):\n%s", diff)
	}

	for _, f := range open {
		for _, done := range []bool{true, false} {
			if err := s.ResolveFile(ctx, f, done); err != nil {
				t.Errorf("ResolveFile(%q, %t) of a closed record error = %v, want nil", f.File, done, err)
			}
		}
	}
	if diff := cmp.Diff([]apply.Item{replaced, redacted}, applyItems(t, s, old)); diff != "" {
		t.Errorf("ApplyItems(%d) after resolving again (-want +got):\n%s", old, diff)
	}
	if got := writtenFiles(t, s, pid)["conf/a.yml"]; len(got) != 1 {
		t.Errorf("WrittenFiles(%d)[conf/a.yml] after resolving again = %+v, want the one written record", pid, got)
	}
}

func TestWrittenFiles(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, _, _ := decidedProject(t, s, "shop")
	first := applyRunIn(t, s, pid, sid, apply.StateRunning)
	writeFile(t, s, first, "conf/a.yml", hashOf("1"))
	writeFile(t, s, first, "keys/id_rsa", "")
	if err := s.SetApplyState(ctx, first, apply.StateSucceeded); err != nil {
		t.Fatalf("SetApplyState(%d) error = %v", first, err)
	}
	second := applyRunIn(t, s, pid, sid, apply.StateRunning)
	writeFile(t, s, second, "conf/a.yml", hashOf("2"))
	beginFile(t, s, second, "conf/b.xml", hashOf("3"))
	otherPid, otherSid, _, _ := decidedProject(t, s, "other")
	otherRun := applyRunIn(t, s, otherPid, otherSid, apply.StateRunning)
	writeFile(t, s, otherRun, "conf/other.yml", hashOf("4"))

	want := map[string][]apply.Written{
		"conf/a.yml":  {{AfterHash: hashOf("1")}, {AfterHash: hashOf("2")}},
		"keys/id_rsa": {{AfterHash: ""}},
	}
	if diff := cmp.Diff(want, writtenFiles(t, s, pid)); diff != "" {
		t.Errorf("WrittenFiles(%d) (-want +got):\n%s", pid, diff)
	}
}

func TestSettledFingerprints(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	save(t, s, pid, mixed()...)
	fp := triage.Decision{Verdict: triage.VerdictFalsePositive}
	decide(t, s, pid, fp, bundleOf(t, s, pid, fpA, "conf/a.yml"), bundleOf(t, s, pid, fpA, "web/c.jsp"))
	decide(t, s, pid, fp, bundleOf(t, s, pid, fpB, "conf/a.yml"))
	otherPid := newProject(t, s, "other")
	save(t, s, otherPid, mixed()...)
	decide(t, s, otherPid, fp, bundleOf(t, s, otherPid, fpB, "conf/a.yml"), bundleOf(t, s, otherPid, fpB, "conf/b.xml"))

	got, err := s.SettledFingerprints(ctx, pid)
	if err != nil {
		t.Fatalf("SettledFingerprints(%d) error = %v", pid, err)
	}
	if diff := cmp.Diff([]string{fpA}, got); diff != "" {
		t.Errorf("SettledFingerprints(%d) (-want +got):\n%s", pid, diff)
	}
}

func TestFinishApply(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, pw, ph := decidedProject(t, s, "shop")
	id, j := claimApply(t, s, pid)
	applied := replaceItem(t, s, pid, pw, "conf/a.yml")
	writeFile(t, s, id, "conf/a.yml", hashOf("1"), applied)
	failed := replaceItem(t, s, pid, pw, "conf/b.xml")
	failed.Status, failed.Reason = apply.StatusFailed, apply.ReasonWriteFailed
	if err := s.InsertApplyItem(ctx, id, failed); err != nil {
		t.Fatalf("InsertApplyItem(%d) error = %v", id, err)
	}
	skipped := redactItem(t, s, pid, ph)
	skipped.Status, skipped.Reason = apply.StatusSkipped, apply.ReasonFileChanged

	// The executor's view of the first two is older than what is stored.
	items := append(withStatus([]apply.Item{applied, failed}, apply.StatusPlanned, ""), skipped)
	if err := s.FinishApply(ctx, j.ID, "worker-b", id, items); !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("FinishApply(worker-b) error = %v, want ErrLeaseLost", err)
	}
	if r := readRun(t, s, id); r.State != apply.StateRunning {
		t.Errorf("ApplyRun(%d) after a refused finish = %s, want running", id, r.State)
	}
	if err := s.FinishApply(ctx, j.ID, "worker-a", id, items); err != nil {
		t.Fatalf("FinishApply(%d) error = %v", id, err)
	}

	applied.Status = apply.StatusApplied
	if diff := cmp.Diff([]apply.Item{applied, failed, skipped}, applyItems(t, s, id)); diff != "" {
		t.Errorf("ApplyItems(%d) after finishing (-want +got):\n%s", id, diff)
	}
	r := readRun(t, s, id)
	if r.State != apply.StateSucceeded || r.FinishedAt.IsZero() || r.Applied != 1 || r.Skipped != 1 || r.Failed != 1 {
		t.Errorf("ApplyRun(%d) after finishing = %+v, want succeeded, finished, 1 applied, 1 skipped, 1 failed", id, r)
	}
	if jr, err := s.ApplyJob(ctx, int64(id), "apply"); err != nil || jr.State != "done" {
		t.Errorf("ApplyJob(%d, apply) = %+v, %v, want done", id, jr, err)
	}
	if err := s.FinishApply(ctx, j.ID, "worker-a", id, items); !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("FinishApply(again) error = %v, want ErrLeaseLost", err)
	}
}

func TestFailApply(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, pw, _ := decidedProject(t, s, "shop")
	id, j := claimApply(t, s, pid)
	applied := replaceItem(t, s, pid, pw, "conf/a.yml")
	writeFile(t, s, id, "conf/a.yml", hashOf("1"), applied)

	blockers := []apply.Blocker{apply.BlockerDirty}
	if err := s.FailApply(ctx, j.ID, "worker-b", id, "the work tree has uncommitted changes", blockers, nil); !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("FailApply(worker-b) error = %v, want ErrLeaseLost", err)
	}
	if err := s.FailApply(ctx, j.ID, "worker-a", id, "the work tree has uncommitted changes", blockers, nil); err != nil {
		t.Fatalf("FailApply(%d) error = %v", id, err)
	}
	r := readRun(t, s, id)
	if r.State != apply.StateFailed || r.FailureReason != "the work tree has uncommitted changes" || r.FinishedAt.IsZero() {
		t.Errorf("ApplyRun(%d) after failing = %+v, want failed with the reason and a finish time", id, r)
	}
	if diff := cmp.Diff(blockers, r.Blockers); diff != "" {
		t.Errorf("ApplyRun(%d).Blockers (-want +got):\n%s", id, diff)
	}
	if jr, err := s.ApplyJob(ctx, int64(id), "apply"); err != nil || jr.State != "failed" {
		t.Errorf("ApplyJob(%d, apply) = %+v, %v, want failed", id, jr, err)
	}
	applied.Status = apply.StatusApplied
	if diff := cmp.Diff([]apply.Item{applied}, applyItems(t, s, id)); diff != "" {
		t.Errorf("ApplyItems(%d) after failing (-want +got):\n%s", id, diff)
	}
	if diff := cmp.Diff(map[string][]apply.Written{"conf/a.yml": {{AfterHash: hashOf("1")}}}, writtenFiles(t, s, pid)); diff != "" {
		t.Errorf("WrittenFiles(%d) after failing (-want +got):\n%s", pid, diff)
	}
	// The run changed a.yml, so it requested a confirmation scan; it is
	// finished here so that the subtests claim their own jobs.
	rescan := assertRescan(t, s, pid, id)
	if err := s.FailScan(ctx, claimRescan(t, s, rescan).ID, "worker-a", rescan, "fake reason"); err != nil {
		t.Fatalf("FailScan(%d) error = %v", rescan, err)
	}

	t.Run("unknown reason", func(t *testing.T) {
		pid, _, _, _ := decidedProject(t, s, "reason")
		id := requestCheck(t, s, pid)
		j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
		if err != nil {
			t.Fatalf("ClaimJob() error = %v", err)
		}
		if err := s.FailApply(ctx, j.ID, "worker-a", id, "fake reason from an error", nil, nil); err == nil {
			t.Errorf("FailApply(unknown reason) error = nil, want an error")
		}
		if r := readRun(t, s, id); r.State != apply.StateChecking || r.FailureReason != "" {
			t.Errorf("ApplyRun(%d) after a refused failure = %+v, want unchanged", id, r)
		}
	})
	t.Run("manifest", func(t *testing.T) {
		pid, _, _, _ := decidedProject(t, s, "manifest")
		id, j := claimApply(t, s, pid)
		if err := s.FailApply(ctx, j.ID, "worker-a", id, apply.FailureManifestWrite, nil, nil); err != nil {
			t.Fatalf("FailApply(%d, manifest) error = %v", id, err)
		}
		if r := readRun(t, s, id); r.State != apply.StateFailed || r.FailureReason != apply.FailureManifestWrite {
			t.Errorf("ApplyRun(%d) after failing = %+v, want failed with the manifest reason", id, r)
		}
	})
	t.Run("with items", func(t *testing.T) {
		pid, _, pw, _ := decidedProject(t, s, "items")
		id, j := claimApply(t, s, pid)
		failed := replaceItem(t, s, pid, pw, "conf/a.yml")
		failed.Status, failed.Reason = apply.StatusFailed, apply.ReasonStoreFailed
		if err := s.FailApply(ctx, j.ID, "worker-a", id, apply.FailureSecretStore, nil, []apply.Item{failed}); err != nil {
			t.Fatalf("FailApply(%d, items) error = %v", id, err)
		}
		if diff := cmp.Diff([]apply.Item{failed}, applyItems(t, s, id)); diff != "" {
			t.Errorf("ApplyItems(%d) after failing (-want +got):\n%s", id, diff)
		}
		if r := readRun(t, s, id); r.State != apply.StateFailed || r.Failed != 1 {
			t.Errorf("ApplyRun(%d) after failing = %+v, want failed with one failed item", id, r)
		}
	})
	t.Run("check with items", func(t *testing.T) {
		pid, _, pw, _ := decidedProject(t, s, "check-items")
		id := requestCheck(t, s, pid)
		j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
		if err != nil {
			t.Fatalf("ClaimJob() error = %v", err)
		}
		it := replaceItem(t, s, pid, pw, "conf/a.yml")
		if err := s.FailApply(ctx, j.ID, "worker-a", id, apply.FailureOther, nil, []apply.Item{it}); err == nil {
			t.Errorf("FailApply(check with items) error = nil, want an error")
		}
	})
	t.Run("check", func(t *testing.T) {
		pid, _, _, _ := decidedProject(t, s, "check")
		id := requestCheck(t, s, pid)
		j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
		if err != nil {
			t.Fatalf("ClaimJob() error = %v", err)
		}
		if err := s.FailApply(ctx, j.ID, "worker-a", id, "the project folder is not a git work tree", nil, nil); err != nil {
			t.Fatalf("FailApply(check %d) error = %v", id, err)
		}
		if r := readRun(t, s, id); r.State != apply.StateFailed || r.FailureReason != "the project folder is not a git work tree" {
			t.Errorf("ApplyRun(%d) after failing the check = %+v, want failed", id, r)
		}
		if jr, err := s.ApplyJob(ctx, int64(id), "apply-check"); err != nil || jr.State != "failed" {
			t.Errorf("ApplyJob(%d, apply-check) = %+v, %v, want failed", id, jr, err)
		}
	})
}

// TestFinishApply_ReplacesInterrupted finishes a run whose last attempt
// found an item interrupted by an earlier attempt and then failed or skipped
// it: the stored item takes the last attempt's outcome. An item failed for
// another reason keeps its own.
func TestFinishApply_ReplacesInterrupted(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, pw, _ := decidedProject(t, s, "shop")
	id, j := claimApply(t, s, pid)
	written := replaceItem(t, s, pid, pw, "conf/a.yml")
	beginFile(t, s, id, "conf/a.yml", hashOf("1"), written)
	skipped := replaceItem(t, s, pid, pw, "conf/b.xml")
	beginFile(t, s, id, "conf/b.xml", hashOf("2"), skipped)
	for _, f := range openFiles(t, s, pid) {
		if err := s.ResolveFile(ctx, f, false); err != nil {
			t.Fatalf("ResolveFile(%q, not done) error = %v", f.File, err)
		}
	}
	settled := replaceItem(t, s, pid, pw, "web/c.jsp")
	settled.Status, settled.Reason = apply.StatusFailed, apply.ReasonStoreFailed
	if err := s.InsertApplyItem(ctx, id, settled); err != nil {
		t.Fatalf("InsertApplyItem(%d) error = %v", id, err)
	}

	written.Status, written.Reason = apply.StatusFailed, apply.ReasonWriteFailed
	skipped.Status, skipped.Reason = apply.StatusSkipped, apply.ReasonFileChanged
	later := settled
	later.Status, later.Reason = apply.StatusSkipped, apply.ReasonFileChanged
	if err := s.FinishApply(ctx, j.ID, "worker-a", id, []apply.Item{written, skipped, later}); err != nil {
		t.Fatalf("FinishApply(%d) error = %v", id, err)
	}

	if diff := cmp.Diff([]apply.Item{written, skipped, settled}, applyItems(t, s, id)); diff != "" {
		t.Errorf("ApplyItems(%d) after finishing (-want +got):\n%s", id, diff)
	}
}

// TestBeginFile_ReopensInterrupted follows a retry of a run that stopped
// after announcing a file and before changing it: the next start settles the
// record as not done, the retry announces the file again with the same
// items, and confirming it applies them.
func TestBeginFile_ReopensInterrupted(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, pw, _ := decidedProject(t, s, "shop")
	id := applyRunIn(t, s, pid, sid, apply.StateRunning)
	it := replaceItem(t, s, pid, pw, "conf/a.yml")
	beginFile(t, s, id, "conf/a.yml", hashOf("1"), it)
	for _, f := range openFiles(t, s, pid) {
		if err := s.ResolveFile(ctx, f, false); err != nil {
			t.Fatalf("ResolveFile(%q, not done) error = %v", f.File, err)
		}
	}
	if got := applyItems(t, s, id); len(got) != 1 || got[0].Status != apply.StatusFailed || got[0].Reason != apply.ReasonInterrupted {
		t.Fatalf("ApplyItems(%d) after the interruption = %+v, want one failed, interrupted item", id, got)
	}

	writeFile(t, s, id, "conf/a.yml", hashOf("1"), it)
	it.Status = apply.StatusApplied
	if diff := cmp.Diff([]apply.Item{it}, applyItems(t, s, id)); diff != "" {
		t.Errorf("ApplyItems(%d) after the retry (-want +got):\n%s", id, diff)
	}
	if b := readBundle(t, s, pid, sid, pw); b.AppliedAt.IsZero() || b.StorePath != it.StorePath {
		t.Errorf("Bundle(%d) after the retry applied at %v, store path %q, want a time and %q", pw, b.AppliedAt, b.StorePath, it.StorePath)
	}
}

func TestBeginFile_SettledItem(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, sid, pw, _ := decidedProject(t, s, "shop")
	id := applyRunIn(t, s, pid, sid, apply.StateRunning)
	applied := replaceItem(t, s, pid, pw, "conf/a.yml")
	writeFile(t, s, id, "conf/a.yml", hashOf("1"), applied)
	failed := replaceItem(t, s, pid, pw, "conf/b.xml")
	failed.Status, failed.Reason = apply.StatusFailed, apply.ReasonWriteFailed
	if err := s.InsertApplyItem(ctx, id, failed); err != nil {
		t.Fatalf("InsertApplyItem(%d) error = %v", id, err)
	}
	before := applyItems(t, s, id)
	wantWritten := writtenFiles(t, s, pid)

	for _, it := range []apply.Item{applied, failed} {
		other := it
		other.FindingID, other.StartLine = 0, 99 // a new location, stored before the settled one
		if err := s.BeginFile(ctx, id, it.File, fileHash, hashOf("2"), tempOf(it.File), []apply.Item{other, it}); !errors.Is(err, db.ErrItemSettled) {
			t.Errorf("BeginFile(%q) with a %s item error = %v, want ErrItemSettled", it.File, it.Status, err)
		}
	}
	if diff := cmp.Diff(before, applyItems(t, s, id)); diff != "" {
		t.Errorf("ApplyItems(%d) after refused announcements (-want +got):\n%s", id, diff)
	}
	if got := openFiles(t, s, pid); len(got) != 0 {
		t.Errorf("OpenFiles(%d) after refused announcements = %+v, want none", pid, got)
	}
	if diff := cmp.Diff(wantWritten, writtenFiles(t, s, pid)); diff != "" {
		t.Errorf("WrittenFiles(%d) after refused announcements (-want +got):\n%s", pid, diff)
	}
}

// TestBeginFile_RewriteWritten announces again a file the run has written,
// as a retry that rewrites the manifest does, and settles the new record
// both ways.
func TestBeginFile_RewriteWritten(t *testing.T) {
	const manifest = "secrets-manifest.yml"
	for _, c := range []struct {
		name   string
		settle func(t *testing.T, s *db.Store, id apply.ID, f apply.OpenFile)
		want   string
	}{
		{"not done", func(t *testing.T, s *db.Store, _ apply.ID, f apply.OpenFile) {
			t.Helper()
			if err := s.ResolveFile(t.Context(), f, false); err != nil {
				t.Fatalf("ResolveFile(not done) error = %v", err)
			}
		}, hashOf("1")},
		{"resolved done", func(t *testing.T, s *db.Store, _ apply.ID, f apply.OpenFile) {
			t.Helper()
			if err := s.ResolveFile(t.Context(), f, true); err != nil {
				t.Fatalf("ResolveFile(done) error = %v", err)
			}
		}, hashOf("2")},
		{"ended", func(t *testing.T, s *db.Store, id apply.ID, f apply.OpenFile) {
			t.Helper()
			if err := s.EndFile(t.Context(), id, f.File, f.AfterHash, f.TempName); err != nil {
				t.Fatalf("EndFile() error = %v", err)
			}
		}, hashOf("2")},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newMigratedStore(t)
			pid, sid, _, _ := decidedProject(t, s, "shop")
			id := applyRunIn(t, s, pid, sid, apply.StateRunning)
			writeFile(t, s, id, manifest, hashOf("1"))
			beginFile(t, s, id, manifest, hashOf("2"))
			if got := writtenFiles(t, s, pid); len(got) != 0 {
				t.Errorf("WrittenFiles(%d) while the rewrite is open = %+v, want none", pid, got)
			}
			open := openFiles(t, s, pid)
			if len(open) != 1 {
				t.Fatalf("OpenFiles(%d) = %+v, want the rewrite", pid, open)
			}
			c.settle(t, s, id, open[0])

			if diff := cmp.Diff(map[string][]apply.Written{manifest: {{AfterHash: c.want}}}, writtenFiles(t, s, pid)); diff != "" {
				t.Errorf("WrittenFiles(%d) (-want +got):\n%s", pid, diff)
			}
			if got := openFiles(t, s, pid); len(got) != 0 {
				t.Errorf("OpenFiles(%d) = %+v, want none", pid, got)
			}
			// A later rewrite that does not happen goes back to what is
			// written now, not to an older hash.
			beginFile(t, s, id, manifest, hashOf("3"))
			if err := s.ResolveFile(t.Context(), openFiles(t, s, pid)[0], false); err != nil {
				t.Fatalf("ResolveFile(second rewrite, not done) error = %v", err)
			}
			if diff := cmp.Diff(map[string][]apply.Written{manifest: {{AfterHash: c.want}}}, writtenFiles(t, s, pid)); diff != "" {
				t.Errorf("WrittenFiles(%d) after a second rewrite did not happen (-want +got):\n%s", pid, diff)
			}
		})
	}
}

// claimRescan claims the job of the confirmation scan id and starts it.
func claimRescan(t *testing.T, s *db.Store, id scan.ID) job.Job {
	t.Helper()
	j, err := s.ClaimJob(t.Context(), "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if j.Kind != job.KindScan || j.ScanID != int64(id) {
		t.Fatalf("ClaimJob() = %+v, want the job of scan %d", j, id)
	}
	if err := s.StartScan(t.Context(), id); err != nil {
		t.Fatalf("StartScan(%d) error = %v", id, err)
	}
	return j
}

// assertRescan fails the test unless run id points at a queued scan of the
// project requested by tester for the run, with one queued scan job.
func assertRescan(t *testing.T, s *db.Store, pid project.ID, id apply.ID) scan.ID {
	t.Helper()
	r := readRun(t, s, id)
	if r.RescanID == 0 {
		t.Fatalf("ApplyRun(%d).RescanID = 0, want a confirmation scan", id)
	}
	sc, err := s.Scan(t.Context(), r.RescanID)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", r.RescanID, err)
	}
	if sc.ProjectID != pid || sc.Status != scan.StatusQueued || sc.ApplyID != int64(id) ||
		sc.RequestedBy != tester.Subject || sc.RequesterName != tester.Name {
		t.Errorf("Scan(%d) = %+v, want queued for project %d and run %d, requested by %+v", r.RescanID, sc, pid, id, tester)
	}
	if jr, err := s.Job(t.Context(), int64(r.RescanID)); err != nil || jr.State != "queued" {
		t.Errorf("Job(scan %d) = %+v, %v, want queued", r.RescanID, jr, err)
	}
	return r.RescanID
}

func TestFinishApply_RequestsRescan(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, pw, _ := decidedProject(t, s, "shop")
		scans := jobsOfKind(t, s, "scan")
		id, j := claimApply(t, s, pid)
		writeFile(t, s, id, "conf/a.yml", hashOf("1"), replaceItem(t, s, pid, pw, "conf/a.yml"))
		if err := s.FinishApply(t.Context(), j.ID, "worker-a", id, nil); err != nil {
			t.Fatalf("FinishApply(%d) error = %v", id, err)
		}
		if r := readRun(t, s, id); r.State != apply.StateSucceeded || r.Applied != 1 {
			t.Errorf("ApplyRun(%d) = %+v, want succeeded with 1 applied", id, r)
		}
		assertRescan(t, s, pid, id)
		if n := jobsOfKind(t, s, "scan"); n != scans+1 {
			t.Errorf("scan jobs after finishing = %d, want %d", n, scans+1)
		}
	})
	t.Run("all skipped", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, pw, _ := decidedProject(t, s, "shop")
		scans := jobsOfKind(t, s, "scan")
		id, j := claimApply(t, s, pid)
		skipped := replaceItem(t, s, pid, pw, "conf/a.yml")
		skipped.Status, skipped.Reason = apply.StatusSkipped, apply.ReasonFileChanged
		if err := s.FinishApply(t.Context(), j.ID, "worker-a", id, []apply.Item{skipped}); err != nil {
			t.Fatalf("FinishApply(%d) error = %v", id, err)
		}
		if r := readRun(t, s, id); r.State != apply.StateSucceeded || r.RescanID != 0 {
			t.Errorf("ApplyRun(%d) = %+v, want succeeded with no confirmation scan", id, r)
		}
		if n := jobsOfKind(t, s, "scan"); n != scans {
			t.Errorf("scan jobs after finishing = %d, want %d", n, scans)
		}
	})
	t.Run("scan cannot be added", func(t *testing.T) {
		s := newMigratedStore(t)
		ctx := t.Context()
		pid, _, pw, _ := decidedProject(t, s, "shop")
		id, j := claimApply(t, s, pid)
		writeFile(t, s, id, "conf/a.yml", hashOf("1"), replaceItem(t, s, pid, pw, "conf/a.yml"))
		// The exclusion keeps a scan from being requested while the run is
		// running; a scan put in directly makes the confirmation scan fail
		// the one-active-scan index.
		blocking, err := s.InsertRawScanJob(ctx, pid, "queued", "queued")
		if err != nil {
			t.Fatalf("InsertRawScanJob(%d) error = %v", pid, err)
		}
		if err := s.FinishApply(ctx, j.ID, "worker-a", id, nil); err == nil {
			t.Fatalf("FinishApply(%d) with a scan in the way error = nil, want an error", id)
		}
		if r := readRun(t, s, id); r.State != apply.StateRunning || r.RescanID != 0 || !r.FinishedAt.IsZero() {
			t.Errorf("ApplyRun(%d) after a failed finish = %+v, want running, no confirmation scan", id, r)
		}
		if jr, err := s.ApplyJob(ctx, int64(id), "apply"); err != nil || jr.State != "running" {
			t.Errorf("ApplyJob(%d, apply) = %+v, %v, want running", id, jr, err)
		}
		if n, err := s.Count(ctx, "scan"); err != nil || n != 2 {
			t.Errorf("rows in scan = %d, %v, want 2 (the decided scan and scan %d)", n, err, blocking)
		}
	})
}

func TestFailApply_RequestsRescan(t *testing.T) {
	t.Run("files changed", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, pw, _ := decidedProject(t, s, "shop")
		id, j := claimApply(t, s, pid)
		writeFile(t, s, id, "conf/a.yml", hashOf("1"), replaceItem(t, s, pid, pw, "conf/a.yml"))
		if err := s.FailApply(t.Context(), j.ID, "worker-a", id, apply.FailureManifestWrite, nil, nil); err != nil {
			t.Fatalf("FailApply(%d) error = %v", id, err)
		}
		if r := readRun(t, s, id); r.State != apply.StateFailed || r.Applied != 1 {
			t.Errorf("ApplyRun(%d) = %+v, want failed with 1 applied", id, r)
		}
		assertRescan(t, s, pid, id)
	})
	t.Run("nothing changed", func(t *testing.T) {
		s := newMigratedStore(t)
		pid, _, _, _ := decidedProject(t, s, "shop")
		scans := jobsOfKind(t, s, "scan")
		id, j := claimApply(t, s, pid)
		if err := s.FailApply(t.Context(), j.ID, "worker-a", id, apply.FailureDirty, []apply.Blocker{apply.BlockerDirty}, nil); err != nil {
			t.Fatalf("FailApply(%d) error = %v", id, err)
		}
		if r := readRun(t, s, id); r.State != apply.StateFailed || r.RescanID != 0 {
			t.Errorf("ApplyRun(%d) = %+v, want failed with no confirmation scan", id, r)
		}
		if n := jobsOfKind(t, s, "scan"); n != scans {
			t.Errorf("scan jobs after failing = %d, want %d", n, scans)
		}
	})
}

func TestReappeared(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid, _, pw, _ := decidedProject(t, s, "shop")
	id, j := claimApply(t, s, pid)
	writeFile(t, s, id, "conf/a.yml", hashOf("1"), replaceItem(t, s, pid, pw, "conf/a.yml"))
	writeFile(t, s, id, "conf/b.xml", hashOf("2"), replaceItem(t, s, pid, pw, "conf/b.xml"))
	if err := s.FinishApply(ctx, j.ID, "worker-a", id, nil); err != nil {
		t.Fatalf("FinishApply(%d) error = %v", id, err)
	}
	rescan := assertRescan(t, s, pid, id)

	// The confirmation scan finds the password again in b.xml, and the host
	// and phone numbers no apply touched.
	rj := claimRescan(t, s, rescan)
	again := []finding.Finding{
		password("conf/b.xml"), password("web/c.jsp"),
		host("conf/a.yml"), host("conf/b.xml"), phone("logs/x.log"), phone("logs/y.log"),
	}
	if err := s.SaveResult(ctx, rj.ID, "worker-a", rescan, result(again...)); err != nil {
		t.Fatalf("SaveResult(scan %d) error = %v", rescan, err)
	}
	if n, err := s.Reappeared(ctx, rescan); err != nil || n != 1 {
		t.Errorf("Reappeared(%d) = %d, %v, want 1", rescan, n, err)
	}

	clean := save(t, s, pid, password("web/c.jsp"), host("conf/a.yml"))
	if n, err := s.Reappeared(ctx, clean); err != nil || n != 0 {
		t.Errorf("Reappeared(%d) = %d, %v, want 0", clean, n, err)
	}
}
