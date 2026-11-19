package finding

import (
	"path"
	"strings"
)

// CustomerDataPath reports whether rel, a project-relative path with forward
// slashes, is a file in which the customer-data rules look for customer
// data: a file with the extension log, csv, tsv, txt, sql, dat, or json, or a
// file under a folder named log, logs, sample, samples, data, or dump at any
// depth, in any letter case. Such files are logs and sample data, whose lines
// may hold customer data that no rule reports, so no finding in them gets
// surrounding lines. The path pattern of the customer-data rules in
// rules/betterleaks.toml names the same files.
func CustomerDataPath(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".log", ".csv", ".tsv", ".txt", ".sql", ".dat", ".json":
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
