package textfile

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
)

// isDefaultExclude reports whether a directory with this name is never
// scanned, at any depth. A file with one of these names is not excluded.
func isDefaultExclude(name string) bool {
	switch name {
	case ".git", "target", "build", "node_modules":
		return true
	}
	return false
}

// Walk calls fn for every regular file under root. root must be an absolute
// directory with its symbolic links already resolved. rel is the path of the
// file relative to root, with forward slashes.
//
// Symbolic links are never followed or passed to fn, and neither are other
// non-regular files. Directories named .git, target, build, or node_modules
// are skipped. excludes holds project-relative paths with forward slashes;
// each one skips that file or that whole directory.
//
// A directory that cannot be read is skipped, and skipped counts such
// directories. A failure to read the root is an error. An error from fn stops
// the walk and is returned.
func Walk(root string, excludes []string, fn func(rel string, d fs.DirEntry) error) (skipped int, err error) {
	skip := make(map[string]bool, len(excludes))
	for _, e := range excludes {
		skip[path.Clean(e)] = true
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == root {
				return fmt.Errorf("walking %s: %w", root, walkErr)
			}
			skipped++
			return nil
		}
		if p == root {
			return nil
		}
		relOS, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("making %s relative to %s: %w", p, root, err)
		}
		rel := filepath.ToSlash(relOS)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return nil
		case d.IsDir() && isDefaultExclude(d.Name()), skip[rel]:
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.Type().IsRegular():
			return fn(rel, d)
		}
		return nil
	})
	return skipped, err
}
