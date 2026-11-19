package scan

import (
	"bytes"
	"context"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"time"
)

// gitTimeout bounds the git command that reads the commit hash.
const gitTimeout = 10 * time.Second

// commitHash returns the commit that HEAD points to in the git repository
// holding dir, or "" when git is empty, dir is not in a repository under the
// workspace root, the repository has no commit, or git fails. A missing hash
// does not fail a scan, so the reason is not reported.
//
// git runs without a shell, with an environment that reads no system or
// global configuration and does not look for a repository above root.
// safe.directory=* lets it read a repository owned by another user, as a
// read-only mount of the workspace is.
func commitHash(ctx context.Context, git, root, dir string) string {
	if git == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "-c", "safe.directory=*", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(root),
		"LC_ALL=C",
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	hash := string(bytes.TrimSpace(out))
	if (len(hash) != 40 && len(hash) != 64) || !isLowerHex(hash) {
		return ""
	}
	return hash
}

// isLowerHex reports whether s is lower-case hexadecimal.
func isLowerHex(s string) bool {
	if _, err := hex.DecodeString(s); err != nil {
		return false
	}
	return bytes.Equal(bytes.ToLower([]byte(s)), []byte(s))
}
