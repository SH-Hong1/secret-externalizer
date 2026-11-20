package scan

import (
	"context"

	"github.com/SH-Hong1/secret-externalizer/internal/worktree"
)

// commitHash returns the commit that HEAD points to in the git repository
// holding dir, or "" when git is empty, dir is not in a repository under the
// workspace root, the repository has no commit, or git fails. A missing hash
// does not fail a scan, so the reason is not reported. git runs as
// worktree.CommitHash runs it, without trusting the repository's
// configuration.
func commitHash(ctx context.Context, git, root, dir string) string {
	return worktree.CommitHash(ctx, git, root, dir)
}
