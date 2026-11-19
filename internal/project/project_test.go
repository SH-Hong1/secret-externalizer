package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/project"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"name", "shop", "shop", nil},
		{"nested", "team/shop", "team/shop", nil},
		{"root itself", ".", ".", nil},
		{"trailing slash", "shop/", "shop", nil},
		{"leading dot", "./shop", "shop", nil},
		{"double slash", "team//shop", "team/shop", nil},
		{"empty", "", "", project.ErrInvalidPath},
		{"absolute", "/abs", "", project.ErrInvalidPath},
		{"parent", "..", "", project.ErrInvalidPath},
		{"escapes through parent", "a/../../b", "", project.ErrInvalidPath},
		{"backslash", `a\b`, "", project.ErrInvalidPath},
		{"nul", "a\x00b", "", project.ErrInvalidPath},
		{"too long", strings.Repeat("a", 1025), "", project.ErrInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := project.ParsePath(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParsePath(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParsePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParsePath_LengthLimit(t *testing.T) {
	in := strings.Repeat("a", 1024)
	if got, err := project.ParsePath(in); err != nil || got != in {
		t.Errorf("ParsePath(1024 bytes) = %d bytes, %v, want the same input and no error", len(got), err)
	}
}

// resolveFixture builds a workspace root and a directory outside it.
type resolveFixture struct {
	root    string // resolved path of the workspace root
	outside string
}

func newResolveFixture(t *testing.T) resolveFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(TempDir) error = %v", err)
	}
	f := resolveFixture{root: filepath.Join(base, "root"), outside: filepath.Join(base, "outside")}
	for _, d := range []string{filepath.Join(f.root, "shop"), filepath.Join(f.root, "team", "api"), f.outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.root, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	return f
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(%q, %q) error = %v", target, link, err)
	}
}

func TestResolve(t *testing.T) {
	f := newResolveFixture(t)
	symlink(t, f.outside, filepath.Join(f.root, "escape"))
	symlink(t, filepath.Join(f.root, "shop"), filepath.Join(f.root, "alias"))
	symlink(t, filepath.Join(f.root, "nowhere"), filepath.Join(f.root, "dangling"))

	tests := []struct {
		name    string
		rel     string
		want    string
		wantErr error
	}{
		{"directory", "shop", filepath.Join(f.root, "shop"), nil},
		{"nested directory", "team/api", filepath.Join(f.root, "team", "api"), nil},
		{"root itself", ".", f.root, nil},
		{"symlink outside root", "escape", "", project.ErrOutsideRoot},
		{"symlink inside root", "alias", filepath.Join(f.root, "shop"), nil},
		{"missing", "nope", "", project.ErrNotDirectory},
		{"dangling symlink", "dangling", "", project.ErrNotDirectory},
		{"file", "file.txt", "", project.ErrNotDirectory},
		{"through a file", "file.txt/sub", "", project.ErrNotDirectory},
		{"invalid shape", "../outside", "", project.ErrInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := project.Resolve(f.root, tt.rel)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Resolve(root, %q) error = %v, want %v", tt.rel, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Resolve(root, %q) = %q, want %q", tt.rel, got, tt.want)
			}
		})
	}
}

func TestResolve_ThroughFileMessage(t *testing.T) {
	f := newResolveFixture(t)
	_, err := project.Resolve(f.root, "file.txt/sub")
	if err == nil || !strings.Contains(err.Error(), "goes through a file") {
		t.Errorf("Resolve(root, %q) error = %v, want one saying it goes through a file", "file.txt/sub", err)
	}
}

func TestResolve_RootIsSymlink(t *testing.T) {
	f := newResolveFixture(t)
	link := filepath.Join(filepath.Dir(f.root), "rootlink")
	symlink(t, f.root, link)

	got, err := project.Resolve(link, "shop")
	if err != nil {
		t.Fatalf("Resolve(symlinked root, %q) error = %v", "shop", err)
	}
	if want := filepath.Join(f.root, "shop"); got != want {
		t.Errorf("Resolve(symlinked root, %q) = %q, want %q", "shop", got, want)
	}
}
