package db

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

func TestShellPatterns(t *testing.T) {
	var want string
	for i, ext := range triage.ShellExtensions() {
		if i > 0 {
			want += ","
		}
		want += "%" + ext
	}
	want = "'{" + want + "}'::text[]"
	if diff := cmp.Diff(want, shellPatterns); diff != "" {
		t.Errorf("shellPatterns differs from triage.ShellExtensions() (-want +got):\n%s", diff)
	}
}
