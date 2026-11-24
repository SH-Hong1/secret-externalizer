package web

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// workerSeenWithin is how long a worker may go without asking the queue for
// a job before a queued request says that no worker answers. A worker asks
// every couple of seconds.
const workerSeenWithin = 30 * time.Second

// timeLayout formats times for the pages. Times are shown in UTC with the
// zone named, so a reader never has to guess.
const timeLayout = "2006-01-02 15:04:05 UTC"

// categoryOrder returns the order in which categories are listed.
func categoryOrder() []finding.Category {
	return []finding.Category{
		finding.CategoryStorage,
		finding.CategorySSH,
		finding.CategoryPartnerSession,
		finding.CategoryOther,
		finding.CategoryCustomerData,
	}
}

// scanPage is what the scan page shows. Requester is the name of the user
// who asked for the scan, or the subject for a scan stored without a name.
// Succeeded is true for a succeeded scan, and the sections about what it
// found are left out otherwise. Triage is the path of the triage screen when
// the page links there: from the project's latest succeeded scan, and from a
// queued or running scan when a succeeded one exists. LaterScan is the ID of
// the project's latest succeeded scan when it is newer than this one.
// SkippedMore counts the skipped files and folders whose path is not listed,
// SkippedCapped tells whether the list stopped at scan.MaxSkippedFiles, and
// Unchecked whether the scan left anything unchecked at all. Report is the
// path of the report of a succeeded scan. Confirmation is set for a scan
// that confirms an apply.
type scanPage struct {
	NoWorker      bool
	User          auth.User
	ProjectName   string
	Scan          scan.Scan
	StatusLabel   string
	Requester     string
	RequestedAt   string
	FinishedAt    string
	Tools         []toolVersion
	Succeeded     bool
	Counts        []categoryCount
	Findings      int
	Locations     int
	SkippedMore   int
	SkippedCapped bool
	ScannerErrors []toolErrors
	Unchecked     bool
	Triage        string
	Undecided     int
	LaterScan     scan.ID
	Report        string
	Confirmation  *confirmationView
}

type toolVersion struct {
	Name, Version string
}

// toolErrors is the number of errors one scanner reported for files it
// could analyse only in part.
type toolErrors struct {
	Name string
	N    int
}

type categoryCount struct {
	Category, Label string
	N               int
}

type contextLine struct {
	Number int
	Hit    bool
	Text   string
}

// scanRow is one line of a project's scan list. Report is the path of the
// report of a succeeded scan.
type scanRow struct {
	ID          scan.ID
	Status      scan.Status
	StatusLabel string
	Requester   string
	Commit      string
	RequestedAt string
	Succeeded   bool
	Findings    int
	Basis       bool
	Report      string
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

func statusLabel(s scan.Status) string {
	switch s {
	case scan.StatusQueued:
		return "대기 중"
	case scan.StatusRunning:
		return "실행 중"
	case scan.StatusSucceeded:
		return "완료"
	case scan.StatusFailed:
		return "실패"
	}
	return string(s)
}

func categoryLabel(c finding.Category) string {
	switch c {
	case finding.CategoryStorage:
		return "저장소 접속 정보"
	case finding.CategorySSH:
		return "SSH 접속 정보"
	case finding.CategoryPartnerSession:
		return "제휴사 세션 정보"
	case finding.CategoryOther:
		return "기타 비밀 값"
	case finding.CategoryCustomerData:
		return "고객정보"
	}
	return string(c)
}

func kindLabel(k finding.Kind) string {
	switch k {
	case finding.KindPhone:
		return "전화번호"
	case finding.KindRRN:
		return "주민등록번호"
	case finding.KindEmail:
		return "이메일"
	case finding.KindCard:
		return "카드번호"
	case finding.KindAddress:
		return "주소"
	case finding.KindPassword:
		return "비밀번호"
	case finding.KindToken:
		return "토큰"
	case finding.KindAccount:
		return "계정"
	case finding.KindHost:
		return "호스트"
	case finding.KindPort:
		return "포트"
	case finding.KindURL:
		return "URL"
	case finding.KindPrivateKey:
		return "개인키"
	case finding.KindKeyFile:
		return "키 파일"
	case finding.KindOther:
		return "기타"
	}
	return string(k)
}

// newScanPage shapes a scan for the page. The counts of a succeeded scan
// are added by setCounts.
func newScanPage(sc scan.Scan) scanPage {
	p := scanPage{
		Scan:          sc,
		StatusLabel:   statusLabel(sc.Status),
		Requester:     requester(sc),
		RequestedAt:   formatTime(sc.RequestedAt),
		FinishedAt:    formatTime(sc.FinishedAt),
		Tools:         toolVersions(sc.ToolVersions),
		Succeeded:     sc.Status == scan.StatusSucceeded,
		SkippedMore:   max(0, sc.Skipped-len(sc.SkippedFiles)),
		SkippedCapped: len(sc.SkippedFiles) >= scan.MaxSkippedFiles,
		ScannerErrors: scannerErrors(sc.ScannerErrors),
	}
	p.Unchecked = leftUnchecked(sc)
	if p.Succeeded {
		p.Report = reportPath(sc.ID)
	}
	return p
}

// requester returns the name shown for the user who asked for sc: the name
// kept with the scan, or the subject for a scan stored without one.
func requester(sc scan.Scan) string {
	if sc.RequesterName != "" {
		return sc.RequesterName
	}
	return sc.RequestedBy
}

func toolVersions(m map[string]string) []toolVersion {
	out := make([]toolVersion, 0, len(m))
	for name, v := range m {
		out = append(out, toolVersion{name, v})
	}
	slices.SortFunc(out, func(a, b toolVersion) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// scannerErrors lists the tools of m that reported errors, ordered by name.
func scannerErrors(m map[string]int) []toolErrors {
	var out []toolErrors
	for name, n := range m {
		if n > 0 {
			out = append(out, toolErrors{name, n})
		}
	}
	slices.SortFunc(out, func(a, b toolErrors) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// setCounts adds the counts of findings per category, in categoryOrder,
// and their totals. A category outside categoryOrder follows, ordered by
// name. counts is not changed.
func (p *scanPage) setCounts(counts []scan.CategoryCount) {
	rank := func(c finding.Category) int {
		i := slices.Index(categoryOrder(), c)
		if i < 0 {
			return len(categoryOrder())
		}
		return i
	}
	sorted := slices.Clone(counts)
	slices.SortStableFunc(sorted, func(a, b scan.CategoryCount) int {
		if d := rank(a.Category) - rank(b.Category); d != 0 {
			return d
		}
		return strings.Compare(string(a.Category), string(b.Category))
	})
	p.Counts = make([]categoryCount, 0, len(sorted))
	for _, c := range sorted {
		p.Counts = append(p.Counts, categoryCount{string(c.Category), categoryLabel(c.Category), c.Findings})
		p.Findings += c.Findings
		p.Locations += c.Locations
	}
}

func lineRange(l finding.Location) string {
	if l.EndLine > l.StartLine {
		return strconv.Itoa(l.StartLine) + "-" + strconv.Itoa(l.EndLine)
	}
	return strconv.Itoa(l.StartLine)
}

// contextLines numbers the surrounding lines of l and marks the lines that
// hold the value.
func contextLines(l finding.Location) []contextLine {
	out := make([]contextLine, 0, len(l.Context))
	for i, text := range l.Context {
		n := l.ContextFirstLine + i
		out = append(out, contextLine{Number: n, Hit: n >= l.StartLine && n <= max(l.EndLine, l.StartLine), Text: text})
	}
	return out
}

// shortCommit returns the first 7 characters of a commit hash, as git shows
// it in short form.
func shortCommit(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

func newScanRows(scans []scan.Scan) []scanRow {
	out := make([]scanRow, 0, len(scans))
	for _, sc := range scans {
		row := scanRow{
			ID: sc.ID, Status: sc.Status, StatusLabel: statusLabel(sc.Status), RequestedAt: formatTime(sc.RequestedAt),
			Requester: requester(sc), Commit: shortCommit(sc.CommitHash),
			Succeeded: sc.Status == scan.StatusSucceeded, Findings: sc.Findings,
		}
		if row.Succeeded {
			row.Report = reportPath(sc.ID)
		}
		out = append(out, row)
	}
	return out
}
