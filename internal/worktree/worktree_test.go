package worktree_test

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // G505: the git index checksum is SHA-1 by format
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/worktree"
	"github.com/SH-Hong1/secret-externalizer/internal/worktree/worktreetest"
)

// gitPath returns the git executable, or skips the test when there is none.
func gitPath(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	return git
}

// workspace returns a resolved temporary folder and the workspace root
// folder inside it. The folder around the root stands for what lies outside
// the workspace.
func workspace(t *testing.T) (outer, root string) {
	t.Helper()
	outer, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temporary folder: %v", err)
	}
	root = filepath.Join(outer, "root")
	mkdir(t, root)
	return outer, root
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("making %s: %v", dir, err)
	}
}

// write writes files, given by slash-separated path relative to dir.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		mkdir(t, filepath.Dir(p))
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}
}

// repo makes a committed repository at root/name holding files.
func repo(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	mkdir(t, dir)
	write(t, dir, files)
	worktreetest.Init(t, dir)
	return dir
}

// stale moves the modification time of every file in dir forward, so the
// index no longer matches its stat data and git has to compare contents.
func stale(t *testing.T, dir string, names ...string) {
	t.Helper()
	later := time.Now().Add(time.Hour)
	for _, name := range names {
		if err := os.Chtimes(filepath.Join(dir, filepath.FromSlash(name)), later, later); err != nil {
			t.Fatalf("touching %s: %v", name, err)
		}
	}
}

// markerScript writes an executable script that appends to marker when it
// runs, and returns its path.
func markerScript(t *testing.T, dir, marker string) string {
	t.Helper()
	p := filepath.Join(dir, "marker.sh")
	body := "#!/bin/sh\necho ran >> '" + marker + "'\nexit 0\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // G306: the test script must be executable
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

func checkNoMarker(t *testing.T, what, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s ran a command the repository configures (marker stat error = %v)", what, err)
	}
}

func inspect(t *testing.T, root, dir string) worktree.State {
	t.Helper()
	st, err := worktree.Inspect(t.Context(), gitPath(t), root, dir)
	if err != nil {
		t.Fatalf("Inspect(%s) error = %v", dir, err)
	}
	return st
}

func TestInspect_Clean(t *testing.T) {
	_, root := workspace(t)
	dir := repo(t, root, "p", map[string]string{"a.txt": "a\n", "conf/b.yml": "b: 1\n"})
	stale(t, dir, "a.txt", "conf/b.yml")
	st := inspect(t, root, dir)
	if len(st.Dirty) != 0 {
		t.Errorf("Inspect(clean).Dirty = %q, want none", st.Dirty)
	}
	for _, name := range []string{"a.txt", "conf/b.yml"} {
		if !st.Tracked(name) {
			t.Errorf("Inspect(clean).Tracked(%q) = false, want true", name)
		}
	}
	if st.Tracked("missing.txt") {
		t.Errorf("Inspect(clean).Tracked(%q) = true, want false", "missing.txt")
	}
}

func TestInspect_Dirty(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, dir string)
	}{
		{"modified", func(t *testing.T, dir string) {
			write(t, dir, map[string]string{"conf/a.yml": "changed\n"})
		}},
		{"staged", func(t *testing.T, dir string) {
			write(t, dir, map[string]string{"conf/a.yml": "changed\n"})
			worktreetest.Git(t, dir, "add", "conf/a.yml")
		}},
		{"deleted", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "conf", "a.yml")); err != nil {
				t.Fatalf("removing: %v", err)
			}
		}},
		{"mode only", func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, "conf", "a.yml"), 0o755); err != nil { //nolint:gosec // G302: the test changes the mode on purpose
				t.Fatalf("chmod: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, root := workspace(t)
			dir := repo(t, root, "p", map[string]string{"conf/a.yml": "a\n", "b.txt": "b\n"})
			tt.change(t, dir)
			st := inspect(t, root, dir)
			if diff := cmp.Diff([]string{"conf/a.yml"}, st.Dirty); diff != "" {
				t.Errorf("Inspect(%s).Dirty mismatch (-want +got):\n%s", tt.name, diff)
			}
		})
	}

	t.Run("sixty files", func(t *testing.T) {
		_, root := workspace(t)
		files := map[string]string{}
		var want []string
		for i := range 60 {
			name := fmt.Sprintf("f%02d.txt", i)
			files[name] = "x\n"
			want = append(want, name)
		}
		dir := repo(t, root, "p", files)
		for name := range files {
			files[name] = "y\n"
		}
		write(t, dir, files)
		st := inspect(t, root, dir)
		if diff := cmp.Diff(want, st.Dirty); diff != "" {
			t.Errorf("Inspect(60 changed).Dirty mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestInspect_HiddenChanges(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		for _, edited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s edited=%v", flag, edited), func(t *testing.T) {
				_, root := workspace(t)
				dir := repo(t, root, "p", map[string]string{"a.yml": "a\n", "b.yml": "b\n"})
				worktreetest.Git(t, dir, "update-index", flag, "a.yml")
				if edited {
					write(t, dir, map[string]string{"a.yml": "changed\n"})
				}
				st := inspect(t, root, dir)
				if diff := cmp.Diff([]string{"a.yml"}, st.Dirty); diff != "" {
					t.Errorf("Inspect(%s, edited=%v).Dirty mismatch (-want +got):\n%s", flag, edited, diff)
				}
			})
		}
	}
}

func TestInspect_UntrackedIsNotDirty(t *testing.T) {
	_, root := workspace(t)
	dir := repo(t, root, "p", map[string]string{".gitignore": "*.log\n", "a.txt": "a\n"})
	write(t, dir, map[string]string{"new.txt": "n\n", "app.log": "l\n"})
	st := inspect(t, root, dir)
	if len(st.Dirty) != 0 {
		t.Errorf("Inspect(untracked only).Dirty = %q, want none", st.Dirty)
	}
	for _, name := range []string{"new.txt", "app.log"} {
		if st.Tracked(name) {
			t.Errorf("Inspect(untracked only).Tracked(%q) = true, want false", name)
		}
	}
}

func TestInspect_Subfolder(t *testing.T) {
	_, root := workspace(t)
	top := repo(t, root, "r", map[string]string{"outside.txt": "o\n", "app/conf/a.yml": "a\n", "app/b.txt": "b\n"})
	write(t, top, map[string]string{"outside.txt": "changed\n", "app/conf/a.yml": "changed\n"})
	st := inspect(t, root, filepath.Join(top, "app"))
	if diff := cmp.Diff([]string{"conf/a.yml"}, st.Dirty); diff != "" {
		t.Errorf("Inspect(subfolder).Dirty mismatch (-want +got):\n%s", diff)
	}
	if !st.Tracked("b.txt") {
		t.Errorf("Inspect(subfolder).Tracked(%q) = false, want true", "b.txt")
	}
	if st.Tracked("outside.txt") || st.Tracked("app/b.txt") {
		t.Errorf("Inspect(subfolder).Tracked reports a path not relative to the folder")
	}
}

func TestInspect_PathNames(t *testing.T) {
	names := []string{"with space.txt", "한글 설정.yml", "line\nbreak.txt", "-leading.txt", "conf/-x y.yml"}
	_, root := workspace(t)
	files := map[string]string{"plain.txt": "p\n"}
	for _, n := range names {
		files[n] = "a\n"
	}
	dir := repo(t, root, "p", files)
	for _, n := range names {
		files[n] = "changed\n"
	}
	write(t, dir, files)
	st := inspect(t, root, dir)
	want := []string{"-leading.txt", "conf/-x y.yml", "line\nbreak.txt", "with space.txt", "한글 설정.yml"}
	if diff := cmp.Diff(want, st.Dirty); diff != "" {
		t.Errorf("Inspect(odd names).Dirty mismatch (-want +got):\n%s", diff)
	}
	for _, n := range append(names, "plain.txt") {
		if !st.Tracked(n) {
			t.Errorf("Inspect(odd names).Tracked(%q) = false, want true", n)
		}
	}
}

func TestInspect_AutoCRLF(t *testing.T) {
	_, root := workspace(t)
	dir := filepath.Join(root, "p")
	mkdir(t, dir)
	worktreetest.Git(t, dir, "init", "-q")
	worktreetest.Git(t, dir, "config", "core.autocrlf", "true")
	write(t, dir, map[string]string{"w.properties": "a=1\r\nb=2\r\n"})
	worktreetest.Init(t, dir)
	stale(t, dir, "w.properties")
	st := inspect(t, root, dir)
	if len(st.Dirty) != 0 {
		t.Errorf("Inspect(autocrlf, CRLF file).Dirty = %q, want none", st.Dirty)
	}
}

func TestInspect_NotRepository(t *testing.T) {
	git := gitPath(t)
	outer, root := workspace(t)

	plain := filepath.Join(root, "plain")
	mkdir(t, plain)
	if _, err := worktree.Inspect(t.Context(), git, root, plain); !errors.Is(err, worktree.ErrNotRepository) {
		t.Errorf("Inspect(plain folder) error = %v, want ErrNotRepository", err)
	}

	// The repository holds the root's parent; git must not look above root.
	worktreetest.Init(t, outer)
	inner := filepath.Join(root, "inner")
	mkdir(t, inner)
	if _, err := worktree.Inspect(t.Context(), git, root, inner); !errors.Is(err, worktree.ErrNotRepository) {
		t.Errorf("Inspect(repository above root) error = %v, want ErrNotRepository", err)
	}

	empty := filepath.Join(root, "empty")
	mkdir(t, empty)
	worktreetest.Git(t, empty, "init", "-q")
	if _, err := worktree.Inspect(t.Context(), git, root, empty); !errors.Is(err, worktree.ErrNoCommit) {
		t.Errorf("Inspect(no commit) error = %v, want ErrNoCommit", err)
	}
}

func TestInspect_OutsideRoot(t *testing.T) {
	git := gitPath(t)

	t.Run("git file pointing outside", func(t *testing.T) {
		outer, root := workspace(t)
		dir := filepath.Join(root, "p")
		mkdir(t, dir)
		write(t, dir, map[string]string{"a.txt": "a\n"})
		worktreetest.Git(t, dir, "init", "-q", "--separate-git-dir", filepath.Join(outer, "outside.git"))
		worktreetest.Init(t, dir)
		if _, err := worktree.Inspect(t.Context(), git, root, dir); !errors.Is(err, worktree.ErrNotRepository) {
			t.Errorf("Inspect(.git file to outside) error = %v, want ErrNotRepository", err)
		}
	})

	t.Run("linked worktree of an outside repository", func(t *testing.T) {
		outer, root := workspace(t)
		main := repo(t, outer, "main", map[string]string{"a.txt": "a\n"})
		wt := filepath.Join(root, "wt")
		worktreetest.Git(t, main, "worktree", "add", "-q", wt)
		if _, err := worktree.Inspect(t.Context(), git, root, wt); !errors.Is(err, worktree.ErrNotRepository) {
			t.Errorf("Inspect(linked worktree, common dir outside) error = %v, want ErrNotRepository", err)
		}
	})

	t.Run("work tree root above root", func(t *testing.T) {
		outer, root := workspace(t)
		dir := filepath.Join(root, "p")
		mkdir(t, dir)
		write(t, dir, map[string]string{"a.txt": "a\n"})
		worktreetest.Init(t, dir)
		worktreetest.Git(t, dir, "config", "core.worktree", outer)
		if _, err := worktree.Inspect(t.Context(), git, root, dir); !errors.Is(err, worktree.ErrNotRepository) {
			t.Errorf("Inspect(work tree above root) error = %v, want ErrNotRepository", err)
		}
	})

	t.Run("linked worktree inside root", func(t *testing.T) {
		_, root := workspace(t)
		main := repo(t, root, "main", map[string]string{"a.txt": "a\n"})
		wt := filepath.Join(root, "wt")
		worktreetest.Git(t, main, "worktree", "add", "-q", wt)
		st := inspect(t, root, wt)
		if len(st.Dirty) != 0 || !st.Tracked("a.txt") {
			t.Errorf("Inspect(linked worktree inside root) = %+v, want clean with a.txt tracked", st)
		}
	})
}

// hostile is a repository whose configuration runs marker.sh if git obeys it.
type hostile struct {
	name string
	// setup configures dir, a committed repository, to run script.
	setup func(t *testing.T, dir, script string)
	// want is what Inspect must return.
	want expect
}

// expect is the outcome of Inspect on a hostile repository.
type expect int

const (
	// expectClean means no error and no dirty file.
	expectClean expect = iota + 1
	// expectCleanOrError means either expectClean or an error: a repository
	// with a missing object cannot be compared, and git must fail rather
	// than fetch it.
	expectCleanOrError
	// expectUnsafe means ErrUnsafeConfig.
	expectUnsafe
)

func hostileRepos() []hostile {
	attributes := func(t *testing.T, dir, filter string) {
		write(t, dir, map[string]string{".gitattributes": "* filter=" + filter + "\n"})
		worktreetest.Git(t, dir, "add", ".gitattributes")
		worktreetest.Git(t, dir, "commit", "-q", "-m", "attributes")
	}
	return []hostile{
		{"fsmonitor", func(t *testing.T, dir, script string) {
			worktreetest.Git(t, dir, "config", "core.fsmonitor", script)
		}, expectClean},
		{"post-index-change hook", func(t *testing.T, dir, script string) {
			hooks := filepath.Join(filepath.Dir(script), "hooks")
			mkdir(t, hooks)
			body, err := os.ReadFile(script) //nolint:gosec // G304: the path is a temporary file of the test
			if err != nil {
				t.Fatalf("reading script: %v", err)
			}
			if err := os.WriteFile(filepath.Join(hooks, "post-index-change"), body, 0o755); err != nil { //nolint:gosec // G306: a hook must be executable
				t.Fatalf("writing hook: %v", err)
			}
			worktreetest.Git(t, dir, "config", "core.hooksPath", hooks)
		}, expectClean},
		{"post-index-change hook from config", func(t *testing.T, dir, script string) {
			worktreetest.Git(t, dir, "config", "hook.evil.command", script)
			worktreetest.Git(t, dir, "config", "hook.evil.event", "post-index-change")
		}, expectUnsafe},
		// git starts a pager only when standard output is a terminal, and
		// Inspect reads it through a pipe, so this case cannot fail even
		// without --no-pager. It stays to show the configuration is harmless.
		{"pager", func(t *testing.T, dir, script string) {
			worktreetest.Git(t, dir, "config", "core.pager", script)
			worktreetest.Git(t, dir, "config", "pager.status", "true")
		}, expectClean},
		{"alias", func(t *testing.T, dir, script string) {
			worktreetest.Git(t, dir, "config", "alias.status", "!"+script)
			worktreetest.Git(t, dir, "config", "alias.ls-files", "!"+script)
		}, expectClean},
		{"promisor remote", func(t *testing.T, dir, script string) {
			worktreetest.Git(t, dir, "config", "core.repositoryformatversion", "1")
			worktreetest.Git(t, dir, "config", "extensions.partialClone", "origin")
			worktreetest.Git(t, dir, "config", "remote.origin.promisor", "true")
			worktreetest.Git(t, dir, "config", "remote.origin.url", "ext::"+script+" %S")
			worktreetest.Git(t, dir, "config", "protocol.ext.allow", "always")
			tree := strings.TrimSpace(worktreetest.Git(t, dir, "rev-parse", "HEAD^{tree}"))
			if err := os.Remove(filepath.Join(dir, ".git", "objects", tree[:2], tree[2:])); err != nil {
				t.Fatalf("removing the tree object: %v", err)
			}
		}, expectCleanOrError},
		{"filter with .gitattributes", func(t *testing.T, dir, script string) {
			attributes(t, dir, "x")
			worktreetest.Git(t, dir, "config", "filter.x.clean", script)
		}, expectUnsafe},
		{"filter process with .gitattributes", func(t *testing.T, dir, script string) {
			attributes(t, dir, "x")
			worktreetest.Git(t, dir, "config", "filter.x.process", script)
		}, expectUnsafe},
		{"filter with info/attributes", func(t *testing.T, dir, script string) {
			write(t, dir, map[string]string{".git/info/attributes": "* filter=x\n"})
			worktreetest.Git(t, dir, "config", "filter.x.clean", script)
		}, expectUnsafe},
		{"filter from include.path", func(t *testing.T, dir, script string) {
			attributes(t, dir, "x")
			write(t, dir, map[string]string{".git/extra.conf": "[filter \"x\"]\n\tClean = " + script + "\n"})
			worktreetest.Git(t, dir, "config", "include.path", "extra.conf")
		}, expectUnsafe},
		{"filter from includeIf", func(t *testing.T, dir, script string) {
			attributes(t, dir, "x")
			write(t, dir, map[string]string{".git/extra.conf": "[filter \"x\"]\n\tclean = " + script + "\n"})
			worktreetest.Git(t, dir, "config", "includeIf.gitdir:"+dir+"/.path", "extra.conf")
		}, expectUnsafe},
		{"filter in config.worktree", func(t *testing.T, dir, script string) {
			attributes(t, dir, "x")
			worktreetest.Git(t, dir, "config", "core.repositoryformatversion", "1")
			worktreetest.Git(t, dir, "config", "extensions.worktreeConfig", "true")
			worktreetest.Git(t, dir, "config", "--worktree", "filter.x.clean", script)
		}, expectUnsafe},
		{"filter name with a dot", func(t *testing.T, dir, script string) {
			attributes(t, dir, "a.b")
			worktreetest.Git(t, dir, "config", "filter.a.b.clean", script)
		}, expectUnsafe},
		{"core.worktree", func(t *testing.T, dir, _ string) {
			worktreetest.Git(t, dir, "config", "core.worktree", dir)
		}, expectUnsafe},
	}
}

// setupHostile makes a committed repository under root configured by h, with
// every tracked file's stat data stale, and returns it and the marker path.
func setupHostile(t *testing.T, h hostile) (root, dir, marker string) {
	t.Helper()
	outer, root := workspace(t)
	marker = filepath.Join(outer, "marker")
	script := markerScript(t, outer, marker)
	dir = repo(t, root, "p", map[string]string{"a.txt": "a\n"})
	h.setup(t, dir, script)
	stale(t, dir, "a.txt")
	return root, dir, marker
}

func TestInspect_HostileConfig(t *testing.T) {
	git := gitPath(t)
	for _, h := range hostileRepos() {
		t.Run(h.name, func(t *testing.T) {
			root, dir, marker := setupHostile(t, h)
			st, err := worktree.Inspect(t.Context(), git, root, dir)
			switch {
			case h.want == expectUnsafe:
				if !errors.Is(err, worktree.ErrUnsafeConfig) {
					t.Errorf("Inspect(%s) error = %v, want ErrUnsafeConfig", h.name, err)
				}
			case err != nil:
				if h.want != expectCleanOrError {
					t.Errorf("Inspect(%s) error = %v, want nil", h.name, err)
				}
			case len(st.Dirty) != 0:
				t.Errorf("Inspect(%s).Dirty = %q, want none", h.name, st.Dirty)
			}
			checkNoMarker(t, "Inspect("+h.name+")", marker)
		})
	}
}

// TestInspect_HostileConfigIsReal shows that the hostile repositories of
// TestInspect_HostileConfig do run their command under a plain git status,
// so that test cannot pass by accident.
func TestInspect_HostileConfigIsReal(t *testing.T) {
	git := gitPath(t)
	for _, h := range hostileRepos() {
		switch h.name {
		case "pager", "alias", "core.worktree":
			// A pager needs a terminal, git ignores an alias of a built-in
			// command, and core.worktree runs nothing.
			continue
		}
		t.Run(h.name, func(t *testing.T) {
			_, dir, marker := setupHostile(t, h)
			cmd := exec.CommandContext(t.Context(), git, "-c", "safe.directory=*", "-C", dir, "status")
			cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=/nonexistent", "PATH=/usr/bin:/bin"}
			_ = cmd.Run() // only whether the command ran matters
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("plain git status on %s did not run the command (marker stat error = %v)", h.name, err)
			}
		})
	}
}

// treeDigest returns the name, size and content hash of every file under dir.
func treeDigest(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p) //nolint:gosec // G304: the path is a temporary file of the test
		if err != nil {
			return err
		}
		files[p] = fmt.Sprintf("%d %x", len(body), sha256.Sum256(body))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return files
}

// writeIndex replaces the index of the repository at dir with a version 2
// index holding names, each a regular file whose content is the SHA-1 blob
// blob of size bytes. git itself never writes such names; a hostile
// repository can.
func writeIndex(t *testing.T, dir, blob string, size uint32, names []string) {
	t.Helper()
	oid, err := hex.DecodeString(blob)
	if err != nil || len(oid) != sha1.Size {
		t.Fatalf("blob %q is not a SHA-1 object name", blob)
	}
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, []uint32{2, uint32(len(sorted))}) //nolint:gosec // G115: a test index holds a handful of names; a bytes.Buffer does not fail
	for _, name := range sorted {
		var e bytes.Buffer
		// ctime, mtime (seconds, nanoseconds), dev, ino, mode, uid, gid, size
		_ = binary.Write(&e, binary.BigEndian, []uint32{0, 0, 0, 0, 0, 0, 0o100644, 0, 0, size}) // a bytes.Buffer does not fail
		e.Write(oid)
		_ = binary.Write(&e, binary.BigEndian, uint16(min(len(name), 0xfff))) // a bytes.Buffer does not fail
		e.WriteString(name)
		e.Write(make([]byte, 8-e.Len()%8)) // one to eight NUL bytes
		b.Write(e.Bytes())
	}
	sum := sha1.Sum(b.Bytes()) //nolint:gosec // G401: the git index checksum is SHA-1 by format
	b.Write(sum[:])
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), b.Bytes(), 0o600); err != nil {
		t.Fatalf("writing the index: %v", err)
	}
}

func TestInspect_PathOutsideFolder(t *testing.T) {
	git := gitPath(t)
	tests := []struct {
		name   string
		folder string // the folder to inspect, relative to the repository
		names  []string
		want   error
	}{
		{"crafted index of ordinary names", ".", []string{"a.txt", "sub/b.txt"}, nil},
		{"parent", ".", []string{"a.txt", "../evil"}, worktree.ErrUnsafePath},
		{"git folder", ".", []string{"a.txt", ".git/config"}, worktree.ErrUnsafePath},
		{"git folder in other case", ".", []string{"a.txt", "sub/.GIT/x"}, worktree.ErrUnsafePath},
		{"climbing out", ".", []string{"a.txt", "x/../../y"}, worktree.ErrUnsafePath},
		{"into the git folder", ".", []string{"a.txt", "sub/../.git/hooks/z"}, worktree.ErrUnsafePath},
		{"out of a subfolder", "sub", []string{"sub/b.txt", "sub/../../evil"}, worktree.ErrUnsafePath},
		{"to a sibling of a subfolder", "sub", []string{"sub/b.txt", "sub/../a.txt"}, worktree.ErrUnsafePath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, root := workspace(t)
			dir := repo(t, root, "p", map[string]string{"a.txt": "a\n", "sub/b.txt": "a\n"})
			blob := strings.TrimSpace(worktreetest.Git(t, dir, "rev-parse", "HEAD:a.txt"))
			writeIndex(t, dir, blob, 2, tt.names)
			st, err := worktree.Inspect(t.Context(), git, root, filepath.Join(dir, tt.folder))
			if tt.want == nil {
				if err != nil || len(st.Dirty) != 0 {
					t.Errorf("Inspect(index %q) = %+v, %v, want clean and no error", tt.names, st, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("Inspect(index %q) = %+v, %v, want %v", tt.names, st, err, tt.want)
			}
		})
	}
}

func TestInspect_ChangesNothing(t *testing.T) {
	_, root := workspace(t)
	dir := repo(t, root, "p", map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	stale(t, dir, "a.txt", "b.txt")
	write(t, dir, map[string]string{"b.txt": "changed\n"})
	before := treeDigest(t, filepath.Join(dir, ".git"))
	inspect(t, root, dir)
	if diff := cmp.Diff(before, treeDigest(t, filepath.Join(dir, ".git"))); diff != "" {
		t.Errorf("Inspect changed files under .git (-before +after):\n%s", diff)
	}
}

func TestInspect_Canceled(t *testing.T) {
	_, root := workspace(t)
	dir := repo(t, root, "p", map[string]string{"a.txt": "a\n"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := worktree.Inspect(ctx, gitPath(t), root, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect(canceled) error = %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "fatal") || strings.Contains(err.Error(), "a.txt") {
		t.Errorf("Inspect(canceled) error = %q, want no git output in it", err)
	}
}

func TestNested(t *testing.T) {
	tests := []struct {
		name  string
		setup map[string]string
		mkGit string
		rel   string
		want  bool
	}{
		{"git folder in a subfolder", nil, "sub/.git/HEAD", "sub/a/b.yml", true},
		{"git file in a subfolder", nil, "sub/.git", "sub/a/b.yml", true},
		{"git entry deeper down", nil, "sub/a/.git", "sub/a/b.yml", true},
		{"only the folder's own git", nil, ".git/HEAD", "sub/a/b.yml", false},
		{"file at the top", nil, "sub/.git", "b.yml", false},
		{"sibling repository", nil, "other/.git", "sub/a/b.yml", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, map[string]string{tt.mkGit: "x\n", "sub/a/b.yml": "b\n"})
			got, err := worktree.Nested(dir, tt.rel)
			if err != nil {
				t.Fatalf("Nested(%q) error = %v", tt.rel, err)
			}
			if got != tt.want {
				t.Errorf("Nested(%q) with %s = %v, want %v", tt.rel, tt.mkGit, got, tt.want)
			}
		})
	}
	if _, err := worktree.Nested(t.TempDir(), "../x.yml"); err == nil {
		t.Errorf("Nested(%q) error = nil, want an error", "../x.yml")
	}
}

func TestCommitHash(t *testing.T) {
	git := gitPath(t)
	_, root := workspace(t)
	dir := repo(t, root, "p", map[string]string{"a.txt": "a\n"})
	want := strings.TrimSpace(worktreetest.Git(t, dir, "rev-parse", "HEAD"))
	if !regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`).MatchString(want) {
		t.Fatalf("git rev-parse HEAD = %q, want a hash", want)
	}
	if got := worktree.CommitHash(t.Context(), git, root, dir); got != want {
		t.Errorf("CommitHash(repository) = %q, want %q", got, want)
	}
	if got := worktree.CommitHash(t.Context(), git, root, filepath.Join(dir, ".")); got != want {
		t.Errorf("CommitHash(repository/.) = %q, want %q", got, want)
	}
	plain := filepath.Join(root, "plain")
	mkdir(t, plain)
	if got := worktree.CommitHash(t.Context(), git, root, plain); got != "" {
		t.Errorf("CommitHash(plain folder) = %q, want empty", got)
	}
	if got := worktree.CommitHash(t.Context(), "", root, dir); got != "" {
		t.Errorf("CommitHash(no git) = %q, want empty", got)
	}

	for _, h := range hostileRepos() {
		t.Run(h.name, func(t *testing.T) {
			root, dir, marker := setupHostile(t, h)
			worktree.CommitHash(t.Context(), git, root, dir)
			checkNoMarker(t, "CommitHash("+h.name+")", marker)
		})
	}
}
