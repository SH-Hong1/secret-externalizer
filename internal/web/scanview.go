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

// scanPage is what the scan page shows. Findings is false until the scan
// has succeeded, and the findings sections are left out then.
type scanPage struct {
	User          auth.User
	ProjectName   string
	Scan          scan.Scan
	StatusLabel   string
	RequestedAt   string
	FinishedAt    string
	Tools         []toolVersion
	Findings      bool
	Counts        []categoryCount
	Secrets       []secretRow
	CustomerData  []customerGroup
	FindingsTotal int
}

type toolVersion struct {
	Name, Version string
}

type categoryCount struct {
	Category, Label string
	N               int
}

// secretRow is one location of a secret finding.
type secretRow struct {
	Category, CategoryLabel string
	Kind, KindLabel         string
	File, Lines, Preview    string
	Detections              []finding.Detection
	Context                 []contextLine
}

type contextLine struct {
	Number int
	Hit    bool
	Text   string
}

// customerGroup is the customer data of one kind in one file.
type customerGroup struct {
	File, Kind, KindLabel, Preview string
	Count, FirstLine               int
}

// scanRow is one line of a project's scan list.
type scanRow struct {
	ID          scan.ID
	Status      scan.Status
	StatusLabel string
	RequestedBy string
	Commit      string
	RequestedAt string
	Succeeded   bool
	Findings    int
	Basis       bool
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

// newScanPage shapes a scan and, for a succeeded scan, its findings for the
// page. fs is not changed.
func newScanPage(sc scan.Scan, fs []finding.Finding) scanPage {
	p := scanPage{
		Scan:        sc,
		StatusLabel: statusLabel(sc.Status),
		RequestedAt: formatTime(sc.RequestedAt),
		FinishedAt:  formatTime(sc.FinishedAt),
		Tools:       toolVersions(sc.ToolVersions),
		Findings:    sc.Status == scan.StatusSucceeded,
	}
	if !p.Findings {
		return p
	}
	p.FindingsTotal = len(fs)
	p.Counts = countByCategory(fs)
	p.Secrets, p.CustomerData = rowsOf(fs)
	return p
}

func toolVersions(m map[string]string) []toolVersion {
	out := make([]toolVersion, 0, len(m))
	for name, v := range m {
		out = append(out, toolVersion{name, v})
	}
	slices.SortFunc(out, func(a, b toolVersion) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// countByCategory counts findings per category, in categoryOrder, leaving out
// categories without findings. A category outside categoryOrder follows,
// ordered by name.
func countByCategory(fs []finding.Finding) []categoryCount {
	n := map[finding.Category]int{}
	for _, f := range fs {
		n[f.Category]++
	}
	var out []categoryCount
	for _, c := range categoryOrder() {
		if n[c] > 0 {
			out = append(out, categoryCount{string(c), categoryLabel(c), n[c]})
			delete(n, c)
		}
	}
	var rest []finding.Category
	for c := range n {
		rest = append(rest, c)
	}
	slices.Sort(rest)
	for _, c := range rest {
		out = append(out, categoryCount{string(c), categoryLabel(c), n[c]})
	}
	return out
}

// rowsOf turns findings into one row per location for secrets and one group
// per file and kind for customer data. Both keep the order of fs.
func rowsOf(fs []finding.Finding) ([]secretRow, []customerGroup) {
	var secrets []secretRow
	var groups []customerGroup
	type key struct {
		file string
		kind finding.Kind
	}
	index := map[key]int{}
	for _, f := range fs {
		if f.Category == finding.CategoryCustomerData {
			k := key{f.File, f.Kind}
			for _, l := range f.Locations {
				i, ok := index[k]
				if !ok {
					i = len(groups)
					index[k] = i
					groups = append(groups, customerGroup{
						File: f.File, Kind: string(f.Kind), KindLabel: kindLabel(f.Kind),
						Preview: f.Preview, FirstLine: l.StartLine,
					})
				}
				groups[i].Count++
				groups[i].FirstLine = min(groups[i].FirstLine, l.StartLine)
			}
			continue
		}
		for _, l := range f.Locations {
			secrets = append(secrets, secretRow{
				Category: string(f.Category), CategoryLabel: categoryLabel(f.Category),
				Kind: string(f.Kind), KindLabel: kindLabel(f.Kind),
				File: f.File, Lines: lineRange(l), Preview: f.Preview,
				Detections: slices.Clone(l.Detections),
				Context:    contextLines(l),
			})
		}
	}
	return secrets, groups
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
		out = append(out, scanRow{
			ID: sc.ID, Status: sc.Status, StatusLabel: statusLabel(sc.Status), RequestedAt: formatTime(sc.RequestedAt),
			RequestedBy: sc.RequestedBy, Commit: shortCommit(sc.CommitHash),
			Succeeded: sc.Status == scan.StatusSucceeded, Findings: sc.Findings,
		})
	}
	return out
}
