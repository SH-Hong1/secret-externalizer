package web_test

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/audit"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
	"github.com/SH-Hong1/secret-externalizer/internal/web"
)

// cappedReport is the data of scan 7 filled to every cap of a report:
// report.MaxRows lines in each of the six sections that are cut, each
// externalized entry with report.MaxPlaces places, and long Korean paths and
// notes, one of them a path with no place to break.
func cappedReport() report.Data {
	return filledReport(report.MaxRows, report.MaxPlaces)
}

// filledReport is cappedReport with rows lines in each section and places
// places on each externalized entry.
func filledReport(rows, places int) report.Data {
	d := fullReport()
	d.Changes, d.Redactions, d.Lines, d.Events = nil, nil, nil, nil
	decided := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	path := func(i int) string {
		return fmt.Sprintf("src/main/resources/설정/운영/%s/application-%04d.properties", strings.Repeat("모듈-하위폴더/", 6), i)
	}
	note := "운영 서버 접속 정보이며 배포 스크립트가 읽는다. 다음 분기에 구조를 바꿀 때까지 보류한다. " +
		strings.Repeat("긴사유", 30)
	unbroken := strings.Repeat("a", 160) + ".yml"

	for i := range rows {
		for j := range places {
			d.Changes = append(d.Changes, report.Change{Run: 2, Item: apply.Item{
				BundleID: triage.BundleID(1000 + i), File: path(j), Line: j + 1, Action: apply.ActionReplace,
				Category: finding.CategoryStorage, Kind: finding.KindPassword, Name: fmt.Sprintf("ext.db.main%04d.password", i),
				StorePath: fmt.Sprintf("secret/data/projects/3/ext.db.main%04d.password", i), Tracked: true, CodeChange: i%2 == 0,
				Status: apply.StatusApplied,
			}})
		}
		file := path(i)
		if i == 0 {
			file = unbroken
		}
		d.Redactions = append(d.Redactions, report.Redaction{
			Kind: finding.KindPhone, File: file, Untracked: i%3 == 0, Bundles: 37, Locations: 120,
		})
		d.Lines = append(d.Lines,
			report.Line{
				Bundle: triage.BundleID(5000 + i), Category: finding.CategoryPartnerSession, Kind: finding.KindToken,
				File: "web/화면/" + file + ".jsp", FirstLine: i + 1, Files: 3, Bundles: 1, Locations: 4, Frontend: true,
			},
			report.Line{
				Bundle: triage.BundleID(6000 + i), Category: finding.CategoryOther, Kind: finding.KindToken, File: file,
				FirstLine: i + 1, Files: 2, Bundles: 1, Locations: 3, Verdict: triage.VerdictHold, Reason: triage.HoldConfirm,
				Note: note, DecidedAt: decided,
			},
			report.Line{
				Bundle: triage.BundleID(7000 + i), Category: finding.CategoryOther, Kind: finding.KindOther, File: file,
				FirstLine: i + 1, Files: 1, Bundles: 1, Locations: 1, Verdict: triage.VerdictFalsePositive, Note: note, DecidedAt: decided,
			},
		)
		d.Events = append(d.Events, report.Event{
			At: decided, Actor: audit.Actor{Subject: "fake-subject", Name: "가짜 사용자"}, Action: audit.ActionVerdict,
			Detail: map[string]string{
				audit.DetailVerdict: string(triage.VerdictHold), audit.DetailReason: string(triage.HoldConfirm), audit.DetailNote: note,
			},
			Bundles: 37, Category: finding.CategoryOther, Kind: finding.KindToken, File: file,
		})
	}
	d.EventTotal = rows + 10
	return d
}

// pdfPage matches the dictionary of one page in a PDF.
var pdfPage = regexp.MustCompile(`/Type\s*/Page\b[^s]`)

func TestReportPDF_PrintsAtEveryCap(t *testing.T) {
	baseURL := os.Getenv("TEST_PDF_URL")
	if baseURL == "" {
		t.Skip("TEST_PDF_URL is not set; start the compose pdf-test service with --profile test and set TEST_PDF_URL=http://127.0.0.1:13000")
	}
	client, err := pdf.New(baseURL, &http.Client{Timeout: 25 * time.Second})
	if err != nil {
		t.Fatalf("pdf.New(%q) error = %v", baseURL, err)
	}
	// The limits web runs with.
	e := newTestEnvWith(t, web.Limits{Store: 5 * time.Second, PDF: 20 * time.Second}, client)
	e.reports.put(cappedReport())

	start := time.Now()
	res := e.getReportPDF(t)
	elapsed := time.Since(start)
	if res.status != http.StatusOK {
		t.Fatalf("GET /scans/7/report.pdf of a report at every cap status = %d after %v, want 200; log:\n%s", res.status, elapsed, e.log)
	}
	out := []byte(res.body)
	if !bytes.HasPrefix(out, []byte("%PDF-")) || len(out) > pdf.MaxBytes {
		t.Fatalf("GET /scans/7/report.pdf of a report at every cap = %d bytes starting %q, want a PDF of at most %d bytes",
			len(out), out[:min(len(out), 8)], pdf.MaxBytes)
	}
	// The image draws Korean with Noto Sans CJK KR when the sent fonts are
	// not used; the font descriptors are not compressed, so their names show
	// which fonts drew the page.
	for _, font := range []string{"+IBMPlexSansKR", "+D2Coding"} {
		if !bytes.Contains(out, []byte(font)) {
			t.Errorf("the PDF of a report at every cap does not embed %s", strings.TrimPrefix(font, "+"))
		}
	}
	t.Logf("a report at every cap printed in %v: %d bytes, %d pages", elapsed, len(out), len(pdfPage.FindAll(out, -1)))
}

// pdfLink matches a link annotation or its target in a PDF. The converter
// does not compress annotation dictionaries.
var pdfLink = regexp.MustCompile(`/Subtype\s*/Link\b|/URI\b`)

func TestReportPDF_NoLinkAnnotations(t *testing.T) {
	baseURL := os.Getenv("TEST_PDF_URL")
	if baseURL == "" {
		t.Skip("TEST_PDF_URL is not set; start the compose pdf-test service with --profile test and set TEST_PDF_URL=http://127.0.0.1:13000")
	}
	client, err := pdf.New(baseURL, &http.Client{Timeout: 25 * time.Second})
	if err != nil {
		t.Fatalf("pdf.New(%q) error = %v", baseURL, err)
	}
	e := newTestEnvWith(t, web.Limits{Store: 5 * time.Second, PDF: 20 * time.Second}, client)
	e.reports.put(linkedReport())

	res := e.getReportPDF(t)
	if res.status != http.StatusOK {
		t.Fatalf("GET /scans/7/report.pdf status = %d, want 200; log:\n%s", res.status, e.log)
	}
	if found := pdfLink.FindAllString(res.body, -1); len(found) != 0 {
		t.Errorf("the PDF of a report that links to other screens holds %d link annotation marks %q, want none", len(found), found)
	}
}
