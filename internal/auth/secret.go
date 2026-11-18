package auth

import (
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

// ClientSecret is the OIDC client secret. Every formatting verb and every
// logger prints a redacted form; only Reveal returns the value.
type ClientSecret string

// String returns the redacted form.
func (ClientSecret) String() string { return redacted }

// LogValue returns the redacted form for slog.
func (ClientSecret) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format prints the redacted form for every fmt verb, including %#v and %x.
func (ClientSecret) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redacted)) // a failed write to a fmt buffer has no one to report to
}

// Reveal returns the secret value. Call it only where the value is sent to the
// identity provider.
func (s ClientSecret) Reveal() string { return string(s) }
