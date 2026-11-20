package bao

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/openbao/openbao/api/v2"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

var (
	// ErrNotStored means the path has never held a version.
	ErrNotStored = errors.New("no stored value")
	// ErrVersionConflict means the path was not at the version the write
	// expected.
	ErrVersionConflict = errors.New("stored value is at another version")
)

const (
	// maxKeyLen is the longest key of an externalized value, in bytes.
	maxKeyLen = 104

	// keyPrefix starts every key of an externalized value.
	keyPrefix = "ext."

	// minKeySegments and maxKeySegments bound the number of segments after
	// keyPrefix.
	minKeySegments = 2
	maxKeySegments = 5
)

// Stored is what a project path holds. Value is nil and Deleted is true
// when the latest version was deleted; Version is then still the latest.
// File is true when the value was stored as the bytes of a whole file.
type Stored struct {
	Value   Value
	Version int
	Deleted bool
	File    bool
}

// StoredValue is a value to keep for a placeholder. File is true for the
// bytes of a whole file. fmt, slog and encoding/json print a redacted form.
type StoredValue struct {
	Value       []byte
	Fingerprint string
	Kind        string
	File        bool
}

// storedValueText is what fmt, slog and encoding/json print for a
// StoredValue. The fingerprint is left out too, because it may appear only
// in the database and on a page.
const storedValueText = "bao.StoredValue{" + redacted + "}"

// String returns a fixed text that holds no value.
func (StoredValue) String() string { return storedValueText }

// GoString returns a fixed text that holds no value.
func (StoredValue) GoString() string { return storedValueText }

// Format prints the form String returns for every fmt verb, including %x
// and %#v.
func (StoredValue) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(storedValueText)) // a failed write to a fmt buffer has no one to report to
}

// LogValue returns a fixed text that holds no value.
func (StoredValue) LogValue() slog.Value { return slog.StringValue(storedValueText) }

// MarshalJSON returns the fixed text as a JSON string.
func (StoredValue) MarshalJSON() ([]byte, error) { return json.Marshal(storedValueText) }

// format returns the value of the "format" field that records whether v is
// a whole file.
func (v StoredValue) format() string {
	if v.File {
		return "file"
	}
	return "text"
}

// StorePath returns "secret/data/projects/<projectID>/<key>".
func StorePath(projectID int64, key string) string {
	return "secret/data/projects/" + strconv.FormatInt(projectID, 10) + "/" + key
}

// ReadStored returns the value and version at the path, or ErrNotStored.
// A record that this package did not write (no base64 encoding, or a format
// other than text and file) is an error.
func (c *Client) ReadStored(ctx context.Context, projectID int64, key string) (Stored, error) {
	const op = "reading a stored value"
	if err := checkStoredKey(projectID, key); err != nil {
		return Stored{}, fmt.Errorf("%s: %w", op, err)
	}
	secret, version, err := c.readLatest(ctx, op, projectID, key)
	if err != nil {
		return Stored{}, err
	}
	if version == 0 {
		return Stored{}, fmt.Errorf("%s: %w", op, ErrNotStored)
	}
	data, _ := secret.Data["data"].(map[string]any) // a missing or null data field means the latest version was deleted
	if data == nil {
		return Stored{Version: version, Deleted: true}, nil
	}
	if enc, _ := data["encoding"].(string); enc != "base64" { // a missing encoding is refused like any other
		return Stored{}, fmt.Errorf("%s: the stored record is not base64 encoded", op)
	}
	var file bool
	switch f, _ := data["format"].(string); f { // a missing format is refused like any other
	case "text":
	case "file":
		file = true
	default:
		return Stored{}, fmt.Errorf("%s: the stored record has no known format", op)
	}
	encoded, ok := data["value"].(string)
	if !ok {
		return Stored{}, fmt.Errorf("%s: response holds no value", op)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// The decoding error names only an offset, but it is dropped all
		// the same.
		return Stored{}, fmt.Errorf("%s: the stored value is not base64", op)
	}
	return Stored{Value: Value(raw), Version: version, File: file}, nil
}

// readLatest reads the latest version of the path and returns the answer
// and its version, or a version of 0 when the path has never held one. The
// answer of a deleted or destroyed latest version has a null data field.
func (c *Client) readLatest(ctx context.Context, op string, projectID int64, key string) (*api.Secret, int, error) {
	// The official client answers a 404 that carries no data with a nil
	// secret and no error, and one that carries the metadata of a deleted or
	// destroyed latest version with that metadata and a null data field.
	secret, err := c.do(ctx, op, func() (*api.Secret, error) {
		return c.api.Logical().ReadWithContext(ctx, StorePath(projectID, key))
	})
	if err != nil {
		return nil, 0, err
	}
	if secret == nil || len(secret.Data) == 0 {
		return nil, 0, nil
	}
	metadata, _ := secret.Data["metadata"].(map[string]any) // a missing metadata field fails the version check below
	version, ok := positiveInt(metadata["version"])
	if !ok {
		return nil, 0, fmt.Errorf("%s: response holds no version", op)
	}
	return secret, version, nil
}

// PutStored writes v as a new version when the path is at version cas (0:
// the path has never held one) and returns the new version.
//
// It returns an error matching ErrVersionConflict when the write was
// refused and the path is now at a version other than cas. A conflict never
// means that v was stored: the caller reads the path again, compares the
// stored bytes, and writes again with the version it read, a bounded number
// of times. Any other refusal, including a refused write while the path is
// still at cas, is a plain error and writing again will not help.
//
// v.Kind must be a secret kind that package finding defines; customer data
// is never written.
func (c *Client) PutStored(ctx context.Context, projectID int64, key string, v StoredValue, cas int) (int, error) {
	const op = "storing a value"
	if err := checkStoredKey(projectID, key); err != nil {
		return 0, fmt.Errorf("%s: %w", op, err)
	}
	if !isFingerprint(v.Fingerprint) {
		return 0, fmt.Errorf("%s: the fingerprint is not 64 lower-case hex digits", op)
	}
	if kind, err := finding.ParseKind(v.Kind); err != nil || kind.CustomerData() {
		return 0, fmt.Errorf("%s: the kind is not a kind of secret", op)
	}
	if cas < 0 {
		return 0, fmt.Errorf("%s: check-and-set version %d is negative", op, cas)
	}
	body := map[string]any{
		"options": map[string]any{"cas": cas},
		"data": map[string]any{
			"value":       base64.StdEncoding.EncodeToString(v.Value),
			"encoding":    "base64",
			"format":      v.format(),
			"fingerprint": v.Fingerprint,
			"kind":        v.Kind,
		},
	}
	// KV v2 answers 400 when the path is not at version cas, and also for
	// other refusals. The 400 is noted here, inside fn, because do keeps
	// only the status of an error; the path is then read to tell them apart.
	refused := false
	secret, err := c.do(ctx, op, func() (*api.Secret, error) {
		secret, err := c.api.Logical().WriteWithContext(ctx, StorePath(projectID, key), body)
		if statusOf(err) == http.StatusBadRequest {
			refused = true
			return nil, nil
		}
		return secret, err
	})
	if err != nil {
		return 0, err
	}
	if refused {
		_, version, err := c.readLatest(ctx, "reading the path after status 400", projectID, key)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", op, err)
		}
		if version != cas {
			return 0, fmt.Errorf("%s: %w", op, ErrVersionConflict)
		}
		return 0, fmt.Errorf("%s: openbao answered status 400 while the path is at the expected version", op)
	}
	if secret == nil {
		return 0, fmt.Errorf("%s: empty response", op)
	}
	version, ok := positiveInt(secret.Data["version"])
	if !ok {
		return 0, fmt.Errorf("%s: response holds no version", op)
	}
	return version, nil
}

// checkStoredKey reports whether a project ID and a key can form a store
// path. A key is "ext." followed by two to five dot-separated segments, each
// a lower-case letter followed by lower-case letters and digits, and is at
// most maxKeyLen bytes, so it can never add a path segment.
func checkStoredKey(projectID int64, key string) error {
	if projectID <= 0 {
		return fmt.Errorf("project ID %d is not positive", projectID)
	}
	if len(key) > maxKeyLen {
		return fmt.Errorf("the key is longer than %d bytes", maxKeyLen)
	}
	rest, ok := strings.CutPrefix(key, keyPrefix)
	if !ok {
		return fmt.Errorf("the key does not start with %q", keyPrefix)
	}
	segments := strings.Split(rest, ".")
	if len(segments) < minKeySegments || len(segments) > maxKeySegments {
		return fmt.Errorf("the key does not have %d to %d segments after %q", minKeySegments, maxKeySegments, keyPrefix)
	}
	for _, s := range segments {
		if !isKeySegment(s) {
			return errors.New("a key segment is not a lower-case letter followed by lower-case letters and digits")
		}
	}
	return nil
}

// isKeySegment reports whether s is a lower-case ASCII letter followed by
// lower-case ASCII letters and digits.
func isKeySegment(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		b := s[i]
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') {
			return false
		}
	}
	return true
}

// isFingerprint reports whether s is exactly 64 lower-case hex digits.
func isFingerprint(s string) bool {
	return len(s) == fingerprintLen && strings.Trim(s, "0123456789abcdef") == ""
}

// positiveInt returns x as an int when it is a JSON number that holds a
// positive integer. The official client decodes numbers as json.Number.
func positiveInt(x any) (int, bool) {
	n, ok := x.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(n.String())
	if err != nil || i <= 0 {
		return 0, false
	}
	return i, true
}
