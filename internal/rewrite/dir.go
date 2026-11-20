package rewrite

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	tempInfix   = ".se-apply-"
	tempSuffix  = ".tmp"
	tempRandLen = 16
	createdMode = 0o644
)

// Dir is a project folder. No operation follows a symbolic link or leaves
// the folder.
type Dir struct {
	root *os.Root
	// beforeRename, when set, runs right before a temporary file is renamed
	// over the file it replaces. Only tests set it.
	beforeRename func()
}

// Seen is a file as Read found it: the hash of its bytes and the size,
// modification time and identity it had.
type Seen struct {
	Hash string
	info fs.FileInfo
}

// Open opens the folder dir. The caller closes the Dir.
func Open(dir string) (*Dir, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening project folder: %w", err)
	}
	return &Dir{root: root}, nil
}

// Close releases the folder.
func (d *Dir) Close() error {
	if err := d.root.Close(); err != nil {
		return fmt.Errorf("closing project folder: %w", err)
	}
	return nil
}

// Read returns the bytes of the regular file rel and what it looked like.
// It fails with ErrNotRegular for a symbolic link or anything else that is
// not a regular file, ErrTooLarge for a file longer than maxBytes, an error
// wrapping fs.ErrNotExist for a missing file, and ErrChanged when the file
// changes while it is opened or read.
func (d *Dir) Read(rel string, maxBytes int64) ([]byte, Seen, error) {
	before, err := d.lstatRegular(rel)
	if err != nil {
		return nil, Seen{}, err
	}
	if before.Size() > maxBytes {
		return nil, Seen{}, fmt.Errorf("reading %s: %w", rel, ErrTooLarge)
	}
	// os.Root resolves a symbolic link that stays inside the folder even
	// with O_NOFOLLOW, so the opened file is compared with what Lstat saw.
	// O_NONBLOCK keeps a FIFO swapped in after Lstat from blocking the open.
	f, err := d.root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, Seen{}, fmt.Errorf("opening %s: %w", rel, err)
	}
	defer func() {
		// The file was only read, so a close failure loses nothing.
		_ = f.Close()
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, Seen{}, fmt.Errorf("reading %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, Seen{}, fmt.Errorf("reading %s: %w", rel, ErrChanged)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, Seen{}, fmt.Errorf("reading %s: %w", rel, err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, Seen{}, fmt.Errorf("reading %s: %w", rel, ErrTooLarge)
	}
	if int64(len(raw)) != info.Size() {
		return nil, Seen{}, fmt.Errorf("reading %s: %w", rel, ErrChanged)
	}
	sum := sha256.Sum256(raw)
	return raw, Seen{Hash: hex.EncodeToString(sum[:]), info: info}, nil
}

// TempName returns a new name for a temporary file beside rel, ending in
// 16 random hex digits and ".tmp": "<rel>.se-apply-<digits>.tmp".
func (d *Dir) TempName(rel string) (string, error) {
	if err := checkPath(rel); err != nil {
		return "", err
	}
	b := make([]byte, tempRandLen/2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("making a temporary file name: %w", err)
	}
	return rel + tempInfix + hex.EncodeToString(b) + tempSuffix, nil
}

// Write replaces the regular file rel with content through the temporary
// file tmp, which must not exist. It fails with ErrChanged when rel no
// longer looks as was says, also right before the rename. The new file has
// the mode of the file it replaces. A temporary file that Write made is
// removed when Write fails before the rename; one that already existed is
// left alone.
func (d *Dir) Write(rel string, content []byte, was Seen, tmp string) error {
	if err := checkTemp(rel, tmp); err != nil {
		return err
	}
	info, err := d.lstatRegular(rel)
	if err != nil {
		return err
	}
	if !was.matches(info) {
		return fmt.Errorf("writing %s: %w", rel, ErrChanged)
	}
	// The owner is not copied: the worker has no CAP_CHOWN, so the new file
	// belongs to the worker's user.
	if err := d.writeTemp(tmp, content, info.Mode().Perm()); err != nil {
		return err
	}
	if err := d.commit(tmp, func() error {
		if d.beforeRename != nil {
			d.beforeRename()
		}
		now, err := d.lstatRegular(rel)
		if err != nil {
			return err
		}
		if !was.matches(now) {
			return fmt.Errorf("writing %s: %w", rel, ErrChanged)
		}
		return d.root.Rename(tmp, rel)
	}); err != nil {
		return err
	}
	return d.syncParent(rel)
}

// Create writes the new file rel, which must not exist, through tmp. The
// file gets mode 0644.
func (d *Dir) Create(rel string, content []byte, tmp string) error {
	if err := checkTemp(rel, tmp); err != nil {
		return err
	}
	if err := d.checkParents(rel); err != nil {
		return err
	}
	if _, err := d.root.Lstat(rel); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = fs.ErrExist
		}
		return fmt.Errorf("creating %s: %w", rel, err)
	}
	if err := d.writeTemp(tmp, content, createdMode); err != nil {
		return err
	}
	// A hard link fails when rel exists, where a rename would replace it.
	if err := d.commit(tmp, func() error { return d.root.Link(tmp, rel) }); err != nil {
		return err
	}
	if err := d.root.Remove(tmp); err != nil {
		return fmt.Errorf("removing temporary file %s: %w", tmp, err)
	}
	return d.syncParent(rel)
}

// Remove deletes the regular file rel if it still looks as was says. It
// fails with ErrNotRegular for anything but a regular file and ErrChanged
// when the file changed.
func (d *Dir) Remove(rel string, was Seen) error {
	info, err := d.lstatRegular(rel)
	if err != nil {
		return err
	}
	if !was.matches(info) {
		return fmt.Errorf("removing %s: %w", rel, ErrChanged)
	}
	if err := d.root.Remove(rel); err != nil {
		return fmt.Errorf("removing %s: %w", rel, err)
	}
	return d.syncParent(rel)
}

// RemoveTemp deletes the regular file tmp, a name TempName gave. A
// missing file is not an error. A name of another shape, a symbolic link and
// a folder are refused.
func (d *Dir) RemoveTemp(tmp string) error {
	if err := checkPath(tmp); err != nil {
		return err
	}
	if !isTempName(filepath.Base(tmp)) {
		return fmt.Errorf("%q is not a temporary file name: %w", tmp, fs.ErrInvalid)
	}
	_, err := d.lstatRegular(tmp)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := d.root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing temporary file %s: %w", tmp, err)
	}
	return nil
}

// writeTemp makes tmp, which must not exist, with content and mode perm, and
// flushes it to disk. It removes tmp again if any step after making it fails.
func (d *Dir) writeTemp(tmp string, content []byte, perm fs.FileMode) (err error) {
	f, err := d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("creating temporary file %s: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()          // already closed, or the write failed; that error is the one reported
			_ = d.root.Remove(tmp) // a leftover is recorded by the caller and removed later
		}
	}()
	// The process umask narrows the mode given to OpenFile.
	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", tmp, err)
	}
	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("writing temporary file %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing temporary file %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing temporary file %s: %w", tmp, err)
	}
	return nil
}

// commit runs step, which puts the written temporary file tmp in place, and
// removes tmp if step fails.
func (d *Dir) commit(tmp string, step func() error) error {
	if err := step(); err != nil {
		_ = d.root.Remove(tmp) // a leftover is recorded by the caller and removed later
		return err
	}
	return nil
}

// syncParent flushes the folder that holds rel, so a rename or removal in it
// survives a crash.
func (d *Dir) syncParent(rel string) error {
	dir, err := d.root.Open(filepath.Dir(rel))
	if err != nil {
		return fmt.Errorf("opening the folder of %s: %w", rel, err)
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close() // the sync error is the one reported
		return fmt.Errorf("syncing the folder of %s: %w", rel, err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("closing the folder of %s: %w", rel, err)
	}
	return nil
}

// lstatRegular returns what rel is, without following a symbolic link, and
// fails with ErrNotRegular unless it is a regular file reached through
// folders only.
func (d *Dir) lstatRegular(rel string) (fs.FileInfo, error) {
	if err := checkPath(rel); err != nil {
		return nil, err
	}
	if err := d.checkParents(rel); err != nil {
		return nil, err
	}
	info, err := d.root.Lstat(rel)
	if err != nil {
		return nil, fmt.Errorf("checking %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("checking %s: %w", rel, ErrNotRegular)
	}
	return info, nil
}

// checkParents confirms that every folder on the way to rel is a folder and
// not a symbolic link. os.Root already refuses a link out of the folder; this
// also refuses one that stays inside.
func (d *Dir) checkParents(rel string) error {
	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	if parts[0] == "." {
		return nil
	}
	for i := range parts {
		p := filepath.Join(parts[:i+1]...)
		info, err := d.root.Lstat(p)
		if err != nil {
			return fmt.Errorf("checking %s: %w", p, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("checking %s: not a folder: %w", p, ErrNotRegular)
		}
	}
	return nil
}

// matches reports whether info is the file s describes, with the same size
// and modification time.
func (s Seen) matches(info fs.FileInfo) bool {
	return s.info != nil && os.SameFile(s.info, info) &&
		s.info.Size() == info.Size() && s.info.ModTime().Equal(info.ModTime())
}

// checkPath confirms rel is a clean relative path inside the folder.
func checkPath(rel string) error {
	if rel == "" || rel == "." || !filepath.IsLocal(rel) || filepath.Clean(rel) != rel {
		return fmt.Errorf("path %q: %w", rel, fs.ErrInvalid)
	}
	return nil
}

// checkTemp confirms tmp is a name TempName could give for rel.
func checkTemp(rel, tmp string) error {
	if err := checkPath(rel); err != nil {
		return err
	}
	if err := checkPath(tmp); err != nil {
		return err
	}
	rest, ok := strings.CutPrefix(tmp, rel+tempInfix)
	if !ok || filepath.Dir(tmp) != filepath.Dir(rel) || !isTempName(filepath.Base(tmp)) || len(rest) != tempRandLen+len(tempSuffix) {
		return fmt.Errorf("%q is not a temporary file name for %q: %w", tmp, rel, fs.ErrInvalid)
	}
	return nil
}

// isTempName reports whether base has the shape TempName gives:
// "<name>.se-apply-<16 lower-case hex digits>.tmp".
func isTempName(base string) bool {
	rest, ok := strings.CutSuffix(base, tempSuffix)
	if !ok || len(rest) < tempRandLen {
		return false
	}
	digits := rest[len(rest)-tempRandLen:]
	name, ok := strings.CutSuffix(rest[:len(rest)-tempRandLen], tempInfix)
	if !ok || name == "" {
		return false
	}
	for i := range len(digits) {
		c := digits[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
