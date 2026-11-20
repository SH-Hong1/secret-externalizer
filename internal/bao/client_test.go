package bao_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
)

// Obviously fake fixture values. The tests check that none of them leaks.
const (
	testRoleID   = "dev-only-fake-role-id"
	testSecretID = "dev-only-fake-secret-id" //nolint:gosec // G101: an obviously fake fixture value
	testValue    = "dev-only-fake-value-1234"
	tokenPrefix  = "dev-only-fake-token-" //nolint:gosec // G101: an obviously fake fixture value
)

// fakeBao is an OpenBao server that answers the requests the official client
// sends. Each test sets the handlers it needs; logins are counted and answered
// with a new token each time.
type fakeBao struct {
	t *testing.T

	// mu guards the fields below.
	mu     sync.Mutex
	logins int
	paths  []string
	// handle answers every request other than a login. It gets the token the
	// request carried.
	handle func(w http.ResponseWriter, r *http.Request, token string)
}

func newFakeBao(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, token string)) (*fakeBao, *bao.Client) {
	t.Helper()
	f := &fakeBao{t: t, handle: handle}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	c, err := bao.New(srv.URL, bao.Credentials{RoleID: testRoleID, SecretID: testSecretID}, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("New(%q) error = %v", srv.URL, err)
	}
	return f, c
}

func (f *fakeBao) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if r.URL.Path == "/v1/auth/approle/login" {
		f.login(w, r)
		return
	}
	f.handle(w, r, r.Header.Get("X-Vault-Token"))
}

func (f *fakeBao) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RoleID   string `json:"role_id"`
		SecretID string `json:"secret_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("decoding login body: %v", err)
	}
	if body.RoleID != testRoleID || body.SecretID != testSecretID {
		f.t.Errorf("login body = role_id %q, secret_id %q; want the test credentials", body.RoleID, body.SecretID)
	}
	f.mu.Lock()
	f.logins++
	token := fmt.Sprintf("%s%d", tokenPrefix, f.logins)
	f.mu.Unlock()
	writeJSON(f.t, w, http.StatusOK, map[string]any{"auth": map[string]any{"client_token": token, "lease_duration": 3600}})
}

func (f *fakeBao) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("writing response: %v", err)
	}
}

// hmacRequest is the body of a batch HMAC request.
type hmacRequest struct {
	KeyVersion json.Number `json:"key_version"`
	Algorithm  string      `json:"algorithm"`
	BatchInput []struct {
		Input string `json:"input"`
	} `json:"batch_input"`
}

func decodeHMAC(t *testing.T, r *http.Request) hmacRequest {
	t.Helper()
	var req hmacRequest
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		t.Errorf("decoding HMAC body: %v", err)
	}
	return req
}

// fakeHMAC returns the HMAC the fake answers for an input: the SHA-256 of
// the decoded input under the given prefix.
func fakeHMAC(t *testing.T, prefix, input string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		t.Errorf("decoding input %q: %v", input, err)
	}
	sum := sha256.Sum256(raw)
	return prefix + base64.StdEncoding.EncodeToString(sum[:])
}

func wantFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// answerHMAC answers a batch HMAC request with the fake HMAC of every input.
func answerHMAC(t *testing.T, w http.ResponseWriter, req hmacRequest, prefix string) {
	t.Helper()
	results := make([]map[string]any, len(req.BatchInput))
	for i, in := range req.BatchInput {
		results[i] = map[string]any{"hmac": fakeHMAC(t, prefix, in.Input)}
	}
	writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{"batch_results": results}})
}

func TestFingerprints_Request(t *testing.T) {
	var got hmacRequest
	var method, path string
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		method, path = r.Method, r.URL.Path
		got = decodeHMAC(t, r)
		answerHMAC(t, w, got, "vault:v1:")
	})

	fps, err := c.Fingerprints(t.Context(), [][]byte{[]byte(testValue)})
	if err != nil {
		t.Fatalf("Fingerprints(%q) error = %v", testValue, err)
	}
	// The official client sends every write as PUT, which OpenBao treats as
	// the same update operation as POST.
	if method != http.MethodPut || path != "/v1/transit/hmac/fingerprint" {
		t.Errorf("Fingerprints sent %s %s, want PUT /v1/transit/hmac/fingerprint", method, path)
	}
	if got.KeyVersion.String() != "1" || got.Algorithm != "sha2-256" {
		t.Errorf("Fingerprints body key_version = %s, algorithm = %q; want 1 and sha2-256", got.KeyVersion, got.Algorithm)
	}
	wantInput := base64.StdEncoding.EncodeToString([]byte(testValue))
	if len(got.BatchInput) != 1 || got.BatchInput[0].Input != wantInput {
		t.Errorf("Fingerprints body batch_input = %+v, want one input %q", got.BatchInput, wantInput)
	}
	if diff := cmp.Diff([]string{wantFingerprint(testValue)}, fps); diff != "" {
		t.Errorf("Fingerprints(%q) mismatch (-want +got):\n%s", testValue, diff)
	}
	if len(fps) == 1 && len(fps[0]) != 64 {
		t.Errorf("Fingerprints(%q) = %q, want 64 hex digits", testValue, fps[0])
	}
}

func TestFingerprints_WrongVersion(t *testing.T) {
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		answerHMAC(t, w, decodeHMAC(t, r), "vault:v2:")
	})
	fps, err := c.Fingerprints(t.Context(), [][]byte{[]byte(testValue)})
	if err == nil {
		t.Errorf("Fingerprints() with a vault:v2: answer = %q, want an error", fps)
	}
}

func TestFingerprints_ItemError(t *testing.T) {
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		decodeHMAC(t, r)
		writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{"batch_results": []map[string]any{
			{"hmac": fakeHMAC(t, "vault:v1:", base64.StdEncoding.EncodeToString([]byte("a")))},
			{"error": "refused " + testValue},
		}}})
	})
	_, err := c.Fingerprints(t.Context(), [][]byte{[]byte("a"), []byte(testValue)})
	if err == nil {
		t.Fatal("Fingerprints() with an item error = nil error, want an error")
	}
	if strings.Contains(err.Error(), testValue) {
		t.Errorf("Fingerprints() error = %q, contains the value", err)
	}
}

func TestFingerprints_Batches(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		req := decodeHMAC(t, r)
		mu.Lock()
		sizes = append(sizes, len(req.BatchInput))
		mu.Unlock()
		answerHMAC(t, w, req, "vault:v1:")
	})
	values := make([][]byte, 1200)
	want := make([]string, len(values))
	for i := range values {
		v := fmt.Sprintf("dev-only-fake-value-%04d", i)
		values[i] = []byte(v)
		want[i] = wantFingerprint(v)
	}

	got, err := c.Fingerprints(t.Context(), values)
	if err != nil {
		t.Fatalf("Fingerprints(1200 values) error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Fingerprints(1200 values) mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]int{500, 500, 200}, sizes); diff != "" {
		t.Errorf("Fingerprints(1200 values) batch sizes mismatch (-want +got):\n%s", diff)
	}
}

func TestFingerprints_Empty(t *testing.T) {
	f, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		t.Error("Fingerprints(nil) sent a request, want none")
		w.WriteHeader(http.StatusInternalServerError)
	})
	got, err := c.Fingerprints(t.Context(), nil)
	if err != nil || len(got) != 0 {
		t.Errorf("Fingerprints(nil) = %q, %v; want an empty list and nil", got, err)
	}
	if n := f.loginCount(); n != 0 {
		t.Errorf("Fingerprints(nil) logged in %d times, want 0", n)
	}
}

func TestClient_LogsInOnce(t *testing.T) {
	f, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, token string) {
		if token != tokenPrefix+"1" {
			t.Errorf("request carried token %q, want %q", token, tokenPrefix+"1")
		}
		answerHMAC(t, w, decodeHMAC(t, r), "vault:v1:")
	})
	for i := range 3 {
		if _, err := c.Fingerprints(t.Context(), [][]byte{[]byte(testValue)}); err != nil {
			t.Fatalf("Fingerprints() call %d error = %v", i+1, err)
		}
	}
	if n := f.loginCount(); n != 1 {
		t.Errorf("three calls logged in %d times, want 1", n)
	}
}

func TestClient_ReLoginOn403(t *testing.T) {
	t.Run("second token works", func(t *testing.T) {
		f, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, token string) {
			req := decodeHMAC(t, r)
			if token == tokenPrefix+"1" {
				writeJSON(t, w, http.StatusForbidden, map[string]any{"errors": []string{"permission denied"}})
				return
			}
			answerHMAC(t, w, req, "vault:v1:")
		})
		got, err := c.Fingerprints(t.Context(), [][]byte{[]byte(testValue)})
		if err != nil {
			t.Fatalf("Fingerprints() after a 403 error = %v, want success after a new login", err)
		}
		if diff := cmp.Diff([]string{wantFingerprint(testValue)}, got); diff != "" {
			t.Errorf("Fingerprints() after a 403 mismatch (-want +got):\n%s", diff)
		}
		if n := f.loginCount(); n != 2 {
			t.Errorf("Fingerprints() after a 403 logged in %d times, want 2", n)
		}
	})
	t.Run("second 403 fails", func(t *testing.T) {
		f, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			writeJSON(t, w, http.StatusForbidden, map[string]any{"errors": []string{"permission denied"}})
		})
		_, err := c.Fingerprints(t.Context(), [][]byte{[]byte(testValue)})
		if !errors.Is(err, bao.ErrForbidden) || !strings.Contains(err.Error(), "403") {
			t.Errorf("Fingerprints() with every request refused error = %v, want ErrForbidden naming status 403", err)
		}
		if n := f.loginCount(); n != 2 {
			t.Errorf("Fingerprints() with every request refused logged in %d times, want 2", n)
		}
	})
}

func TestClient_ErrorsHideValues(t *testing.T) {
	fingerprint := wantFingerprint(testValue)
	// leak answers 500 with a body that repeats everything the request held.
	leak := func(t *testing.T) func(w http.ResponseWriter, r *http.Request, token string) {
		return func(w http.ResponseWriter, r *http.Request, token string) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("reading request body: %v", err)
			}
			writeJSON(t, w, http.StatusInternalServerError, map[string]any{"errors": []string{
				testValue, testSecretID, token, string(body), r.URL.String(),
			}})
		}
	}
	calls := map[string]func(c *bao.Client) error{
		"Fingerprints": func(c *bao.Client) error {
			_, err := c.Fingerprints(t.Context(), [][]byte{[]byte(testValue)})
			return err
		},
		"PutPending": func(c *bao.Client) error {
			return c.PutPending(t.Context(), 7, fingerprint, []byte(testValue))
		},
		"ListPending": func(c *bao.Client) error {
			_, err := c.ListPending(t.Context(), 7)
			return err
		},
		"DeletePending": func(c *bao.Client) error {
			return c.DeletePending(t.Context(), 7, fingerprint)
		},
		"ReadPending": func(c *bao.Client) error {
			_, err := c.ReadPending(t.Context(), 7, fingerprint)
			return err
		},
		"PutStored": func(c *bao.Client) error {
			v := bao.StoredValue{Value: []byte(testValue), Fingerprint: fingerprint, Kind: "password"}
			_, err := c.PutStored(t.Context(), 7, "ext.db.main.password", v, 0)
			return err
		},
		"ReadStored": func(c *bao.Client) error {
			_, err := c.ReadStored(t.Context(), 7, "ext.db.main.password")
			return err
		},
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(testValue))
	forbidden := []string{testValue, encoded, testSecretID, tokenPrefix}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, c := newFakeBao(t, leak(t))
			err := call(c)
			if err == nil {
				t.Fatalf("%s() against a failing server error = nil, want an error", name)
			}
			outputs := []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)}
			for _, out := range outputs {
				for _, s := range forbidden {
					if strings.Contains(out, s) {
						t.Errorf("%s() error output %q contains %q", name, out, s)
					}
				}
			}
			if !strings.Contains(err.Error(), "500") {
				t.Errorf("%s() error = %q, want it to name status 500", name, err)
			}
		})
	}
	t.Run("login", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("reading request body: %v", err)
			}
			writeJSON(t, w, http.StatusInternalServerError, map[string]any{"errors": []string{string(body)}})
		}))
		t.Cleanup(srv.Close)
		c, err := bao.New(srv.URL, bao.Credentials{RoleID: testRoleID, SecretID: testSecretID}, &http.Client{Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		_, err = c.Fingerprints(t.Context(), [][]byte{[]byte(testValue)})
		if err == nil {
			t.Fatal("Fingerprints() with a failing login error = nil, want an error")
		}
		for _, out := range []string{err.Error(), fmt.Sprintf("%+v", err)} {
			if strings.Contains(out, testSecretID) {
				t.Errorf("Fingerprints() with a failing login error output %q contains the secret-id", out)
			}
		}
	})
}

func TestClient_HidesCredentials(t *testing.T) {
	creds := bao.Credentials{RoleID: testRoleID, SecretID: testSecretID}
	c, err := bao.New("http://127.0.0.1:1", creds, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("creds", "creds", creds, "secret_id", creds.SecretID, "client", c)
	outputs := []string{buf.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		outputs = append(outputs,
			fmt.Sprintf(verb, creds), fmt.Sprintf(verb, creds.SecretID),
			fmt.Sprintf(verb, c))
	}
	text, err := creds.SecretID.MarshalText()
	if err != nil {
		t.Fatalf("SecretID.MarshalText() error = %v", err)
	}
	outputs = append(outputs, string(text))
	for _, out := range outputs {
		if strings.Contains(out, testSecretID) {
			t.Errorf("output %q contains the secret-id", out)
		}
	}
}

func TestPutPending(t *testing.T) {
	fingerprint := wantFingerprint(testValue)
	var method, path string
	var body struct {
		Data map[string]string `json:"data"`
	}
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		method, path = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding body: %v", err)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{"version": 1}})
	})
	if err := c.PutPending(t.Context(), 7, fingerprint, []byte(testValue)); err != nil {
		t.Fatalf("PutPending(7, %q) error = %v", fingerprint, err)
	}
	if want := "/v1/secret/data/pending/7/" + fingerprint; method != http.MethodPut || path != want {
		t.Errorf("PutPending sent %s %s, want PUT %s", method, path, want)
	}
	want := map[string]string{"value": base64.StdEncoding.EncodeToString([]byte(testValue))}
	if diff := cmp.Diff(want, body.Data); diff != "" {
		t.Errorf("PutPending body data mismatch (-want +got):\n%s", diff)
	}
}

func TestPending_RejectsBadArguments(t *testing.T) {
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		t.Errorf("request %s %s sent for bad arguments, want none", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	good := wantFingerprint(testValue)
	tests := []struct {
		name        string
		projectID   int64
		fingerprint string
	}{
		{"zero project", 0, good},
		{"negative project", -1, good},
		{"path in fingerprint", 7, "../../projects/7/" + good[:47]},
		{"upper-case fingerprint", 7, strings.ToUpper(good)},
		{"short fingerprint", 7, good[:63]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := c.PutPending(t.Context(), tt.projectID, tt.fingerprint, []byte(testValue)); err == nil {
				t.Errorf("PutPending(%d, %q) error = nil, want an error", tt.projectID, tt.fingerprint)
			}
			if err := c.DeletePending(t.Context(), tt.projectID, tt.fingerprint); err == nil {
				t.Errorf("DeletePending(%d, %q) error = nil, want an error", tt.projectID, tt.fingerprint)
			}
			if _, err := c.ReadPending(t.Context(), tt.projectID, tt.fingerprint); err == nil || errors.Is(err, bao.ErrNoOriginal) {
				t.Errorf("ReadPending(%d, %q) error = %v, want an error other than ErrNoOriginal", tt.projectID, tt.fingerprint, err)
			}
		})
	}
	if _, err := c.ListPending(t.Context(), 0); err == nil {
		t.Error("ListPending(0) error = nil, want an error")
	}
}

func TestListPending(t *testing.T) {
	t.Run("keys", func(t *testing.T) {
		var method, path, list string
		_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
			method, path, list = r.Method, r.URL.Path, r.URL.Query().Get("list")
			writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{"keys": []string{"a", "b"}}})
		})
		got, err := c.ListPending(t.Context(), 7)
		if err != nil {
			t.Fatalf("ListPending(7) error = %v", err)
		}
		if method != http.MethodGet || path != "/v1/secret/metadata/pending/7" || list != "true" {
			t.Errorf("ListPending(7) sent %s %s list=%q, want GET /v1/secret/metadata/pending/7 list=true", method, path, list)
		}
		if diff := cmp.Diff([]string{"a", "b"}, got); diff != "" {
			t.Errorf("ListPending(7) mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("none", func(t *testing.T) {
		_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			writeJSON(t, w, http.StatusNotFound, map[string]any{"errors": []string{}})
		})
		got, err := c.ListPending(t.Context(), 7)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("ListPending(7) with nothing stored = %q, %v; want an empty list and nil", got, err)
		}
	})
}

func TestDeletePending(t *testing.T) {
	fingerprint := wantFingerprint(testValue)
	var method, path string
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeletePending(t.Context(), 7, fingerprint); err != nil {
		t.Fatalf("DeletePending(7, %q) error = %v", fingerprint, err)
	}
	if want := "/v1/secret/metadata/pending/7/" + fingerprint; method != http.MethodDelete || path != want {
		t.Errorf("DeletePending sent %s %s, want DELETE %s", method, path, want)
	}
}

func TestDeletePending_Missing(t *testing.T) {
	_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"errors": []string{}})
	})
	fingerprint := wantFingerprint(testValue)
	if err := c.DeletePending(t.Context(), 7, fingerprint); err != nil {
		t.Errorf("DeletePending(7, %q) with a 404 answer = %v, want nil", fingerprint, err)
	}
}

func TestNew_RequiresTimeout(t *testing.T) {
	creds := bao.Credentials{RoleID: testRoleID, SecretID: testSecretID}
	tests := []struct {
		name string
		hc   *http.Client
	}{
		{"nil client", nil},
		{"no timeout", &http.Client{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := bao.New("http://127.0.0.1:1", creds, tt.hc); err == nil {
				t.Errorf("New() with %s error = nil, want an error", tt.name)
			}
		})
	}
}

func TestNew_RequiresCredentials(t *testing.T) {
	hc := &http.Client{Timeout: time.Second}
	for _, creds := range []bao.Credentials{{RoleID: testRoleID}, {SecretID: testSecretID}} {
		if _, err := bao.New("http://127.0.0.1:1", creds, hc); err == nil {
			t.Errorf("New() with role-id %q and secret-id set=%t error = nil, want an error", creds.RoleID, creds.SecretID != "")
		}
	}
}

// kvFake answers the KV v2 requests of the pending originals from memory, as
// OpenBao does: a write stores the data under the path, a read returns it
// with its metadata, and a read of a path with nothing stored answers 404.
type kvFake struct {
	t *testing.T

	// mu guards data.
	mu   sync.Mutex
	data map[string]map[string]any
}

func (k *kvFake) handle(w http.ResponseWriter, r *http.Request, _ string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		var body struct {
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			k.t.Errorf("decoding body: %v", err)
		}
		k.data[r.URL.Path] = body.Data
		writeJSON(k.t, w, http.StatusOK, map[string]any{"data": map[string]any{"version": 1}})
	case http.MethodGet:
		d, ok := k.data[r.URL.Path]
		if !ok {
			writeJSON(k.t, w, http.StatusNotFound, map[string]any{"errors": []string{}})
			return
		}
		writeJSON(k.t, w, http.StatusOK, map[string]any{"data": map[string]any{
			"data":     d,
			"metadata": map[string]any{"version": 1, "deletion_time": "", "destroyed": false},
		}})
	default:
		k.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestReadPending(t *testing.T) {
	kv := &kvFake{t: t, data: map[string]map[string]any{}}
	f, c := newFakeBao(t, kv.handle)
	fingerprint := wantFingerprint(testValue)
	value := []byte(testValue + "\x00\xff<script>")
	if err := c.PutPending(t.Context(), 7, fingerprint, value); err != nil {
		t.Fatalf("PutPending(7, %q) error = %v", fingerprint, err)
	}

	got, err := c.ReadPending(t.Context(), 7, fingerprint)
	if err != nil {
		t.Fatalf("ReadPending(7, %q) error = %v", fingerprint, err)
	}
	if diff := cmp.Diff(value, got.Reveal()); diff != "" {
		t.Errorf("ReadPending(7, %q) mismatch (-want +got):\n%s", fingerprint, diff)
	}
	f.mu.Lock()
	last := f.paths[len(f.paths)-1]
	f.mu.Unlock()
	if want := "GET /v1/secret/data/pending/7/" + fingerprint; last != want {
		t.Errorf("ReadPending sent %s, want %s", last, want)
	}

	other := wantFingerprint("dev-only-fake-other")
	if _, err := c.ReadPending(t.Context(), 7, other); !errors.Is(err, bao.ErrNoOriginal) {
		t.Errorf("ReadPending(7, %q) with nothing stored error = %v, want ErrNoOriginal", other, err)
	}
	if _, err := c.ReadPending(t.Context(), 8, fingerprint); !errors.Is(err, bao.ErrNoOriginal) {
		t.Errorf("ReadPending(8, %q) of another project error = %v, want ErrNoOriginal", fingerprint, err)
	}
}

func TestReadPending_Answers(t *testing.T) {
	fingerprint := wantFingerprint(testValue)
	tests := []struct {
		name     string
		status   int
		body     map[string]any
		wantNone bool
	}{
		{"deleted version", http.StatusNotFound, map[string]any{"data": map[string]any{
			"data": nil, "metadata": map[string]any{"version": 2, "deletion_time": "2026-11-19T06:41:07Z"},
		}}, true},
		{"no value field", http.StatusOK, map[string]any{"data": map[string]any{"data": map[string]any{"other": "x"}}}, false},
		{"value not base64", http.StatusOK, map[string]any{"data": map[string]any{"data": map[string]any{"value": testValue + "!"}}}, false},
		{"server error", http.StatusInternalServerError, map[string]any{"errors": []string{testValue}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
				writeJSON(t, w, tt.status, tt.body)
			})
			got, err := c.ReadPending(t.Context(), 7, fingerprint)
			if err == nil {
				t.Fatalf("ReadPending() = %d bytes, nil; want an error", len(got.Reveal()))
			}
			if errors.Is(err, bao.ErrNoOriginal) != tt.wantNone {
				t.Errorf("ReadPending() error = %v, matches ErrNoOriginal: %v, want %v", err, !tt.wantNone, tt.wantNone)
			}
			if strings.Contains(err.Error(), testValue) {
				t.Errorf("ReadPending() error = %q, contains the value", err)
			}
		})
	}
}

func TestValueRedacts(t *testing.T) {
	v := bao.Value(testValue)
	if got := string(v.Reveal()); got != testValue {
		t.Fatalf("Value.Reveal() = %q, want %q", got, testValue)
	}
	hexValue := hex.EncodeToString([]byte(testValue))
	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("value", "value", v, "nested", struct{ V bao.Value }{v})
	slog.New(slog.NewTextHandler(&buf, nil)).Info("value", "value", v)
	outputs := []string{buf.String()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		outputs = append(outputs, fmt.Sprintf(verb, v), fmt.Sprintf(verb, struct{ V bao.Value }{v}), fmt.Sprintf(verb, []bao.Value{v}))
	}
	for _, x := range []any{v, struct{ V bao.Value }{v}, map[string]bao.Value{"v": v}} {
		j, err := json.Marshal(x)
		if err != nil {
			t.Fatalf("json.Marshal(%T) error = %v", x, err)
		}
		outputs = append(outputs, string(j))
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(testValue))
	for _, out := range outputs {
		for _, s := range []string{testValue, hexValue, strings.ToUpper(hexValue), encoded, "100 101 118"} {
			if strings.Contains(out, s) {
				t.Errorf("output %q contains %q", out, s)
			}
		}
	}
}

func TestClient_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	c, err := bao.New(addr, bao.Credentials{RoleID: testRoleID, SecretID: testSecretID}, &http.Client{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New(%q) error = %v", addr, err)
	}
	if _, err := c.ReadPending(t.Context(), 7, wantFingerprint(testValue)); !errors.Is(err, bao.ErrUnreachable) {
		t.Errorf("ReadPending() against a closed server error = %v, want ErrUnreachable", err)
	}

	_, c = newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"errors": []string{"permission denied"}})
	})
	if _, err := c.ReadPending(t.Context(), 7, wantFingerprint(testValue)); err == nil || errors.Is(err, bao.ErrUnreachable) {
		t.Errorf("ReadPending() refused with 403 error = %v, want an error other than ErrUnreachable", err)
	}
}
