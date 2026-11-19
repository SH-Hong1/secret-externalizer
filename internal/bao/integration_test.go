package bao_test

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openbao/openbao/api/v2"

	"github.com/SH-Hong1/secret-externalizer/internal/bao"
)

// testRootToken is the root token of the openbao-test compose service, an
// in-memory dev server that never holds real values.
const testRootToken = "dev-only-openbao-test-root-token" //nolint:gosec // G101: the fake root token of a local dev server

// workerPolicy is the worker policy that deploy/openbao/config.hcl creates.
const workerPolicy = `
path "transit/hmac/fingerprint" {
  capabilities = ["update"]
}
path "secret/data/pending/*" {
  capabilities = ["create", "update"]
}
path "secret/data/projects/*" {
  capabilities = ["create", "update"]
}
path "secret/metadata/pending/*" {
  capabilities = ["list", "delete"]
}
`

// webPolicy is the web policy that deploy/openbao/config.hcl creates.
const webPolicy = `
path "secret/data/pending/*" {
  capabilities = ["read"]
}
path "secret/data/projects/*" {
  capabilities = ["read"]
}
`

// setUpTestBao prepares the dev server the way deploy/openbao/config.hcl
// prepares the real one, and returns the worker role's credentials. Each
// step is safe to repeat against a server an earlier run prepared.
func setUpTestBao(t *testing.T, addr string) bao.Credentials {
	t.Helper()
	ctx := t.Context()
	cfg := api.NewConfig()
	cfg.Address = addr
	root, err := api.NewClient(cfg)
	if err != nil {
		t.Fatalf("api.NewClient(%q) error = %v", addr, err)
	}
	root.SetToken(testRootToken)

	mounts, err := root.Sys().ListMountsWithContext(ctx)
	if err != nil {
		t.Fatalf("listing mounts: %v", err)
	}
	if _, ok := mounts["transit/"]; !ok {
		if err := root.Sys().MountWithContext(ctx, "transit", &api.MountInput{Type: "transit"}); err != nil {
			t.Fatalf("mounting transit: %v", err)
		}
	}
	if _, ok := mounts["secret/"]; !ok {
		in := &api.MountInput{Type: "kv", Options: map[string]string{"version": "2"}}
		if err := root.Sys().MountWithContext(ctx, "secret", in); err != nil {
			t.Fatalf("mounting secret: %v", err)
		}
	}
	auths, err := root.Sys().ListAuthWithContext(ctx)
	if err != nil {
		t.Fatalf("listing auth methods: %v", err)
	}
	if _, ok := auths["approle/"]; !ok {
		if err := root.Sys().EnableAuthWithOptionsWithContext(ctx, "approle", &api.EnableAuthOptions{Type: "approle"}); err != nil {
			t.Fatalf("enabling approle: %v", err)
		}
	}
	if _, err := root.Logical().WriteWithContext(ctx, "transit/keys/fingerprint", map[string]any{"type": "aes256-gcm96"}); err != nil {
		t.Fatalf("writing transit/keys/fingerprint: %v", err)
	}
	return setUpRole(t, root, "worker", workerPolicy)
}

// setUpWebRole adds the web policy and role to a dev server that
// setUpTestBao prepared, and returns the web role's credentials.
func setUpWebRole(t *testing.T, addr string) bao.Credentials {
	t.Helper()
	cfg := api.NewConfig()
	cfg.Address = addr
	root, err := api.NewClient(cfg)
	if err != nil {
		t.Fatalf("api.NewClient(%q) error = %v", addr, err)
	}
	root.SetToken(testRootToken)
	return setUpRole(t, root, "web", webPolicy)
}

// setUpRole writes the policy and AppRole name with role-id se-<name>, as
// deploy/openbao/config.hcl does, and returns a new secret-id for it.
func setUpRole(t *testing.T, root *api.Client, name, policy string) bao.Credentials {
	t.Helper()
	ctx := t.Context()
	roleID := "se-" + name
	writes := []struct {
		path string
		data map[string]any
	}{
		{"sys/policies/acl/" + name, map[string]any{"policy": policy}},
		{"auth/approle/role/" + name, map[string]any{"token_policies": []string{name}, "token_ttl": "1h", "token_max_ttl": "1h"}},
		{"auth/approle/role/" + name + "/role-id", map[string]any{"role_id": roleID}},
	}
	for _, w := range writes {
		if _, err := root.Logical().WriteWithContext(ctx, w.path, w.data); err != nil {
			t.Fatalf("writing %s: %v", w.path, err)
		}
	}
	secret, err := root.Logical().WriteWithContext(ctx, "auth/approle/role/"+name+"/secret-id", nil)
	if err != nil {
		t.Fatalf("creating a secret-id for %s: %v", name, err)
	}
	secretID, ok := secret.Data["secret_id"].(string)
	if !ok {
		t.Fatalf("creating a secret-id for %s: the response holds no secret_id", name)
	}
	return bao.Credentials{RoleID: roleID, SecretID: bao.SecretID(secretID)}
}

func TestIntegration_RoundTrip(t *testing.T) {
	addr := os.Getenv("TEST_BAO_ADDR")
	if addr == "" {
		t.Skip("set TEST_BAO_ADDR to run against OpenBao, for example TEST_BAO_ADDR=http://127.0.0.1:18200 (the compose openbao-test service)")
	}
	creds := setUpTestBao(t, addr)
	ctx := t.Context()
	c, err := bao.New(addr, creds, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("New(%q) error = %v", addr, err)
	}

	values := [][]byte{[]byte("dev-only-fake-integration-a"), []byte("dev-only-fake-integration-b")}
	first, err := c.Fingerprints(ctx, values)
	if err != nil {
		t.Fatalf("Fingerprints() error = %v", err)
	}
	second, err := c.Fingerprints(ctx, values[:1])
	if err != nil {
		t.Fatalf("Fingerprints() again error = %v", err)
	}
	if len(first) != 2 || len(second) != 1 || first[0] != second[0] || first[0] == first[1] || len(first[0]) != 64 {
		t.Fatalf("Fingerprints() = %q, then %q; want two different 64-digit fingerprints, the first repeated", first, second)
	}

	projectID := randomProjectID(t)
	fp := first[0]
	if err := c.PutPending(ctx, projectID, fp, values[0]); err != nil {
		t.Fatalf("PutPending(%d, %q) error = %v", projectID, fp, err)
	}
	listed, err := c.ListPending(ctx, projectID)
	if err != nil || !slices.Equal(listed, []string{fp}) {
		t.Errorf("ListPending(%d) = %q, %v; want [%q]", projectID, listed, err, fp)
	}

	assertWorkerCannotRead(t, addr, creds, fmt.Sprintf("secret/data/pending/%d/%s", projectID, fp))

	if err := c.DeletePending(ctx, projectID, fp); err != nil {
		t.Fatalf("DeletePending(%d, %q) error = %v", projectID, fp, err)
	}
	if err := c.DeletePending(ctx, projectID, fp); err != nil {
		t.Errorf("DeletePending(%d, %q) a second time error = %v, want nil", projectID, fp, err)
	}
	listed, err = c.ListPending(ctx, projectID)
	if err != nil || listed == nil || len(listed) != 0 {
		t.Errorf("ListPending(%d) after delete = %q, %v; want an empty list", projectID, listed, err)
	}
}

// TestIntegration_WebReadsPending checks that the web role's policy reads a
// pending original the worker stored at the KV v2 data path, and that a
// path with nothing stored comes back as 404, which ReadPending reports as
// ErrNoOriginal.
func TestIntegration_WebReadsPending(t *testing.T) {
	addr := os.Getenv("TEST_BAO_ADDR")
	if addr == "" {
		t.Skip("set TEST_BAO_ADDR to run against OpenBao, for example TEST_BAO_ADDR=http://127.0.0.1:18200 (the compose openbao-test service)")
	}
	workerCreds := setUpTestBao(t, addr)
	webCreds := setUpWebRole(t, addr)
	ctx := t.Context()
	hc := &http.Client{Timeout: 10 * time.Second}
	worker, err := bao.New(addr, workerCreds, hc)
	if err != nil {
		t.Fatalf("New(%q) for the worker error = %v", addr, err)
	}
	web, err := bao.New(addr, webCreds, hc)
	if err != nil {
		t.Fatalf("New(%q) for web error = %v", addr, err)
	}

	value := []byte("dev-only-fake-reveal-<b>\x00")
	fps, err := worker.Fingerprints(ctx, [][]byte{value, []byte("dev-only-fake-never-stored")})
	if err != nil {
		t.Fatalf("Fingerprints() error = %v", err)
	}
	projectID := randomProjectID(t)
	if err := worker.PutPending(ctx, projectID, fps[0], value); err != nil {
		t.Fatalf("PutPending(%d, %q) error = %v", projectID, fps[0], err)
	}
	t.Cleanup(func() {
		if err := worker.DeletePending(context.Background(), projectID, fps[0]); err != nil {
			t.Errorf("DeletePending(%d, %q) error = %v", projectID, fps[0], err)
		}
	})

	got, err := web.ReadPending(ctx, projectID, fps[0])
	if err != nil {
		t.Fatalf("ReadPending(%d, %q) as web error = %v", projectID, fps[0], err)
	}
	if string(got.Reveal()) != string(value) {
		t.Errorf("ReadPending(%d, %q) as web = %d bytes, want the %d bytes stored", projectID, fps[0], len(got.Reveal()), len(value))
	}

	assertStatus(t, addr, webCreds, fmt.Sprintf("secret/data/pending/%d/%s", projectID, fps[1]), http.StatusNotFound)
	if _, err := web.ReadPending(ctx, projectID, fps[1]); !errors.Is(err, bao.ErrNoOriginal) {
		t.Errorf("ReadPending(%d, %q) of a value never stored error = %v, want ErrNoOriginal", projectID, fps[1], err)
	}
	if err := web.PutPending(ctx, projectID, fps[1], value); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("PutPending() as web error = %v, want status 403", err)
	}
}

// assertStatus logs in with creds through the official client and reads
// path, and checks that OpenBao answered with status. The official client
// turns a 404 with no data into a nil secret and a nil error, so a 404 is
// read from the raw response.
func assertStatus(t *testing.T, addr string, creds bao.Credentials, path string, status int) {
	t.Helper()
	ctx := t.Context()
	client := loginAs(t, addr, creds)
	resp, err := client.Logical().ReadRawWithContext(ctx, path)
	if resp != nil {
		defer resp.Body.Close() //nolint:errcheck // a read-only response body; closing cannot lose data
	}
	if resp == nil {
		t.Fatalf("reading %s: no response, error = %v", path, err)
	}
	if resp.StatusCode != status {
		t.Errorf("reading %s status = %d, want %d", path, resp.StatusCode, status)
	}
}

// loginAs logs in with creds through the official client and returns a
// client that carries the token.
func loginAs(t *testing.T, addr string, creds bao.Credentials) *api.Client {
	t.Helper()
	cfg := api.NewConfig()
	cfg.Address = addr
	client, err := api.NewClient(cfg)
	if err != nil {
		t.Fatalf("api.NewClient(%q) error = %v", addr, err)
	}
	login, err := client.Logical().WriteWithContext(t.Context(), "auth/approle/login", map[string]any{
		"role_id":   creds.RoleID,
		"secret_id": string(creds.SecretID),
	})
	if err != nil || login == nil || login.Auth == nil {
		t.Fatalf("logging in as %s: %v", creds.RoleID, err)
	}
	client.SetToken(login.Auth.ClientToken)
	return client
}

// assertWorkerCannotRead logs in as the worker role with the official client
// and checks that reading path is refused with 403.
func assertWorkerCannotRead(t *testing.T, addr string, creds bao.Credentials, path string) {
	t.Helper()
	client := loginAs(t, addr, creds)
	_, err := client.Logical().ReadWithContext(t.Context(), path)
	var re *api.ResponseError
	if !errors.As(err, &re) || re.StatusCode != http.StatusForbidden {
		t.Errorf("reading %s with the worker token error = %v, want status 403", path, err)
	}
}

// randomProjectID returns a positive project ID that no earlier run used.
func randomProjectID(t *testing.T) int64 {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("reading random bytes: %v", err)
	}
	return int64(binary.BigEndian.Uint64(b[:])>>2) + 1
}
