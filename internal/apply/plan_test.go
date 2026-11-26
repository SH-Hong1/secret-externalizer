package apply_test

import (
	"path"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/apply"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/project"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

// span returns a location from line sl, column sc to line el, column ec,
// the end being the last character.
func span(sl, sc, el, ec int) finding.Location {
	return finding.Location{StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec, FileHash: hashF}
}

// at returns a member in file whose finding ID is id, at the locations. It
// is a shell script when its name makes it one.
func at(id finding.ID, file string, locations ...finding.Location) triage.Member {
	return triage.Member{FindingID: id, File: file, Shell: triage.ShellFile(file, nil), Locations: locations}
}

// shellAt returns the member at gives, marked as a shell script whatever
// its name, as a scan marks a file whose first line is a shell shebang.
func shellAt(id finding.ID, file string, locations ...finding.Location) triage.Member {
	m := at(id, file, locations...)
	m.Shell = true
	return m
}

// externalized returns a password bundle externalized as db.main.password.
func externalized(id triage.BundleID, members ...triage.Member) triage.Bundle {
	return triage.Bundle{
		ID: id, ProjectID: 7, Fingerprint: "fp-secret", Category: finding.CategoryStorage, Kind: finding.KindPassword,
		Preview:  "****",
		Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"},
		Members:  members,
	}
}

// keyFile returns a key-file bundle externalized as ssh.partner.key.
func keyFile(id triage.BundleID, members ...triage.Member) triage.Bundle {
	return triage.Bundle{
		ID: id, ProjectID: 7, Fingerprint: "fp-key", Category: finding.CategorySSH, Kind: finding.KindKeyFile,
		Preview:  "****",
		Decision: triage.Decision{Verdict: triage.VerdictExternalize, Name: "ssh.partner.key"},
		Members:  members,
	}
}

// redacted returns a customer-data bundle of kind k to redact.
func redacted(id triage.BundleID, k finding.Kind, members ...triage.Member) triage.Bundle {
	return triage.Bundle{
		ID: id, ProjectID: 7, Fingerprint: "fp-customer", Category: finding.CategoryCustomerData, Kind: k,
		Preview:  "****",
		Decision: triage.Decision{Verdict: triage.VerdictRedact},
		Members:  members,
	}
}

// planInput returns the input of a run on scan 3, the newest scan of
// project 7.
func planInput(bundles ...triage.Bundle) apply.Input {
	return apply.Input{
		Project:    project.Project{ID: 7},
		Scan:       scan.Scan{ID: 3, ProjectID: 7, Status: scan.StatusSucceeded},
		NewestScan: 3,
		Bundles:    bundles,
	}
}

// replaceTarget returns the target that replaces the password of
// externalized, bundle b, finding f, at l in file.
func replaceTarget(b triage.BundleID, f finding.ID, file string, l finding.Location) apply.Target {
	name := triage.Name("db.main.password").For(triage.ShellFile(file, nil))
	return apply.Target{
		Item: apply.Item{
			BundleID: b, FindingID: f, File: file, StartLine: l.StartLine, StartCol: l.StartCol,
			Action: apply.ActionReplace, Category: finding.CategoryStorage, Kind: finding.KindPassword,
			Name: name, StorePath: "secret/data/projects/7/ext.db.main.password",
			CodeChange: strings.EqualFold(path.Ext(file), ".java"), Status: apply.StatusPlanned,
		},
		Location:    l,
		Fingerprint: "fp-secret",
		StoreKey:    "ext.db.main.password",
		Text:        "${" + name + "}",
	}
}

// redactTarget returns the target that redacts customer data of kind k of
// redacted, bundle b, finding f, at l in file.
func redactTarget(b triage.BundleID, f finding.ID, k finding.Kind, file string, l finding.Location) apply.Target {
	return apply.Target{
		Item: apply.Item{
			BundleID: b, FindingID: f, File: file, StartLine: l.StartLine, StartCol: l.StartCol,
			Action: apply.ActionRedact, Category: finding.CategoryCustomerData, Kind: k, Status: apply.StatusPlanned,
		},
		Location:    l,
		Fingerprint: "fp-customer",
		Text:        apply.Marker(k),
	}
}

// wholeTarget returns the target that removes file, the key file of
// keyFile, bundle b, finding f, at l.
func wholeTarget(b triage.BundleID, f finding.ID, file string, l finding.Location, pemOnly bool) apply.Target {
	return apply.Target{
		Item: apply.Item{
			BundleID: b, FindingID: f, File: file, StartLine: l.StartLine, StartCol: l.StartCol,
			Action: apply.ActionRemoveFile, Category: finding.CategorySSH, Kind: finding.KindKeyFile,
			Name: triage.Name("ssh.partner.key").For(triage.ShellFile(file, nil)), StorePath: "secret/data/projects/7/ext.ssh.partner.key",
			Status: apply.StatusPlanned,
		},
		Location:    l,
		Fingerprint: "fp-key",
		StoreKey:    "ext.ssh.partner.key",
		WholeFile:   true,
		PEMOnly:     pemOnly,
	}
}

// skippedAs returns the item of t skipped for reason.
func skippedAs(t apply.Target, reason apply.Reason) apply.Item {
	it := t.Item
	it.Status, it.Reason = apply.StatusSkipped, reason
	return it
}

// targetsIn returns the targets of p in file.
func targetsIn(p apply.Plan, file string) []apply.Target {
	var out []apply.Target
	for _, t := range p.Targets {
		if t.Item.File == file {
			out = append(out, t)
		}
	}
	return out
}

// skippedIn returns the skipped items of p in file.
func skippedIn(p apply.Plan, file string) []apply.Item {
	var out []apply.Item
	for _, it := range p.Skipped {
		if it.File == file {
			out = append(out, it)
		}
	}
	return out
}

func TestPlan_OnlyDecidedBundles(t *testing.T) {
	undecided := externalized(1, at(11, "conf/a.yml", span(1, 5, 1, 20)))
	undecided.Decision = triage.Decision{}
	falsePositive := externalized(2, at(12, "conf/b.yml", span(1, 5, 1, 20)))
	falsePositive.Decision = triage.Decision{Verdict: triage.VerdictFalsePositive}
	hold := externalized(3, at(13, "conf/c.yml", span(1, 5, 1, 20)))
	hold.Decision = triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldLater}
	customerHold := redacted(4, finding.KindPhone, at(14, "conf/d.yml", span(1, 5, 1, 20)))
	customerHold.Decision = triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm}
	decided := externalized(5, at(15, "conf/e.yml", span(1, 5, 1, 20)))

	p := apply.NewPlan(planInput(undecided, falsePositive, hold, customerHold, decided))

	var got []triage.BundleID
	for _, tg := range p.Targets {
		got = append(got, tg.Item.BundleID)
	}
	if diff := cmp.Diff([]triage.BundleID{5}, got); diff != "" {
		t.Errorf("NewPlan(undecided, false positive, hold, decided) target bundles (-want +got):\n%s", diff)
	}
	if len(p.Skipped) != 0 {
		t.Errorf("NewPlan(undecided, false positive, hold, decided).Skipped = %+v, want none", p.Skipped)
	}
}

func TestPlan_ChecksBundlesAgain(t *testing.T) {
	frontend := externalized(1,
		at(11, "conf/a.yml", span(1, 5, 1, 20), span(4, 5, 4, 20)),
		at(12, "web/index.jsp", span(2, 5, 2, 20)))
	customerExternalized := redacted(2, finding.KindPhone, at(21, "conf/b.yml", span(1, 5, 1, 20)))
	customerExternalized.Decision = triage.Decision{Verdict: triage.VerdictExternalize, Name: "customer.phone"}
	secretRedacted := externalized(3, at(31, "conf/c.yml", span(1, 5, 1, 20)))
	secretRedacted.Decision = triage.Decision{Verdict: triage.VerdictRedact}
	noName := externalized(4, at(41, "conf/d.yml", span(1, 5, 1, 20)))
	noName.Decision.Name = ""
	badName := externalized(5, at(51, "conf/e.yml", span(1, 5, 1, 20)))
	badName.Decision.Name = "Not A Name"

	tests := []struct {
		name      string
		bundle    triage.Bundle
		locations int
	}{
		{"externalize with a JSP member", frontend, 3},
		{"customer data externalized", customerExternalized, 1},
		{"secret redacted", secretRedacted, 1},
		{"externalize without a name", noName, 1},
		{"externalize with an invalid name", badName, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := apply.NewPlan(planInput(tt.bundle))
			if len(p.Targets) != 0 {
				t.Errorf("NewPlan(%s).Targets = %+v, want none", tt.name, p.Targets)
			}
			if len(p.Skipped) != tt.locations {
				t.Fatalf("NewPlan(%s).Skipped has %d items, want %d", tt.name, len(p.Skipped), tt.locations)
			}
			for _, it := range p.Skipped {
				if it.Status != apply.StatusSkipped || it.Reason != apply.ReasonNotAllowed || it.BundleID != tt.bundle.ID {
					t.Errorf("NewPlan(%s) skipped %+v, want bundle %d skipped as %s", tt.name, it, tt.bundle.ID, apply.ReasonNotAllowed)
				}
				if it.StorePath != "" {
					t.Errorf("NewPlan(%s) skipped item store path = %q, want empty", tt.name, it.StorePath)
				}
			}
		})
	}
}

func TestPlan_Text(t *testing.T) {
	tests := []struct {
		file       string
		text       string
		name       string
		codeChange bool
		shell      bool
	}{
		{"conf/a.yml", "${ext.db.main.password}", "ext.db.main.password", false, false},
		{"conf/b.XML", "${ext.db.main.password}", "ext.db.main.password", false, false},
		{"src/C.java", "${ext.db.main.password}", "ext.db.main.password", true, false},
		{"pom.xml", "${ext.db.main.password}", "ext.db.main.password", false, false},
		{"conf/x.properties", "${ext.db.main.password}", "ext.db.main.password", false, false},
		{"bin/deploy", "${ext.db.main.password}", "ext.db.main.password", false, false},
		{"bin/start", "${EXT_DB_MAIN_PASSWORD}", "EXT_DB_MAIN_PASSWORD", false, true},
		{"bin/deploy.sh", "${EXT_DB_MAIN_PASSWORD}", "EXT_DB_MAIN_PASSWORD", false, false},
		{"bin/DEPLOY.SH", "${EXT_DB_MAIN_PASSWORD}", "EXT_DB_MAIN_PASSWORD", false, false},
		{"bin/deploy.bash", "${EXT_DB_MAIN_PASSWORD}", "EXT_DB_MAIN_PASSWORD", false, false},
		{"bin/deploy.ksh", "${EXT_DB_MAIN_PASSWORD}", "EXT_DB_MAIN_PASSWORD", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			loc := span(3, 12, 3, 30)
			m := at(11, tt.file, loc)
			if tt.shell {
				m = shellAt(11, tt.file, loc)
			}
			p := apply.NewPlan(planInput(externalized(1, m)))
			want := []apply.Target{{
				Item: apply.Item{
					BundleID: 1, FindingID: 11, File: tt.file, StartLine: 3, StartCol: 12,
					Action: apply.ActionReplace, Category: finding.CategoryStorage, Kind: finding.KindPassword,
					Name: tt.name, StorePath: "secret/data/projects/7/ext.db.main.password",
					CodeChange: tt.codeChange, Status: apply.StatusPlanned,
				},
				Location:    loc,
				Fingerprint: "fp-secret",
				StoreKey:    "ext.db.main.password",
				Text:        tt.text,
			}}
			if diff := cmp.Diff(want, p.Targets); diff != "" {
				t.Errorf("NewPlan(password in %q).Targets (-want +got):\n%s", tt.file, diff)
			}
		})
	}
}

func TestPlan_Redact(t *testing.T) {
	tests := []struct {
		kind finding.Kind
		text string
	}{
		{finding.KindPhone, "[REDACTED:phone]"},
		{finding.KindRRN, "[REDACTED:rrn]"},
		{finding.KindEmail, "[REDACTED:email]"},
		{finding.KindCard, "[REDACTED:card]"},
		{finding.KindAddress, "[REDACTED:address]"},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			loc := span(2, 1, 2, 13)
			p := apply.NewPlan(planInput(redacted(1, tt.kind, at(11, "logs/app.log", loc))))
			want := []apply.Target{{
				Item: apply.Item{
					BundleID: 1, FindingID: 11, File: "logs/app.log", StartLine: 2, StartCol: 1,
					Action: apply.ActionRedact, Category: finding.CategoryCustomerData, Kind: tt.kind,
					Status: apply.StatusPlanned,
				},
				Location:    loc,
				Fingerprint: "fp-customer",
				Text:        tt.text,
			}}
			if diff := cmp.Diff(want, p.Targets); diff != "" {
				t.Errorf("NewPlan(%s redacted).Targets (-want +got):\n%s", tt.kind, diff)
			}
			if got := apply.Marker(tt.kind); got != tt.text {
				t.Errorf("Marker(%q) = %q, want %q", tt.kind, got, tt.text)
			}
		})
	}
	for _, k := range []finding.Kind{"unknown", finding.KindPassword, ""} {
		if got, want := apply.Marker(k), "[REDACTED:data]"; got != want {
			t.Errorf("Marker(%q) = %q, want %q", k, got, want)
		}
	}
}

func TestPlan_KeyFile(t *testing.T) {
	whole := span(1, 1, 20, 5)
	tests := []struct {
		file    string
		target  bool
		pemOnly bool
	}{
		{"conf/partner.pem", true, false},
		{"keys/store.JKS", true, false},
		{"deploy/id_rsa", true, false},
		{"certs/ca.crt", true, false},
		{"conf/partner-key", true, true},
		{"notes.md", true, true},
		{"server.conf", true, true},
		{"conf/application.yml", false, false},
		{"src/Keys.java", false, false},
		{"bin/run.sh", false, false},
		{"bin/run.bash", false, false},
		{"bin/run.ksh", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			other := externalized(2, at(21, tt.file, span(25, 3, 25, 18)))
			p := apply.NewPlan(planInput(keyFile(1, at(11, tt.file, whole)), other))

			var gotWhole []apply.Target
			for _, tg := range targetsIn(p, tt.file) {
				if tg.Item.BundleID == 1 {
					gotWhole = append(gotWhole, tg)
				}
			}
			if !tt.target {
				want := apply.Plan{
					Digest:  p.Digest,
					Targets: []apply.Target{replaceTarget(2, 21, tt.file, span(25, 3, 25, 18))},
					Skipped: []apply.Item{skippedAs(wholeTarget(1, 11, tt.file, whole, false), apply.ReasonKeyInSourceFile)},
				}
				if diff := cmp.Diff(want, p); diff != "" {
					t.Errorf("NewPlan(key file in %q) (-want +got):\n%s", tt.file, diff)
				}
				return
			}
			{
				want := []apply.Target{wholeTarget(1, 11, tt.file, whole, tt.pemOnly)}
				if diff := cmp.Diff(want, gotWhole); diff != "" {
					t.Errorf("NewPlan(key file %q) whole-file targets (-want +got):\n%s", tt.file, diff)
				}
				if len(skippedIn(p, tt.file)) != 0 {
					t.Errorf("NewPlan(key file %q).Skipped = %+v, want none", tt.file, skippedIn(p, tt.file))
				}
			}

			var replaced int
			for _, tg := range targetsIn(p, tt.file) {
				if tg.Item.BundleID == 2 && tg.Item.Action == apply.ActionReplace {
					replaced++
				}
			}
			if replaced != 1 {
				t.Errorf("NewPlan(key file %q) keeps %d replace targets of the other bundle, want 1", tt.file, replaced)
			}
		})
	}

	t.Run("private key in a pem file", func(t *testing.T) {
		privateKey := externalized(2, at(21, "conf/partner.pem", span(1, 1, 20, 5)))
		privateKey.Kind = finding.KindPrivateKey
		privateKey.Category = finding.CategorySSH
		privateKey.Decision.Name = "ssh.partner.private"
		p := apply.NewPlan(planInput(keyFile(1, at(11, "conf/partner.pem", whole)), privateKey))
		var actions []apply.Action
		for _, tg := range p.Targets {
			actions = append(actions, tg.Item.Action)
		}
		if diff := cmp.Diff([]apply.Action{apply.ActionRemoveFile, apply.ActionReplace}, actions); diff != "" {
			t.Errorf("NewPlan(key file and private key in one pem file) actions (-want +got):\n%s", diff)
		}
	})
}

func TestPlan_KeyFileInShellScriptWithoutExtension(t *testing.T) {
	whole := span(1, 1, 20, 5)
	p := apply.NewPlan(planInput(keyFile(1, shellAt(11, "bin/start", whole))))
	want := wholeTarget(1, 11, "bin/start", whole, false)
	want.Item.Name = triage.Name("ssh.partner.key").Shell()
	want.Item.Status, want.Item.Reason = apply.StatusSkipped, apply.ReasonKeyInSourceFile
	if len(p.Targets) != 0 || len(p.Skipped) != 1 {
		t.Fatalf("NewPlan(key file in shell script bin/start) = %d targets, %d skipped; want 0 targets, 1 skipped", len(p.Targets), len(p.Skipped))
	}
	if diff := cmp.Diff(want.Item, p.Skipped[0]); diff != "" {
		t.Errorf("NewPlan(key file in shell script bin/start).Skipped[0] (-want +got):\n%s", diff)
	}
}

func TestPlan_KeyFileWithCustomerData(t *testing.T) {
	whole := span(1, 1, 20, 5)
	in := planInput(
		keyFile(1, at(11, "logs/app.log", whole)),
		keyFile(2, at(21, "samples/dump.sql", whole)),
		keyFile(3, at(31, "data/keys.txt", whole)),
		keyFile(4, at(41, "conf/partner.pem", whole)),
		keyFile(5, at(51, "conf/other.pem", whole)),
		redacted(6, finding.KindPhone, at(61, "logs/app.log", span(30, 7, 30, 19))),
		redacted(7, finding.KindEmail, at(71, "conf/other.pem", span(25, 1, 25, 22))),
	)
	in.CustomerDataFiles = []string{"conf/other.pem", "conf/partner.pem", "logs/app.log"}
	p := apply.NewPlan(in)

	held := apply.ReasonKeyHoldsCustomerData
	want := apply.Plan{
		Digest: apply.Digest(in.Scan.ID, in.Bundles, nil),
		Targets: []apply.Target{
			redactTarget(7, 71, finding.KindEmail, "conf/other.pem", span(25, 1, 25, 22)),
			redactTarget(6, 61, finding.KindPhone, "logs/app.log", span(30, 7, 30, 19)),
		},
		Skipped: []apply.Item{
			skippedAs(wholeTarget(5, 51, "conf/other.pem", whole, false), held),
			skippedAs(wholeTarget(4, 41, "conf/partner.pem", whole, false), held),
			skippedAs(wholeTarget(3, 31, "data/keys.txt", whole, false), held),
			skippedAs(wholeTarget(1, 11, "logs/app.log", whole, false), held),
			skippedAs(wholeTarget(2, 21, "samples/dump.sql", whole, false), held),
		},
	}
	if diff := cmp.Diff(want, p); diff != "" {
		t.Errorf("NewPlan(key files holding customer data) (-want +got):\n%s", diff)
	}

	t.Run("customer data bundle without the list", func(t *testing.T) {
		in := planInput(
			keyFile(1, at(11, "conf/other.pem", whole)),
			redacted(2, finding.KindEmail, at(21, "conf/other.pem", span(25, 1, 25, 22))),
		)
		p := apply.NewPlan(in)
		skipped := skippedIn(p, "conf/other.pem")
		if len(skipped) != 1 || skipped[0].Reason != apply.ReasonKeyHoldsCustomerData {
			t.Errorf("NewPlan(key file with a redacted email).Skipped = %+v, want one item skipped as %s", skipped, apply.ReasonKeyHoldsCustomerData)
		}
	})
}

func TestPEMOnly(t *testing.T) {
	const block = "-----BEGIN PRIVATE KEY-----\nZGV2LW9ubHktZmFrZS1rZXk=\nZGV2LW9ubHktZmFrZQ==\n-----END PRIVATE KEY-----\n"
	const cert = "-----BEGIN CERTIFICATE-----\nZGV2LW9ubHktZmFrZS1jZXJ0\n-----END CERTIFICATE-----"
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"one block", block, true},
		{"two blocks", block + cert + "\n", true},
		{"blank lines around", "\n\n  \n" + block + "\n\t\n", true},
		{"CRLF", "-----BEGIN PRIVATE KEY-----\r\nZGV2LW9ubHktZmFrZQ==\r\n-----END PRIVATE KEY-----\r\n", true},
		{"no final newline", cert, true},
		{"encrypted headers", "-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,00\n\nZGV2LW9ubHktZmFrZQ==\n-----END RSA PRIVATE KEY-----\n", true},
		{"text before", "key for the partner\n" + block, false},
		{"text after", block + "password=dev-only-fake\n", false},
		{"text between", block + "# next\n" + cert, false},
		{"no block", "dev-only-fake-key\n", false},
		{"empty", "", false},
		{"only white space", " \n\r\n", false},
		{"no end line", "-----BEGIN PRIVATE KEY-----\nZGV2LW9ubHktZmFrZQ==\n", false},
		{"end of another label", "-----BEGIN PRIVATE KEY-----\nZGV2\n-----END CERTIFICATE-----\n", false},
		{"begin inside a block", "-----BEGIN PRIVATE KEY-----\n" + block, false},
		{"text after the end marker", "-----BEGIN PRIVATE KEY-----\nZGV2\n-----END PRIVATE KEY----- trailing\n", false},
		{"end without begin", "-----END PRIVATE KEY-----\n", false},
		{"source code inside a block", "-----BEGIN PRIVATE KEY-----\nString pw = \"dev-only-fake\";\n-----END PRIVATE KEY-----\n", false},
		{"phone number inside a block", "-----BEGIN PRIVATE KEY-----\nZGV2\n010-0000-0000\n-----END PRIVATE KEY-----\n", false},
		{"assignment inside a block", "-----BEGIN PRIVATE KEY-----\npassword=dev-only-fake\n-----END PRIVATE KEY-----\n", false},
		{"header without a value", "-----BEGIN PRIVATE KEY-----\nProc-Type: \nZGV2\n-----END PRIVATE KEY-----\n", false},
		{"header name with a space", "-----BEGIN PRIVATE KEY-----\nkey name: dev-only-fake\nZGV2\n-----END PRIVATE KEY-----\n", false},
		{"indented base64 inside a block", "-----BEGIN PRIVATE KEY-----\n  ZGV2LW9ubHk=  \n-----END PRIVATE KEY-----\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apply.PEMOnly([]byte(tt.text)); got != tt.want {
				t.Errorf("PEMOnly(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestPlan_Overlap(t *testing.T) {
	const file = "conf/a.yml"
	dup := externalized(7, at(71, file, span(12, 1, 12, 9), span(12, 1, 12, 9)))
	keyLoc := span(1, 1, 40, 5)
	privateKey := externalized(9, at(91, "conf/k.pem", span(2, 1, 4, 10)))
	privateKey.Kind = finding.KindPrivateKey
	in := planInput(
		externalized(1, at(11, file, span(3, 5, 3, 20))),
		externalized(2, at(21, file, span(3, 10, 3, 30))),
		externalized(3, at(31, file, span(5, 1, 5, 1))),
		externalized(4, at(41, file, span(5, 3, 5, 3))),
		externalized(5, at(51, file, span(6, 1, 6, 1))),
		externalized(6, at(61, file, span(6, 2, 6, 4))),
		dup,
		keyFile(8, at(81, "conf/k.pem", keyLoc)),
		privateKey,
		redacted(10, finding.KindPhone, at(101, file, span(8, 1, 10, 3))),
		externalized(11, at(111, file, span(9, 50, 9, 60))),
		externalized(12, at(121, "conf/other.yml", span(3, 10, 3, 30))),
	)
	p := apply.NewPlan(in)

	pk := replaceTarget(9, 91, "conf/k.pem", span(2, 1, 4, 10))
	pk.Item.Kind = finding.KindPrivateKey
	overlap := apply.ReasonOverlap
	want := apply.Plan{
		Digest: apply.Digest(in.Scan.ID, in.Bundles, nil),
		Targets: []apply.Target{
			replaceTarget(3, 31, file, span(5, 1, 5, 1)),
			replaceTarget(4, 41, file, span(5, 3, 5, 3)),
			replaceTarget(5, 51, file, span(6, 1, 6, 1)),
			replaceTarget(6, 61, file, span(6, 2, 6, 4)),
			replaceTarget(7, 71, file, span(12, 1, 12, 9)),
			wholeTarget(8, 81, "conf/k.pem", keyLoc, false),
			pk,
			replaceTarget(12, 121, "conf/other.yml", span(3, 10, 3, 30)),
		},
		Skipped: []apply.Item{
			skippedAs(replaceTarget(1, 11, file, span(3, 5, 3, 20)), overlap),
			skippedAs(replaceTarget(2, 21, file, span(3, 10, 3, 30)), overlap),
			skippedAs(redactTarget(10, 101, finding.KindPhone, file, span(8, 1, 10, 3)), overlap),
			skippedAs(replaceTarget(11, 111, file, span(9, 50, 9, 60)), overlap),
		},
	}
	if diff := cmp.Diff(want, p); diff != "" {
		t.Errorf("NewPlan(overlaps) (-want +got):\n%s", diff)
	}
}

func TestPlan_HoldsCustomerData(t *testing.T) {
	const file = "conf/db.properties"
	cd := func(sl, sc, el, ec int) apply.CustomerSpan {
		return apply.CustomerSpan{File: file, StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec}
	}
	in := planInput(
		// A URL whose user and host part an email rule also reported.
		externalized(1, at(11, file, span(3, 10, 3, 60))),
		// Ends right before the customer data: kept.
		externalized(2, at(21, file, span(5, 1, 5, 10))),
		// Shares one character with the customer data.
		externalized(3, at(31, file, span(6, 5, 6, 9))),
		// A value over three lines, with customer data on the middle one.
		externalized(4, at(41, file, span(8, 1, 10, 2))),
		// A secret over a redacted email: the secret holds customer data,
		// the redaction overlaps the secret.
		externalized(5, at(51, file, span(12, 1, 12, 30))),
		redacted(6, finding.KindEmail, at(61, file, span(12, 5, 12, 20))),
		// A redaction on its own span: unchanged.
		redacted(7, finding.KindPhone, at(71, file, span(14, 1, 14, 12))),
		// The same position in another file: kept.
		externalized(8, at(81, "conf/other.properties", span(3, 10, 3, 60))),
	)
	in.CustomerDataSpans = []apply.CustomerSpan{
		cd(14, 1, 14, 12), cd(3, 30, 3, 45), cd(5, 11, 5, 20), cd(6, 1, 6, 5), cd(9, 1, 9, 3), cd(12, 5, 12, 20),
	}
	in.CustomerDataFiles = []string{file}
	p := apply.NewPlan(in)

	held := apply.ReasonHoldsCustomerData
	want := apply.Plan{
		Digest: apply.Digest(in.Scan.ID, in.Bundles, in.CustomerDataSpans),
		Targets: []apply.Target{
			replaceTarget(2, 21, file, span(5, 1, 5, 10)),
			redactTarget(7, 71, finding.KindPhone, file, span(14, 1, 14, 12)),
			replaceTarget(8, 81, "conf/other.properties", span(3, 10, 3, 60)),
		},
		Skipped: []apply.Item{
			skippedAs(replaceTarget(1, 11, file, span(3, 10, 3, 60)), held),
			skippedAs(replaceTarget(3, 31, file, span(6, 5, 6, 9)), held),
			skippedAs(replaceTarget(4, 41, file, span(8, 1, 10, 2)), held),
			skippedAs(replaceTarget(5, 51, file, span(12, 1, 12, 30)), held),
			skippedAs(redactTarget(6, 61, finding.KindEmail, file, span(12, 5, 12, 20)), apply.ReasonOverlap),
		},
	}
	if diff := cmp.Diff(want, p); diff != "" {
		t.Errorf("NewPlan(secrets over customer data) (-want +got):\n%s", diff)
	}
}

func TestPlan_KindCategoryMismatch(t *testing.T) {
	secretPhone := externalized(1, at(11, "conf/a.yml", span(3, 5, 3, 20)))
	secretPhone.Kind = finding.KindPhone
	customerPassword := redacted(2, finding.KindPassword, at(21, "logs/x.log", span(2, 1, 2, 13)))
	tests := []struct {
		name   string
		bundle triage.Bundle
	}{
		{"storage category with a phone kind, externalized", secretPhone},
		{"customer-data category with a password kind, redacted", customerPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.bundle.Check(tt.bundle.Decision); err != nil {
				t.Fatalf("Bundle.Check(%+v) = %v, want nil so that only the kind guard refuses it", tt.bundle.Decision, err)
			}
			p := apply.NewPlan(planInput(tt.bundle))
			if len(p.Targets) != 0 {
				t.Errorf("NewPlan(%s).Targets = %+v, want none", tt.name, p.Targets)
			}
			if len(p.Skipped) != 1 || p.Skipped[0].Reason != apply.ReasonNotAllowed {
				t.Errorf("NewPlan(%s).Skipped = %+v, want one item skipped as %s", tt.name, p.Skipped, apply.ReasonNotAllowed)
			}
		})
	}
}

func TestPlan_Blockers(t *testing.T) {
	target := externalized(1, at(11, "conf/a.yml", span(1, 5, 1, 20)))
	unlisted := []apply.Item{{BundleID: 9, File: "conf/old.yml", Action: apply.ActionReplace, Status: apply.StatusApplied}}
	tests := []struct {
		name     string
		newest   scan.ID
		bundles  []triage.Bundle
		unlisted []apply.Item
		want     []apply.Blocker
	}{
		{"ready", 3, []triage.Bundle{target}, nil, nil},
		{"newer scan", 4, []triage.Bundle{target}, nil, []apply.Blocker{apply.BlockerScanNotLatest}},
		{"nothing", 3, nil, nil, []apply.Blocker{apply.BlockerNothingToApply}},
		{"newer scan and nothing", 4, nil, nil, []apply.Blocker{apply.BlockerScanNotLatest, apply.BlockerNothingToApply}},
		{"only skipped", 3, []triage.Bundle{keyFile(2, at(21, "conf/app.yml", span(1, 1, 3, 4)))}, nil, []apply.Blocker{apply.BlockerNothingToApply}},
		{"only unlisted", 3, nil, unlisted, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := planInput(tt.bundles...)
			in.NewestScan = tt.newest
			in.Unlisted = tt.unlisted
			if diff := cmp.Diff(tt.want, apply.NewPlan(in).Blockers); diff != "" {
				t.Errorf("NewPlan(%s).Blockers (-want +got):\n%s", tt.name, diff)
			}
		})
	}
}

func TestPlan_Order(t *testing.T) {
	bundles := []triage.Bundle{
		redacted(4, finding.KindPhone, at(41, "logs/b.log", span(9, 1, 9, 13), span(2, 1, 2, 13))),
		externalized(3,
			at(32, "conf/b.yml", span(7, 3, 7, 10)),
			at(31, "conf/a.yml", span(7, 30, 7, 40), span(7, 3, 7, 10), span(2, 3, 2, 10))),
		externalized(1, at(11, "bin/z.sh", span(1, 1, 1, 4))),
	}
	in := planInput(bundles...)
	p := apply.NewPlan(in)

	type place struct {
		File      string
		Line, Col int
	}
	var got []place
	for _, tg := range p.Targets {
		got = append(got, place{tg.Item.File, tg.Item.StartLine, tg.Item.StartCol})
	}
	want := []place{
		{"bin/z.sh", 1, 1},
		{"conf/a.yml", 2, 3},
		{"conf/a.yml", 7, 3},
		{"conf/a.yml", 7, 30},
		{"conf/b.yml", 7, 3},
		{"logs/b.log", 2, 1},
		{"logs/b.log", 9, 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("NewPlan(unordered bundles) target order (-want +got):\n%s", diff)
	}
	in.CustomerDataSpans = []apply.CustomerSpan{{File: "logs/b.log", StartLine: 2, StartCol: 1, EndLine: 2, EndCol: 13}}
	if got, want := apply.NewPlan(in).Digest, apply.Digest(in.Scan.ID, in.Bundles, in.CustomerDataSpans); got != want {
		t.Errorf("NewPlan(unordered bundles).Digest = %q, want Digest(scan, bundles, spans) = %q", got, want)
	}
	if p.Digest == "" {
		t.Errorf("NewPlan(unordered bundles).Digest is empty")
	}
}
