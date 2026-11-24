package scan_test

import (
	"encoding/base64"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
)

// decodedSecret is a value that a file holds only in base64.
const decodedSecret = "dev-only-fake-decoded-7" //nolint:gosec // G101: an obviously fake fixture value

// decodedAt returns a betterleaks result of decodedSecret, found by decoding
// base64 that starts at line 2, column 6 of a file, in category c.
func decodedAt(file string, c finding.Category, k finding.Kind) *needleScanner {
	return onlyAt(scan.Raw{
		Tool: "betterleaks", Rule: "generic-password", Category: c, Kind: k,
		File: file, Offset: -1, EndOffset: -1, Line: 2, ByteCol: 6, Value: scan.Value(decodedSecret),
		Encoding: finding.EncodingBase64, Depth: 1,
	})
}

func TestHandle_EncodedValueIsAFinding(t *testing.T) {
	blob := base64.StdEncoding.EncodeToString([]byte("db.password=" + decodedSecret))
	content := "a.user=dev\nblob=" + blob + "\njdbc.password=" + password + "\nnote " + decodedSecret + "\n"
	e := newEnv(t, map[string]string{propsFile: content})
	if err := e.handle(t.Context(), passwordOG(), decodedAt(propsFile, finding.CategoryOther, finding.KindOther)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 0 || len(got.Findings) != 2 {
		t.Fatalf("Handle() = %+v, want the password and the encoded value, nothing dropped", got)
	}
	var enc *finding.Finding
	for i := range got.Findings {
		if got.Findings[i].Fingerprint == fingerprintOf(blob) {
			enc = &got.Findings[i]
		}
	}
	if enc == nil {
		t.Fatalf("Handle() findings = %+v, want one whose value is the encoded text", got.Findings)
	}
	if enc.Key != "blob" || enc.Preview != finding.Preview(finding.CategoryOther, finding.KindOther, blob) || !enc.HasOriginal {
		t.Errorf("Handle() encoded finding = %+v, want key blob, the preview of the encoded text, and an original", enc)
	}
	l := enc.Locations[0]
	if l.StartLine != 2 || l.StartCol != 6 || l.EndLine != 2 || l.EndCol != 5+len(blob) {
		t.Errorf("Handle() encoded location = %+v, want line 2, columns 6 to %d", l, 5+len(blob))
	}
	wantDet := []finding.Detection{{Tool: "betterleaks", Rule: "generic-password", Encoding: finding.EncodingBase64}}
	if diff := cmp.Diff(wantDet, l.Detections); diff != "" {
		t.Errorf("Handle() encoded detections (-want +got):\n%s", diff)
	}
	// Both values are placed, so the file keeps its surrounding lines, with
	// the encoded text and the decoded value masked wherever they are.
	wantContext := []string{"a.user=dev", "blob=****", "jdbc.password=****", "note ****"}
	if diff := cmp.Diff(wantContext, l.Context); diff != "" {
		t.Errorf("Handle() surrounding lines of the encoded value (-want +got):\n%s", diff)
	}
	for _, dump := range dumps(t, got) {
		if v := containsAny(dump, blob, decodedSecret, password); v != "" {
			t.Errorf("Handle() saved a result that holds %q:\n%s", v, dump)
		}
	}
}

// Customer data is never stored, and its preview reads the shape of the
// value, so customer data a scanner found by decoding stays unplaced.
func TestHandle_EncodedCustomerDataIsDropped(t *testing.T) {
	blob := base64.StdEncoding.EncodeToString([]byte("tel=" + decodedSecret))
	e := newEnv(t, map[string]string{propsFile: "a=1\nblob=" + blob + "\njdbc.password=" + password + "\n"})
	if err := e.handle(t.Context(), passwordOG(), decodedAt(propsFile, finding.CategoryCustomerData, finding.KindPhone)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 1 || len(got.Findings) != 1 {
		t.Fatalf("Handle() = %+v, want the password found and the encoded customer data dropped", got)
	}
	if l := got.Findings[0].Locations[0]; l.Context != nil {
		t.Errorf("surrounding lines of the password = %q, want none", l.Context)
	}
}

// Text that does not decode to the reported value is not placed.
func TestHandle_EncodedValueNotThereIsDropped(t *testing.T) {
	blob := base64.StdEncoding.EncodeToString([]byte("db.password=something-else"))
	e := newEnv(t, map[string]string{propsFile: "a=1\nblob=" + blob + "\njdbc.password=" + password + "\n"})
	if err := e.handle(t.Context(), passwordOG(), decodedAt(propsFile, finding.CategoryOther, finding.KindOther)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	got := e.result(t)
	if got.Dropped != 1 || len(got.Findings) != 1 || got.Findings[0].Locations[0].Context != nil {
		t.Fatalf("Handle() = %+v, want the password without surrounding lines and the encoded value dropped", got)
	}
}
