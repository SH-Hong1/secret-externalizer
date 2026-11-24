package session

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Password is the password of the valkey user that the store signs in as.
// Every way of printing it gives "[redacted]".
type Password string

// String returns "[redacted]".
func (Password) String() string { return redacted }

// LogValue returns "[redacted]".
func (Password) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format prints "[redacted]" for every fmt verb, including %#v.
func (Password) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redacted)) // a failed write to a fmt buffer has no one to report to
}

// MarshalText returns "[redacted]".
func (Password) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// ReadPasswordFile reads a valkey password from the file at path, without
// surrounding white space. A missing file gives an error matching
// fs.ErrNotExist. The errors of os.ReadFile name the path and never the
// content.
func ReadPasswordFile(path string) (Password, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own VALKEY_PASSWORD_FILE setting
	if err != nil {
		return "", fmt.Errorf("reading the password file: %w", err)
	}
	p := Password(strings.TrimSpace(string(b)))
	if p == "" {
		return "", fmt.Errorf("%q is empty", path)
	}
	return p, nil
}
