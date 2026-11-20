package apply_test

import (
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

// at returns a member in file whose finding ID is id, at the locations.
func at(id finding.ID, file string, locations ...finding.Location) triage.Member {
	return triage.Member{FindingID: id, File: file, Locations: locations}
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
	}{
		{"conf/a.yml", "${ext.db.main.password}", "ext.db.main.password", false},
		{"conf/b.XML", "${ext.db.main.password}", "ext.db.main.password", false},
		{"src/C.java", "${ext.db.main.password}", "ext.db.main.password", true},
		{"pom.xml", "${ext.db.main.password}", "ext.db.main.password", false},
		{"conf/x.properties", "${ext.db.main.password}", "ext.db.main.password", false},
		{"bin/deploy", "${ext.db.main.password}", "ext.db.main.password", false},
		{"bin/deploy.sh", "${EXT_DB_MAIN_PASSWORD}", "EXT_DB_MAIN_PASSWORD", false},
		{"bin/DEPLOY.SH", "${EXT_DB_MAIN_PASSWORD}", "EXT_DB_MAIN_PASSWORD", false},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			loc := span(3, 12, 3, 30)
			p := apply.NewPlan(planInput(externalized(1, at(11, tt.file, loc))))
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
				if len(gotWhole) != 0 {
					t.Errorf("NewPlan(key file %q) whole-file targets = %+v, want none", tt.file, gotWhole)
				}
				skipped := skippedIn(p, tt.file)
				if len(skipped) != 1 || skipped[0].Reason != apply.ReasonKeyInSourceFile || skipped[0].Action != apply.ActionRemoveFile {
					t.Errorf("NewPlan(key file %q).Skipped = %+v, want one remove-file item skipped as %s", tt.file, skipped, apply.ReasonKeyInSourceFile)
				}
			} else {
				want := []apply.Target{{
					Item: apply.Item{
						BundleID: 1, FindingID: 11, File: tt.file, StartLine: 1, StartCol: 1,
						Action: apply.ActionRemoveFile, Category: finding.CategorySSH, Kind: finding.KindKeyFile,
						Name: "ext.ssh.partner.key", StorePath: "secret/data/projects/7/ext.ssh.partner.key",
						Status: apply.StatusPlanned,
					},
					Location:    whole,
					Fingerprint: "fp-key",
					StoreKey:    "ext.ssh.partner.key",
					WholeFile:   true,
					PEMOnly:     tt.pemOnly,
				}}
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

	for _, tg := range p.Targets {
		if tg.WholeFile || tg.StoreKey != "" || tg.Item.StorePath != "" {
			t.Errorf("NewPlan(key files holding customer data) target %+v, want no whole-file or stored target", tg)
		}
	}
	for _, file := range []string{"logs/app.log", "samples/dump.sql", "data/keys.txt", "conf/partner.pem", "conf/other.pem"} {
		skipped := skippedIn(p, file)
		if len(skipped) != 1 || skipped[0].Reason != apply.ReasonKeyHoldsCustomerData {
			t.Errorf("NewPlan(key file %q).Skipped = %+v, want one item skipped as %s", file, skipped, apply.ReasonKeyHoldsCustomerData)
		}
	}
	var redactions []string
	for _, tg := range p.Targets {
		if tg.Item.Action == apply.ActionRedact {
			redactions = append(redactions, tg.Item.File)
		}
	}
	if diff := cmp.Diff([]string{"conf/other.pem", "logs/app.log"}, redactions); diff != "" {
		t.Errorf("NewPlan(key files holding customer data) redactions (-want +got):\n%s", diff)
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
	}
	for _, tt := range tests {
		if got := apply.PEMOnly([]byte(tt.text)); got != tt.want {
			t.Errorf("PEMOnly(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPlan_Overlap(t *testing.T) {
	const file = "conf/a.yml"
	dup := externalized(7, at(71, file, span(12, 1, 12, 9), span(12, 1, 12, 9)))
	dup.Fingerprint = "fp-dup"
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

	var targets, overlapped []triage.BundleID
	for _, tg := range p.Targets {
		targets = append(targets, tg.Item.BundleID)
	}
	for _, it := range p.Skipped {
		if it.Reason != apply.ReasonOverlap {
			t.Errorf("NewPlan(overlaps) skipped %+v as %s, want only %s", it, it.Reason, apply.ReasonOverlap)
		}
		overlapped = append(overlapped, it.BundleID)
	}
	if diff := cmp.Diff([]triage.BundleID{3, 4, 5, 6, 7, 8, 9, 12}, targets); diff != "" {
		t.Errorf("NewPlan(overlaps) target bundles in order (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]triage.BundleID{1, 2, 10, 11}, overlapped); diff != "" {
		t.Errorf("NewPlan(overlaps) overlapped bundles in order (-want +got):\n%s", diff)
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
	if got, want := p.Digest, apply.Digest(in.Scan.ID, in.Bundles); got != want {
		t.Errorf("NewPlan(unordered bundles).Digest = %q, want Digest(scan, bundles) = %q", got, want)
	}
	if p.Digest == "" {
		t.Errorf("NewPlan(unordered bundles).Digest is empty")
	}
}
