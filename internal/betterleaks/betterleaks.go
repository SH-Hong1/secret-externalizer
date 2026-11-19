// Package betterleaks runs the betterleaks secret scanner over a folder and
// reads its JSON report.
package betterleaks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
)

const name = "betterleaks"

// exitLeaks is the exit code betterleaks is told to use when it finds
// something. It differs from 1, which betterleaks uses for its own errors.
const exitLeaks = 3

// Scanner runs betterleaks. Build it with New.
type Scanner struct {
	bin, config string
	env         scanexec.Env
}

// New returns a Scanner that runs the betterleaks executable bin with the
// rule file config and the environment env.
func New(bin, config string, env scanexec.Env) *Scanner {
	return &Scanner{bin: bin, config: config, env: env}
}

// Name returns "betterleaks".
func (*Scanner) Name() string { return name }

// Version returns the version betterleaks reports.
func (s *Scanner) Version(ctx context.Context) (string, error) {
	return scanexec.Version(ctx, scanexec.Command{Tool: name, Bin: s.bin, Args: []string{"version"}, Env: s.env})
}

// Scan scans dir, an absolute path, and returns what betterleaks found.
func (s *Scanner) Scan(ctx context.Context, dir string) ([]scan.Raw, error) {
	raws, _, err := s.ScanWithCounts(ctx, dir)
	return raws, err
}

// ScanWithCounts is Scan that also returns how many results were dropped.
func (s *Scanner) ScanWithCounts(ctx context.Context, dir string) ([]scan.Raw, scan.Counts, error) {
	if !filepath.IsAbs(dir) {
		return nil, scan.Counts{}, fmt.Errorf("scanning %s: the folder is not an absolute path", name)
	}
	out, err := scanexec.Run(ctx, scanexec.Command{
		Tool:    name,
		Bin:     s.bin,
		Args:    s.args(),
		Dir:     dir,
		Env:     s.env,
		OKCodes: []int{0, exitLeaks},
	})
	if err != nil {
		return nil, scan.Counts{}, fmt.Errorf("scanning: %w", err)
	}
	return Parse(out, dir)
}

// args returns the arguments for a scan of the working directory. Comments
// that allow a line (gitleaks:allow) are ignored, so a scanned project cannot
// hide a value from the scan.
//
// The target is "." and betterleaks runs in the scanned folder, so the paths
// it matches rules' path patterns against, and reports, are relative to that
// folder: a folder above it named, say, data does not make every file a data
// file.
func (s *Scanner) args() []string {
	return []string{
		"dir",
		"--no-banner",
		"--config=" + s.config,
		"--report-format=json",
		"--report-path=-",
		fmt.Sprintf("--exit-code=%d", exitLeaks),
		"--max-target-megabytes=20",
		"--timeout=1500",
		"--log-level=warn",
		"--ignore-gitleaks-allow",
		"--",
		".",
	}
}

// leak is the part of a betterleaks result that Parse reads. StartLine and
// StartColumn are the 1-based line and 1-based byte column where Match
// starts; Secret is the part of Match that is the value.
type leak struct {
	RuleID      string
	File        string
	StartLine   int
	StartColumn int
	Match       string
	Secret      string
	Tags        []string
}

// Parse reads a betterleaks JSON report of a scan of dir. Each result is
// placed at the first place its value appears in the matched text. A result
// whose value is not in the matched text, or whose file is not inside dir,
// is dropped and counted, and the file of a dropped result that is inside
// dir is listed in Counts.DroppedFiles.
//
// Parse fails with scan.ErrScannerFailed when the report is not valid JSON
// or a result's rule has a category or kind tag that is missing or unknown.
// The error never quotes the report.
func Parse(out []byte, dir string) ([]scan.Raw, scan.Counts, error) {
	var leaks []leak
	// A report with no results is "[]"; "null" or no output at all means
	// betterleaks did not write one. The JSON error quotes the input, so it
	// is not wrapped.
	if err := json.Unmarshal(out, &leaks); err != nil || leaks == nil {
		return nil, scan.Counts{}, fmt.Errorf("reading %s report: output is not a valid report: %w", name, scan.ErrScannerFailed)
	}
	var counts scan.Counts
	raws := make([]scan.Raw, 0, len(leaks))
	for _, l := range leaks {
		category, kind, err := classify(l.RuleID, l.Tags)
		if err != nil {
			return nil, scan.Counts{}, err
		}
		file, ok := scan.RelPath(dir, l.File)
		if !ok {
			counts.Dropped++
			continue
		}
		line, col, ok := locate(l)
		if !ok {
			counts.Dropped++
			counts.DroppedFiles = append(counts.DroppedFiles, file)
			continue
		}
		raws = append(raws, scan.Raw{
			Tool: name, Rule: l.RuleID, Category: category, Kind: kind, File: file,
			Offset: -1, EndOffset: -1, Line: line, ByteCol: col, Value: scan.Value(l.Secret),
		})
	}
	return raws, counts, nil
}

// locate returns the 1-based line and byte column of the first appearance
// of l.Secret in l.Match.
func locate(l leak) (line, col int, ok bool) {
	if l.StartLine < 1 || l.StartColumn < 1 || l.Secret == "" {
		return 0, 0, false
	}
	i := strings.Index(l.Match, l.Secret)
	if i < 0 {
		return 0, 0, false
	}
	before := l.Match[:i]
	nl := strings.LastIndexByte(before, '\n')
	if nl < 0 {
		return l.StartLine, l.StartColumn + i, true
	}
	return l.StartLine + strings.Count(before, "\n"), i - nl, true
}

// errRule means a rule's tags do not give a known category and kind.
var errRule = errors.New("a rule has a missing or unknown category or kind tag")

// classify returns the category and kind of a result from its rule's tags,
// "category:<category>" and "kind:<kind>". A rule with neither tag is one of
// betterleaks' own rules: it is a private key when its ID says so, and
// other-secret/other otherwise.
func classify(ruleID string, tags []string) (finding.Category, finding.Kind, error) {
	var categoryTag, kindTag string
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, "category:"); ok {
			categoryTag = v
		}
		if v, ok := strings.CutPrefix(t, "kind:"); ok {
			kindTag = v
		}
	}
	if categoryTag == "" && kindTag == "" {
		if strings.Contains(ruleID, "private-key") {
			return finding.CategorySSH, finding.KindPrivateKey, nil
		}
		return finding.CategoryOther, finding.KindOther, nil
	}
	category, err := finding.ParseCategory(categoryTag)
	if err != nil {
		return "", "", fmt.Errorf("reading %s report: %w: %w", name, errRule, scan.ErrScannerFailed)
	}
	kind, err := finding.ParseKind(kindTag)
	if err != nil {
		return "", "", fmt.Errorf("reading %s report: %w: %w", name, errRule, scan.ErrScannerFailed)
	}
	return category, kind, nil
}
