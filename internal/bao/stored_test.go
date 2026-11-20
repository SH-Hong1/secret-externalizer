package bao_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
)

// testKey is a well-formed key for an externalized value.
const testKey = "ext.db.main.password"

func TestStorePath(t *testing.T) {
	if got, want := bao.StorePath(7, testKey), "secret/data/projects/7/"+testKey; got != want {
		t.Errorf("StorePath(7, %q) = %q, want %q", testKey, got, want)
	}
}

// storedRequest is the body of a KV v2 write of an externalized value.
type storedRequest struct {
	Options struct {
		CAS json.Number `json:"cas"`
	} `json:"options"`
	Data map[string]any `json:"data"`
}

func TestPutStored(t *testing.T) {
	fingerprint := wantFingerprint(testValue)
	tests := []struct {
		name       string
		value      bao.StoredValue
		cas        int
		wantFormat string
	}{
		{"text at cas 0", bao.StoredValue{Value: []byte(testValue), Fingerprint: fingerprint, Kind: "password"}, 0, "text"},
		{"file at cas 3", bao.StoredValue{Value: []byte(testValue + "\x00\xff"), Fingerprint: fingerprint, Kind: "private-key", File: true}, 3, "file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var method, path string
			var body storedRequest
			_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
				method, path = r.Method, r.URL.Path
				dec := json.NewDecoder(r.Body)
				dec.UseNumber()
				if err := dec.Decode(&body); err != nil {
					t.Errorf("decoding body: %v", err)
				}
				writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{
					"version": tt.cas + 1, "deletion_time": "", "destroyed": false,
				}})
			})
			got, err := c.PutStored(t.Context(), 7, testKey, tt.value, tt.cas)
			if err != nil {
				t.Fatalf("PutStored(7, %q, cas %d) error = %v", testKey, tt.cas, err)
			}
			if got != tt.cas+1 {
				t.Errorf("PutStored(7, %q, cas %d) = version %d, want %d", testKey, tt.cas, got, tt.cas+1)
			}
			if want := "/v1/secret/data/projects/7/" + testKey; method != http.MethodPut || path != want {
				t.Errorf("PutStored sent %s %s, want PUT %s", method, path, want)
			}
			if body.Options.CAS.String() != fmt.Sprint(tt.cas) {
				t.Errorf("PutStored body options.cas = %q, want %d", body.Options.CAS, tt.cas)
			}
			want := map[string]any{
				"value":       base64.StdEncoding.EncodeToString(tt.value.Value),
				"encoding":    "base64",
				"format":      tt.wantFormat,
				"fingerprint": fingerprint,
				"kind":        tt.value.Kind,
			}
			if diff := cmp.Diff(want, body.Data); diff != "" {
				t.Errorf("PutStored body data mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPutStored_Refused checks that a refused write is a version conflict
// only when the path is at another version than cas. KV v2 answers 400 for
// other refusals too, and those must not ask the caller to write again.
func TestPutStored_Refused(t *testing.T) {
	tests := []struct {
		name         string
		cas          int
		readStatus   int
		readBody     any
		wantConflict bool
	}{
		{"path at a later version", 1, http.StatusOK, storedAnswer(2, "text"), true},
		{"path written since cas 0", 0, http.StatusOK, storedAnswer(1, "text"), true},
		{"deleted path at a later version", 2, http.StatusNotFound, deletedAnswer(3), true},
		{"path at cas", 2, http.StatusOK, storedAnswer(2, "text"), false},
		{"deleted path at cas", 3, http.StatusNotFound, deletedAnswer(3), false},
		{"empty path at cas 0", 0, http.StatusNotFound, map[string]any{"errors": []string{}}, false},
		{"foreign record at a later version", 1, http.StatusOK, map[string]any{"data": map[string]any{
			"data": map[string]any{"password": "x"}, "metadata": map[string]any{"version": 2},
		}}, true},
		{"read fails", 1, http.StatusInternalServerError, map[string]any{"errors": []string{testValue}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var reads int
			_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
				if r.Method == http.MethodGet {
					mu.Lock()
					reads++
					mu.Unlock()
					writeJSON(t, w, tt.readStatus, tt.readBody)
					return
				}
				writeJSON(t, w, http.StatusBadRequest, map[string]any{"errors": []string{"refused " + testValue}})
			})
			v := bao.StoredValue{Value: []byte(testValue), Fingerprint: wantFingerprint(testValue), Kind: "password"}
			got, err := c.PutStored(t.Context(), 7, testKey, v, tt.cas)
			if err == nil {
				t.Fatalf("PutStored(cas %d) answered 400 = %d, nil; want an error", tt.cas, got)
			}
			if errors.Is(err, bao.ErrVersionConflict) != tt.wantConflict {
				t.Errorf("PutStored(cas %d) answered 400 error = %v, matches ErrVersionConflict: %t, want %t", tt.cas, err, !tt.wantConflict, tt.wantConflict)
			}
			if strings.Contains(err.Error(), testValue) {
				t.Errorf("PutStored() error = %q, contains the value", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if reads == 0 {
				t.Errorf("PutStored(cas %d) answered 400 read the path %d times, want at least once", tt.cas, reads)
			}
		})
	}
}

// storedAnswer is a KV v2 read answer of a stored value at version.
func storedAnswer(version int, format string) map[string]any {
	return map[string]any{"data": map[string]any{
		"data": map[string]any{
			"value": base64.StdEncoding.EncodeToString([]byte(testValue)), "encoding": "base64",
			"format": format, "fingerprint": wantFingerprint(testValue), "kind": "password",
		},
		"metadata": map[string]any{"version": version, "deletion_time": "", "destroyed": false},
	}}
}

// deletedAnswer is the body of a KV v2 404 for a path whose latest version
// was deleted.
func deletedAnswer(version int) map[string]any {
	return map[string]any{"data": map[string]any{"data": nil, "metadata": map[string]any{
		"version": version, "deletion_time": "2026-11-20T01:45:03Z", "destroyed": false,
	}}}
}

func TestPutStored_RefusesCustomerData(t *testing.T) {
	f, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		t.Errorf("request %s %s sent for a refused kind, want none", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	for _, kind := range []string{"phone", "rrn", "email", "card", "address", "", "unknown"} {
		v := bao.StoredValue{Value: []byte("010-0000-0000"), Fingerprint: wantFingerprint(testValue), Kind: kind}
		if got, err := c.PutStored(t.Context(), 7, testKey, v, 0); err == nil || errors.Is(err, bao.ErrVersionConflict) {
			t.Errorf("PutStored(kind %q) = %d, %v; want a refusal", kind, got, err)
		}
	}
	if n := f.loginCount(); n != 0 {
		t.Errorf("PutStored of refused kinds logged in %d times, want 0", n)
	}
}

func TestReadStored_Format(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]any
		wantFile bool
		wantErr  bool
	}{
		{"text", map[string]any{"encoding": "base64", "format": "text"}, false, false},
		{"file", map[string]any{"encoding": "base64", "format": "file"}, true, false},
		{"other format", map[string]any{"encoding": "base64", "format": "binary"}, false, true},
		{"no format", map[string]any{"encoding": "base64"}, false, true},
		{"other encoding", map[string]any{"encoding": "plain", "format": "text"}, false, true},
		{"no encoding", map[string]any{"format": "text"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := map[string]any{"value": base64.StdEncoding.EncodeToString([]byte(testValue))}
			for k, v := range tt.data {
				data[k] = v
			}
			_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
				writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{
					"data": data, "metadata": map[string]any{"version": 1},
				}})
			})
			got, err := c.ReadStored(t.Context(), 7, testKey)
			if tt.wantErr {
				if err == nil || errors.Is(err, bao.ErrNotStored) {
					t.Errorf("ReadStored() of %v = file %t, %v; want an error other than ErrNotStored", tt.data, got.File, err)
				}
				return
			}
			if err != nil || got.File != tt.wantFile || string(got.Value.Reveal()) != testValue {
				t.Errorf("ReadStored() of %v = file %t, %v; want file %t and the value", tt.data, got.File, err, tt.wantFile)
			}
		})
	}
}

func TestStored_Forbidden(t *testing.T) {
	_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"errors": []string{"permission denied"}})
	})
	v := bao.StoredValue{Value: []byte(testValue), Fingerprint: wantFingerprint(testValue), Kind: "password"}
	if _, err := c.PutStored(t.Context(), 7, testKey, v, 0); !errors.Is(err, bao.ErrForbidden) || errors.Is(err, bao.ErrVersionConflict) {
		t.Errorf("PutStored() refused with 403 error = %v, want ErrForbidden", err)
	}
	if _, err := c.ReadStored(t.Context(), 7, testKey); !errors.Is(err, bao.ErrForbidden) {
		t.Errorf("ReadStored() refused with 403 error = %v, want ErrForbidden", err)
	}
}

func TestPutStored_BadAnswer(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   map[string]any
	}{
		{"no version", http.StatusOK, map[string]any{"data": map[string]any{"deletion_time": ""}}},
		{"zero version", http.StatusOK, map[string]any{"data": map[string]any{"version": 0}}},
		{"server error", http.StatusInternalServerError, map[string]any{"errors": []string{testValue}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
				writeJSON(t, w, tt.status, tt.body)
			})
			v := bao.StoredValue{Value: []byte(testValue), Fingerprint: wantFingerprint(testValue), Kind: "password"}
			got, err := c.PutStored(t.Context(), 7, testKey, v, 0)
			if err == nil || errors.Is(err, bao.ErrVersionConflict) {
				t.Errorf("PutStored() = %d, %v; want an error other than ErrVersionConflict", got, err)
			}
		})
	}
}

func TestStored_RejectsBadArguments(t *testing.T) {
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		t.Errorf("request %s %s sent for bad arguments, want none", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	good := bao.StoredValue{Value: []byte(testValue), Fingerprint: wantFingerprint(testValue), Kind: "password"}
	tests := []struct {
		name      string
		projectID int64
		key       string
		fp        string
	}{
		{"zero project", 0, testKey, good.Fingerprint},
		{"negative project", -1, testKey, good.Fingerprint},
		{"empty key", 7, "", good.Fingerprint},
		{"no ext prefix", 7, "db.main.password", good.Fingerprint},
		{"one segment", 7, "ext.password", good.Fingerprint},
		{"six segments", 7, "ext.a.b.c.d.e.f", good.Fingerprint},
		{"upper case", 7, "ext.db.Main", good.Fingerprint},
		{"digit first", 7, "ext.db.1main", good.Fingerprint},
		{"empty segment", 7, "ext.db..main", good.Fingerprint},
		{"trailing dot", 7, "ext.db.main.", good.Fingerprint},
		{"slash", 7, "ext.db/main", good.Fingerprint},
		{"parent path", 7, "../pending/7/ext.db", good.Fingerprint},
		{"underscore", 7, "ext.db.main_pw", good.Fingerprint},
		{"new line", 7, "ext.db.main\n", good.Fingerprint},
		{"too long", 7, "ext.a." + strings.Repeat("b", 99), good.Fingerprint},
		{"upper-case fingerprint", 7, testKey, strings.ToUpper(good.Fingerprint)},
		{"short fingerprint", 7, testKey, good.Fingerprint[:63]},
		{"empty fingerprint", 7, testKey, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := good
			v.Fingerprint = tt.fp
			if _, err := c.PutStored(t.Context(), tt.projectID, tt.key, v, 0); err == nil || errors.Is(err, bao.ErrVersionConflict) {
				t.Errorf("PutStored(%d, %q, fingerprint %q) error = %v, want an argument error", tt.projectID, tt.key, tt.fp, err)
			}
			if tt.fp != good.Fingerprint {
				return
			}
			if _, err := c.ReadStored(t.Context(), tt.projectID, tt.key); err == nil || errors.Is(err, bao.ErrNotStored) {
				t.Errorf("ReadStored(%d, %q) error = %v, want an argument error", tt.projectID, tt.key, err)
			}
		})
	}
}

func TestStored_AcceptsKeys(t *testing.T) {
	_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{"version": 1}})
	})
	v := bao.StoredValue{Value: []byte(testValue), Fingerprint: wantFingerprint(testValue), Kind: "password"}
	for _, key := range []string{"ext.a.b", "ext.db.main.password", "ext.a.b.c.d.e", "ext.a1.b2c3", "ext.a." + strings.Repeat("b", 98)} {
		if _, err := c.PutStored(t.Context(), 7, key, v, 0); err != nil {
			t.Errorf("PutStored(7, %q) error = %v, want nil", key, err)
		}
	}
}

func TestReadStored(t *testing.T) {
	value := []byte(testValue + "\x00\xff<script>")
	var mu sync.Mutex
	var paths []string
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{
			"data": map[string]any{
				"value": base64.StdEncoding.EncodeToString(value), "encoding": "base64",
				"format": "text", "fingerprint": wantFingerprint(testValue), "kind": "password",
			},
			"metadata": map[string]any{"version": 4, "deletion_time": "", "destroyed": false},
		}})
	})
	got, err := c.ReadStored(t.Context(), 7, testKey)
	if err != nil {
		t.Fatalf("ReadStored(7, %q) error = %v", testKey, err)
	}
	if diff := cmp.Diff(value, got.Value.Reveal()); diff != "" {
		t.Errorf("ReadStored(7, %q) value mismatch (-want +got):\n%s", testKey, diff)
	}
	if got.Version != 4 || got.Deleted || got.File {
		t.Errorf("ReadStored(7, %q) = version %d, deleted %t, file %t; want 4, false, false", testKey, got.Version, got.Deleted, got.File)
	}
	if want := []string{"GET /v1/secret/data/projects/7/" + testKey}; !cmp.Equal(want, paths) {
		t.Errorf("ReadStored sent %q, want %q", paths, want)
	}

	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("stored", "stored", got, "value", got.Value)
	j, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(Stored) error = %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(value)
	for _, out := range []string{fmt.Sprintf("%v", got), fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", got), buf.String(), string(j)} {
		for _, s := range []string{testValue, encoded} {
			if strings.Contains(out, s) {
				t.Errorf("output %q of a Stored contains %q", out, s)
			}
		}
	}
}

func TestReadStored_Answers(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        any
		want        bao.Stored
		wantErr     error
		wantAnyErr  bool
		wantVersion int
	}{
		{name: "nothing stored", status: http.StatusNotFound, body: map[string]any{"errors": []string{}}, wantErr: bao.ErrNotStored},
		{
			name: "latest deleted", status: http.StatusNotFound,
			body: map[string]any{"data": map[string]any{"data": nil, "metadata": map[string]any{
				"version": 3, "deletion_time": "2026-11-20T01:45:03Z", "destroyed": false,
			}}},
			want: bao.Stored{Version: 3, Deleted: true},
		},
		{
			name: "latest destroyed", status: http.StatusNotFound,
			body: map[string]any{"data": map[string]any{"data": nil, "metadata": map[string]any{
				"version": 5, "deletion_time": "", "destroyed": true,
			}}},
			want: bao.Stored{Version: 5, Deleted: true},
		},
		{
			name: "no data and no version", status: http.StatusNotFound,
			body:       map[string]any{"data": map[string]any{"data": nil, "metadata": map[string]any{"deletion_time": ""}}},
			wantAnyErr: true,
		},
		{
			name: "value not base64", status: http.StatusOK,
			body: map[string]any{"data": map[string]any{
				"data":     map[string]any{"value": testValue + "!", "encoding": "base64", "format": "text"},
				"metadata": map[string]any{"version": 1},
			}},
			wantAnyErr: true,
		},
		{
			name: "no value field", status: http.StatusOK,
			body: map[string]any{"data": map[string]any{
				"data":     map[string]any{"other": "x", "encoding": "base64", "format": "text"},
				"metadata": map[string]any{"version": 1},
			}},
			wantAnyErr: true,
		},
		{
			name: "no version", status: http.StatusOK,
			body: map[string]any{"data": map[string]any{
				"data": map[string]any{"value": base64.StdEncoding.EncodeToString([]byte(testValue))},
			}},
			wantAnyErr: true,
		},
		{name: "server error", status: http.StatusInternalServerError, body: map[string]any{"errors": []string{testValue}}, wantAnyErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
				writeJSON(t, w, tt.status, tt.body)
			})
			got, err := c.ReadStored(t.Context(), 7, testKey)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("ReadStored() = %+v, %v; want %v", got, err, tt.wantErr)
				}
			case tt.wantAnyErr:
				if err == nil || errors.Is(err, bao.ErrNotStored) {
					t.Errorf("ReadStored() = %+v, %v; want an error other than ErrNotStored", got, err)
				}
				if err != nil && strings.Contains(err.Error(), testValue) {
					t.Errorf("ReadStored() error = %q, contains the value", err)
				}
			default:
				if err != nil {
					t.Fatalf("ReadStored() error = %v", err)
				}
				if got.Value != nil || got.Version != tt.want.Version || got.Deleted != tt.want.Deleted {
					t.Errorf("ReadStored() = value %d bytes, version %d, deleted %t; want no value, version %d, deleted %t",
						len(got.Value), got.Version, got.Deleted, tt.want.Version, tt.want.Deleted)
				}
			}
		})
	}
}

func TestStoredValueRedacts(t *testing.T) {
	fingerprint := wantFingerprint(testValue)
	v := bao.StoredValue{Value: []byte(testValue), Fingerprint: fingerprint, Kind: "password", File: true}
	hexValue := hex.EncodeToString([]byte(testValue))
	encoded := base64.StdEncoding.EncodeToString([]byte(testValue))

	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("stored", "stored", v, "pointer", &v, "nested", struct{ V bao.StoredValue }{v})
	slog.New(slog.NewTextHandler(&buf, nil)).Info("stored", "stored", v)
	outputs := []string{buf.String(), v.String(), v.GoString()}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		outputs = append(outputs,
			fmt.Sprintf(verb, v), fmt.Sprintf(verb, &v),
			fmt.Sprintf(verb, struct{ V bao.StoredValue }{v}), fmt.Sprintf(verb, []bao.StoredValue{v}))
	}
	for _, x := range []any{v, &v, struct{ V bao.StoredValue }{v}, map[string]bao.StoredValue{"v": v}} {
		j, err := json.Marshal(x)
		if err != nil {
			t.Fatalf("json.Marshal(%T) error = %v", x, err)
		}
		outputs = append(outputs, string(j))
	}

	// An error from a refused write names neither the value nor the
	// fingerprint.
	_, c := newFakeBao(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"errors": []string{testValue, encoded}})
	})
	_, err := c.PutStored(t.Context(), 7, testKey, v, 0)
	if err == nil {
		t.Fatal("PutStored() against a failing server error = nil, want an error")
	}
	outputs = append(outputs, err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err))

	for _, out := range outputs {
		for _, s := range []string{testValue, hexValue, strings.ToUpper(hexValue), encoded, fingerprint, "100 101 118"} {
			if strings.Contains(out, s) {
				t.Errorf("output %q contains %q", out, s)
			}
		}
	}
}

func TestFingerprints_BatchesBySize(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	_, c := newFakeBao(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		req := decodeHMAC(t, r)
		mu.Lock()
		sizes = append(sizes, len(req.BatchInput))
		mu.Unlock()
		answerHMAC(t, w, req, "vault:v1:")
	})
	third := bao.MaxBatchBytes / 3
	// Three values of a third fill one request; the fourth starts another,
	// the value over the limit goes alone, and the two small ones after it
	// share the last request.
	lengths := []int{third, third, third, third, bao.MaxBatchBytes + 1, 10, 10}
	values := make([][]byte, len(lengths))
	want := make([]string, len(lengths))
	for i, n := range lengths {
		v := strings.Repeat(string(rune('a'+i)), n)
		values[i] = []byte(v)
		want[i] = wantFingerprint(v)
	}

	got, err := c.Fingerprints(t.Context(), values)
	if err != nil {
		t.Fatalf("Fingerprints(%d large values) error = %v", len(values), err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Fingerprints(%d large values) mismatch (-want +got):\n%s", len(values), diff)
	}
	if diff := cmp.Diff([]int{3, 1, 1, 2}, sizes); diff != "" {
		t.Errorf("Fingerprints(%d large values) batch sizes mismatch (-want +got):\n%s", len(values), diff)
	}
}
