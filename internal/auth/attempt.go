package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"

	"golang.org/x/oauth2"
)

// Attempt is the state of one login in progress, kept between the redirect to
// the identity provider and its callback.
type Attempt struct {
	State    string
	Nonce    string
	Verifier string
	ReturnTo string
}

// NewAttempt creates an Attempt with fresh random values that will send the
// user to returnTo after login.
func NewAttempt(returnTo string) (Attempt, error) {
	state, err := randomToken()
	if err != nil {
		return Attempt{}, fmt.Errorf("generating state: %w", err)
	}
	nonce, err := randomToken()
	if err != nil {
		return Attempt{}, fmt.Errorf("generating nonce: %w", err)
	}
	return Attempt{
		State:    state,
		Nonce:    nonce,
		Verifier: oauth2.GenerateVerifier(),
		ReturnTo: returnTo,
	}, nil
}

// LogValue redacts State, Nonce, and Verifier.
func (a Attempt) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("state", redacted),
		slog.String("nonce", redacted),
		slog.String("verifier", redacted),
		slog.String("return_to", a.ReturnTo),
	)
}

// String returns a form that omits State, Nonce, and Verifier.
func (a Attempt) String() string {
	return "Attempt{return_to: " + a.ReturnTo + ", secrets: " + redacted + "}"
}

// Format prints the form String returns for every fmt verb, including %#v,
// which would otherwise print every field.
func (a Attempt) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(a.String())) // a failed write to a fmt buffer has no one to report to
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
