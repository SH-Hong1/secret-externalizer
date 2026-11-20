// Package bao fingerprints values and holds original values in OpenBao.
//
// A fingerprint is the transit HMAC of a value under the key "fingerprint",
// always at key version 1, so the same value gives the same fingerprint in
// every scan and every project. An original value waits in the KV v2 mount
// "secret" at pending/<project ID>/<fingerprint> until it is judged. A value
// taken out of the source by an apply is kept at projects/<project ID>/<key>,
// where every write is a check-and-set and earlier versions stay.
//
// The package wraps the official OpenBao client, and none of its types
// appear in this API. Errors from the official client can carry the response
// body and the request URL, so no error returned here wraps one. An error
// from a request names the operation and the HTTP status, and nothing else;
// an error from New is fixed text, with the server address where it helps.
package bao

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/openbao/openbao/api/auth/approle/v2"
	"github.com/openbao/openbao/api/v2"
)

const (
	redacted = "[redacted]"

	// batchSize is the number of values sent in one HMAC request.
	batchSize = 500

	// maxBatchBytes is the most value bytes sent in one HMAC request. Base64
	// and JSON make the request about 4/3 of it, well under the 32 MiB that
	// OpenBao accepts by default; a single larger value is sent alone.
	maxBatchBytes = 8 << 20

	// hmacPrefix starts every HMAC under key version 1.
	hmacPrefix = "vault:v1:"

	// fingerprintLen is the length of a SHA-256 HMAC in hex digits.
	fingerprintLen = 64
)

// SecretID is an AppRole secret-id. fmt, slog, and encoding print it as
// "[redacted]".
type SecretID string

// String returns "[redacted]".
func (SecretID) String() string { return redacted }

// LogValue returns "[redacted]".
func (SecretID) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format prints "[redacted]" for every fmt verb, including %#v.
func (SecretID) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redacted)) // a failed write to a fmt buffer has no one to report to
}

// MarshalText returns "[redacted]".
func (SecretID) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// ErrNoOriginal means OpenBao holds no pending original at that path.
var ErrNoOriginal = errors.New("no pending original")

// ErrUnreachable means a request got no answer from OpenBao: the connection
// failed or the HTTP client timed out. An error that names a status means
// OpenBao answered and refused or failed the request.
var ErrUnreachable = errors.New("openbao could not be reached")

// ErrForbidden means OpenBao refused a request with status 403 after the
// client logged in again: the role's policy does not allow it.
var ErrForbidden = errors.New("openbao refused the request")

// Value holds an original read from OpenBao. fmt, slog and encoding/json
// print a redacted form. Only Reveal gives the bytes. fmt cannot call the
// methods of a value in an unexported struct field, so a Value is never kept
// in one.
type Value []byte

// Reveal returns a copy of the original bytes.
func (v Value) Reveal() []byte { return slices.Clone(v) }

// String returns "[redacted]".
func (Value) String() string { return redacted }

// GoString returns "[redacted]".
func (Value) GoString() string { return redacted }

// LogValue returns "[redacted]".
func (Value) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format prints "[redacted]" for every fmt verb, including %x and %#v.
func (Value) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redacted)) // a failed write to a fmt buffer has no one to report to
}

// MarshalText returns "[redacted]", which encoding/json also uses.
func (Value) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Credentials identify the AppRole that a Client logs in as.
type Credentials struct {
	RoleID   string
	SecretID SecretID
}

// Client fingerprints values, stores, lists, and deletes pending original
// values, and stores and reads externalized values. It logs in with its
// AppRole on first use and again when OpenBao refuses its token. It is safe
// for concurrent use.
type Client struct {
	api  *api.Client
	auth *approle.AppRoleAuth

	// mu serialises logins and guards loggedIn.
	mu       sync.Mutex
	loggedIn bool
}

// clientText is what fmt and slog print for a Client. The AppRole login
// inside a Client holds the secret-id, and fmt prints nested values by
// reflection for some verbs, so a Client never prints its fields.
const clientText = "bao.Client{" + redacted + "}"

// String returns a fixed text that holds no credentials.
func (c *Client) String() string { return clientText }

// Format prints the form String returns for every fmt verb.
func (c *Client) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(clientText)) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns a fixed text that holds no credentials.
func (c *Client) LogValue() slog.Value { return slog.StringValue(clientText) }

// New returns a Client for the OpenBao server at addr. hc must have a
// timeout; it is copied, not changed. The official client retries a request
// at most twice after a server error. New does not contact the server.
func New(addr string, creds Credentials, hc *http.Client) (*Client, error) {
	if hc == nil || hc.Timeout <= 0 {
		return nil, errors.New("creating openbao client: the HTTP client has no timeout")
	}
	if creds.RoleID == "" || creds.SecretID == "" {
		return nil, errors.New("creating openbao client: the role-id or the secret-id is empty")
	}
	// NewConfig, unlike DefaultConfig, reads no environment variables.
	cfg := api.NewConfig()
	if cfg.Error != nil {
		return nil, errors.New("creating openbao client: preparing the default configuration")
	}
	httpClient := *hc
	// The official client handles redirects itself and expects the HTTP
	// client not to follow them.
	httpClient.CheckRedirect = cfg.HttpClient.CheckRedirect
	cfg.HttpClient = &httpClient
	cfg.Address = addr
	cfg.MaxRetries = 2
	ac, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating openbao client for %q: the official client refused the configuration", addr)
	}
	auth, err := approle.NewAppRoleAuth(creds.RoleID, &approle.SecretID{FromString: string(creds.SecretID)})
	if err != nil {
		// The error describes which secret-id source is missing and holds no
		// value, but it is dropped all the same.
		return nil, errors.New("creating openbao client: preparing the AppRole login")
	}
	return &Client{api: ac, auth: auth}, nil
}

// Fingerprints returns the fingerprint of each value, in the order of
// values, as 64 lower-case hex digits. One request holds at most batchSize
// values and maxBatchBytes value bytes, except that a larger value goes
// alone.
func (c *Client) Fingerprints(ctx context.Context, values [][]byte) ([]string, error) {
	const op = "fingerprinting values"
	fps := make([]string, 0, len(values))
	for start := 0; start < len(values); {
		end := batchEnd(values, start)
		batch := values[start:end]
		inputs := make([]map[string]any, len(batch))
		for i, v := range batch {
			inputs[i] = map[string]any{"input": base64.StdEncoding.EncodeToString(v)}
		}
		body := map[string]any{
			"key_version": 1,
			"algorithm":   "sha2-256",
			"batch_input": inputs,
		}
		secret, err := c.do(ctx, op, func() (*api.Secret, error) {
			return c.api.Logical().WriteWithContext(ctx, "transit/hmac/fingerprint", body)
		})
		if err != nil {
			return nil, err
		}
		got, err := parseHMACs(secret, len(batch))
		if err != nil {
			return nil, fmt.Errorf("%s: values %d to %d: %w", op, start, start+len(batch)-1, err)
		}
		fps = append(fps, got...)
		start = end
	}
	return fps, nil
}

// batchEnd returns the end of the batch that starts at values[start]: it
// takes values while the batch has fewer than batchSize of them and their
// bytes stay within maxBatchBytes, and always takes at least one.
func batchEnd(values [][]byte, start int) int {
	end, size := start+1, len(values[start])
	for end < len(values) && end-start < batchSize && size+len(values[end]) <= maxBatchBytes {
		size += len(values[end])
		end++
	}
	return end
}

// parseHMACs reads n fingerprints from a batch HMAC response. Errors name
// the position of an item and never its content.
func parseHMACs(secret *api.Secret, n int) ([]string, error) {
	if secret == nil {
		return nil, errors.New("empty response")
	}
	results, ok := secret.Data["batch_results"].([]any)
	if !ok || len(results) != n {
		return nil, fmt.Errorf("response holds no batch_results with %d items", n)
	}
	fps := make([]string, n)
	for i, r := range results {
		item, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d is not an object", i)
		}
		if e, _ := item["error"].(string); e != "" { // a missing error field means success
			return nil, fmt.Errorf("item %d was refused", i)
		}
		h, _ := item["hmac"].(string) // a missing hmac fails the prefix check below
		encoded, ok := strings.CutPrefix(h, hmacPrefix)
		if !ok {
			return nil, fmt.Errorf("item %d is not an HMAC under key version 1", i)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw)*2 != fingerprintLen {
			return nil, fmt.Errorf("item %d is not a base64 SHA-256 HMAC", i)
		}
		fps[i] = hex.EncodeToString(raw)
	}
	return fps, nil
}

// PutPending stores value as the pending original for fingerprint in the
// project, replacing any earlier version.
func (c *Client) PutPending(ctx context.Context, projectID int64, fingerprint string, value []byte) error {
	const op = "storing a pending value"
	if err := checkPendingKey(projectID, fingerprint); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	body := map[string]any{"data": map[string]any{"value": base64.StdEncoding.EncodeToString(value)}}
	_, err := c.do(ctx, op, func() (*api.Secret, error) {
		return c.api.Logical().WriteWithContext(ctx, pendingPath("data", projectID, fingerprint), body)
	})
	return err
}

// ReadPending returns the pending original for fingerprint in the project.
// It returns an error matching ErrNoOriginal when OpenBao holds none there,
// including when its latest version was deleted.
func (c *Client) ReadPending(ctx context.Context, projectID int64, fingerprint string) (Value, error) {
	const op = "reading a pending value"
	if err := checkPendingKey(projectID, fingerprint); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	// The official client answers a 404 that carries no data with a nil
	// secret and no error, and one that carries the metadata of a deleted
	// version with that metadata and no data.
	secret, err := c.do(ctx, op, func() (*api.Secret, error) {
		return c.api.Logical().ReadWithContext(ctx, pendingPath("data", projectID, fingerprint))
	})
	if err != nil {
		return nil, err
	}
	if secret == nil {
		return nil, fmt.Errorf("%s: %w", op, ErrNoOriginal)
	}
	data, _ := secret.Data["data"].(map[string]any) // a missing or null data field means no version to read
	if data == nil {
		return nil, fmt.Errorf("%s: %w", op, ErrNoOriginal)
	}
	encoded, ok := data["value"].(string)
	if !ok {
		return nil, fmt.Errorf("%s: response holds no value", op)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// The decoding error names only an offset, but it is dropped all
		// the same.
		return nil, fmt.Errorf("%s: the stored value is not base64", op)
	}
	return Value(raw), nil
}

// ListPending returns the fingerprints that have a pending original in the
// project. It returns an empty list when there are none.
func (c *Client) ListPending(ctx context.Context, projectID int64) ([]string, error) {
	const op = "listing pending values"
	if projectID <= 0 {
		return nil, fmt.Errorf("%s: project ID %d is not positive", op, projectID)
	}
	secret, err := c.do(ctx, op, func() (*api.Secret, error) {
		return c.api.Logical().ListWithContext(ctx, fmt.Sprintf("secret/metadata/pending/%d", projectID))
	})
	if err != nil {
		return nil, err
	}
	fps := []string{}
	if secret == nil {
		return fps, nil
	}
	keys, ok := secret.Data["keys"].([]any)
	if !ok {
		return nil, fmt.Errorf("%s: response holds no list of keys", op)
	}
	for i, k := range keys {
		s, ok := k.(string)
		if !ok {
			return nil, fmt.Errorf("%s: key %d is not a string", op, i)
		}
		fps = append(fps, s)
	}
	return fps, nil
}

// DeletePending deletes every version and the metadata of the pending
// original for fingerprint in the project. Deleting one that does not exist
// is not an error.
func (c *Client) DeletePending(ctx context.Context, projectID int64, fingerprint string) error {
	const op = "deleting a pending value"
	if err := checkPendingKey(projectID, fingerprint); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	_, err := c.do(ctx, op, func() (*api.Secret, error) {
		secret, err := c.api.Logical().DeleteWithContext(ctx, pendingPath("metadata", projectID, fingerprint))
		if statusOf(err) == http.StatusNotFound {
			return nil, nil
		}
		return secret, err
	})
	return err
}

// checkPendingKey reports whether a project ID and a fingerprint can form a
// pending path. A fingerprint is exactly 64 lower-case hex digits, so it can
// never add a path segment.
func checkPendingKey(projectID int64, fingerprint string) error {
	if projectID <= 0 {
		return fmt.Errorf("project ID %d is not positive", projectID)
	}
	if len(fingerprint) != fingerprintLen || strings.Trim(fingerprint, "0123456789abcdef") != "" {
		return errors.New("the fingerprint is not 64 lower-case hex digits")
	}
	return nil
}

// pendingPath returns the KV v2 path of a pending original under kind, which
// is "data" or "metadata".
func pendingPath(kind string, projectID int64, fingerprint string) string {
	return fmt.Sprintf("secret/%s/pending/%d/%s", kind, projectID, fingerprint)
}

// do logs in if the client has not, then calls fn. When OpenBao answers 403,
// the token has expired or been revoked, so do logs in again and calls fn
// once more. Every error it returns is built by describe.
func (c *Client) do(ctx context.Context, op string, fn func() (*api.Secret, error)) (*api.Secret, error) {
	if err := c.login(ctx, false); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	secret, err := fn()
	if statusOf(err) == http.StatusForbidden {
		if err := c.login(ctx, true); err != nil {
			return nil, fmt.Errorf("%s: %w", op, err)
		}
		secret, err = fn()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, describe(ctx, err))
	}
	return secret, nil
}

// login logs in with the AppRole unless the client already has a token and
// again is false. The official client keeps the new token.
func (c *Client) login(ctx context.Context, again bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedIn && !again {
		return nil
	}
	c.loggedIn = false
	if _, err := c.api.Auth().Login(ctx, c.auth); err != nil {
		return fmt.Errorf("logging in: %w", describe(ctx, err))
	}
	c.loggedIn = true
	return nil
}

// statusOf returns the HTTP status that OpenBao answered with, or 0 when err
// carries none.
func statusOf(err error) int {
	var re *api.ResponseError
	if errors.As(err, &re) {
		return re.StatusCode
	}
	return 0
}

// describe returns a new error that says why a request failed without
// wrapping err, which may hold the response body or the request URL. A
// cancelled or expired ctx is returned as it is, and a request that got no
// answer matches ErrUnreachable, so callers can tell them apart with
// errors.Is.
func describe(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	code := statusOf(err)
	if code == http.StatusForbidden {
		return fmt.Errorf("%w: openbao answered status %d", ErrForbidden, code)
	}
	if code != 0 {
		return fmt.Errorf("openbao answered status %d", code)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return fmt.Errorf("%w: it did not answer in time", ErrUnreachable)
		}
		return ErrUnreachable
	}
	return errors.New("openbao gave no usable answer")
}
