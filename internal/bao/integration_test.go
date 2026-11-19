package bao_test

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
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
	writes := []struct {
		path string
		data map[string]any
	}{
		{"transit/keys/fingerprint", map[string]any{"type": "aes256-gcm96"}},
		{"sys/policies/acl/worker", map[string]any{"policy": workerPolicy}},
		{"auth/approle/role/worker", map[string]any{"token_policies": []string{"worker"}, "token_ttl": "1h", "token_max_ttl": "1h"}},
		{"auth/approle/role/worker/role-id", map[string]any{"role_id": "se-worker"}},
	}
	for _, w := range writes {
		if _, err := root.Logical().WriteWithContext(ctx, w.path, w.data); err != nil {
			t.Fatalf("writing %s: %v", w.path, err)
		}
	}
	secret, err := root.Logical().WriteWithContext(ctx, "auth/approle/role/worker/secret-id", nil)
	if err != nil {
		t.Fatalf("creating a secret-id: %v", err)
	}
	secretID, ok := secret.Data["secret_id"].(string)
	if !ok {
		t.Fatal("creating a secret-id: the response holds no secret_id")
	}
	return bao.Credentials{RoleID: "se-worker", SecretID: bao.SecretID(secretID)}
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

// assertWorkerCannotRead logs in as the worker role with the official client
// and checks that reading path is refused with 403.
func assertWorkerCannotRead(t *testing.T, addr string, creds bao.Credentials, path string) {
	t.Helper()
	ctx := t.Context()
	cfg := api.NewConfig()
	cfg.Address = addr
	client, err := api.NewClient(cfg)
	if err != nil {
		t.Fatalf("api.NewClient(%q) error = %v", addr, err)
	}
	login, err := client.Logical().WriteWithContext(ctx, "auth/approle/login", map[string]any{
		"role_id":   creds.RoleID,
		"secret_id": string(creds.SecretID),
	})
	if err != nil || login == nil || login.Auth == nil {
		t.Fatalf("logging in as the worker role: %v", err)
	}
	client.SetToken(login.Auth.ClientToken)
	_, err = client.Logical().ReadWithContext(ctx, path)
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
