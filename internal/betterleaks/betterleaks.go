// Package betterleaks runs the betterleaks secret scanner over a folder and
// reads its JSON report.
package betterleaks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
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
// excludes are paths relative to dir with forward slashes, each a file or a
// folder that betterleaks does not read.
func (s *Scanner) Scan(ctx context.Context, dir string, excludes []string) ([]scan.Raw, error) {
	raws, _, err := s.ScanWithCounts(ctx, dir, excludes)
	return raws, err
}

// ScanWithCounts is Scan that also returns how many results were dropped.
func (s *Scanner) ScanWithCounts(ctx context.Context, dir string, excludes []string) (_ []scan.Raw, _ scan.Counts, err error) {
	if !filepath.IsAbs(dir) {
		return nil, scan.Counts{}, fmt.Errorf("scanning %s: the folder is not an absolute path", name)
	}
	config := s.config
	if len(excludes) > 0 {
		config, err = s.writeConfig(excludes)
		if err != nil {
			return nil, scan.Counts{}, err
		}
		defer func() {
			if rmErr := os.Remove(config); rmErr != nil && err == nil {
				err = fmt.Errorf("removing the %s rule file for the scan: %w", name, rmErr)
			}
		}()
	}
	out, err := scanexec.Run(ctx, scanexec.Command{
		Tool:    name,
		Bin:     s.bin,
		Args:    args(config),
		Dir:     dir,
		Env:     s.env,
		OKCodes: []int{0, exitLeaks},
	})
	if err != nil {
		return nil, scan.Counts{}, fmt.Errorf("scanning: %w", err)
	}
	return Parse(out, dir)
}

// writeConfig writes a rule file to the scanners' temporary folder that
// extends the configured rule file with a global allowlist of the excludes,
// and returns its path. betterleaks has no option to leave paths out, and it
// does not descend into a folder that this allowlist names. Each exclude
// becomes a regular expression that matches that path and everything under
// it, and nothing else; betterleaks matches it against paths relative to the
// scanned folder.
func (s *Scanner) writeConfig(excludes []string) (_ string, err error) {
	base, err := filepath.Abs(s.config)
	if err != nil {
		return "", fmt.Errorf("finding the %s rule file: %w", name, err)
	}
	var b strings.Builder
	b.WriteString("[extend]\npath = " + tomlString(base) + "\n\n[[allowlists]]\ndescription = \"project excludes\"\npaths = [\n")
	for _, e := range excludes {
		b.WriteString("  " + tomlString("^"+regexp.QuoteMeta(path.Clean(e))+"(?:/|$)") + ",\n")
	}
	b.WriteString("]\n")

	f, err := os.CreateTemp(s.env.TmpDir, "betterleaks-*.toml")
	if err != nil {
		return "", fmt.Errorf("creating the %s rule file for the scan: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name()) // the write error is the one to report
		}
	}()
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close() // the write error is the one to report
		return "", fmt.Errorf("writing the %s rule file for the scan: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("writing the %s rule file for the scan: %w", name, err)
	}
	return f.Name(), nil
}

// tomlString returns s as a TOML basic string: quotes, backslashes, and
// control characters are escaped.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// args returns the arguments for a scan of the working directory with the
// rule file config. Comments
// that allow a line (gitleaks:allow) are ignored, so a scanned project cannot
// hide a value from the scan.
//
// The target is "." and betterleaks runs in the scanned folder, so the paths
// it matches rules' path patterns against, and reports, are relative to that
// folder: a folder above it named, say, data does not make every file a data
// file.
func args(config string) []string {
	return []string{
		"dir",
		"--no-banner",
		"--config=" + config,
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

// String returns the rule and where the result is. Match and Secret hold
// text from the scanned file, so they are never printed.
func (l leak) String() string {
	return fmt.Sprintf("{rule:%s file:%s line:%d column:%d match:[redacted] secret:[redacted]}",
		l.RuleID, l.File, l.StartLine, l.StartColumn)
}

// Format prints the form String returns for every fmt verb, including %#v
// and %x, which would otherwise print Match and Secret.
func (l leak) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, l.String()) // a failed write to a fmt buffer has no one to report to
}

// MarshalJSON encodes the rule and where the result is. A leak inside a
// slice or a struct is logged through this method, not through LogValue.
func (l leak) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Rule   string `json:"rule"`
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	}{l.RuleID, l.File, l.StartLine, l.StartColumn})
}

// LogValue returns the rule and where the result is.
func (l leak) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("rule", l.RuleID),
		slog.String("file", l.File),
		slog.Int("line", l.StartLine),
		slog.Int("column", l.StartColumn),
	)
}

// Parse reads a betterleaks JSON report of a scan of dir. Each result is
// placed at the first place its value appears in the matched text. A result
// that betterleaks found by decoding text has a decoded match, so it keeps
// the line and column betterleaks gave, which are where the encoded text
// starts, and carries the encoding and the depth for the scan to place it. A
// result whose value is not in the matched text, whose decoding is unknown,
// or whose file is not inside dir, is dropped and counted, and the file of a dropped result that is inside
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
		encoding, depth, decoded, ok := decoding(l.Tags)
		line, col := l.StartLine, l.StartColumn
		if decoded {
			ok = ok && line >= 1 && col >= 1 && l.Secret != ""
		} else {
			line, col, ok = locate(l)
		}
		if !ok {
			counts.Dropped++
			counts.DroppedFiles = append(counts.DroppedFiles, file)
			continue
		}
		raws = append(raws, scan.Raw{
			Tool: name, Rule: l.RuleID, Category: category, Kind: kind, File: file,
			Offset: -1, EndOffset: -1, Line: line, ByteCol: col, Value: scan.Value(l.Secret),
			Encoding: encoding, Depth: depth,
		})
	}
	return raws, counts, nil
}

// decoding reads the tags betterleaks adds to a result it found in decoded
// text, "decoded:<encoding>" and "decode-depth:<n>". decoded is false when
// there is no decoded tag. ok is false when the encoding is not one the scan
// can decode or the depth is not a positive number. A missing depth is one.
func decoding(tags []string) (_ finding.Encoding, depth int, decoded, ok bool) {
	var name, depthTag string
	for _, t := range tags {
		if v, found := strings.CutPrefix(t, "decoded:"); found {
			name, decoded = v, true
		}
		if v, found := strings.CutPrefix(t, "decode-depth:"); found {
			depthTag = v
		}
	}
	if !decoded {
		return "", 0, false, true
	}
	encoding, err := finding.ParseEncoding(name)
	if err != nil {
		return "", 0, true, false
	}
	depth = 1
	if depthTag != "" {
		depth, err = strconv.Atoi(depthTag)
		if err != nil || depth < 1 {
			return "", 0, true, false
		}
	}
	return encoding, depth, true, true
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
