// Package worktreetest makes throw-away git repositories for tests. It runs
// git with no system or global configuration, so a test does not depend on
// the developer's git settings.
package worktreetest

import (
	"bytes"
	"os/exec"
	"testing"
)

// Init makes dir a git repository holding one commit of everything in it.
// It skips the test when git is not installed.
func Init(t testing.TB, dir string) {
	t.Helper()
	Git(t, dir, "init", "-q")
	Git(t, dir, "config", "user.name", "fake")
	Git(t, dir, "config", "user.email", "fake@example.invalid")
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "--allow-empty", "-m", "fake")
}

// Git runs git with args in dir and returns its standard output. It skips
// the test when git is not installed and fails it when git fails. Hooks and
// fsmonitor are turned off, so a test may set them up in a repository and
// keep using Git on it.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	full := append([]string{
		"-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "safe.directory=*", "-C", dir,
	}, args...)
	cmd := exec.CommandContext(t.Context(), git, full...)
	cmd.Env = []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=/nonexistent",
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %q in %s error = %v: %s", args, dir, err, stderr.Bytes())
	}
	return stdout.String()
}
