package bao_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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
  capabilities = ["create", "update", "read"]
}
path "secret/metadata/pending/*" {
  capabilities = ["list", "delete"]
}
`

// kvMaxVersions is the number of versions per path that
// deploy/openbao/config.hcl sets on the KV v2 mount "secret".
const kvMaxVersions = 100

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
	if _, err := root.Logical().WriteWithContext(ctx, "secret/config", map[string]any{"max_versions": kvMaxVersions}); err != nil {
		t.Fatalf("writing secret/config: %v", err)
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
	if err := web.PutPending(ctx, projectID, fps[1], value); !errors.Is(err, bao.ErrForbidden) {
		t.Errorf("PutPending() as web error = %v, want ErrForbidden", err)
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

// TestPolicyMatchesConfig checks that the policies the integration tests
// create are the ones deploy/openbao/config.hcl creates, so the tests check
// the permissions the real server grants.
func TestPolicyMatchesConfig(t *testing.T) {
	const path = "../../deploy/openbao/config.hcl"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	for name, want := range map[string]string{"worker": workerPolicy, "web": webPolicy} {
		got, err := configPolicy(string(raw), name)
		if err != nil {
			t.Errorf("reading the %s policy from %s: %v", name, path, err)
			continue
		}
		if diff := cmp.Diff(strings.TrimLeft(want, "\n"), got); diff != "" {
			t.Errorf("%s policy in %s mismatch (-test constant +config):\n%s", name, path, diff)
		}
	}
}

// TestKVConfigMatchesConfig checks that deploy/openbao/config.hcl sets the
// version limit of the KV v2 mount that the integration tests set.
func TestKVConfigMatchesConfig(t *testing.T) {
	const path = "../../deploy/openbao/config.hcl"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	_, after, ok := strings.Cut(string(raw), `request "kv-max-versions" {`)
	block, _, _ := strings.Cut(after, "\n  }\n")
	want := []string{`operation = "update"`, `path      = "secret/config"`, fmt.Sprintf("max_versions = %d", kvMaxVersions)}
	for _, w := range want {
		if !ok || !strings.Contains(block, w) {
			t.Errorf("request kv-max-versions in %s does not hold %q", path, w)
		}
	}
}

// configPolicy returns the text of the indented heredoc in the request
// "<name>-policy" of config, with the common indentation removed, as HCL
// does for <<-.
func configPolicy(config, name string) (string, error) {
	_, after, ok := strings.Cut(config, `request "`+name+`-policy" {`)
	if !ok {
		return "", errors.New("no such request")
	}
	_, after, ok = strings.Cut(after, "policy = <<-EOT\n")
	if !ok {
		return "", errors.New("no heredoc")
	}
	var lines []string
	for line := range strings.Lines(after) {
		if strings.TrimSpace(line) == "EOT" {
			indent := -1
			for _, l := range lines {
				n := len(l) - len(strings.TrimLeft(l, " "))
				if strings.TrimSpace(l) != "" && (indent < 0 || n < indent) {
					indent = n
				}
			}
			if indent < 0 {
				return "", errors.New("empty heredoc")
			}
			var b strings.Builder
			for _, l := range lines {
				b.WriteString(l[min(indent, len(l)-len(strings.TrimLeft(l, " "))):])
			}
			return b.String(), nil
		}
		lines = append(lines, line)
	}
	return "", errors.New("heredoc does not end")
}

// testBaoAddr returns the address of the openbao-test service, and skips the
// test when none is set.
func testBaoAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_BAO_ADDR")
	if addr == "" {
		t.Skip("set TEST_BAO_ADDR to run against OpenBao, for example TEST_BAO_ADDR=http://127.0.0.1:18200 (the compose openbao-test service)")
	}
	return addr
}

// rootClient returns an official client that carries the dev server's root
// token.
func rootClient(t *testing.T, addr string) *api.Client {
	t.Helper()
	cfg := api.NewConfig()
	cfg.Address = addr
	root, err := api.NewClient(cfg)
	if err != nil {
		t.Fatalf("api.NewClient(%q) error = %v", addr, err)
	}
	root.SetToken(testRootToken)
	return root
}

// newIntegrationClient returns a Client for the role creds, and removes
// every version of the project path key with the root token when the test
// ends.
func newIntegrationClient(t *testing.T, addr string, creds bao.Credentials, projectID int64, key string) *bao.Client {
	t.Helper()
	c, err := bao.New(addr, creds, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("New(%q) error = %v", addr, err)
	}
	root := rootClient(t, addr)
	t.Cleanup(func() {
		metadata := fmt.Sprintf("secret/metadata/projects/%d/%s", projectID, key)
		if _, err := root.Logical().DeleteWithContext(context.Background(), metadata); err != nil {
			t.Errorf("deleting %s: %v", metadata, err)
		}
	})
	return c
}

// storedFixture returns a StoredValue for value, with its fingerprint from
// the server.
func storedFixture(t *testing.T, c *bao.Client, value []byte) bao.StoredValue {
	t.Helper()
	fps, err := c.Fingerprints(t.Context(), [][]byte{value})
	if err != nil {
		t.Fatalf("Fingerprints() error = %v", err)
	}
	return bao.StoredValue{Value: value, Fingerprint: fps[0], Kind: "password"}
}

func TestIntegration_WorkerStoresProjectValue(t *testing.T) {
	addr := testBaoAddr(t)
	creds := setUpTestBao(t, addr)
	ctx := t.Context()
	projectID := randomProjectID(t)
	const key = "ext.db.main.password"
	c := newIntegrationClient(t, addr, creds, projectID, key)

	first := storedFixture(t, c, []byte("dev-only-fake-stored-\x00\xff-first"))
	second := storedFixture(t, c, []byte("dev-only-fake-stored-second"))
	second.File = true

	version, err := c.PutStored(ctx, projectID, key, first, 0)
	if err != nil || version != 1 {
		t.Fatalf("PutStored(%d, %q, cas 0) = %d, %v; want version 1", projectID, key, version, err)
	}
	got, err := c.ReadStored(ctx, projectID, key)
	if err != nil {
		t.Fatalf("ReadStored(%d, %q) error = %v", projectID, key, err)
	}
	if string(got.Value.Reveal()) != string(first.Value) || got.Version != 1 || got.Deleted {
		t.Errorf("ReadStored(%d, %q) = %d bytes, version %d, deleted %t; want the %d bytes written, version 1",
			projectID, key, len(got.Value), got.Version, got.Deleted, len(first.Value))
	}
	again, err := c.Fingerprints(ctx, [][]byte{got.Value.Reveal()})
	if err != nil || again[0] != first.Fingerprint {
		t.Errorf("Fingerprints(the bytes read back) = %q, %v; want the fingerprint of the bytes written", again, err)
	}

	if v, err := c.PutStored(ctx, projectID, key, second, 0); !errors.Is(err, bao.ErrVersionConflict) {
		t.Errorf("PutStored(%d, %q, cas 0) over version 1 = %d, %v; want ErrVersionConflict", projectID, key, v, err)
	}
	version, err = c.PutStored(ctx, projectID, key, second, 1)
	if err != nil || version != 2 {
		t.Fatalf("PutStored(%d, %q, cas 1) = %d, %v; want version 2", projectID, key, version, err)
	}
	got, err = c.ReadStored(ctx, projectID, key)
	if err != nil || string(got.Value.Reveal()) != string(second.Value) || got.Version != 2 {
		t.Errorf("ReadStored(%d, %q) after the second write = version %d, %v; want the second value at version 2", projectID, key, got.Version, err)
	}

	old, err := rootClient(t, addr).Logical().ReadWithDataWithContext(ctx, bao.StorePath(projectID, key), map[string][]string{"version": {"1"}})
	if err != nil || old == nil {
		t.Fatalf("reading version 1 with the root token: %v", err)
	}
	data, _ := old.Data["data"].(map[string]any) // a missing data field fails the comparison below
	want := map[string]any{
		"value":       base64.StdEncoding.EncodeToString(first.Value),
		"encoding":    "base64",
		"format":      "text",
		"fingerprint": first.Fingerprint,
		"kind":        "password",
	}
	if diff := cmp.Diff(want, data); diff != "" {
		t.Errorf("version 1 data mismatch (-want +got):\n%s", diff)
	}
}

func TestIntegration_ReadStoredAfterSoftDelete(t *testing.T) {
	addr := testBaoAddr(t)
	creds := setUpTestBao(t, addr)
	ctx := t.Context()
	projectID := randomProjectID(t)
	const key = "ext.app.api.token"
	c := newIntegrationClient(t, addr, creds, projectID, key)
	first := storedFixture(t, c, []byte("dev-only-fake-deleted-first"))
	second := storedFixture(t, c, []byte("dev-only-fake-deleted-second"))

	if _, err := c.PutStored(ctx, projectID, key, first, 0); err != nil {
		t.Fatalf("PutStored(%d, %q, cas 0) error = %v", projectID, key, err)
	}
	if _, err := rootClient(t, addr).Logical().DeleteWithContext(ctx, bao.StorePath(projectID, key)); err != nil {
		t.Fatalf("deleting the latest version with the root token: %v", err)
	}

	got, err := c.ReadStored(ctx, projectID, key)
	if err != nil || got.Value != nil || got.Version != 1 || !got.Deleted {
		t.Fatalf("ReadStored(%d, %q) after a soft delete = %d bytes, version %d, deleted %t, %v; want no value, version 1, deleted",
			projectID, key, len(got.Value), got.Version, got.Deleted, err)
	}
	if v, err := c.PutStored(ctx, projectID, key, second, 0); !errors.Is(err, bao.ErrVersionConflict) {
		t.Errorf("PutStored(%d, %q, cas 0) over a deleted version = %d, %v; want ErrVersionConflict", projectID, key, v, err)
	}
	version, err := c.PutStored(ctx, projectID, key, second, got.Version)
	if err != nil || version != 2 {
		t.Fatalf("PutStored(%d, %q, cas %d) = %d, %v; want version 2", projectID, key, got.Version, version, err)
	}
	got, err = c.ReadStored(ctx, projectID, key)
	if err != nil || string(got.Value.Reveal()) != string(second.Value) || got.Version != 2 || got.Deleted {
		t.Errorf("ReadStored(%d, %q) after writing over the deleted version = version %d, deleted %t, %v; want the second value at version 2",
			projectID, key, got.Version, got.Deleted, err)
	}
	if _, err := c.ReadStored(ctx, projectID, "ext.app.never.stored"); !errors.Is(err, bao.ErrNotStored) {
		t.Errorf("ReadStored(%d, %q) of a path never written error = %v, want ErrNotStored", projectID, "ext.app.never.stored", err)
	}
}

func TestIntegration_WorkerCannotDeleteProjectValue(t *testing.T) {
	addr := testBaoAddr(t)
	creds := setUpTestBao(t, addr)
	ctx := t.Context()
	projectID := randomProjectID(t)
	const key = "ext.ssh.deploy.key"
	c := newIntegrationClient(t, addr, creds, projectID, key)
	if _, err := c.PutStored(ctx, projectID, key, storedFixture(t, c, []byte("dev-only-fake-kept")), 0); err != nil {
		t.Fatalf("PutStored(%d, %q) error = %v", projectID, key, err)
	}

	worker := loginAs(t, addr, creds)
	metadata := fmt.Sprintf("secret/metadata/projects/%d/%s", projectID, key)
	requests := []struct {
		name string
		do   func() error
	}{
		{"delete the latest version", func() error {
			_, err := worker.Logical().DeleteWithContext(ctx, bao.StorePath(projectID, key))
			return err
		}},
		{"read the metadata", func() error {
			_, err := worker.Logical().ReadWithContext(ctx, metadata)
			return err
		}},
		{"delete the metadata", func() error {
			_, err := worker.Logical().DeleteWithContext(ctx, metadata)
			return err
		}},
		{"destroy a version", func() error {
			_, err := worker.Logical().WriteWithContext(ctx, fmt.Sprintf("secret/destroy/projects/%d/%s", projectID, key), map[string]any{"versions": []int{1}})
			return err
		}},
	}
	for _, r := range requests {
		var re *api.ResponseError
		if err := r.do(); !errors.As(err, &re) || re.StatusCode != http.StatusForbidden {
			t.Errorf("worker token: %s error = %v, want status 403", r.name, err)
		}
	}
	got, err := c.ReadStored(ctx, projectID, key)
	if err != nil || got.Deleted || got.Version != 1 {
		t.Errorf("ReadStored(%d, %q) after the refused requests = version %d, deleted %t, %v; want version 1 kept", projectID, key, got.Version, got.Deleted, err)
	}
}

func TestIntegration_WebCannotWriteProjects(t *testing.T) {
	addr := testBaoAddr(t)
	workerCreds := setUpTestBao(t, addr)
	webCreds := setUpWebRole(t, addr)
	ctx := t.Context()
	projectID := randomProjectID(t)
	const key = "ext.db.main.password"
	worker := newIntegrationClient(t, addr, workerCreds, projectID, key)
	web := newIntegrationClient(t, addr, webCreds, projectID, key)
	v := storedFixture(t, worker, []byte("dev-only-fake-web-write"))

	if version, err := web.PutStored(ctx, projectID, key, v, 0); !errors.Is(err, bao.ErrForbidden) {
		t.Errorf("PutStored() as web = %d, %v; want ErrForbidden", version, err)
	}
	if _, err := web.ReadStored(ctx, projectID, key); !errors.Is(err, bao.ErrNotStored) {
		t.Errorf("ReadStored() as web after the refused write error = %v, want ErrNotStored", err)
	}
}

// TestIntegration_KeepsEarlierVersions checks that the version limit set on
// the mount keeps the first version of a path after more writes than the
// KV v2 default of 10 would keep.
func TestIntegration_KeepsEarlierVersions(t *testing.T) {
	addr := testBaoAddr(t)
	creds := setUpTestBao(t, addr)
	ctx := t.Context()
	projectID := randomProjectID(t)
	const key = "ext.db.rotated.password"
	c := newIntegrationClient(t, addr, creds, projectID, key)
	first := storedFixture(t, c, []byte("dev-only-fake-version-1"))
	later := storedFixture(t, c, []byte("dev-only-fake-version-later"))

	for cas := range 11 {
		v := later
		if cas == 0 {
			v = first
		}
		if version, err := c.PutStored(ctx, projectID, key, v, cas); err != nil || version != cas+1 {
			t.Fatalf("PutStored(cas %d) = %d, %v; want version %d", cas, version, err, cas+1)
		}
	}
	old, err := rootClient(t, addr).Logical().ReadWithDataWithContext(ctx, bao.StorePath(projectID, key), map[string][]string{"version": {"1"}})
	if err != nil || old == nil {
		t.Fatalf("reading version 1 after 11 writes with the root token = %v, %v; want the first value", old, err)
	}
	data, _ := old.Data["data"].(map[string]any) // a missing data field fails the comparison below
	if want := base64.StdEncoding.EncodeToString(first.Value); data["value"] != want {
		t.Errorf("version 1 after 11 writes holds another value or none, want the first value")
	}
}
