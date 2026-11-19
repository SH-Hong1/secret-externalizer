package finding

import (
	"path"
	"strings"
)

// CustomerDataPath reports whether rel, a project-relative path with forward
// slashes, is a file in which the customer-data rules look for customer
// data: a file with the extension log, out, csv, tsv, txt, sql, dat, or json,
// a rotated log (a name in which ".log" or ".out" is followed by ".old",
// ".bak", or one of ".", "-", "_" and a digit, such as app.log.1 and
// catalina.out-20261101), or a file under a folder named log, logs, sample,
// samples, data, or dump at any depth, in any letter case. Such files are logs and sample data, whose lines
// may hold customer data that no rule reports, so no finding in them gets
// surrounding lines. The path pattern of the customer-data rules in
// rules/betterleaks.toml names the same files.
func CustomerDataPath(rel string) bool {
	base := strings.ToLower(path.Base(rel))
	switch path.Ext(base) {
	case ".log", ".out", ".csv", ".tsv", ".txt", ".sql", ".dat", ".json":
		return true
	}
	if rotatedLog(base) {
		return true
	}
	dir := path.Dir(rel)
	if dir == "." {
		return false
	}
	for _, name := range strings.Split(dir, "/") {
		switch strings.ToLower(name) {
		case "log", "logs", "sample", "samples", "data", "dump":
			return true
		}
	}
	return false
}

// rotatedLog reports whether base, a lower-case file name, is a log file
// with a rotation suffix.
func rotatedLog(base string) bool {
	for _, ext := range []string{".log", ".out"} {
		for rest := base; ; {
			i := strings.Index(rest, ext)
			if i < 0 {
				break
			}
			rest = rest[i+len(ext):]
			if rest == ".old" || rest == ".bak" {
				return true
			}
			if len(rest) >= 2 && strings.ContainsRune(".-_", rune(rest[0])) && rest[1] >= '0' && rest[1] <= '9' {
				return true
			}
		}
	}
	return false
}
