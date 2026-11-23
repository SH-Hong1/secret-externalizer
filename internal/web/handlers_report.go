package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/db"
	"github.com/SH-Hong1/secret-externalizer/internal/pdf"
	"github.com/SH-Hong1/secret-externalizer/internal/report"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// The error page texts of the PDF export.
const (
	msgPDFUnavailable = "PDF 변환 서비스에 연결할 수 없습니다. 잠시 뒤 다시 시도해 주세요."
	msgPDFRejected    = "PDF를 만들지 못했습니다. 리포트가 너무 커서 변환기가 인쇄하지 못했을 수 있습니다."
)

// reportPage is what the report screen shows. PDF is the address of the
// PDF export, or empty when the screen offers none.
type reportPage struct {
	User auth.User
	View reportView
	PDF  string
}

// reportPath returns the path of the report of scan id.
func reportPath(id scan.ID) string {
	return scanPath(id) + "/report"
}

// reportPDFPath returns the path of the PDF of the report of scan id.
func reportPDFPath(id scan.ID) string {
	return reportPath(id) + ".pdf"
}

// handleReport shows the report of one succeeded scan, made at the time now
// returns. A scan that has not succeeded has no report, and the browser is
// sent to its scan page instead.
func handleReport(logger *slog.Logger, rd *renderer, reports ReportStore, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context()) // requireLogin guarantees a user
		d, ok := readReport(w, r, logger, rd, reports)
		if !ok {
			return
		}
		page := reportPage{User: u, View: newReportView(report.Build(d, now())), PDF: reportPDFPath(d.Scan.ID)}
		rd.page(w, r, logger, http.StatusOK, "report.html", page)
	})
}

// handleReportPDF sends the report of one succeeded scan as a PDF file. It
// reads the store under limits.Store and converts under limits.PDF, each
// from the moment it starts, so a slow database does not shorten the
// conversion. The PDF is written only once the converter has returned all
// of it; otherwise the browser gets an error page and no partial file.
func handleReportPDF(logger *slog.Logger, rd *renderer, reports ReportStore, p printer, limits Limits, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storeCtx, cancel := context.WithTimeout(r.Context(), limits.Store)
		d, ok := readReport(w, r.WithContext(storeCtx), logger, rd, reports)
		cancel()
		if !ok {
			return
		}
		view := newReportView(report.Build(d, now()))
		index, err := rd.render("report.html", "print", view)
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, err)
			return
		}
		footer, err := rd.render("report.html", "print-footer", view)
		if err != nil {
			rd.fail(w, r, logger, http.StatusInternalServerError, err)
			return
		}

		pdfCtx, cancel := context.WithTimeout(r.Context(), limits.PDF)
		defer cancel()
		out, err := p.converter.Convert(pdfCtx, pdf.Document{Index: index, Footer: footer, Files: p.files})
		switch {
		case errors.Is(err, pdf.ErrUnavailable):
			rd.errorPage(w, r, logger, http.StatusServiceUnavailable, msgPDFUnavailable, false, fmt.Errorf("converting report: %w", err))
			return
		case errors.Is(err, pdf.ErrRejected):
			rd.errorPage(w, r, logger, http.StatusInternalServerError, msgPDFRejected, false, fmt.Errorf("converting report: %w", err))
			return
		case err != nil:
			rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("converting report: %w", err))
			return
		}

		h := w.Header()
		h.Set("Content-Type", "application/pdf")
		h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="report-project-%d-scan-%d.pdf"`, d.Project.ID, d.Scan.ID))
		h.Set("Content-Length", strconv.Itoa(len(out)))
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(out); err != nil {
			logger.DebugContext(r.Context(), "writing response", "error", err)
		}
	})
}

// readReport reads the report data of the scan the path names. It writes
// the response and returns false when the scan is unknown, cannot be read,
// or has not succeeded, which sends the browser to the scan's page.
func readReport(w http.ResponseWriter, r *http.Request, logger *slog.Logger, rd *renderer, reports ReportStore) (report.Data, bool) {
	n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || n < 1 {
		rd.fail(w, r, logger, http.StatusNotFound, errors.New("scan id is not a number"))
		return report.Data{}, false
	}
	d, err := reports.Report(r.Context(), scan.ID(n))
	if errors.Is(err, db.ErrNotFound) {
		rd.fail(w, r, logger, http.StatusNotFound, err)
		return report.Data{}, false
	}
	if err != nil {
		rd.fail(w, r, logger, http.StatusInternalServerError, fmt.Errorf("reading report: %w", err))
		return report.Data{}, false
	}
	if d.Scan.Status != scan.StatusSucceeded {
		http.Redirect(w, r, scanPath(d.Scan.ID), http.StatusSeeOther)
		return report.Data{}, false
	}
	return d, true
}
