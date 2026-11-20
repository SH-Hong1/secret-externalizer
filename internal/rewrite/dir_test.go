package rewrite_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SH-Hong1/secret-externalizer/internal/rewrite"
)

// openDir returns a new folder and a Dir for it that is closed at the end of
// the test.
func openDir(t *testing.T) (string, *rewrite.Dir) {
	t.Helper()
	root := t.TempDir()
	d, err := rewrite.Open(root)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", root, err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close error = %v", err)
		}
	})
	return root, d
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod(%q) error = %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is under t.TempDir
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return string(b)
}

// names returns the names in folder dir, sorted.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
}

func readSeen(t *testing.T, d *rewrite.Dir, rel string) rewrite.Seen {
	t.Helper()
	_, seen, err := d.Read(rel, 1<<20)
	if err != nil {
		t.Fatalf("Read(%q) error = %v", rel, err)
	}
	return seen
}

func tempName(t *testing.T, d *rewrite.Dir, rel string) string {
	t.Helper()
	tmp, err := d.TempName(rel)
	if err != nil {
		t.Fatalf("TempName(%q) error = %v", rel, err)
	}
	return tmp
}

func TestDir_Read(t *testing.T) {
	root, d := openDir(t)
	writeFile(t, filepath.Join(root, "a.txt"), "pw=dev-only-fake-pw\n", 0o644)
	raw, seen, err := d.Read("a.txt", 1<<20)
	if err != nil {
		t.Fatalf("Read(a.txt) error = %v", err)
	}
	if string(raw) != "pw=dev-only-fake-pw\n" {
		t.Errorf("Read(a.txt) = %q, want the file content", raw)
	}
	sum := sha256.Sum256(raw)
	if want := hex.EncodeToString(sum[:]); seen.Hash != want {
		t.Errorf("Read(a.txt).Hash = %s, want %s", seen.Hash, want)
	}
	if _, _, err := d.Read("a.txt", 5); !errors.Is(err, rewrite.ErrTooLarge) {
		t.Errorf("Read(a.txt, 5) error = %v, want ErrTooLarge", err)
	}
	if _, _, err := d.Read("missing.txt", 1<<20); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read(missing.txt) error = %v, want fs.ErrNotExist", err)
	}
}

func TestDir_Write(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o444} {
		t.Run(mode.String(), func(t *testing.T) {
			root, d := openDir(t)
			if err := os.Mkdir(filepath.Join(root, "conf"), 0o750); err != nil {
				t.Fatalf("Mkdir error = %v", err)
			}
			path := filepath.Join(root, "conf", "a.properties")
			writeFile(t, path, "pw=dev-only-fake-pw\n", mode)
			seen := readSeen(t, d, "conf/a.properties")
			if err := d.Write("conf/a.properties", []byte("pw=${SE}\n"), seen, tempName(t, d, "conf/a.properties")); err != nil {
				t.Fatalf("Write error = %v", err)
			}
			if got := readFile(t, path); got != "pw=${SE}\n" {
				t.Errorf("content after Write = %q, want %q", got, "pw=${SE}\n")
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("Lstat error = %v", err)
			}
			if info.Mode() != mode {
				t.Errorf("mode after Write = %v, want %v", info.Mode(), mode)
			}
			if got := names(t, filepath.Join(root, "conf")); !slices.Equal(got, []string{"a.properties"}) {
				t.Errorf("folder after Write = %v, want only a.properties", got)
			}
		})
	}

	t.Run("folder not writable", func(t *testing.T) {
		skipIfRoot(t)
		root, d := openDir(t)
		path := filepath.Join(root, "a.txt")
		writeFile(t, path, "pw=dev-only-fake-pw\n", 0o644)
		seen := readSeen(t, d, "a.txt")
		tmp := tempName(t, d, "a.txt")
		if err := os.Chmod(root, 0o500); err != nil { //nolint:gosec // G302: a folder needs its search bit for the file in it to be read
			t.Fatalf("Chmod error = %v", err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // G302: the folder must be writable again for t.TempDir to remove it
				t.Errorf("Chmod error = %v", err)
			}
		})
		if err := d.Write("a.txt", []byte("x"), seen, tmp); err == nil {
			t.Errorf("Write in a read-only folder error = nil, want an error")
		}
		if got := readFile(t, path); got != "pw=dev-only-fake-pw\n" {
			t.Errorf("content after failed Write = %q, want it unchanged", got)
		}
	})
}

func TestDir_WriteDetectsChange(t *testing.T) {
	const original = "pw=dev-only-fake-pw\n"
	changes := []struct {
		name   string
		change func(t *testing.T, path string) string
	}{
		{"size", func(t *testing.T, path string) string {
			writeFile(t, path, original+"more\n", 0o644)
			return original + "more\n"
		}},
		{"modification time", func(t *testing.T, path string) string {
			const same = "pw=dev-only-fake-PW\n"
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat error = %v", err)
			}
			writeFile(t, path, same, 0o644)
			if err := os.Chtimes(path, time.Time{}, info.ModTime().Add(time.Hour)); err != nil {
				t.Fatalf("Chtimes error = %v", err)
			}
			return same
		}},
		{"other file", func(t *testing.T, path string) string {
			const same = "pw=dev-only-fake-QQ\n"
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat error = %v", err)
			}
			other := path + ".other"
			writeFile(t, other, same, 0o644)
			if err := os.Chtimes(other, time.Time{}, info.ModTime()); err != nil {
				t.Fatalf("Chtimes error = %v", err)
			}
			if err := os.Rename(other, path); err != nil {
				t.Fatalf("Rename error = %v", err)
			}
			return same
		}},
	}
	for _, c := range changes {
		for _, beforeRename := range []bool{false, true} {
			name := c.name
			if beforeRename {
				name += " right before the rename"
			}
			t.Run(name, func(t *testing.T) {
				root, d := openDir(t)
				path := filepath.Join(root, "a.txt")
				writeFile(t, path, original, 0o644)
				seen := readSeen(t, d, "a.txt")
				var want string
				if beforeRename {
					rewrite.SetBeforeRename(d, func() { want = c.change(t, path) })
				} else {
					want = c.change(t, path)
				}
				err := d.Write("a.txt", []byte("pw=${SE}\n"), seen, tempName(t, d, "a.txt"))
				if !errors.Is(err, rewrite.ErrChanged) {
					t.Errorf("Write after a change of %s error = %v, want ErrChanged", c.name, err)
				}
				if got := readFile(t, path); got != want {
					t.Errorf("content after Write = %q, want the changed content %q", got, want)
				}
				if got := names(t, root); !slices.Equal(got, []string{"a.txt"}) {
					t.Errorf("folder after Write = %v, want only a.txt", got)
				}
			})
		}
	}
}

func TestDir_TempFile(t *testing.T) {
	root, d := openDir(t)
	if err := os.Mkdir(filepath.Join(root, "conf"), 0o750); err != nil {
		t.Fatalf("Mkdir error = %v", err)
	}
	path := filepath.Join(root, "conf", "a.txt")
	writeFile(t, path, "pw=dev-only-fake-pw\n", 0o644)

	a, b := tempName(t, d, "conf/a.txt"), tempName(t, d, "conf/a.txt")
	if a == b {
		t.Errorf("TempName twice = %q and %q, want different names", a, b)
	}
	for _, n := range []string{a, b} {
		if filepath.Dir(n) != "conf" || !strings.HasPrefix(filepath.Base(n), "a.txt.se-apply-") || !strings.HasSuffix(n, ".tmp") ||
			len(filepath.Base(n)) != len("a.txt.se-apply-")+16+len(".tmp") {
			t.Errorf("TempName(conf/a.txt) = %q, want conf/a.txt.se-apply-<16 hex digits>.tmp", n)
		}
	}

	t.Run("existing temporary file", func(t *testing.T) {
		writeFile(t, filepath.Join(root, a), "someone else's\n", 0o644)
		if err := d.Write("conf/a.txt", []byte("x"), readSeen(t, d, "conf/a.txt"), a); err == nil {
			t.Errorf("Write through an existing temporary file error = nil, want an error")
		}
		if got := readFile(t, filepath.Join(root, a)); got != "someone else's\n" {
			t.Errorf("existing temporary file after Write = %q, want it untouched", got)
		}
		if got := readFile(t, path); got != "pw=dev-only-fake-pw\n" {
			t.Errorf("content after failed Write = %q, want it unchanged", got)
		}
	})

	t.Run("temporary file in another folder", func(t *testing.T) {
		other := tempName(t, d, "a.txt")
		if err := d.Write("conf/a.txt", []byte("x"), readSeen(t, d, "conf/a.txt"), other); err == nil {
			t.Errorf("Write(conf/a.txt) through %q error = nil, want an error", other)
		}
	})

	t.Run("RemoveTemp", func(t *testing.T) {
		if err := d.RemoveTemp(a); err != nil {
			t.Errorf("RemoveTemp(%q) error = %v", a, err)
		}
		if _, err := os.Lstat(filepath.Join(root, a)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat after RemoveTemp error = %v, want fs.ErrNotExist", err)
		}
		if err := d.RemoveTemp(a); err != nil {
			t.Errorf("RemoveTemp(missing %q) error = %v, want nil", a, err)
		}

		link := tempName(t, d, "conf/a.txt")
		if err := os.Symlink("a.txt", filepath.Join(root, link)); err != nil {
			t.Fatalf("Symlink error = %v", err)
		}
		folder := tempName(t, d, "conf/a.txt")
		if err := os.Mkdir(filepath.Join(root, folder), 0o750); err != nil {
			t.Fatalf("Mkdir error = %v", err)
		}
		for _, n := range []string{link, folder, "conf/a.txt", "conf/a.txt.se-apply-0123.tmp", "conf/a.txt.se-apply-0123456789ABCDEF.tmp"} {
			if err := d.RemoveTemp(n); err == nil {
				t.Errorf("RemoveTemp(%q) error = nil, want an error", n)
			}
			if _, err := os.Lstat(filepath.Join(root, n)); err != nil && !strings.Contains(n, "0123") {
				t.Errorf("Lstat(%q) after refused RemoveTemp error = %v, want it to remain", n, err)
			}
		}
		if got := readFile(t, path); got != "pw=dev-only-fake-pw\n" {
			t.Errorf("content of conf/a.txt = %q, want it unchanged", got)
		}
	})
}

func TestDir_RefusesLinks(t *testing.T) {
	root, d := openDir(t)
	target := filepath.Join(root, "target.txt")
	writeFile(t, target, "pw=dev-only-fake-pw\n", 0o644)
	if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	seen := readSeen(t, d, "target.txt")

	if _, _, err := d.Read("link.txt", 1<<20); !errors.Is(err, rewrite.ErrNotRegular) {
		t.Errorf("Read(link.txt) error = %v, want ErrNotRegular", err)
	}
	if err := d.Write("link.txt", []byte("x"), seen, tempName(t, d, "link.txt")); !errors.Is(err, rewrite.ErrNotRegular) {
		t.Errorf("Write(link.txt) error = %v, want ErrNotRegular", err)
	}
	if err := d.Remove("link.txt", seen); !errors.Is(err, rewrite.ErrNotRegular) {
		t.Errorf("Remove(link.txt) error = %v, want ErrNotRegular", err)
	}
	if got := readFile(t, target); got != "pw=dev-only-fake-pw\n" {
		t.Errorf("link target after refused operations = %q, want it unchanged", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "link.txt")); err != nil {
		t.Errorf("Lstat(link.txt) error = %v, want the link to remain", err)
	}

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "x.txt")
	writeFile(t, outsideFile, "outside\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	if _, _, err := d.Read("sub/x.txt", 1<<20); err == nil {
		t.Errorf("Read(sub/x.txt) through a link out of the folder error = nil, want an error")
	}
	if err := d.Write("sub/x.txt", []byte("x"), seen, "sub/x.txt.se-apply-0123456789abcdef.tmp"); err == nil {
		t.Errorf("Write(sub/x.txt) error = nil, want an error")
	}
	if err := d.Remove("sub/x.txt", seen); err == nil {
		t.Errorf("Remove(sub/x.txt) error = nil, want an error")
	}
	if err := d.Create("sub/new.txt", []byte("x"), "sub/new.txt.se-apply-0123456789abcdef.tmp"); err == nil {
		t.Errorf("Create(sub/new.txt) error = nil, want an error")
	}
	if got := readFile(t, outsideFile); got != "outside\n" {
		t.Errorf("file outside after refused operations = %q, want it unchanged", got)
	}
	if got := names(t, outside); !slices.Equal(got, []string{"x.txt"}) {
		t.Errorf("folder outside = %v, want only x.txt", got)
	}

	for _, rel := range []string{"../x.txt", outsideFile, "", ".", "a/../target.txt"} {
		if _, _, err := d.Read(rel, 1<<20); err == nil {
			t.Errorf("Read(%q) error = nil, want an error", rel)
		}
		if _, err := d.TempName(rel); err == nil {
			t.Errorf("TempName(%q) error = nil, want an error", rel)
		}
	}

	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatalf("Mkfifo error = %v", err)
	}
	if _, _, err := d.Read("pipe", 1<<20); !errors.Is(err, rewrite.ErrNotRegular) {
		t.Errorf("Read(pipe) error = %v, want ErrNotRegular", err)
	}
}

func TestDir_Remove(t *testing.T) {
	root, d := openDir(t)
	path := filepath.Join(root, "id_rsa")
	writeFile(t, path, "dev-only-fake-key\n", 0o600)
	if err := d.Remove("id_rsa", readSeen(t, d, "id_rsa")); err != nil {
		t.Fatalf("Remove error = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat after Remove error = %v, want fs.ErrNotExist", err)
	}

	writeFile(t, path, "dev-only-fake-key\n", 0o600)
	seen := readSeen(t, d, "id_rsa")
	writeFile(t, path, "dev-only-fake-key-changed\n", 0o600)
	if err := d.Remove("id_rsa", seen); !errors.Is(err, rewrite.ErrChanged) {
		t.Errorf("Remove after a change error = %v, want ErrChanged", err)
	}
	if got := readFile(t, path); got != "dev-only-fake-key-changed\n" {
		t.Errorf("content after refused Remove = %q, want the changed content", got)
	}
}

func TestDir_Create(t *testing.T) {
	root, d := openDir(t)
	if err := d.Create("se-manifest.json", []byte("{}\n"), tempName(t, d, "se-manifest.json")); err != nil {
		t.Fatalf("Create error = %v", err)
	}
	path := filepath.Join(root, "se-manifest.json")
	if got := readFile(t, path); got != "{}\n" {
		t.Errorf("content after Create = %q, want %q", got, "{}\n")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat error = %v", err)
	}
	if info.Mode() != 0o644 {
		t.Errorf("mode after Create = %v, want -rw-r--r--", info.Mode())
	}
	if err := d.Create("se-manifest.json", []byte("[]\n"), tempName(t, d, "se-manifest.json")); err == nil {
		t.Errorf("Create of an existing file error = nil, want an error")
	}
	if got := readFile(t, path); got != "{}\n" {
		t.Errorf("content after refused Create = %q, want it unchanged", got)
	}
	if got := names(t, root); !slices.Equal(got, []string{"se-manifest.json"}) {
		t.Errorf("folder after Create = %v, want only se-manifest.json", got)
	}
}
