// Package opengrep runs the opengrep pattern scanner over a folder and reads
// its JSON report.
package opengrep

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/scanexec"
)

const name = "opengrep"

// Scanner runs opengrep. Build it with New.
type Scanner struct {
	bin, rulesDir string
	excludes      []string
	env           scanexec.Env
}

// New returns a Scanner that runs the opengrep executable bin with the rules
// in rulesDir and the environment env. excludes are project-relative paths
// with forward slashes, each a file or a directory that is not scanned.
func New(bin, rulesDir string, excludes []string, env scanexec.Env) *Scanner {
	return &Scanner{bin: bin, rulesDir: rulesDir, excludes: slices.Clone(excludes), env: env}
}

// Name returns "opengrep".
func (*Scanner) Name() string { return name }

// Version returns the version opengrep reports.
func (s *Scanner) Version(ctx context.Context) (string, error) {
	return scanexec.Version(ctx, scanexec.Command{Tool: name, Bin: s.bin, Args: []string{"--version"}, Env: s.env})
}

// Scan scans dir, an absolute path, and returns what opengrep found.
func (s *Scanner) Scan(ctx context.Context, dir string) ([]scan.Raw, error) {
	raws, _, err := s.ScanWithCounts(ctx, dir)
	return raws, err
}

// ScanWithCounts is Scan that also returns how many results were dropped and
// how many errors opengrep reported for files it could analyse only in part.
func (s *Scanner) ScanWithCounts(ctx context.Context, dir string) ([]scan.Raw, scan.Counts, error) {
	if !filepath.IsAbs(dir) {
		return nil, scan.Counts{}, fmt.Errorf("scanning %s: the folder is not an absolute path", name)
	}
	out, err := scanexec.Run(ctx, scanexec.Command{Tool: name, Bin: s.bin, Args: s.args(dir), Dir: dir, Env: s.env})
	if err != nil {
		return nil, scan.Counts{}, fmt.Errorf("scanning: %w", err)
	}
	return Parse(out, dir)
}

// args returns the arguments for a scan of dir.
//
// dir is made the project root, so opengrep treats it as a folder without
// version control and anchors exclude patterns there. opengrep runs in dir
// with the target ".", so rules' paths patterns (such as *.properties) are
// matched against paths inside dir only, never against the folders above it. .gitignore files,
// .semgrepignore files, and nosemgrep comments are not honoured, so a
// scanned project cannot hide a file or a line from the scan. Rule IDs are
// kept as written in the rule files.
func (s *Scanner) args(dir string) []string {
	args := []string{
		"scan",
		"--config=" + s.rulesDir,
		"--json",
		"--quiet",
		"--no-git-ignore",
		"--x-ignore-semgrepignore-files",
		"--no-rewrite-rule-ids",
		"--disable-nosem",
		"--project-root=" + dir,
		"--max-target-bytes=20MB",
		"--timeout=10",
	}
	// The directory names never scanned, at any depth. The trailing slash
	// makes each pattern match directories only.
	for _, e := range []string{".git/", "target/", "build/", "node_modules/"} {
		args = append(args, "--exclude="+e)
	}
	for _, e := range s.excludes {
		args = append(args, "--exclude=/"+escapePattern(e))
	}
	return append(args, "--", ".")
}

// escapePattern escapes the characters that have a meaning in a gitignore
// pattern, so that p matches only the path it names.
func escapePattern(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '\\', '*', '?', '[', ']', ' ':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// position is a place in a file as opengrep reports it. Offset is a byte
// offset.
type position struct {
	Offset *int `json:"offset"`
}

// report is the part of an opengrep JSON report that Parse reads. The
// matched text (extra.lines, extra.metavars) and the error messages, which
// quote the source, are not read.
type report struct {
	Results *[]struct {
		CheckID string   `json:"check_id"`
		Path    string   `json:"path"`
		Start   position `json:"start"`
		End     position `json:"end"`
		Extra   struct {
			Metadata struct {
				Category string `json:"category"`
				Kind     string `json:"kind"`
			} `json:"metadata"`
		} `json:"extra"`
	} `json:"results"`
	Errors []struct{} `json:"errors"`
}

// Parse reads an opengrep JSON report of a scan of dir. A result whose file
// is not inside dir, or whose byte offsets are missing or out of order, is
// dropped and counted. The errors opengrep reports are counted.
//
// Parse fails with scan.ErrScannerFailed when the report is not valid JSON
// or a result's rule has no known metadata.category and metadata.kind,
// which is a mistake in the rule file. The error never quotes the report.
func Parse(out []byte, dir string) ([]scan.Raw, scan.Counts, error) {
	var rep report
	// The JSON error quotes the input, so it is not wrapped.
	if err := json.Unmarshal(out, &rep); err != nil || rep.Results == nil {
		return nil, scan.Counts{}, fmt.Errorf("reading %s report: output is not a valid report: %w", name, scan.ErrScannerFailed)
	}
	counts := scan.Counts{Errors: len(rep.Errors)}
	raws := make([]scan.Raw, 0, len(*rep.Results))
	for _, r := range *rep.Results {
		category, catErr := finding.ParseCategory(r.Extra.Metadata.Category)
		kind, kindErr := finding.ParseKind(r.Extra.Metadata.Kind)
		if catErr != nil || kindErr != nil {
			return nil, scan.Counts{}, fmt.Errorf("reading %s report: a rule has a missing or unknown metadata.category or metadata.kind: %w", name, scan.ErrScannerFailed)
		}
		file, ok := scan.RelPath(dir, r.Path)
		if !ok || r.Start.Offset == nil || r.End.Offset == nil || *r.Start.Offset < 0 || *r.End.Offset < *r.Start.Offset {
			counts.Dropped++
			continue
		}
		raws = append(raws, scan.Raw{
			Tool: name, Rule: r.CheckID, Category: category, Kind: kind, File: file,
			Offset: *r.Start.Offset, EndOffset: *r.End.Offset,
		})
	}
	return raws, counts, nil
}
