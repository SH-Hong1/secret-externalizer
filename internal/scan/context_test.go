package scan_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// bearer is a fake partner token, found by bearerBL.
const bearer = "dev-only-fake-bearer-5" //nolint:gosec // G101: an obviously fake fixture value

// bearerBL finds bearer the way betterleaks reports a partner session.
func bearerBL() *needleScanner {
	return &needleScanner{tool: "betterleaks", rule: "partner-bearer-token", category: finding.CategoryPartnerSession, kind: finding.KindToken, needle: bearer, lineCol: true}
}

// dumps returns the result printed with %+v and as JSON.
func dumps(t *testing.T, r scan.Result) []string {
	t.Helper()
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	return []string{fmt.Sprintf("%+v", r), string(encoded)}
}

func TestHandle_NoContextInCustomerDataFiles(t *testing.T) {
	// Customer data no rule reports: a name, and a phone number without
	// hyphens.
	undetected := []string{"Hong Gildong", "01000000000", "Kim Gasang"}
	lines := "Authorization: Bearer " + bearer + " user=Hong Gildong tel 01000000000\nname=Kim Gasang\n"
	for _, rel := range []string{"logs/app.log", "app.log", "sample/orders.sql", "a/b/dump/x.properties", "export/orders.CSV"} {
		t.Run(rel, func(t *testing.T) {
			e := newEnv(t, map[string]string{rel: lines})
			if err := e.handle(t.Context(), bearerBL()); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			got := e.result(t)
			if len(got.Findings) != 1 || len(got.Findings[0].Locations) != 1 {
				t.Fatalf("Handle(token in %s) = %+v, want one finding at one location", rel, got)
			}
			if l := got.Findings[0].Locations[0]; l.Context != nil || l.ContextFirstLine != 0 {
				t.Errorf("Handle(token in %s) location = %+v, want no surrounding lines", rel, l)
			}
			for _, dump := range dumps(t, got) {
				if v := containsAny(dump, append([]string{bearer}, undetected...)...); v != "" {
					t.Errorf("Handle(token in %s) saved a result that holds %q:\n%s", rel, v, dump)
				}
			}
		})
	}
}

func TestHandle_SourceFileKeepsContext(t *testing.T) {
	content := "class A {\n  String h = \"Authorization: Bearer " + bearer + "\";\n}\n"
	e := newEnv(t, map[string]string{"src/main/java/A.java": content})
	if err := e.handle(t.Context(), bearerBL()); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t).Findings
	if len(got) != 1 {
		t.Fatalf("Handle() findings = %+v, want one", got)
	}
	want := []string{"class A {", `  String h = "Authorization: Bearer ****";`, "}"}
	if diff := cmp.Diff(want, got[0].Locations[0].Context); diff != "" {
		t.Errorf("Handle(token in A.java) surrounding lines (-want +got):\n%s", diff)
	}
}

func TestHandle_CustomerDataFindingDropsFileContext(t *testing.T) {
	// A source file outside the customer data paths, in which a rule reports
	// customer data: other lines of it may hold customer data no rule found.
	content := "// owner Hong Gildong\nString mail = \"" + phone + "\";\nString h = \"Bearer " + bearer + "\";\n"
	e := newEnv(t, map[string]string{"src/A.java": content})
	customer := &needleScanner{tool: "betterleaks", rule: "customer-phone", category: finding.CategoryCustomerData, kind: finding.KindPhone, needle: phone, lineCol: true}
	if err := e.handle(t.Context(), bearerBL(), customer); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if len(got.Findings) != 2 {
		t.Fatalf("Handle() = %+v, want two findings", got)
	}
	for _, f := range got.Findings {
		for _, l := range f.Locations {
			if l.Context != nil || l.ContextFirstLine != 0 {
				t.Errorf("Handle() %s location = %+v, want no surrounding lines in a file with customer data", f.Kind, l)
			}
		}
	}
	for _, dump := range dumps(t, got) {
		if v := containsAny(dump, "Hong Gildong", bearer, phone); v != "" {
			t.Errorf("Handle() saved a result that holds %q:\n%s", v, dump)
		}
	}
}
