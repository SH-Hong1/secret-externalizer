package auth

import (
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

// ClientSecret is the OIDC client secret. fmt (every verb), slog, and every
// encoder that uses encoding.TextMarshaler, such as encoding/json, print a
// redacted form, also when the secret is an exported field of a value being
// printed. fmt prints unexported struct fields by reflection without calling
// their methods, so a type that keeps a ClientSecret in an unexported field
// must redact itself. Only Reveal returns the value.
type ClientSecret string

// String returns the redacted form.
func (ClientSecret) String() string { return redacted }

// LogValue returns the redacted form for slog.
func (ClientSecret) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format prints the redacted form for every fmt verb, including %#v and %x.
func (ClientSecret) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redacted)) // a failed write to a fmt buffer has no one to report to
}

// MarshalText returns the redacted form, so encoding/json and slog's JSON
// handler never write the value.
func (ClientSecret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Reveal returns the secret value. Call it only where the value is sent to the
// identity provider.
func (s ClientSecret) Reveal() string { return string(s) }
