// Package project defines a registered project and the rules for the folder
// it points to. A project is a folder under the workspace root.
package project

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ID identifies a project.
type ID int64

// Project is a folder under the workspace root that is scanned for secrets.
// Path and Excludes are relative to the workspace root and use forward
// slashes.
type Project struct {
	ID        ID
	Name      string
	Path      string
	Excludes  []string
	CreatedBy string
	CreatedAt time.Time
}

var (
	// ErrInvalidPath means a path is empty, absolute, too long, has a
	// character that is not allowed, or leaves the workspace root.
	ErrInvalidPath = errors.New("invalid project path")
	// ErrOutsideRoot means a path resolves to a place outside the workspace
	// root.
	ErrOutsideRoot = errors.New("path is outside the workspace root")
	// ErrNotDirectory means a path does not name an existing directory.
	ErrNotDirectory = errors.New("path is not a directory")
)

// maxPathLen is the longest path, in bytes, that ParsePath accepts.
const maxPathLen = 1024

// isControl reports whether r is an ASCII control character or DEL.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// ParsePath checks the shape of a path relative to the workspace root and
// returns it in clean form. It does not touch the file system.
func ParsePath(raw string) (string, error) {
	switch {
	case raw == "":
		return "", fmt.Errorf("%w: empty", ErrInvalidPath)
	case len(raw) > maxPathLen:
		return "", fmt.Errorf("%w: longer than %d bytes", ErrInvalidPath, maxPathLen)
	case strings.IndexFunc(raw, isControl) >= 0:
		return "", fmt.Errorf("%w: contains a control character", ErrInvalidPath)
	case strings.ContainsRune(raw, '\\'):
		return "", fmt.Errorf("%w: contains a backslash", ErrInvalidPath)
	case strings.HasPrefix(raw, "/"):
		return "", fmt.Errorf("%w: absolute", ErrInvalidPath)
	}
	clean := path.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: leaves the workspace root", ErrInvalidPath)
	}
	return clean, nil
}

// Resolve returns the absolute path of the directory rel under root with
// every symbolic link resolved. It fails with ErrOutsideRoot when the result
// is not inside root and with ErrNotDirectory when it is not a directory,
// does not exist, or lies beyond a file.
func Resolve(root, rel string) (string, error) {
	rel, err := ParsePath(rel)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolving workspace root: %w", err)
	}
	full, err := filepath.EvalSymlinks(filepath.Join(realRoot, filepath.FromSlash(rel)))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %q does not exist", ErrNotDirectory, rel)
	}
	if errors.Is(err, syscall.ENOTDIR) {
		return "", fmt.Errorf("%w: %q goes through a file", ErrNotDirectory, rel)
	}
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", rel, err)
	}
	within, err := filepath.Rel(realRoot, full)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, rel)
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("checking %q: %w", rel, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %q", ErrNotDirectory, rel)
	}
	return full, nil
}
