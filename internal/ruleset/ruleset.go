// Package ruleset identifies a version of the detection rule files that the
// scanners run with.
package ruleset

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"slices"
)

// versionLen is the number of hex digits of the hash a version keeps.
const versionLen = 12

// Version returns the version of the rule files under dir: the first 12 hex
// digits of a SHA-256 over every regular file's path relative to dir, with
// forward slashes, and its content, taken in the order of the paths. It
// does not depend on the order the files are listed in, their modification
// times, or where dir is. A symbolic link or other non-regular file under
// dir is an error, because what it points to is not part of the rules.
func Version(dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("opening rule folder: %w", err)
	}
	defer func() {
		// The files were only read, so a close failure loses nothing.
		_ = root.Close()
	}()
	var names []string
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s in the rule folder is not a regular file", name)
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("listing rule files: %w", err)
	}
	slices.Sort(names)

	h := sha256.New()
	for _, name := range names {
		content, err := root.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("reading rule file: %w", err)
		}
		// Each name and content is preceded by its length, so that moving
		// bytes from one file to the next, or into a name, changes the hash.
		writeField(h, []byte(name))
		writeField(h, content)
	}
	return hex.EncodeToString(h.Sum(nil))[:versionLen], nil
}

// writeField writes the length of b as 8 big-endian bytes and then b.
func writeField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	// A hash.Hash never returns an error from Write.
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}
