package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

// sourceFile is a regular file the scan listed in the project folder.
type sourceFile struct {
	// text is true when the file was read as UTF-8 or EUC-KR text.
	text bool
	// encoding is the encoding the file had on disk, when text is true.
	encoding textfile.Encoding
	// hash is the SHA-256 of the file's bytes on disk, when text is true.
	hash string
}

// fileList is the files of a project folder that the scan considers. A
// scanner result for a file not in files is not used.
type fileList struct {
	files map[string]sourceFile
	// unstorable counts the regular files whose path cannot be stored or
	// shown (see storablePath). They are not in files, and are counted in
	// skipped as well.
	unstorable int
	// skipped counts the listed files that could not be read as text, the
	// files whose path cannot be stored, and the directories that could not
	// be read.
	skipped int
	// skippedPaths holds the paths of the skipped files, in walk order. A
	// path that is not storable is kept as it is.
	skippedPaths []string
	// copied counts the EUC-KR files written to the folder of copies.
	copied int
}

// checkIgnoreFiles fails with errIgnoreFile when the top of dir holds a
// .gitleaksignore or .betterleaksignore entry, in any letter case.
// betterleaks reads these files from the folder it scans, and only from its
// top, and hides the results they list; no option turns that off.
func checkIgnoreFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listing project folder: %w", err)
	}
	for _, e := range entries {
		switch strings.ToLower(e.Name()) {
		case ".gitleaksignore", ".betterleaksignore":
			return fmt.Errorf("%w: %s", errIgnoreFile, e.Name())
		}
	}
	return nil
}

// listFiles walks dir, which is resolved, without the excluded paths, and
// reads every regular file as text. It writes the text of each EUC-KR file
// as UTF-8 to the same relative path under copies, which it empties first.
func listFiles(ctx context.Context, dir string, excludes []string, maxBytes int64, copies string) (fileList, error) {
	if err := os.RemoveAll(copies); err != nil {
		return fileList{}, fmt.Errorf("emptying the folder of copies: %w", err)
	}
	l := fileList{files: map[string]sourceFile{}}
	skippedDirs, err := textfile.Walk(dir, excludes, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !storablePath(rel) {
			l.unstorable++
			l.skipped++
			l.skippedPaths = append(l.skippedPaths, rel)
			return nil
		}
		f, err := textfile.Read(dir, rel, maxBytes)
		if notText(err) {
			l.files[rel] = sourceFile{}
			l.skipped++
			l.skippedPaths = append(l.skippedPaths, rel)
			return nil
		}
		if err != nil {
			return err
		}
		l.files[rel] = sourceFile{text: true, encoding: f.Encoding, hash: f.Hash}
		if f.Encoding != textfile.EUCKR {
			return nil
		}
		l.copied++
		return writeCopy(copies, rel, f.Text)
	})
	if err != nil {
		return fileList{}, fmt.Errorf("listing project files: %w", err)
	}
	l.skipped += skippedDirs
	return l, nil
}

// storablePath reports whether rel is valid UTF-8 without control
// characters other than tab. postgres refuses NUL and invalid UTF-8 in text,
// so a finding in a file with another path could not be saved and would
// fail every scan of the project.
func storablePath(rel string) bool {
	return finding.Printable(rel) == rel
}

// notText reports whether err from textfile.Read means the file is skipped:
// too large, binary, in another encoding, no longer a regular file, gone, or
// not readable.
func notText(err error) bool {
	for _, target := range []error{
		textfile.ErrTooLarge, textfile.ErrBinary, textfile.ErrUnknownEncoding, textfile.ErrNotRegular,
		fs.ErrPermission, fs.ErrNotExist,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// writeCopy writes text to rel under copies.
func writeCopy(copies, rel string, text []byte) error {
	p := filepath.Join(copies, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("making the folder for the copy of %s: %w", rel, err)
	}
	if err := os.WriteFile(p, text, 0o600); err != nil {
		return fmt.Errorf("writing the copy of %s: %w", rel, err)
	}
	return nil
}

// readOriginal returns the bytes of the regular file rel under dir exactly
// as they are on disk, and their hex SHA-256. It does not follow a symbolic
// link and fails for a file longer than maxBytes.
func readOriginal(dir, rel string, maxBytes int64) (_ []byte, hash string, err error) {
	data, err := textfile.ReadRaw(dir, rel, maxBytes)
	if err != nil {
		return nil, "", err
	}
	return data, hexSHA256(data), nil
}
