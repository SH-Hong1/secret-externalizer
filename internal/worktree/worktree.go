// Package worktree inspects a project's git work tree with the git
// executable without trusting the repository it inspects.
//
// A scanned repository's configuration is untrusted input: it can name
// programs for git to run (fsmonitor, hooks, a pager, filters, a promisor
// remote's transport). Every git call here runs with options and an
// environment that turn those off, uses only commands that read, and takes
// no optional locks, so inspecting a repository neither runs a program the
// repository names nor writes to it. A clean or process filter cannot be
// turned off from the command line, so a repository that configures one is
// refused before git compares any file contents.
package worktree

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	// ErrNotRepository means the folder is not in a git work tree whose
	// work tree root, git folder, and common git folder all lie under the
	// workspace root.
	ErrNotRepository = errors.New("folder is not in a git work tree under the workspace root")
	// ErrNoCommit means the repository holding the folder has no commit.
	ErrNoCommit = errors.New("repository has no commit")
	// ErrUnsafeConfig means the repository configures something that git
	// would run while comparing file contents (a filter or a hook), or moves
	// its work tree.
	ErrUnsafeConfig = errors.New("repository configuration could run a command")
	// ErrUnsafePath means git reported a tracked path that is not inside
	// the folder or that has a .git component. git never writes such a path
	// to an index itself; a hand-made index can hold one.
	ErrUnsafePath = errors.New("repository index holds a path outside the folder")
)

const (
	// callTimeout bounds one git call.
	callTimeout = 60 * time.Second
	// waitDelay bounds the wait for git's output to close once it has exited.
	waitDelay = time.Second
	// maxOutput is the most standard output one git call may write.
	maxOutput = 64 << 20
)

// baseArgs come before the folder and command of every git call. They turn
// off what a repository's configuration could make git run or write:
// fsmonitor, hooks, the pager, the untracked cache, a global attributes
// file, submodule recursion, and every transport protocol. checkStat and
// trustctime make a work tree mounted into a container, whose inode, ctime
// and owner differ from the host's, compare by size and modification time.
var baseArgs = []string{
	"--no-pager", "--no-optional-locks",
	"-c", "safe.directory=*",
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.untrackedCache=false",
	"-c", "core.checkStat=minimal",
	"-c", "core.trustctime=false",
	"-c", "core.attributesFile=/dev/null",
	"-c", "core.quotePath=false",
	"-c", "submodule.recurse=false",
	"-c", "protocol.allow=never",
}

// environ returns the whole environment of a git call for the workspace
// root root. Nothing of the caller's environment is passed on. The ceiling
// stops git from finding a repository above root. GIT_NO_LAZY_FETCH stops
// a partial clone from fetching a missing object, and GIT_ALLOW_PROTOCOL
// allows no transport even where the repository's own protocol.<name>.allow
// would override protocol.allow.
func environ(root string) []string {
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(root),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_ALLOW_PROTOCOL=none",
		"GIT_LITERAL_PATHSPECS=1",
		"HOME=/nonexistent",
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
	}
}

// State is what git knows about the files under one folder of a work tree.
// Paths are relative to that folder, with forward slashes.
type State struct {
	// Dirty holds every tracked file under the folder that has a staged or
	// unstaged change or is marked assume-unchanged or skip-worktree, in
	// path order. It is not cut short: callers subtract from it.
	Dirty []string

	// tracked holds every tracked file under the folder, in path order.
	tracked []string
}

// Tracked reports whether git tracks the file rel.
func (s State) Tracked(rel string) bool {
	_, ok := slices.BinarySearch(s.tracked, rel)
	return ok
}

// exitError is a git call that exited with a non-zero code. It names only
// the command and the code: git's messages can quote file contents.
type exitError struct {
	command string
	code    int
}

func (e *exitError) Error() string {
	return fmt.Sprintf("git %s exited with code %d", e.command, e.code)
}

// limitedBuffer keeps up to limit bytes and calls stop when more arrive.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	stop     func()
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.exceeded || b.buf.Len()+len(p) > b.limit {
		b.exceeded = true
		b.stop()
		return 0, errors.New("output too large")
	}
	return b.buf.Write(p)
}

// run runs git with baseArgs, then -C dir, then args, and returns its
// standard output. Standard error is discarded. An error says which command
// failed and how, and never holds git's output.
func run(ctx context.Context, git, root, dir string, args ...string) ([]byte, error) {
	name := args[0]
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	full := make([]string, 0, len(baseArgs)+2+len(args))
	full = append(full, baseArgs...)
	full = append(full, "-C", dir)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, git, full...)
	cmd.Env = environ(root)
	cmd.WaitDelay = waitDelay
	out := &limitedBuffer{limit: maxOutput, stop: cancel}
	cmd.Stdout = out
	cmd.Stderr = nil // the null device

	err := cmd.Run()
	if out.exceeded {
		return nil, fmt.Errorf("running git %s: output larger than %d bytes", name, maxOutput)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("running git %s: %w", name, ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, &exitError{command: name, code: exitErr.ExitCode()}
	}
	if err != nil {
		return nil, fmt.Errorf("running git %s: %w", name, err)
	}
	return out.buf.Bytes(), nil
}

// repository is where the work tree holding a folder lives.
type repository struct {
	// prefix is the folder relative to the work tree root, with forward
	// slashes and a trailing slash, or empty at the root.
	prefix string
	// head is the commit HEAD points to.
	head string
}

// locate finds the work tree holding dir, checks that its work tree root,
// git folder and common git folder resolve to places under root, and reads
// HEAD. root is already resolved.
func locate(ctx context.Context, git, root, dir string) (repository, error) {
	out, err := run(ctx, git, root, dir, "rev-parse",
		"--is-inside-work-tree", "--show-prefix", "--absolute-git-dir", "--git-common-dir", "--show-toplevel")
	var exitErr *exitError
	if errors.As(err, &exitErr) {
		return repository{}, fmt.Errorf("%w: %w", ErrNotRepository, err)
	}
	if err != nil {
		return repository{}, err
	}
	// A path holding a line break makes more lines than expected; such a
	// repository is refused rather than misread.
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 5 || lines[0] != "true" {
		return repository{}, fmt.Errorf("reading git rev-parse: %w", ErrNotRepository)
	}
	commonDir := lines[3]
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	for _, p := range []string{lines[2], commonDir, lines[4]} {
		inside, err := under(root, p)
		if err != nil {
			return repository{}, err
		}
		if !inside {
			return repository{}, ErrNotRepository
		}
	}

	out, err = run(ctx, git, root, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if errors.As(err, &exitErr) && exitErr.code == 1 {
		return repository{}, ErrNoCommit
	}
	if err != nil {
		return repository{}, err
	}
	head := string(bytes.TrimSuffix(out, []byte("\n")))
	if !isHash(head) {
		return repository{}, fmt.Errorf("reading HEAD: %w", ErrNoCommit)
	}
	return repository{prefix: lines[1], head: head}, nil
}

// under reports whether p, once its symbolic links are resolved, is root or
// lies below it. root is already resolved.
func under(root, p string) (bool, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolving a repository path: %w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return false, nil //nolint:nilerr // a path that cannot be made relative to root is not under it
	}
	return filepath.IsLocal(rel) || rel == ".", nil
}

// isHash reports whether s is a full lower-case SHA-1 or SHA-256 object name.
func isHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return false
	}
	return strings.ToLower(s) == s
}

// checkConfig refuses a repository whose configuration, with included
// files and worktree configuration, sets a clean or process filter, any
// hook.* key, or core.worktree. core.hooksPath turns off only the hooks
// folder: a hook defined in configuration (hook.<name>.command with
// hook.<name>.event) still runs, and only --no-optional-locks keeps status
// from writing the index that would fire it. Git lower-cases section and
// key names in the listing; a subsection name keeps its case and may hold
// dots.
func checkConfig(ctx context.Context, git, root, dir string) error {
	out, err := run(ctx, git, root, dir, "config", "--name-only", "--list", "-z")
	if err != nil {
		return err
	}
	for name := range strings.SplitSeq(string(out), "\x00") {
		key := strings.ToLower(name)
		if key == "core.worktree" || strings.HasPrefix(key, "hook.") ||
			(strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".process"))) {
			return ErrUnsafeConfig
		}
	}
	return nil
}

// Inspect runs git, the path of the executable, on dir, a resolved folder
// under root. It changes nothing in the repository.
//
// It fails with ErrNotRepository when dir is not in a work tree whose work
// tree root, git folder and common git folder all resolve to places under
// root, with ErrNoCommit when the repository has no commit, and with
// ErrUnsafeConfig when its configuration sets a clean or process filter, a
// hook, or core.worktree, and with ErrUnsafePath when git reports a tracked
// path that is not inside dir or has a .git component. Other errors name the
// git command that failed and its exit code, and hold none of git's output.
func Inspect(ctx context.Context, git, root, dir string) (State, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return State{}, fmt.Errorf("resolving the workspace root: %w", err)
	}
	repo, err := locate(ctx, git, root, dir)
	if err != nil {
		return State{}, err
	}
	if err := checkConfig(ctx, git, root, dir); err != nil {
		return State{}, err
	}

	dirty := map[string]bool{}
	out, err := run(ctx, git, root, dir, "status", "--porcelain=v1", "-z",
		"--untracked-files=no", "--ignore-submodules=all", "--no-renames", "--", ".")
	if err != nil {
		return State{}, err
	}
	for entry := range strings.SplitSeq(string(out), "\x00") {
		if entry == "" {
			continue
		}
		// An entry is "XY path", path relative to the work tree root.
		if len(entry) < 4 || entry[2] != ' ' || !strings.HasPrefix(entry[3:], repo.prefix) {
			return State{}, errors.New("reading git status: unexpected entry")
		}
		rel := entry[3+len(repo.prefix):]
		if !confined(rel) {
			return State{}, ErrUnsafePath
		}
		dirty[rel] = true
	}

	out, err = run(ctx, git, root, dir, "ls-files", "-z", "-v", "-t", "--", ".")
	if err != nil {
		return State{}, err
	}
	var tracked []string
	for entry := range strings.SplitSeq(string(out), "\x00") {
		if entry == "" {
			continue
		}
		// An entry is "T path", path relative to dir. T is H for an
		// ordinary file, S for skip-worktree, M for unmerged, and lower
		// case for assume-unchanged.
		if len(entry) < 3 || entry[1] != ' ' {
			return State{}, errors.New("reading git ls-files: unexpected entry")
		}
		rel := entry[2:]
		if !confined(rel) {
			return State{}, ErrUnsafePath
		}
		tracked = append(tracked, rel)
		if entry[0] != 'H' {
			dirty[rel] = true
		}
	}
	slices.Sort(tracked)

	st := State{Dirty: make([]string, 0, len(dirty)), tracked: slices.Compact(tracked)}
	for rel := range dirty {
		st.Dirty = append(st.Dirty, rel)
	}
	slices.Sort(st.Dirty)
	return st, nil
}

// confined reports whether rel, a slash-separated path from git, stays
// inside the folder it is relative to and has no .git component in any
// case, as git itself would refuse to check out.
func confined(rel string) bool {
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return false
	}
	for part := range strings.SplitSeq(rel, "/") {
		if strings.EqualFold(part, ".git") || part == ".." {
			return false
		}
	}
	return true
}

// Nested reports whether rel, a file under dir, lies in a repository of
// its own: some folder between dir and the file holds a .git entry. The .git
// entry of dir itself does not count. rel uses forward slashes and must not
// leave dir.
func Nested(dir, rel string) (bool, error) {
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return false, fmt.Errorf("checking for a nested repository: path %q is not under the folder", rel)
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))), "/")
	folder := dir
	for _, part := range parts[:len(parts)-1] {
		folder = filepath.Join(folder, part)
		_, err := os.Lstat(filepath.Join(folder, ".git"))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("checking for a nested repository: %w", err)
		}
	}
	return false, nil
}

// CommitHash returns the commit HEAD points to in the repository holding
// dir, or "" when there is none or git fails. git is the path of the
// executable, and empty means no hash is read. The repository must lie
// under root as Inspect requires; its configuration is not checked, because
// reading HEAD compares no file contents.
func CommitHash(ctx context.Context, git, root, dir string) string {
	if git == "" {
		return ""
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ""
	}
	repo, err := locate(ctx, git, root, dir)
	if err != nil {
		return ""
	}
	return repo.head
}
