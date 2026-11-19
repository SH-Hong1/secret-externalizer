package textfile_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

func mkfile(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", p, err)
	}
}

func resolvedTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks error = %v", err)
	}
	return root
}

func collect(t *testing.T, root string, excludes []string) ([]string, int) {
	t.Helper()
	var got []string
	skipped, err := textfile.Walk(root, excludes, func(rel string, d fs.DirEntry) error {
		if !d.Type().IsRegular() {
			t.Errorf("Walk passed %q with type %v, want a regular file", rel, d.Type())
		}
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk(%q) error = %v", root, err)
	}
	slices.Sort(got)
	return got, skipped
}

func TestWalk_SkipsSymlinksAndExcludes(t *testing.T) {
	root := resolvedTemp(t)
	for _, rel := range []string{
		"App.java",
		"src/main/Config.java",
		"src/build.xml",
		"src/build", // a file named like a default exclude stays
		"src/deep/target",
		".git/config",
		"target/classes/A.class",
		"sub/build/out.txt",
		"web/node_modules/x/index.js",
		"keep/a.txt",
		"keep/gen/b.txt",
		"keep/gen2/c.txt",
		"docs/skipme.md",
	} {
		mkfile(t, root, rel)
	}
	outside := resolvedTemp(t)
	mkfile(t, outside, "secret.txt")
	if err := os.Symlink(filepath.Join(root, "App.java"), filepath.Join(root, "link-to-file")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link-to-dir")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	if err := os.Symlink("missing", filepath.Join(root, "dangling")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}

	got, skipped := collect(t, root, []string{"keep/gen", "docs/skipme.md"})
	want := []string{
		"App.java",
		"keep/a.txt",
		"keep/gen2/c.txt",
		"src/build",
		"src/build.xml",
		"src/deep/target",
		"src/main/Config.java",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Walk files mismatch (-want +got):\n%s", diff)
	}
	if skipped != 0 {
		t.Errorf("Walk skipped = %d, want 0 (excludes and symlinks are not read failures)", skipped)
	}
}

func TestWalk_FnErrorStopsTheWalk(t *testing.T) {
	root := resolvedTemp(t)
	mkfile(t, root, "a.txt")
	mkfile(t, root, "b.txt")
	stop := errors.New("stop")
	calls := 0
	_, err := textfile.Walk(root, nil, func(string, fs.DirEntry) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("Walk with failing fn = %v after %d calls, want the fn error after 1 call", err, calls)
	}
}

func TestWalk_UnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a directory with mode 000")
	}
	root := resolvedTemp(t)
	mkfile(t, root, "ok.txt")
	mkfile(t, root, "locked/inner.txt")
	mkfile(t, root, "z/after.txt")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("Chmod error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(locked, 0o750); err != nil { //nolint:gosec // G302: restores a directory so TempDir can remove it
			t.Errorf("restoring permissions: %v", err)
		}
	})

	got, skipped := collect(t, root, nil)
	if diff := cmp.Diff([]string{"ok.txt", "z/after.txt"}, got); diff != "" {
		t.Errorf("Walk files mismatch (-want +got):\n%s", diff)
	}
	if skipped != 1 {
		t.Errorf("Walk skipped = %d, want 1", skipped)
	}
}

func TestWalk_MissingRootIsAnError(t *testing.T) {
	_, err := textfile.Walk(filepath.Join(resolvedTemp(t), "nope"), nil, func(string, fs.DirEntry) error { return nil })
	if err == nil {
		t.Error("Walk(missing root) error = nil, want an error")
	}
}
