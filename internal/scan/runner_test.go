package scan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/text/encoding/korean"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/job"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/worktree/worktreetest"
)

const (
	reasonOutside      = "project path points outside the workspace root"
	reasonNotDirectory = "project path is not a directory in the workspace"
	reasonOther        = "scan could not finish; see the worker log"
	reasonSecretStore  = "the secret store could not be reached; see the worker log"
	reasonIgnoreFile   = "project has a scanner ignore file (.gitleaksignore or .betterleaksignore); remove it and scan again"
	reasonTimeLimit    = "scan took longer than the time limit"
)

// propsFile holds password on its first line.
const propsFile = "conf/db.properties"

func TestHandle_MergesTools(t *testing.T) {
	content := "jdbc.password=" + password + "\n"
	e := newEnv(t, map[string]string{propsFile: content})

	if err := e.handle(t.Context(), passwordBL(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	want := []finding.Finding{{
		Fingerprint: fingerprintOf(password), File: propsFile, Key: "jdbc.password",
		// storage-credential comes before other-secret, and the kind comes
		// from the detection that gave the category.
		Category: finding.CategoryStorage, Kind: finding.KindPassword,
		Preview: "dev-****", HasOriginal: true,
		Locations: []finding.Location{{
			StartLine: 1, StartCol: 15, EndLine: 1, EndCol: 14 + len(password),
			Context: []string{"jdbc.password=****"}, ContextFirstLine: 1,
			FileHash: hashOf(content),
			Detections: []finding.Detection{
				{Tool: "betterleaks", Rule: "generic-password"},
				{Tool: "opengrep", Rule: "properties-password"},
			},
		}},
	}}
	if diff := cmp.Diff(want, got.Findings); diff != "" {
		t.Errorf("Handle() findings (-want +got):\n%s", diff)
	}
	if got.Files != 1 || got.Skipped != 0 || got.Dropped != 0 || got.RulesVersion != "fake-rules-1" {
		t.Errorf("Handle() result = %+v, want 1 file, none skipped or dropped, rules fake-rules-1", got)
	}
	wantVersions := map[string]string{"betterleaks": "fake-1", "opengrep": "fake-1", "keyfile": "1"}
	if diff := cmp.Diff(wantVersions, got.ToolVersions); diff != "" {
		t.Errorf("Handle() tool versions (-want +got):\n%s", diff)
	}
	if e.orig.puts != 1 || string(e.orig.pending[fingerprintOf(password)]) != password {
		t.Errorf("Handle() put %d pending values, %v; want the password once", e.orig.puts, e.orig.pending)
	}
	if diff := cmp.Diff([]string{"settled", "put", "save"}, e.events.all()); diff != "" {
		t.Errorf("Handle() calls (-want +got):\n%s", diff)
	}
}

func TestHandle_SameValueTwoFiles(t *testing.T) {
	e := newEnv(t, map[string]string{
		"a.properties": "db.password=" + password + "\n",
		"b.properties": "x=1\ndb.password=" + password + "\n",
	})
	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	if len(got) != 2 {
		t.Fatalf("Handle() found %d findings, want 2: %+v", len(got), got)
	}
	for i, file := range []string{"a.properties", "b.properties"} {
		if got[i].File != file || got[i].Fingerprint != fingerprintOf(password) || len(got[i].Locations) != 1 {
			t.Errorf("finding %d = %+v, want the password in %s at one location", i, got[i], file)
		}
	}
	if e.orig.puts != 1 {
		t.Errorf("Handle() put %d pending values, want 1 for one fingerprint", e.orig.puts)
	}
}

func TestHandle_SameValueTwiceInFile(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "a=" + password + "\n#\na=" + password + "\n"})
	if err := e.handle(t.Context(), passwordBL(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	if len(got) != 1 || len(got[0].Locations) != 2 {
		t.Fatalf("Handle() findings = %+v, want one finding with two locations", got)
	}
	if got[0].Key != "a" {
		t.Errorf("Handle() key = %q, want %q", got[0].Key, "a")
	}
	for i, line := range []int{1, 3} {
		l := got[0].Locations[i]
		if l.StartLine != line || l.StartCol != 3 || len(l.Detections) != 2 {
			t.Errorf("location %d = %+v, want line %d column 3 found by both tools", i, l, line)
		}
	}
}

func TestHandle_SameValueDifferentKeys(t *testing.T) {
	content := "local.db.password=" + password + "\ndev.db.password=" + password + "\nlive.db.password=" + password + "\n"
	e := newEnv(t, map[string]string{propsFile: content})
	if err := e.handle(t.Context(), passwordBL(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	if len(got) != 3 {
		t.Fatalf("Handle() findings = %+v, want three", got)
	}
	for i, key := range []string{"local.db.password", "dev.db.password", "live.db.password"} {
		if got[i].Key != key || len(got[i].Locations) != 1 || got[i].Fingerprint != fingerprintOf(password) {
			t.Errorf("finding %d = %+v, want key %q, one location, the password's fingerprint", i, got[i], key)
		}
	}
	if e.orig.puts != 1 {
		t.Errorf("Handle() put %d pending values, want 1 for one fingerprint", e.orig.puts)
	}
}

func TestHandle_KeyInCRLFAndEUCKR(t *testing.T) {
	text := "# 주문 설정\r\ndb.password=" + password + "\r\n"
	euckr, err := korean.EUCKR.NewEncoder().String(text)
	if err != nil {
		t.Fatalf("encoding EUC-KR: %v", err)
	}
	e := newEnv(t, map[string]string{"crlf/a.properties": text, "euckr/a.properties": euckr})
	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	if len(got) != 2 {
		t.Fatalf("Handle() findings = %+v, want two", got)
	}
	for _, f := range got {
		if f.Key != "db.password" {
			t.Errorf("Handle() key in %s = %q, want %q", f.File, f.Key, "db.password")
		}
	}
}

func TestHandle_URLComponentsShareKey(t *testing.T) {
	const url = "jdbc:mysql://shop_app:dev-only-fake-pw@db.invalid:3306/shop" //nolint:gosec // G101: an obviously fake fixture value
	e := newEnv(t, map[string]string{propsFile: "live.jdbc.url=" + url + "\n"})
	scanner := &needleScanner{tool: "opengrep", rule: "properties-url", category: finding.CategoryStorage, kind: finding.KindURL, needle: url}
	if err := e.handle(t.Context(), scanner); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	if len(got) != 4 {
		t.Fatalf("Handle() findings = %+v, want four components", got)
	}
	for _, f := range got {
		if f.Key != "live.jdbc.url" {
			t.Errorf("Handle() %s key = %q, want %q", f.Kind, f.Key, "live.jdbc.url")
		}
	}
}

func TestHandle_KeyNeverHoldsValueFoundElsewhere(t *testing.T) {
	e := newEnv(t, map[string]string{
		"a.properties": "acct=shopadmin\n",
		"b.properties": "shopadmin.password=" + password + "\n",
	})
	user := &needleScanner{tool: "opengrep", rule: "properties-user", category: finding.CategoryStorage, kind: finding.KindAccount, needle: "shopadmin"}
	if err := e.handle(t.Context(), passwordOG(), user); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	for _, f := range e.result(t).Findings {
		if f.File == "b.properties" && f.Fingerprint == fingerprintOf(password) && f.Key != "" {
			t.Errorf("Handle() key of the password in b.properties = %q, want none", f.Key)
		}
	}
}

func TestHandle_ValueMismatchDropped(t *testing.T) {
	tests := []struct {
		name    string
		scanner *needleScanner
	}{
		{name: "betterleaks value differs", scanner: &needleScanner{
			tool: "betterleaks", rule: "generic-password", category: finding.CategoryOther, kind: finding.KindOther, needle: "no such value",
			extra: []scan.Raw{{
				Tool: "betterleaks", Rule: "generic-password", Category: finding.CategoryOther, Kind: finding.KindOther,
				File: propsFile, Offset: -1, EndOffset: -1, Line: 1, ByteCol: 15, Value: scan.Value("dev-only-other-value"),
			}},
		}},
		{name: "betterleaks line past the end", scanner: &needleScanner{
			tool: "betterleaks", rule: "generic-password", category: finding.CategoryOther, kind: finding.KindOther, needle: "no such value",
			extra: []scan.Raw{{
				Tool: "betterleaks", Rule: "generic-password", Category: finding.CategoryOther, Kind: finding.KindOther,
				File: propsFile, Offset: -1, EndOffset: -1, Line: 9, ByteCol: 1, Value: scan.Value(password),
			}},
		}},
		{name: "opengrep offsets past the end", scanner: &needleScanner{
			tool: "opengrep", rule: "properties-password", category: finding.CategoryStorage, kind: finding.KindPassword, needle: "no such value",
			extra: []scan.Raw{{
				Tool: "opengrep", Rule: "properties-password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
				File: propsFile, Offset: 15, EndOffset: 500,
			}},
		}},
		{name: "dropped by the scanner", scanner: &needleScanner{
			tool: "opengrep", rule: "properties-password", category: finding.CategoryStorage, kind: finding.KindPassword, needle: "no such value",
			dropped: 1,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
			if err := e.handle(t.Context(), tt.scanner); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			got := e.result(t)
			if len(got.Findings) != 0 || got.Dropped != 1 {
				t.Errorf("Handle(%s) = %d findings, %d dropped; want none stored and 1 dropped", tt.name, len(got.Findings), got.Dropped)
			}
			if e.orig.puts != 0 {
				t.Errorf("Handle(%s) put %d pending values, want none", tt.name, e.orig.puts)
			}
		})
	}
}

func TestHandle_CRLFAndEUCKR(t *testing.T) {
	text := "# 주문 설정\r\n이름=상점 jdbc.password=" + password + "\r\n# 끝\r\n"
	raw, err := korean.EUCKR.NewEncoder().String(text)
	if err != nil {
		t.Fatalf("encoding EUC-KR: %v", err)
	}
	e := newEnv(t, map[string]string{propsFile: raw})
	bl, og := passwordBL(), passwordOG()

	if err := e.handle(t.Context(), bl, og); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	// The value is ASCII, so the fakes also find it in the EUC-KR bytes of
	// the project folder; those results are at EUC-KR offsets and must be
	// left out, not stored or counted as dropped.
	if got.Dropped != 0 || len(got.Findings) != 1 || len(got.Findings[0].Locations) != 1 {
		t.Fatalf("Handle() = %+v, want one finding at one location and nothing dropped", got)
	}
	col := len([]rune("이름=상점 jdbc.password=")) + 1
	want := finding.Location{
		StartLine: 2, StartCol: col, EndLine: 2, EndCol: col + len(password) - 1,
		Context: []string{"# 주문 설정", "이름=상점 jdbc.password=****", "# 끝"}, ContextFirstLine: 1,
		FileHash: hashOf(raw),
		Detections: []finding.Detection{
			{Tool: "betterleaks", Rule: "generic-password"},
			{Tool: "opengrep", Rule: "properties-password"},
		},
	}
	if diff := cmp.Diff(want, got.Findings[0].Locations[0]); diff != "" {
		t.Errorf("Handle() location (-want +got):\n%s", diff)
	}
	if dirs := og.scannedDirs(); len(dirs) != 2 || dirs[0] != e.dir || !strings.HasPrefix(dirs[1], e.tempDir+string(filepath.Separator)) {
		t.Errorf("opengrep scanned %v, want the project folder and then a copy folder under %s", dirs, e.tempDir)
	}
}

func TestHandle_BOM(t *testing.T) {
	content := "\uFEFFdb.password=" + password + "\r\ndb.password=" + password + "\r\n"
	e := newEnv(t, map[string]string{propsFile: content})
	if err := e.handle(t.Context(), passwordBL(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 0 || len(got.Findings) != 1 || len(got.Findings[0].Locations) != 2 {
		t.Fatalf("Handle() = %+v, want one finding at two locations and nothing dropped", got)
	}
	for i, want := range [][2]int{{1, 13}, {2, 13}} {
		l := got.Findings[0].Locations[i]
		if l.StartLine != want[0] || l.StartCol != want[1] || len(l.Detections) != 2 {
			t.Errorf("location %d = %+v, want line %d column %d found by both tools", i, l, want[0], want[1])
		}
	}
	if string(e.orig.pending[fingerprintOf(password)]) != password {
		t.Errorf("Handle() parked %q, want exactly the password", e.orig.pending[fingerprintOf(password)])
	}
}

func TestHandle_EUCKRCopyRemoved(t *testing.T) {
	raw, err := korean.EUCKR.NewEncoder().String("# 설정\njdbc.password=" + password + "\n")
	if err != nil {
		t.Fatalf("encoding EUC-KR: %v", err)
	}
	tests := []struct {
		name    string
		scanner func(cancel context.CancelFunc, e *env) *needleScanner
		wantErr error
	}{
		{name: "success", scanner: func(context.CancelFunc, *env) *needleScanner { return passwordOG() }},
		{name: "scanner fails on the copy", scanner: func(_ context.CancelFunc, e *env) *needleScanner {
			s := passwordOG()
			s.onScan = func(dir string) {
				if dir != e.dir {
					s.err = fmt.Errorf("scanning: opengrep exited with code 2: %w", scan.ErrScannerFailed)
				}
			}
			return s
		}},
		{name: "cancelled during the copy scan", wantErr: context.Canceled, scanner: func(cancel context.CancelFunc, e *env) *needleScanner {
			s := passwordOG()
			s.onScan = func(dir string) {
				if dir != e.dir {
					cancel()
				}
			}
			return s
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, map[string]string{propsFile: raw, "plain.txt": "ascii only\n"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s := tt.scanner(cancel, e)

			err := e.handle(ctx, s)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Handle(%s) error = %v, want %v", tt.name, err, tt.wantErr)
			}
			if dirs := s.scannedDirs(); len(dirs) != 2 {
				t.Fatalf("scanner ran on %v, want the project folder and its copy folder", dirs)
			}
			if names := e.tempEntries(t); len(names) != 0 {
				t.Errorf("temporary folder after Handle(%s) holds %v, want it empty", tt.name, names)
			}
		})
	}
}

func TestHandle_CustomerDataHasNoContextNoOriginal(t *testing.T) {
	content := "2026-11-19 order by " + phone + " ok\n"
	e := newEnv(t, map[string]string{"logs/app.log": content})
	s := &needleScanner{tool: "betterleaks", rule: "customer-phone", category: finding.CategoryCustomerData, kind: finding.KindPhone, needle: phone, lineCol: true}

	if err := e.handle(t.Context(), s); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	if len(got) != 1 {
		t.Fatalf("Handle() findings = %+v, want one", got)
	}
	f := got[0]
	if f.HasOriginal || f.Category != finding.CategoryCustomerData || f.Kind != finding.KindPhone || f.Fingerprint != fingerprintOf(phone) {
		t.Errorf("Handle() finding = %+v, want customer data, phone, fingerprinted, without an original", f)
	}
	if strings.Contains(f.Preview, "0000") || strings.Contains(f.Preview, "1234") {
		t.Errorf("Handle() preview = %q, want no digits of the number past the first group", f.Preview)
	}
	if l := f.Locations[0]; l.Context != nil || l.ContextFirstLine != 0 || l.StartLine != 1 || l.StartCol != 21 {
		t.Errorf("Handle() location = %+v, want line 1 column 21 without surrounding lines", l)
	}
	if e.orig.puts != 0 {
		t.Errorf("Handle() put %d pending values for customer data, want none", e.orig.puts)
	}
}

func TestHandle_ContextMasksOtherFindings(t *testing.T) {
	content := "db.user=" + account + "\ndb.password=" + password + "\ndb.url=" + hostURL + "\n"
	e := newEnv(t, map[string]string{propsFile: content})
	scanners := []scan.Scanner{
		passwordOG(),
		&needleScanner{tool: "opengrep", rule: "properties-user", category: finding.CategoryStorage, kind: finding.KindAccount, needle: account},
		&needleScanner{tool: "opengrep", rule: "properties-url", category: finding.CategoryStorage, kind: finding.KindURL, needle: hostURL},
		// Overlaps the end of the password and the start of the next line.
		&needleScanner{tool: "betterleaks", rule: "overlap", category: finding.CategoryOther, kind: finding.KindOther, needle: "word-1\ndb.url", lineCol: true},
	}
	if err := e.handle(t.Context(), scanners...); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	if len(got) != 4 {
		t.Fatalf("Handle() found %d findings, want 4: %+v", len(got), got)
	}
	var pw *finding.Finding
	for i := range got {
		if got[i].Fingerprint == fingerprintOf(password) {
			pw = &got[i]
		}
		for _, l := range got[i].Locations {
			if v := containsAny(strings.Join(l.Context, "\n"), password, account, hostURL, "word-1"); v != "" {
				t.Errorf("surrounding lines of %s line %d = %q, hold %q", got[i].Kind, l.StartLine, l.Context, v)
			}
		}
	}
	if pw == nil {
		t.Fatalf("Handle() findings = %+v, want one for the password", got)
	}
	want := []string{"db.user=****", "db.password=****", "=****"}
	if diff := cmp.Diff(want, pw.Locations[0].Context); diff != "" {
		t.Errorf("surrounding lines of the password (-want +got):\n%s", diff)
	}
}

func TestHandle_KeyFile(t *testing.T) {
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\r\ndev-only-fake-key-material\r\n-----END OPENSSH PRIVATE KEY-----\r\n"
	e := newEnv(t, map[string]string{"deploy/id_rsa": key})
	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	want := []finding.Finding{{
		Fingerprint: fingerprintOf(key), File: "deploy/id_rsa", Category: finding.CategorySSH, Kind: finding.KindKeyFile,
		Preview: "****", HasOriginal: true,
		Locations: []finding.Location{{
			StartLine: 1, StartCol: 1, EndLine: 3, EndCol: 35, FileHash: hashOf(key),
			Detections: []finding.Detection{{Tool: "keyfile", Rule: "key-file-name"}},
		}},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Handle() findings (-want +got):\n%s", diff)
	}
	if string(e.orig.pending[fingerprintOf(key)]) != key {
		t.Errorf("Handle() parked %q, want the original bytes of the key file", e.orig.pending[fingerprintOf(key)])
	}
}

func TestHandle_BinaryKeyStore(t *testing.T) {
	store := "\xfe\xed\xfe\xed\x00\x00\x00\x02dev-only-fake-keystore"
	e := newEnv(t, map[string]string{"conf/server.jks": store})
	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Skipped != 1 || len(got.Findings) != 1 || got.Findings[0].Kind != finding.KindKeyFile {
		t.Fatalf("Handle() = %+v, want the key store skipped as text and reported as a key file", got)
	}
	l := got.Findings[0].Locations[0]
	if l.StartLine != 1 || l.StartCol != 1 || l.EndLine != 1 || l.EndCol != 1 || l.Context != nil || l.FileHash != hashOf(store) {
		t.Errorf("Handle() location = %+v, want line 1 column 1 with the file's hash and no surrounding lines", l)
	}
	if string(e.orig.pending[fingerprintOf(store)]) != store {
		t.Errorf("Handle() did not park the original bytes of the key store")
	}
}

func TestHandle_FingerprintFails(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
	e.fps.err = errors.New("fingerprinting values: openbao returned status 503")

	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got := e.failure(t); got != reasonSecretStore {
		t.Errorf("Handle() failure reason = %q, want %q", got, reasonSecretStore)
	}
	if len(e.store.results) != 0 || e.orig.puts != 0 {
		t.Errorf("Handle() saved %d results and put %d values, want neither", len(e.store.results), e.orig.puts)
	}
}

func TestHandle_PutPendingFails(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
	e.orig.putErr = errors.New("storing a pending value: openbao returned status 503")

	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got := e.failure(t); got != reasonSecretStore {
		t.Errorf("Handle() failure reason = %q, want %q", got, reasonSecretStore)
	}
	if len(e.store.results) != 0 {
		t.Errorf("Handle() saved %d results, want none", len(e.store.results))
	}
}

func TestHandle_ScannerFails(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
	bl := passwordBL()
	bl.err = fmt.Errorf("scanning: betterleaks exited with code 1: %w", scan.ErrScannerFailed)

	if err := e.handle(t.Context(), passwordOG(), bl); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := e.failure(t), "betterleaks could not finish; see the worker log"; got != want {
		t.Errorf("Handle() failure reason = %q, want %q", got, want)
	}
	if len(e.store.results) != 0 || e.orig.puts != 0 || e.fps.calls != 0 {
		t.Errorf("Handle() saved %d results, put %d values, fingerprinted %d times; want none", len(e.store.results), e.orig.puts, e.fps.calls)
	}
}

func TestHandle_DeletesStalePending(t *testing.T) {
	stale := strings.Repeat("e", 64)
	for _, deleteFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete fails %v", deleteFails), func(t *testing.T) {
			e := newEnv(t, map[string]string{
				propsFile:      "jdbc.password=" + password + "\n",
				"logs/app.log": "call " + phone + "\n",
			})
			e.orig.pending[stale] = []byte("dev-only-old-value")
			e.orig.pending[fingerprintOf(phone)] = []byte(phone)
			if deleteFails {
				e.orig.deleteErr = errors.New("deleting a pending value: openbao returned status 503")
			}
			customer := &needleScanner{tool: "betterleaks", rule: "customer-phone", category: finding.CategoryCustomerData, kind: finding.KindPhone, needle: phone, lineCol: true}

			if err := e.handle(t.Context(), passwordOG(), customer); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if len(e.store.got) != 1 || e.store.got[0].status != scan.StatusSucceeded {
				t.Fatalf("Handle() wrote %+v, want the scan succeeded", e.store.got)
			}
			if deleteFails {
				if !strings.Contains(e.logs.String(), "removing stale pending values failed") {
					t.Errorf("Handle() log = %s, want the failed clean-up logged", e.logs)
				}
				return
			}
			// The customer data fingerprint has no original to keep.
			want := []string{fingerprintOf(phone), stale}
			if fingerprintOf(phone) > stale {
				want = []string{stale, fingerprintOf(phone)}
			}
			if diff := cmp.Diff(want, e.orig.deleted); diff != "" {
				t.Errorf("Handle() deleted (-want +got):\n%s", diff)
			}
			if _, ok := e.orig.pending[fingerprintOf(password)]; !ok {
				t.Errorf("Handle() removed the pending value of this scan's password")
			}
			if diff := cmp.Diff([]string{"settled", "put", "save", "delete", "delete"}, e.events.all()); diff != "" {
				t.Errorf("Handle() calls (-want +got):\n%s", diff)
			}
		})
	}
}

// accountOG finds account the way opengrep reports values.
func accountOG() *needleScanner {
	return &needleScanner{tool: "opengrep", rule: "properties-user", category: finding.CategoryStorage, kind: finding.KindAccount, needle: account}
}

// hasOriginal returns HasOriginal of each finding of r, by fingerprint.
func hasOriginal(r scan.Result) map[string]bool {
	out := map[string]bool{}
	for _, f := range r.Findings {
		out[f.Fingerprint] = f.HasOriginal
	}
	return out
}

func TestHandle_DropsOriginalsOfFalsePositives(t *testing.T) {
	files := map[string]string{propsFile: "jdbc.password=" + password + "\njdbc.user=" + account + "\n"}
	fpPassword, fpAccount := fingerprintOf(password), fingerprintOf(account)

	t.Run("settled", func(t *testing.T) {
		e := newEnv(t, files)
		e.store.settled = []string{fpPassword}
		// An earlier scan parked the original before the verdict.
		e.orig.pending[fpPassword] = []byte(password)

		if err := e.handle(t.Context(), passwordOG(), accountOG()); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		if diff := cmp.Diff(map[string]bool{fpPassword: false, fpAccount: true}, hasOriginal(e.result(t))); diff != "" {
			t.Errorf("Handle() HasOriginal by fingerprint (-want +got):\n%s", diff)
		}
		if e.orig.puts != 1 {
			t.Errorf("Handle() put %d values, want 1 (the account)", e.orig.puts)
		}
		if diff := cmp.Diff([]string{fpPassword}, e.orig.deleted); diff != "" {
			t.Errorf("Handle() deleted (-want +got):\n%s", diff)
		}
		if _, ok := e.orig.pending[fpAccount]; !ok || len(e.orig.pending) != 1 {
			t.Errorf("Handle() left %d pending values, want only the account's", len(e.orig.pending))
		}
		if diff := cmp.Diff([]string{"settled", "put", "save", "delete"}, e.events.all()); diff != "" {
			t.Errorf("Handle() calls (-want +got):\n%s", diff)
		}
	})
	t.Run("not read", func(t *testing.T) {
		e := newEnv(t, files)
		e.store.settledErr = errors.New("listing settled fingerprints of project 7: connection refused")

		if err := e.handle(t.Context(), passwordOG(), accountOG()); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		if diff := cmp.Diff(map[string]bool{fpPassword: true, fpAccount: true}, hasOriginal(e.result(t))); diff != "" {
			t.Errorf("Handle() HasOriginal by fingerprint (-want +got):\n%s", diff)
		}
		if e.orig.puts != 2 || len(e.orig.deleted) != 0 {
			t.Errorf("Handle() put %d values and deleted %v, want 2 and none", e.orig.puts, e.orig.deleted)
		}
		if n := strings.Count(e.logs.String(), "reading settled fingerprints failed"); n != 1 {
			t.Errorf("Handle() logged the failed read %d times, want once; log:\n%s", n, e.logs)
		}
	})
}

func TestHandle_RemovesOriginalsOfAppliedValues(t *testing.T) {
	// An apply replaced this value by its placeholder, so the scan no
	// longer finds it.
	applied := fingerprintOf("dev-only-fake-applied-value")
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\njdbc.old=${ext.db.old}\n"})
	e.orig.pending[applied] = []byte("dev-only-fake-applied-value")
	e.orig.pending[fingerprintOf(password)] = []byte(password)

	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if diff := cmp.Diff([]string{applied}, e.orig.deleted); diff != "" {
		t.Errorf("Handle() deleted (-want +got):\n%s", diff)
	}
	if _, ok := e.orig.pending[fingerprintOf(password)]; !ok {
		t.Errorf("Handle() removed the pending value of this scan's password")
	}
}

func TestHandle_SkippedFiles(t *testing.T) {
	e := newEnv(t, map[string]string{
		propsFile:        "jdbc.password=" + password + "\n",
		"big.txt":        strings.Repeat("a", scan.DefaultMaxFileBytes+1),
		"image.png":      "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR",
		"utf16.txt":      "\xff\xfeh\x00i\x00",
		"nested/ok.java": "class A {}\n",
	})
	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Files != 5 || got.Skipped != 3 || len(got.Findings) != 1 {
		t.Errorf("Handle() = %d files, %d skipped, %d findings; want 5 files, 3 skipped, 1 finding", got.Files, got.Skipped, len(got.Findings))
	}
}

func TestHandle_UnreadableSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files without permission")
	}
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n", "secret.env": "x", "closed/a.txt": "x"})
	for _, p := range []string{filepath.Join(e.dir, "secret.env"), filepath.Join(e.dir, "closed")} {
		if err := os.Chmod(p, 0); err != nil {
			t.Fatalf("Chmod(%q) error = %v", p, err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o700) }) //nolint:gosec // G302: a test folder made readable again so TempDir can remove it; a failure shows there
	}
	if err := e.handle(t.Context(), passwordOG()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Files != 2 || got.Skipped != 2 || len(got.Findings) != 1 {
		t.Errorf("Handle() = %d files, %d skipped, %d findings; want 2 files, 2 skipped (a file and a folder), 1 finding", got.Files, got.Skipped, len(got.Findings))
	}
}

func TestHandle_ResultOutsideFileListIgnored(t *testing.T) {
	e := newEnv(t, map[string]string{
		"vendor/lib.properties": "p=" + password + "\n",
		"vendor/id_rsa":         "dev-only-fake-key\n",
		"build/out.properties":  "p=" + password + "\n",
	})
	e.store.project.Excludes = []string{"vendor"}
	outside := filepath.Join(filepath.Dir(e.root), "outside.properties")
	writeFile(t, outside, "p="+password+"\n")
	if err := os.Symlink(outside, filepath.Join(e.dir, "link.properties")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	s := passwordOG()
	s.extra = []scan.Raw{{
		Tool: "opengrep", Rule: "properties-password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
		File: "link.properties", Offset: 2, EndOffset: 2 + len(password),
	}}

	if err := e.handle(t.Context(), s); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	// vendor/lib.properties, build/out.properties, link.properties, and the
	// key file under vendor are left out without counting them as dropped.
	if len(got.Findings) != 0 || got.Dropped != 0 || got.Files != 0 {
		t.Errorf("Handle() = %+v, want no findings, none dropped, no files listed", got)
	}
}

func TestHandle_ScannerIgnoreFileFails(t *testing.T) {
	for _, name := range []string{".gitleaksignore", ".betterleaksignore", ".GitLeaksIgnore"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n", name: "conf/db.properties:generic-password:1\n"})
			s := passwordBL()
			if err := e.handle(t.Context(), s); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if got := e.failure(t); got != reasonIgnoreFile {
				t.Errorf("Handle(%s at the root) failure reason = %q, want %q", name, got, reasonIgnoreFile)
			}
			if dirs := s.scannedDirs(); len(dirs) != 0 {
				t.Errorf("Handle(%s at the root) ran a scanner on %v, want none", name, dirs)
			}
		})
	}
	t.Run("below the root", func(t *testing.T) {
		e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n", "conf/.gitleaksignore": "x\n"})
		if err := e.handle(t.Context(), passwordBL()); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		if got := e.result(t); len(got.Findings) != 1 {
			t.Errorf("Handle(.gitleaksignore below the root) = %+v, want the scan to run", got)
		}
	})
}

func TestHandle_TimeLimit(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
	e.opts.TimeLimit = time.Nanosecond

	err := e.handle(t.Context(), &blockingScanner{})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil after failing the scan", err)
	}
	if got := e.failure(t); got != reasonTimeLimit {
		t.Errorf("Handle() failure reason = %q, want %q", got, reasonTimeLimit)
	}
	if names := e.tempEntries(t); len(names) != 0 {
		t.Errorf("temporary folder after the time limit holds %v, want it empty", names)
	}
}

// blockingScanner runs until its context ends.
type blockingScanner struct{}

func (*blockingScanner) Name() string { return "opengrep" }

func (*blockingScanner) Version(context.Context) (string, error) { return "fake-1", nil }

func (*blockingScanner) Scan(ctx context.Context, _ string) ([]scan.Raw, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("scanning: %w", ctx.Err())
}

func TestHandle_CommitHash(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	e := newEnv(t, map[string]string{"a.txt": "x\n"})
	e.opts.Git = git
	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle(no repository) error = %v", err)
	}
	if got := e.result(t).CommitHash; got != "" {
		t.Errorf("Handle(no repository) commit = %q, want empty", got)
	}

	worktreetest.Init(t, e.dir)
	head := worktreetest.Git(t, e.dir, "rev-parse", "HEAD")
	e.store.results = nil
	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle(repository) error = %v", err)
	}
	if got, want := e.result(t).CommitHash, strings.TrimSpace(head); got != want {
		t.Errorf("Handle(repository) commit = %q, want %q", got, want)
	}
}

func TestHandle_NoRawValueInLogs(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\ndev-only-fake-rsa-material\n-----END RSA PRIVATE KEY-----\n" //nolint:gosec // G101: an obviously fake fixture value
	legacy, err := korean.EUCKR.NewEncoder().String("# 주문 설정\r\n이름=상점 url=" + hostURL + "\r\n# 끝\r\n")
	if err != nil {
		t.Fatalf("encoding EUC-KR: %v", err)
	}
	files := map[string]string{
		// dev-only-other-value is on line 3, but reported at line 1.
		propsFile:                "db.user=" + account + "\ndb.password=" + password + "\ndb.other=dev-only-other-value\n",
		"conf/legacy.properties": legacy,
		"logs/app.log":           "call " + phone + "\n",
		"deploy/id_rsa":          key,
		// crossValue is reported in cross.properties only; in Dao.java it is
		// next to the password, where no scanner reports it.
		"conf/cross.properties": "cross.user=" + crossValue + "\n",
		"src/Dao.java":          "x=1\nuser(\"" + crossValue + "\")\npw=\"" + password + "\"\n",
	}
	values := []string{password, account, phone, hostURL, "dev-only-fake-rsa-material", "dev-only-other-value", encodedBlob, crossValue}
	scanners := func() []scan.Scanner {
		mismatch := passwordBL()
		mismatch.extra = []scan.Raw{{
			Tool: "betterleaks", Rule: "generic-password", Category: finding.CategoryOther, Kind: finding.KindOther,
			File: propsFile, Offset: -1, EndOffset: -1, Line: 1, ByteCol: 1, Value: scan.Value("dev-only-other-value"),
		}}
		return []scan.Scanner{
			mismatch, passwordOG(),
			onlyAt(reportedAt(t, "conf/cross.properties", files["conf/cross.properties"], crossValue)),
			&needleScanner{tool: "opengrep", rule: "properties-user", category: finding.CategoryStorage, kind: finding.KindAccount, needle: account},
			&needleScanner{tool: "opengrep", rule: "properties-url", category: finding.CategoryStorage, kind: finding.KindURL, needle: hostURL},
			&needleScanner{tool: "betterleaks", rule: "customer-phone", category: finding.CategoryCustomerData, kind: finding.KindPhone, needle: phone, lineCol: true},
		}
	}
	var logs bytes.Buffer
	cases := []struct {
		name  string
		setup func(*env)
		extra scan.Scanner
	}{
		{name: "success"},
		{name: "fingerprint fails", setup: func(e *env) { e.fps.err = errors.New("fingerprinting values: refused") }},
		{name: "put fails", setup: func(e *env) { e.orig.putErr = errors.New("storing a pending value: refused") }},
		{name: "list fails", setup: func(e *env) { e.orig.listErr = errors.New("listing pending values: refused") }},
		{name: "delete fails", setup: func(e *env) {
			e.orig.pending[strings.Repeat("e", 64)] = []byte("x")
			e.orig.deleteErr = errors.New("deleting a pending value: refused")
		}},
		{name: "save fails", setup: func(e *env) { e.store.saveErr = errors.New("storing finding 0: refused") }},
		{name: "lease lost", setup: func(e *env) { e.store.saveErr = job.ErrLeaseLost }},
		{name: "scanner fails", extra: &needleScanner{tool: "opengrep", needle: "x", err: fmt.Errorf("scanning: opengrep exited with code 2: %w", scan.ErrScannerFailed)}},
		{name: "time limit", setup: func(e *env) { e.opts.TimeLimit = time.Nanosecond }, extra: &blockingScanner{}},
		{name: "value that cannot be located", setup: func(e *env) {
			writeFile(t, filepath.Join(e.dir, "conf", "encoded.properties"), "a.user=dev\nblob="+encodedBlob+"\njdbc.password="+password+"\n")
		}, extra: unlocatable("conf/encoded.properties")},
	}
	saved := 0
	for _, c := range cases {
		e := newEnv(t, files)
		e.logs = &logs
		if c.setup != nil {
			c.setup(e)
		}
		all := scanners()
		if c.extra != nil {
			all = append(all, c.extra)
		}
		err := e.handle(t.Context(), all...)
		if err != nil {
			if v := containsAny(err.Error(), values...); v != "" {
				t.Errorf("Handle(%s) error = %v, holds %q", c.name, err, v)
			}
		}
		for _, o := range e.store.got {
			if v := containsAny(o.reason, values...); v != "" {
				t.Errorf("Handle(%s) failure reason %q holds %q", c.name, o.reason, v)
			}
		}
		for _, r := range e.store.results {
			saved++
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("json.Marshal(result) error = %v", err)
			}
			for _, dump := range []string{fmt.Sprintf("%+v", r), string(encoded)} {
				if v := containsAny(dump, values...); v != "" {
					t.Errorf("Handle(%s) saved a result that holds %q:\n%s", c.name, v, dump)
				}
			}
		}
	}
	if saved == 0 {
		t.Fatal("no scenario saved a result, want the results searched too")
	}
	if logs.Len() == 0 {
		t.Fatal("the runner logged nothing, want at least the failures")
	}
	if v := containsAny(logs.String(), values...); v != "" {
		t.Errorf("the log holds %q:\n%s", v, logs.String())
	}
}

func TestHandle_FailureReasons(t *testing.T) {
	refused := errors.New("dial tcp 10.0.0.5:5432: refused")
	tests := []struct {
		name  string
		path  string
		store func(*fakeStore)
		want  string
	}{
		{name: "outside root", path: "escape", want: reasonOutside},
		{name: "missing folder", path: "nope", want: reasonNotDirectory},
		{name: "a file", path: "file.txt", want: reasonNotDirectory},
		{name: "through a file", path: "file.txt/sub", want: reasonNotDirectory},
		{name: "store error on start", path: "shop", store: func(f *fakeStore) { f.startErr = refused }, want: reasonOther},
		{name: "store error on scan", path: "shop", store: func(f *fakeStore) { f.scanErr = refused }, want: reasonOther},
		{name: "store error on project", path: "shop", store: func(f *fakeStore) { f.projectErr = refused }, want: reasonOther},
		{name: "store error on save", path: "shop", store: func(f *fakeStore) { f.saveErr = refused }, want: reasonOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, map[string]string{"a.txt": "x"})
			outside := filepath.Join(filepath.Dir(e.root), "outside")
			writeFile(t, filepath.Join(outside, "secret.txt"), "x")
			if err := os.Symlink(outside, filepath.Join(e.root, "escape")); err != nil {
				t.Fatalf("Symlink error = %v", err)
			}
			writeFile(t, filepath.Join(e.root, "file.txt"), "x")
			e.store.project.Path = tt.path
			if tt.store != nil {
				tt.store(e.store)
			}
			if err := e.handle(t.Context()); err != nil {
				t.Fatalf("Handle(path %q) error = %v", tt.path, err)
			}
			if got := e.failure(t); got != tt.want {
				t.Errorf("Handle(path %q) failure reason = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestHandle_OutsideRootReadsNothing(t *testing.T) {
	e := newEnv(t, nil)
	outside := filepath.Join(filepath.Dir(e.root), "outside")
	writeFile(t, filepath.Join(outside, "db.properties"), "p="+password+"\n")
	if err := os.Symlink(outside, filepath.Join(e.root, "escape")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	e.store.project.Path = "escape"
	s := passwordOG()

	if err := e.handle(t.Context(), s); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got := e.failure(t); got != reasonOutside {
		t.Errorf("Handle(project through a link to outside) reason = %q, want %q", got, reasonOutside)
	}
	if dirs := s.scannedDirs(); len(dirs) != 0 {
		t.Errorf("Handle(project through a link to outside) scanned %v, want nothing", dirs)
	}
}

func TestHandle_FailureReasonHidesError(t *testing.T) {
	e := newEnv(t, nil)
	e.store.startErr = errors.New("dial tcp 10.0.0.5:5432: connection refused")

	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got := e.failure(t); strings.Contains(got, "10.0.0.5") || strings.Contains(got, "dial") {
		t.Errorf("Handle() failure reason = %q, want one without the store error text", got)
	}
	if n := strings.Count(e.logs.String(), "10.0.0.5"); n != 1 {
		t.Errorf("Handle() logged the store error %d times, want once; log:\n%s", n, e.logs.String())
	}
}

func TestHandle_LeaseLostWritesNothingElse(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "jdbc.password=" + password + "\n"})
	e.orig.pending[strings.Repeat("e", 64)] = []byte("x")
	e.store.saveErr = job.ErrLeaseLost

	err := e.handle(t.Context(), passwordOG())
	if !errors.Is(err, job.ErrLeaseLost) {
		t.Errorf("Handle() with a lost lease error = %v, want ErrLeaseLost", err)
	}
	if len(e.store.got) != 0 || len(e.orig.deleted) != 0 {
		t.Errorf("Handle() with a lost lease wrote %+v and deleted %v, want nothing", e.store.got, e.orig.deleted)
	}
	if names := e.tempEntries(t); len(names) != 0 {
		t.Errorf("temporary folder after a lost lease holds %v, want it empty", names)
	}
}

func TestHandle_CancelledLeavesScanRunning(t *testing.T) {
	e := newEnv(t, map[string]string{"a.txt": "x"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := e.handle(ctx, passwordOG())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Handle(cancelled) error = %v, want context.Canceled", err)
	}
	if len(e.store.got) != 0 {
		t.Errorf("Handle(cancelled) wrote %+v, want nothing so the scan is retried", e.store.got)
	}
}

func TestHandle_CountsListedFiles(t *testing.T) {
	e := newEnv(t, map[string]string{"a.txt": "x", "b.txt": "x", "sub/c.txt": "x"})
	// A symbolic link is not a regular file and is not followed.
	if err := os.Symlink(filepath.Join(e.dir, "a.txt"), filepath.Join(e.dir, "link")); err != nil {
		t.Fatalf("Symlink error = %v", err)
	}
	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	want := []outcome{{status: scan.StatusSucceeded, files: 3}}
	if diff := cmp.Diff(want, e.store.got, cmp.AllowUnexported(outcome{})); diff != "" {
		t.Errorf("Handle() wrote (-want +got):\n%s", diff)
	}
}

func TestHandle_RelativeTempDirFails(t *testing.T) {
	e := newEnv(t, map[string]string{"a.txt": "x"})
	e.opts.TempDir = ""
	t.Chdir(e.root)
	// A folder a relative copies path would name: it must not be touched.
	writeFile(t, filepath.Join(e.root, "5", "keep.txt"), "x")

	if err := e.handle(t.Context()); err != nil {
		t.Fatalf("Handle(no temporary folder) error = %v", err)
	}
	if got := e.failure(t); got != reasonOther {
		t.Errorf("Handle(no temporary folder) failure reason = %q, want %q", got, reasonOther)
	}
	if _, err := os.Stat(filepath.Join(e.root, "5", "keep.txt")); err != nil {
		t.Errorf("Handle(no temporary folder) removed a folder in the working directory: %v", err)
	}
}

func TestHandle_ContextMasksUnplacedValues(t *testing.T) {
	const other = "dev-only-unplaced-value-9"
	t.Run("betterleaks value not at its column", func(t *testing.T) {
		e := newEnv(t, map[string]string{propsFile: "a.password=" + password + "\nb.secret=" + other + "\n"})
		bl := &needleScanner{
			tool: "betterleaks", rule: "generic-password", category: finding.CategoryOther, kind: finding.KindOther, needle: "no such value",
			// The value is on line 2, but reported at line 2, column 1.
			extra: []scan.Raw{{
				Tool: "betterleaks", Rule: "generic-password", Category: finding.CategoryOther, Kind: finding.KindOther,
				File: propsFile, Offset: -1, EndOffset: -1, Line: 2, ByteCol: 1, Value: scan.Value(other),
			}},
		}
		if err := e.handle(t.Context(), passwordOG(), bl); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		got := e.result(t)
		if got.Dropped != 1 || len(got.Findings) != 1 {
			t.Fatalf("Handle() = %+v, want the password found and the misplaced value dropped", got)
		}
		if l := got.Findings[0].Locations[0]; l.Context != nil {
			t.Errorf("surrounding lines of the password = %q, want none in a file with a result that cannot be placed", l.Context)
		}
		if dump := fmt.Sprintf("%+v", got); strings.Contains(dump, other) {
			t.Errorf("Handle() saved a result that holds %q:\n%s", other, dump)
		}
	})
	t.Run("opengrep range off a character boundary", func(t *testing.T) {
		e := newEnv(t, map[string]string{propsFile: "a.password=" + password + "\nb=비밀값\n"})
		start := len("a.password=" + password + "\nb=")
		og := &needleScanner{
			tool: "opengrep", rule: "properties-password", category: finding.CategoryStorage, kind: finding.KindPassword, needle: "no such value",
			// Starts one byte into the first Hangul character.
			extra: []scan.Raw{{
				Tool: "opengrep", Rule: "properties-password", Category: finding.CategoryStorage, Kind: finding.KindPassword,
				File: propsFile, Offset: start + 1, EndOffset: start + len("비밀값"),
			}},
		}
		if err := e.handle(t.Context(), passwordOG(), og); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		got := e.result(t)
		if got.Dropped != 1 || len(got.Findings) != 1 {
			t.Fatalf("Handle() = %+v, want the password found and the misplaced range dropped", got)
		}
		if l := got.Findings[0].Locations[0]; l.Context != nil {
			t.Errorf("surrounding lines of the password = %q, want none in a file with a result that cannot be placed", l.Context)
		}
		if dump := fmt.Sprintf("%+v", got); strings.Contains(dump, "비밀값") {
			t.Errorf("Handle() saved a result that holds the misplaced range:\n%s", dump)
		}
	})
	t.Run("whole-file result in a text file", func(t *testing.T) {
		content := "spring:\n  password: " + password + "\nkey: |\n-----BEGIN PRIVATE KEY-----\ndev-only-fake-inline-key\n-----END PRIVATE KEY-----\n"
		e := newEnv(t, map[string]string{"application.yml": content})
		if err := e.handle(t.Context(), passwordOG()); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		got := e.result(t)
		if len(got.Findings) != 2 {
			t.Fatalf("Handle() = %+v, want the password and the key file", got)
		}
		for _, f := range got.Findings {
			for _, l := range f.Locations {
				if l.Context != nil || l.ContextFirstLine != 0 {
					t.Errorf("%s location = %+v, want no surrounding lines in a file that is itself a key", f.Kind, l)
				}
			}
		}
	})
}

// encodedBlob stands for text that encodes a value a scanner reported in
// decoded form: the value itself appears nowhere in the file.
const encodedBlob = "dev-only-encoded-blob-text"

// unlocatable reports, for file, a betterleaks-style value at line 2,
// column 6 that does not occur anywhere in the file.
func unlocatable(file string) *needleScanner {
	return &needleScanner{
		tool: "betterleaks", rule: "generic-password", category: finding.CategoryOther, kind: finding.KindOther, needle: "no such value",
		extra: []scan.Raw{{
			Tool: "betterleaks", Rule: "generic-password", Category: finding.CategoryOther, Kind: finding.KindOther,
			File: file, Offset: -1, EndOffset: -1, Line: 2, ByteCol: 6, Value: scan.Value("dev-only-decoded-value"),
		}},
	}
}

func TestHandle_UnlocatableValueDropsContext(t *testing.T) {
	e := newEnv(t, map[string]string{propsFile: "a.user=dev\nblob=" + encodedBlob + "\njdbc.password=" + password + "\n"})
	if err := e.handle(t.Context(), passwordOG(), unlocatable(propsFile)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 1 || len(got.Findings) != 1 {
		t.Fatalf("Handle() = %+v, want the password found and the unlocatable value dropped", got)
	}
	if l := got.Findings[0].Locations[0]; l.Context != nil || l.ContextFirstLine != 0 {
		t.Errorf("Handle() password location = %+v, want no surrounding lines", l)
	}
	if dump := fmt.Sprintf("%+v", got); strings.Contains(dump, encodedBlob) {
		t.Errorf("Handle() saved a result that holds the encoded text:\n%s", dump)
	}
}

func TestHandle_MisplacedValueAlsoInPlainText(t *testing.T) {
	// The value is reported at line 2, where only encodedBlob is; it also
	// appears in plain text on line 3, where it is placed.
	e := newEnv(t, map[string]string{propsFile: "a.user=dev\nblob=" + encodedBlob + "\njdbc.password=" + password + "\nx=1\n"})
	misplaced := &needleScanner{
		tool: "betterleaks", rule: "generic-password", category: finding.CategoryOther, kind: finding.KindOther, needle: "no such value",
		extra: []scan.Raw{{
			Tool: "betterleaks", Rule: "generic-password", Category: finding.CategoryOther, Kind: finding.KindOther,
			File: propsFile, Offset: -1, EndOffset: -1, Line: 2, ByteCol: 6, Value: scan.Value(password),
		}},
	}
	user := &needleScanner{tool: "opengrep", rule: "properties-user", category: finding.CategoryStorage, kind: finding.KindAccount, needle: "dev\n"}
	if err := e.handle(t.Context(), passwordOG(), user, misplaced); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 1 || len(got.Findings) != 2 {
		t.Fatalf("Handle() = %+v, want two findings and the misplaced value dropped", got)
	}
	for _, f := range got.Findings {
		for _, l := range f.Locations {
			if l.Context != nil || l.ContextFirstLine != 0 {
				t.Errorf("%s location = %+v, want no surrounding lines", f.Kind, l)
			}
		}
	}
	if dump := fmt.Sprintf("%+v", got); strings.Contains(dump, encodedBlob) {
		t.Errorf("Handle() saved a result that holds the encoded text:\n%s", dump)
	}
}

func TestHandle_ScannerDroppedResultDropsContext(t *testing.T) {
	const other = "conf/other.properties"
	e := newEnv(t, map[string]string{
		propsFile: "a.user=dev\njdbc.password=" + password + "\n",
		other:     "b.user=dev\njdbc.password=" + password + "\n",
	})
	// The scanner dropped a result in propsFile; where its value is cannot
	// be told.
	dropper := &needleScanner{
		tool: "betterleaks", rule: "generic-password", category: finding.CategoryOther, kind: finding.KindOther, needle: "no such value",
		dropped: 1, droppedFiles: []string{propsFile},
	}
	if err := e.handle(t.Context(), passwordOG(), dropper); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 1 || len(got.Findings) != 2 {
		t.Fatalf("Handle() = %+v, want two findings and one dropped", got)
	}
	for _, f := range got.Findings {
		l := f.Locations[0]
		switch f.File {
		case propsFile:
			if l.Context != nil || l.ContextFirstLine != 0 {
				t.Errorf("Handle() %s location = %+v, want no surrounding lines in a file with a dropped result", f.File, l)
			}
		case other:
			if l.Context == nil {
				t.Errorf("Handle() %s location = %+v, want surrounding lines", f.File, l)
			}
		}
	}
}

func TestHandle_ScannerDroppedUnknownFileDropsAllContext(t *testing.T) {
	e := newEnv(t, map[string]string{
		propsFile:               "jdbc.password=" + password + "\n",
		"conf/other.properties": "jdbc.password=" + password + "\n",
	})
	dropper := &needleScanner{
		tool: "betterleaks", rule: "generic-password", category: finding.CategoryOther, kind: finding.KindOther, needle: "no such value",
		dropped: 1,
	}
	if err := e.handle(t.Context(), passwordOG(), dropper); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if len(got.Findings) != 2 {
		t.Fatalf("Handle() = %+v, want two findings", got)
	}
	for _, f := range got.Findings {
		for _, l := range f.Locations {
			if l.Context != nil || l.ContextFirstLine != 0 {
				t.Errorf("Handle() %s location = %+v, want no surrounding lines when a dropped result has no known file", f.File, l)
			}
		}
	}
}

func TestHandle_UnstorableFileNameSkipped(t *testing.T) {
	tests := []struct {
		name, rel, content string
	}{
		{name: "not valid UTF-8", rel: "conf/db\xff.properties", content: "jdbc.password=" + password + "\n"},
		{name: "control character", rel: "conf/db\x01.properties", content: "jdbc.password=" + password + "\n"},
		{name: "line break", rel: "conf/db\n.properties", content: "jdbc.password=" + password + "\n"},
		{name: "key store with a control character", rel: "conf/server\x1b.jks", content: "\xfe\xed\xfe\xed\x00\x00\x00\x02dev-only-fake-keystore"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, map[string]string{"conf/ok.properties": "x=1\n"})
			p := filepath.Join(e.dir, filepath.FromSlash(tt.rel))
			if err := os.WriteFile(p, []byte(tt.content), 0o600); err != nil {
				t.Skipf("the file system refuses the name %q: %v", tt.rel, err)
			}
			if err := e.handle(t.Context(), passwordOG()); err != nil {
				t.Fatalf("Handle(%q) error = %v", tt.rel, err)
			}
			got := e.result(t)
			if got.Files != 2 || got.Skipped != 1 || len(got.Findings) != 0 {
				t.Errorf("Handle(%q) = %d files, %d skipped, findings %+v; want 2 files, 1 skipped, no findings", tt.rel, got.Files, got.Skipped, got.Findings)
			}
			if e.orig.puts != 0 {
				t.Errorf("Handle(%q) put %d pending values, want none", tt.rel, e.orig.puts)
			}
		})
	}
}

func TestHandle_SplitsConnectionString(t *testing.T) {
	const (
		url       = "jdbc:mysql://shop_app:dev-only-fake-pw@db.invalid:3306/shop" //nolint:gosec // G101: an obviously fake fixture value
		urlPrefix = "    url: "
		h2        = "jdbc:h2:mem:test"
		paramURL  = "jdbc:postgresql://db.invalid/shop?user=a_user&sslpassword=dev-only-fake-ssl"
	)
	lines := []string{"# 주문 DB", "spring:", "  datasource:", urlPrefix + url}
	euckr, err := korean.EUCKR.NewEncoder().String(strings.Join(lines, "\r\n") + "\r\n")
	if err != nil {
		t.Fatalf("encoding EUC-KR: %v", err)
	}
	e := newEnv(t, map[string]string{
		"lf/application.yml":    strings.Join(lines, "\n") + "\n",
		"crlf/application.yml":  strings.Join(lines, "\r\n") + "\r\n",
		"euckr/application.yml": euckr,
		"h2/application.yml":    "spring:\n  datasource:\n" + urlPrefix + h2 + "\n",
		"param/application.yml": "spring:\n  datasource:\n" + urlPrefix + paramURL + "\n",
	})
	urlScanner := func(needle string) *needleScanner {
		return &needleScanner{tool: "opengrep", rule: "yaml-jdbc-url", category: finding.CategoryStorage, kind: finding.KindURL, needle: needle}
	}
	if err := e.handle(t.Context(), urlScanner(url), urlScanner(h2), urlScanner(paramURL)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 0 {
		t.Errorf("Handle() dropped %d results, want none", got.Dropped)
	}

	parts := []struct {
		kind  finding.Kind
		value string
	}{
		{finding.KindAccount, "shop_app"},
		{finding.KindPassword, "dev-only-fake-pw"},
		{finding.KindHost, "db.invalid"},
		{finding.KindPort, "3306"},
	}
	byFile := map[string][]finding.Finding{}
	for _, f := range got.Findings {
		byFile[f.File] = append(byFile[f.File], f)
	}
	for _, file := range []string{"lf/application.yml", "crlf/application.yml", "euckr/application.yml"} {
		fs := byFile[file]
		if len(fs) != len(parts) {
			t.Errorf("Handle() findings in %s = %+v, want one for each of %d components", file, fs, len(parts))
			continue
		}
		for _, p := range parts {
			var f *finding.Finding
			for i := range fs {
				if fs[i].Kind == p.kind {
					f = &fs[i]
				}
			}
			if f == nil || len(f.Locations) != 1 || f.Fingerprint != fingerprintOf(p.value) {
				t.Errorf("Handle() findings in %s = %+v, want one %s finding of %q at one location", file, fs, p.kind, p.value)
				continue
			}
			col := len(urlPrefix) + strings.Index(url, p.value) + 1
			l := f.Locations[0]
			if l.StartLine != 4 || l.StartCol != col || l.EndLine != 4 || l.EndCol != col+len(p.value)-1 {
				t.Errorf("%s %s location = line %d column %d to line %d column %d, want line 4 column %d to %d",
					file, p.kind, l.StartLine, l.StartCol, l.EndLine, l.EndCol, col, col+len(p.value)-1)
			}
			wantDetections := []finding.Detection{{Tool: "opengrep", Rule: "yaml-jdbc-url"}}
			if diff := cmp.Diff(wantDetections, l.Detections); diff != "" {
				t.Errorf("%s %s detections (-want +got):\n%s", file, p.kind, diff)
			}
			wantContext := []string{"spring:", "  datasource:", urlPrefix + "jdbc:mysql://****:****@****:****/shop"}
			if diff := cmp.Diff(wantContext, l.Context); diff != "" {
				t.Errorf("%s %s surrounding lines (-want +got):\n%s", file, p.kind, diff)
			}
			if string(e.orig.pending[f.Fingerprint]) != p.value {
				t.Errorf("Handle() parked %q for the %s, want %q", e.orig.pending[f.Fingerprint], p.kind, p.value)
			}
		}
	}
	if fs := byFile["param/application.yml"]; len(fs) != 3 {
		t.Errorf("Handle() findings in param/application.yml = %+v, want the host, the account, and the ssl password", fs)
	}
	for _, f := range byFile["param/application.yml"] {
		if f.Kind == finding.KindPassword && f.Fingerprint != fingerprintOf("dev-only-fake-ssl") {
			t.Errorf("Handle() password finding in param/application.yml = %+v, want the sslpassword value", f)
		}
		for _, l := range f.Locations {
			if v := containsAny(strings.Join(l.Context, "\n"), "dev-only-fake-ssl"); v != "" {
				t.Errorf("param/application.yml %s surrounding lines = %q, hold %q", f.Kind, l.Context, v)
			}
		}
	}
	if fs := byFile["h2/application.yml"]; len(fs) != 1 || fs[0].Kind != finding.KindURL || fs[0].Fingerprint != fingerprintOf(h2) {
		t.Errorf("Handle() findings in h2/application.yml = %+v, want the whole URL as one url finding", fs)
	}
}

func TestHandle_PlaceholderIsNotAFinding(t *testing.T) {
	const (
		realPW      = "dev-only-fake-pw" //nolint:gosec // G101: an obviously fake fixture value
		pwScanner   = "ssh-pass"
		urlAllHold  = "jdbc:mysql://${ext.db.main3.account}:${ext.db.main8.password}@${ext.db.main3.host}:${ext.db.main.port}/report"
		urlSomeHold = "jdbc:mysql://${ext.db.main.host}:3306/shop"
	)
	values := []string{
		"${EXT_SSH_MAIN_PASSWORD}",
		"${ext.secret.main.password}",
		"prefix-${x}",
		"${a}${b}",
		realPW,
	}
	content := "sshpass -p '" + strings.Join(values, "' -p '") + "'\nurl=" + urlAllHold + "\nurl=" + urlSomeHold + "\n"
	e := newEnv(t, map[string]string{"scripts/deploy.sh": content})
	pw := func(needle string) *needleScanner {
		return &needleScanner{tool: "opengrep", rule: pwScanner, category: finding.CategorySSH, kind: finding.KindPassword, needle: needle}
	}
	url := func(needle string) *needleScanner {
		return &needleScanner{tool: "opengrep", rule: "yaml-jdbc-url", category: finding.CategoryStorage, kind: finding.KindURL, needle: needle}
	}
	var scanners []scan.Scanner
	for _, v := range values {
		scanners = append(scanners, pw(v))
	}
	scanners = append(scanners, url(urlAllHold), url(urlSomeHold))
	if err := e.handle(t.Context(), scanners...); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 0 {
		t.Errorf("Handle() dropped %d results, want none", got.Dropped)
	}
	var gotFPs []string
	for _, f := range got.Findings {
		gotFPs = append(gotFPs, f.Fingerprint)
	}
	var wantFPs []string
	for _, v := range []string{"prefix-${x}", "${a}${b}", realPW, "3306"} {
		wantFPs = append(wantFPs, fingerprintOf(v))
	}
	slices.Sort(gotFPs)
	slices.Sort(wantFPs)
	if diff := cmp.Diff(wantFPs, gotFPs); diff != "" {
		t.Errorf("Handle() finding fingerprints (-want +got):\n%s", diff)
	}
	for _, v := range []string{"${EXT_SSH_MAIN_PASSWORD}", "${ext.secret.main.password}", urlAllHold} {
		if _, ok := e.orig.pending[fingerprintOf(v)]; ok {
			t.Errorf("Handle() parked an original for %q, want none", v)
		}
	}
}
