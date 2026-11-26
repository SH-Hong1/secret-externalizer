package triage_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/triage"
)

func TestParseName(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{"db.main.password", true},
		{"ssh.deploy2.key", true},
		{"a.b", true},
		{"a.b.c.d.e", true},
		{"db." + strings.Repeat("a", 97), true},
		{"", false},
		{"password", false},
		{"a.b.c.d.e.f", false},
		{"db.Main.password", false},
		{"db.main-1.password", false},
		{"db.main_1.password", false},
		{"db.2nd.password", false},
		{"ext.db.password", false},
		{"db..password", false},
		{".db.password", false},
		{"db.password.", false},
		{"db." + strings.Repeat("a", 98), false},
	}
	for _, tt := range tests {
		got, err := triage.ParseName(tt.in)
		switch {
		case tt.ok && err != nil:
			t.Errorf("ParseName(%q) error = %v, want nil", tt.in, err)
		case tt.ok && string(got) != tt.in:
			t.Errorf("ParseName(%q) = %q, want %q", tt.in, got, tt.in)
		case !tt.ok && !errors.Is(err, triage.ErrInvalidName):
			t.Errorf("ParseName(%q) = %q, %v, want ErrInvalidName", tt.in, got, err)
		}
	}
}

func TestNameForms(t *testing.T) {
	n := triage.Name("db.main.password")
	if got, want := n.Config(), "ext.db.main.password"; got != want {
		t.Errorf("Name(%q).Config() = %q, want %q", n, got, want)
	}
	if got, want := n.Shell(), "EXT_DB_MAIN_PASSWORD"; got != want {
		t.Errorf("Name(%q).Shell() = %q, want %q", n, got, want)
	}
}

func TestShellFile(t *testing.T) {
	tests := map[string]bool{
		"deploy.sh":        true,
		"bin/DEPLOY.SH":    true,
		"bin/run.Sh":       true,
		"deploy":           false,
		"conf/app.yml":     false,
		"bin/run.sh.bak":   false,
		"bin/sh":           false,
		"bin/run.bash":     true,
		"bin/run.KSH":      true,
		"bin/run.zsh":      false,
		"src/Shell.java":   false,
		"scripts.sh/x.yml": false,
	}
	for file, want := range tests {
		t.Run(file, func(t *testing.T) {
			if got := triage.ShellFile(file); got != want {
				t.Errorf("ShellFile(%q) = %v, want %v", file, got, want)
			}
		})
	}
}

func TestNameFor(t *testing.T) {
	n := triage.Name("db.main.password")
	tests := map[string]string{
		"deploy.sh":            "EXT_DB_MAIN_PASSWORD",
		"DEPLOY.SH":            "EXT_DB_MAIN_PASSWORD",
		"conf/application.yml": "ext.db.main.password",
		"src/Main.java":        "ext.db.main.password",
		"deploy":               "ext.db.main.password",
	}
	for file, want := range tests {
		t.Run(file, func(t *testing.T) {
			if got := n.For(file); got != want {
				t.Errorf("Name(%q).For(%q) = %q, want %q", n, file, got, want)
			}
		})
	}
}

func TestFrontend(t *testing.T) {
	tests := map[string]bool{
		"web/index.jsp":         true,
		"web/head.JSPF":         true,
		"static/app.js":         true,
		"static/page.Html":      true,
		"static/old.htm":        true,
		"conf/app.yml":          false,
		"src/Main.java":         false,
		"static/app.json":       false,
		"static/jsp":            false,
		"static/app.js.example": false,
	}
	for file, want := range tests {
		if got := triage.Frontend(file); got != want {
			t.Errorf("Frontend(%q) = %v, want %v", file, got, want)
		}
	}
}

func TestFrontendExtensions(t *testing.T) {
	exts := triage.FrontendExtensions()
	want := []string{".jsp", ".jspf", ".js", ".html", ".htm"}
	if diff := cmp.Diff(want, exts); diff != "" {
		t.Errorf("FrontendExtensions() (-want +got):\n%s", diff)
	}
	for _, ext := range exts {
		for _, file := range []string{"web/a" + ext, "web/A" + strings.ToUpper(ext)} {
			if !triage.Frontend(file) {
				t.Errorf("Frontend(%q) = false, want true for FrontendExtensions() entry %q", file, ext)
			}
		}
	}
	exts[0] = ".yml"
	if triage.Frontend("conf/app.yml") || !triage.Frontend("web/index.jsp") {
		t.Errorf("FrontendExtensions() returned its own slice: changing it changed Frontend")
	}
}

func TestShellExtensions(t *testing.T) {
	exts := triage.ShellExtensions()
	want := []string{".sh", ".bash", ".ksh"}
	if diff := cmp.Diff(want, exts); diff != "" {
		t.Errorf("ShellExtensions() (-want +got):\n%s", diff)
	}
	for _, ext := range exts {
		for _, file := range []string{"bin/a" + ext, "bin/A" + strings.ToUpper(ext)} {
			if !triage.ShellFile(file) {
				t.Errorf("ShellFile(%q) = false, want true for ShellExtensions() entry %q", file, ext)
			}
		}
	}
	exts[0] = ".yml"
	if triage.ShellFile("conf/app.yml") || !triage.ShellFile("bin/run.sh") {
		t.Errorf("ShellExtensions() returned its own slice: changing it changed ShellFile")
	}
}

func TestSharesBundle(t *testing.T) {
	tests := map[finding.Kind]bool{
		finding.KindPassword:   true,
		finding.KindToken:      true,
		finding.KindURL:        true,
		finding.KindPrivateKey: true,
		finding.KindKeyFile:    true,
		finding.KindOther:      true,
		finding.KindHost:       false,
		finding.KindPort:       false,
		finding.KindAccount:    false,
		finding.KindPhone:      false,
		finding.KindRRN:        false,
		finding.KindEmail:      false,
		finding.KindCard:       false,
		finding.KindAddress:    false,
	}
	for k, want := range tests {
		if got := triage.SharesBundle(k); got != want {
			t.Errorf("SharesBundle(%q) = %v, want %v", k, got, want)
		}
	}
}

// bundle returns a bundle of the category with one member per file.
func bundle(c finding.Category, files ...string) triage.Bundle {
	b := triage.Bundle{ID: 1, Category: c, Kind: finding.KindPassword}
	if c == finding.CategoryCustomerData {
		b.Kind = finding.KindPhone
	}
	for i, f := range files {
		b.Members = append(b.Members, triage.Member{FindingID: finding.ID(i + 1), File: f})
	}
	return b
}

func TestAllowed(t *testing.T) {
	tests := []struct {
		name string
		b    triage.Bundle
		want []triage.Verdict
	}{
		{
			"secret", bundle(finding.CategoryStorage, "conf/a.yml", "conf/b.xml"),
			[]triage.Verdict{triage.VerdictExternalize, triage.VerdictFalsePositive, triage.VerdictHold},
		},
		{
			"customer data", bundle(finding.CategoryCustomerData, "logs/x.log"),
			[]triage.Verdict{triage.VerdictRedact, triage.VerdictFalsePositive, triage.VerdictHold},
		},
		{
			"secret with a frontend member", bundle(finding.CategoryPartnerSession, "conf/a.yml", "web/c.jsp"),
			[]triage.Verdict{triage.VerdictFalsePositive, triage.VerdictHold},
		},
	}
	for _, tt := range tests {
		if diff := cmp.Diff(tt.want, tt.b.Allowed()); diff != "" {
			t.Errorf("Allowed() of a %s bundle (-want +got):\n%s", tt.name, diff)
		}
	}
}

func TestCheck(t *testing.T) {
	secret := bundle(finding.CategoryStorage, "conf/a.yml")
	customer := bundle(finding.CategoryCustomerData, "logs/x.log")
	frontend := bundle(finding.CategoryStorage, "web/c.jsp")
	tests := []struct {
		name string
		b    triage.Bundle
		d    triage.Decision
		ok   bool
	}{
		{"externalize with a name", secret, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}, true},
		{"false positive", secret, triage.Decision{Verdict: triage.VerdictFalsePositive, Note: "test fixture"}, true},
		{"hold with a reason", secret, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldConfirm}, true},
		{"redact customer data", customer, triage.Decision{Verdict: triage.VerdictRedact}, true},
		{"frontend hold", frontend, triage.Decision{Verdict: triage.VerdictHold, Reason: triage.HoldStructure}, true},
		{"note of 500 characters", secret, triage.Decision{Verdict: triage.VerdictFalsePositive, Note: strings.Repeat("가", 500)}, true},
		{"empty decision on a secret", secret, triage.Decision{}, true},
		{"empty decision on customer data", customer, triage.Decision{}, true},
		{"empty decision on a frontend bundle", frontend, triage.Decision{}, true},

		{"externalize customer data", customer, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}, false},
		{"redact a secret", secret, triage.Decision{Verdict: triage.VerdictRedact}, false},
		{"externalize a frontend bundle", frontend, triage.Decision{Verdict: triage.VerdictExternalize, Name: "db.main.password"}, false},
		{"unknown verdict", secret, triage.Decision{Verdict: "ignore"}, false},
		{"externalize without a name", secret, triage.Decision{Verdict: triage.VerdictExternalize}, false},
		{"externalize with a malformed name", secret, triage.Decision{Verdict: triage.VerdictExternalize, Name: "DB.main"}, false},
		{"name without externalize", secret, triage.Decision{Verdict: triage.VerdictFalsePositive, Name: "db.main.password"}, false},
		{"hold without a reason", secret, triage.Decision{Verdict: triage.VerdictHold}, false},
		{"hold with an unknown reason", secret, triage.Decision{Verdict: triage.VerdictHold, Reason: "someday"}, false},
		{"reason without hold", secret, triage.Decision{Verdict: triage.VerdictFalsePositive, Reason: triage.HoldLater}, false},
		{"note of 501 characters", secret, triage.Decision{Verdict: triage.VerdictFalsePositive, Note: strings.Repeat("가", 501)}, false},
		{"note with a newline", secret, triage.Decision{Verdict: triage.VerdictFalsePositive, Note: "line\nline"}, false},
		{"note with NUL", secret, triage.Decision{Verdict: triage.VerdictFalsePositive, Note: "a\x00b"}, false},
		{"note that is not UTF-8", secret, triage.Decision{Verdict: triage.VerdictFalsePositive, Note: "a\xffb"}, false},
		{"note without a verdict", secret, triage.Decision{Note: "later"}, false},
	}
	for _, tt := range tests {
		err := tt.b.Check(tt.d)
		switch {
		case tt.ok && err != nil:
			t.Errorf("Check(%s: %+v) error = %v, want nil", tt.name, tt.d, err)
		case !tt.ok && !errors.Is(err, triage.ErrNotAllowed):
			t.Errorf("Check(%s: %+v) error = %v, want ErrNotAllowed", tt.name, tt.d, err)
		}
	}
}
