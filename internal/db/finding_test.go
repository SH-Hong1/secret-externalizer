package db_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// Fake fingerprints: 64 lower-case hex digits that no HMAC produced.
var (
	fpA = strings.Repeat("a", 64)
	fpB = strings.Repeat("b", 64)
	fpC = strings.Repeat("c", 64)
)

// fileHash is a fake SHA-256 of a file.
var fileHash = strings.Repeat("f", 64)

// claimedScan requests a scan of the project and claims its job as
// worker-a.
func claimedScan(t *testing.T, s *db.Store, pid project.ID) (scan.ID, job.Job) {
	t.Helper()
	ctx := t.Context()
	id, err := s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject"})
	if err != nil {
		t.Fatalf("RequestScan(%d) error = %v", pid, err)
	}
	j, err := s.ClaimJob(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimJob() error = %v", err)
	}
	if j.ScanID != int64(id) {
		t.Fatalf("ClaimJob() = %+v, want the job of scan %d", j, id)
	}
	if err := s.StartScan(ctx, id); err != nil {
		t.Fatalf("StartScan(%d) error = %v", id, err)
	}
	return id, j
}

// propsFile is the file every fake password is in.
const propsFile = "conf/db.properties"

// secretAt returns a finding of a password in propsFile at line, with one
// location found by two tools.
func secretAt(fp string, line int) finding.Finding {
	return finding.Finding{
		Fingerprint: fp, File: propsFile, Category: finding.CategoryStorage, Kind: finding.KindPassword,
		Preview: "dev-****", HasOriginal: true,
		Locations: []finding.Location{{
			StartLine: line, StartCol: 15, EndLine: line, EndCol: 40,
			Context:          []string{"# db", "jdbc.password=****", ""},
			ContextFirstLine: line - 1,
			FileHash:         fileHash,
			Detections:       []finding.Detection{{Tool: "betterleaks", Rule: "generic-password"}, {Tool: "opengrep", Rule: "properties-password"}},
		}},
	}
}

func result(findings ...finding.Finding) scan.Result {
	return scan.Result{
		CommitHash:   strings.Repeat("1", 40),
		RulesVersion: "0123456789ab",
		ToolVersions: map[string]string{"betterleaks": "1.10.1", "opengrep": "2.0.0", "keyfile": "1"},
		Files:        12,
		Skipped:      2,
		Dropped:      1,
		Findings:     findings,
	}
}

func TestSaveResult_StoresScanAndFindings(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	id, j := claimedScan(t, s, pid)

	customer := finding.Finding{
		Fingerprint: fpB, File: "logs/app.log", Category: finding.CategoryCustomerData, Kind: finding.KindPhone,
		Preview: "010-****-****",
		Locations: []finding.Location{
			{StartLine: 9, StartCol: 3, EndLine: 9, EndCol: 15, FileHash: fileHash, Detections: []finding.Detection{{Tool: "betterleaks", Rule: "customer-phone"}}},
			{StartLine: 2, StartCol: 1, EndLine: 2, EndCol: 13, FileHash: fileHash, Detections: []finding.Detection{{Tool: "betterleaks", Rule: "customer-phone"}}},
		},
	}
	in := result(secretAt(fpA, 4), customer, secretAt(fpC, 2))
	if err := s.SaveResult(ctx, j.ID, "worker-a", id, in); err != nil {
		t.Fatalf("SaveResult() error = %v", err)
	}

	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.Status != scan.StatusSucceeded || got.FinishedAt.IsZero() || got.Files != 12 || got.Skipped != 2 ||
		got.Dropped != 1 || got.Findings != 3 || got.CommitHash != in.CommitHash || got.RulesVersion != in.RulesVersion ||
		!cmp.Equal(got.ToolVersions, in.ToolVersions) {
		t.Errorf("Scan(%d) after SaveResult = %+v, want succeeded with the counts and versions of %+v", id, got, in)
	}
	assertJobState(t, s, id, "done")

	findings, err := s.FindingsByScan(ctx, id)
	if err != nil {
		t.Fatalf("FindingsByScan(%d) error = %v", id, err)
	}
	// Ordered by file and then by the first location; locations by line and
	// column.
	sorted := customer
	sorted.Locations = []finding.Location{customer.Locations[1], customer.Locations[0]}
	want := []finding.Finding{secretAt(fpC, 2), secretAt(fpA, 4), sorted}
	// Each finding gets its own ID; TestBundle checks the IDs.
	if diff := cmp.Diff(want, findings, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(finding.Finding{}, "ID")); diff != "" {
		t.Errorf("FindingsByScan(%d) (-want +got):\n%s", id, diff)
	}
}

func TestSaveResult_StoresPort(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id, j := claimedScan(t, s, newProject(t, s, "shop"))
	port := secretAt(fpA, 4)
	port.Kind, port.Preview = finding.KindPort, "****"

	if err := s.SaveResult(ctx, j.ID, "worker-a", id, result(port)); err != nil {
		t.Fatalf("SaveResult() of a port error = %v", err)
	}
	findings, err := s.FindingsByScan(ctx, id)
	if err != nil {
		t.Fatalf("FindingsByScan(%d) error = %v", id, err)
	}
	if len(findings) != 1 || findings[0].Kind != finding.KindPort {
		t.Errorf("FindingsByScan(%d) = %+v, want one port finding", id, findings)
	}
}

func TestSaveResult_SecondScanKeepsFinding(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")

	first, j := claimedScan(t, s, pid)
	if err := s.SaveResult(ctx, j.ID, "worker-a", first, result(secretAt(fpA, 4))); err != nil {
		t.Fatalf("SaveResult(first) error = %v", err)
	}
	before, err := s.FindingRow(ctx, pid, fpA, propsFile)
	if err != nil {
		t.Fatalf("FindingRow() error = %v", err)
	}

	second, j := claimedScan(t, s, pid)
	moved := secretAt(fpA, 9)
	moved.Preview = "new-****"
	if err := s.SaveResult(ctx, j.ID, "worker-a", second, result(moved)); err != nil {
		t.Fatalf("SaveResult(second) error = %v", err)
	}
	after, err := s.FindingRow(ctx, pid, fpA, propsFile)
	if err != nil {
		t.Fatalf("FindingRow() error = %v", err)
	}
	want := db.FindingIDs{ID: before.ID, FirstScanID: int64(first), LastScanID: int64(second)}
	if after != want {
		t.Errorf("finding after the second scan = %+v, want %+v (same ID, first scan kept)", after, want)
	}
	if n, err := s.Count(ctx, "finding"); err != nil || n != 1 {
		t.Errorf("rows in finding = %d, %v, want 1", n, err)
	}

	// Each scan keeps the locations it found; the finding shows the preview
	// of the latest scan.
	for _, tt := range []struct {
		id   scan.ID
		line int
	}{{first, 4}, {second, 9}} {
		got, err := s.FindingsByScan(ctx, tt.id)
		if err != nil {
			t.Fatalf("FindingsByScan(%d) error = %v", tt.id, err)
		}
		if len(got) != 1 || len(got[0].Locations) != 1 || got[0].Locations[0].StartLine != tt.line || got[0].Preview != "new-****" {
			t.Errorf("FindingsByScan(%d) = %+v, want one finding at line %d with preview new-****", tt.id, got, tt.line)
		}
	}
}

func TestSaveResult_LeaseLost(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	id, err := s.RequestScan(ctx, pid, audit.Actor{Subject: "fake-subject"})
	if err != nil {
		t.Fatalf("RequestScan() error = %v", err)
	}
	a, err := s.ClaimJob(ctx, "worker-a", 0)
	if err != nil {
		t.Fatalf("ClaimJob(worker-a) error = %v", err)
	}
	if _, err := s.ClaimJob(ctx, "worker-b", time.Minute); err != nil {
		t.Fatalf("ClaimJob(worker-b) error = %v", err)
	}
	if err := s.StartScan(ctx, id); err != nil {
		t.Fatalf("StartScan(%d) error = %v", id, err)
	}

	err = s.SaveResult(ctx, a.ID, "worker-a", id, result(secretAt(fpA, 4)))
	if !errors.Is(err, job.ErrLeaseLost) {
		t.Fatalf("SaveResult(worker-a, taken over) error = %v, want ErrLeaseLost", err)
	}
	for _, table := range []string{"finding", "location"} {
		if n, err := s.Count(ctx, table); err != nil || n != 0 {
			t.Errorf("rows in %s after a refused SaveResult = %d, %v, want 0", table, n, err)
		}
	}
	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.Status != scan.StatusRunning || got.Files != 0 || got.Findings != 0 {
		t.Errorf("Scan(%d) after a refused SaveResult = %+v, want running with no counts", id, got)
	}
	assertJobState(t, s, id, "running")
}

func TestSaveResult_Twice(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	id, j := claimedScan(t, s, pid)
	in := result(secretAt(fpA, 4))

	if err := s.SaveResult(ctx, j.ID, "worker-a", id, in); err != nil {
		t.Fatalf("SaveResult(first) error = %v", err)
	}
	if err := s.SaveResult(ctx, j.ID, "worker-a", id, in); !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("SaveResult(second time) error = %v, want ErrLeaseLost", err)
	}
	if n, err := s.Count(ctx, "location"); err != nil || n != 1 {
		t.Errorf("rows in location after saving twice = %d, %v, want 1", n, err)
	}
}

func TestSaveResult_RejectsMalformedFinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*finding.Finding)
	}{
		{name: "short fingerprint", mutate: func(f *finding.Finding) { f.Fingerprint = "abc" }},
		{name: "upper-case fingerprint", mutate: func(f *finding.Finding) { f.Fingerprint = strings.Repeat("A", 64) }},
		{name: "unknown category", mutate: func(f *finding.Finding) { f.Category = "secret" }},
		{name: "unknown kind", mutate: func(f *finding.Finding) { f.Kind = "database" }},
		{name: "empty file", mutate: func(f *finding.Finding) { f.File = "" }},
		{name: "customer data with an original", mutate: func(f *finding.Finding) {
			f.Category, f.Kind, f.HasOriginal = finding.CategoryCustomerData, finding.KindPhone, true
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newMigratedStore(t)
			ctx := t.Context()
			id, j := claimedScan(t, s, newProject(t, s, "shop"))
			f := secretAt(fpA, 4)
			tt.mutate(&f)

			if err := s.SaveResult(ctx, j.ID, "worker-a", id, result(f)); err == nil {
				t.Fatalf("SaveResult(%s) error = nil, want the database to refuse it", tt.name)
			}
			for _, table := range []string{"finding", "location"} {
				if n, err := s.Count(ctx, table); err != nil || n != 0 {
					t.Errorf("rows in %s after a refused SaveResult = %d, %v, want 0", table, n, err)
				}
			}
			assertJobState(t, s, id, "running")
		})
	}
}

func TestFindingsByScan_NoFindings(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id, j := claimedScan(t, s, newProject(t, s, "shop"))
	if err := s.SaveResult(ctx, j.ID, "worker-a", id, result()); err != nil {
		t.Fatalf("SaveResult(no findings) error = %v", err)
	}
	got, err := s.FindingsByScan(ctx, id)
	if err != nil || len(got) != 0 {
		t.Errorf("FindingsByScan(%d) = %+v, %v, want none", id, got, err)
	}
}

func TestSaveResult_KeepsSkippedFilesAndScannerErrors(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id, j := claimedScan(t, s, newProject(t, s, "shop"))

	in := result()
	in.SkippedFiles = []string{"bin/fake.jar", "docs/한글 설명서.pdf"}
	in.ScannerErrors = map[string]int{"opengrep": 3}
	if err := s.SaveResult(ctx, j.ID, "worker-a", id, in); err != nil {
		t.Fatalf("SaveResult() error = %v", err)
	}
	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if diff := cmp.Diff(in.SkippedFiles, got.SkippedFiles); diff != "" {
		t.Errorf("Scan(%d) SkippedFiles (-want +got):\n%s", id, diff)
	}
	if diff := cmp.Diff(in.ScannerErrors, got.ScannerErrors); diff != "" {
		t.Errorf("Scan(%d) ScannerErrors (-want +got):\n%s", id, diff)
	}
}

func TestSaveResult_NoSkippedFilesOrScannerErrors(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	id, j := claimedScan(t, s, newProject(t, s, "shop"))
	if err := s.SaveResult(ctx, j.ID, "worker-a", id, result()); err != nil {
		t.Fatalf("SaveResult() error = %v", err)
	}
	got, err := s.Scan(ctx, id)
	if err != nil {
		t.Fatalf("Scan(%d) error = %v", id, err)
	}
	if got.SkippedFiles != nil || got.ScannerErrors != nil {
		t.Errorf("Scan(%d) SkippedFiles = %q, ScannerErrors = %v, want both nil", id, got.SkippedFiles, got.ScannerErrors)
	}
}

func TestCategoryCounts(t *testing.T) {
	s := newMigratedStore(t)
	ctx := t.Context()
	pid := newProject(t, s, "shop")
	customer := finding.Finding{
		Fingerprint: fpB, File: "logs/app.log", Category: finding.CategoryCustomerData, Kind: finding.KindPhone,
		Preview: "010-****-****",
		Locations: []finding.Location{
			{StartLine: 9, StartCol: 3, EndLine: 9, EndCol: 15, FileHash: fileHash},
			{StartLine: 2, StartCol: 1, EndLine: 2, EndCol: 13, FileHash: fileHash},
		},
	}
	first, j := claimedScan(t, s, pid)
	if err := s.SaveResult(ctx, j.ID, "worker-a", first, result(secretAt(fpA, 4), customer, secretAt(fpC, 2))); err != nil {
		t.Fatalf("SaveResult(%d) error = %v", first, err)
	}
	second, j := claimedScan(t, s, pid)
	if err := s.SaveResult(ctx, j.ID, "worker-a", second, result(secretAt(fpA, 4))); err != nil {
		t.Fatalf("SaveResult(%d) error = %v", second, err)
	}

	tests := []struct {
		id   scan.ID
		want []scan.CategoryCount
	}{
		{first, []scan.CategoryCount{
			{Category: finding.CategoryCustomerData, Findings: 1, Locations: 2},
			{Category: finding.CategoryStorage, Findings: 2, Locations: 2},
		}},
		{second, []scan.CategoryCount{{Category: finding.CategoryStorage, Findings: 1, Locations: 1}}},
		{424242, nil},
	}
	for _, tt := range tests {
		got, err := s.CategoryCounts(ctx, tt.id)
		if err != nil {
			t.Fatalf("CategoryCounts(%d) error = %v", tt.id, err)
		}
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("CategoryCounts(%d) (-want +got):\n%s", tt.id, diff)
		}
	}
}
