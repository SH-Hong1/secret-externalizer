package finding_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

func TestPreview_Table(t *testing.T) {
	tests := []struct {
		name  string
		c     finding.Category
		k     finding.Kind
		value string
		want  string
	}{
		{"long password", finding.CategoryOther, finding.KindPassword, "dev-only-fake-password", "dev-****"},
		{"exactly eight", finding.CategoryOther, finding.KindToken, "12345678", "1234****"},
		{"seven", finding.CategoryOther, finding.KindToken, "1234567", "****"},
		{"empty", finding.CategoryOther, finding.KindOther, "", "****"},
		{"hangul eight characters", finding.CategoryPartnerSession, finding.KindToken, "가나다라마바사아", "가나다라****"},
		{"hangul seven characters", finding.CategoryPartnerSession, finding.KindToken, "가나다라마바사", "****"},
		{"control character in prefix", finding.CategoryOther, finding.KindPassword, "ab\ncdefghij", "****"},
		{"control character after prefix", finding.CategoryOther, finding.KindPassword, "abcd\nefghij", "abcd****"},
		{"private key", finding.CategorySSH, finding.KindPrivateKey, "-----BEGIN FAKE KEY-----", "****"},
		{"key file", finding.CategoryStorage, finding.KindKeyFile, "dev-only-fake-key.pem", "****"},
		{"phone", finding.CategoryCustomerData, finding.KindPhone, "010-0000-0000", "010-****-****"},
		{"phone seoul", finding.CategoryCustomerData, finding.KindPhone, "02-000-0000", "02-***-****"},
		{"phone country code", finding.CategoryCustomerData, finding.KindPhone, "+82-10-0000-0000", "+82-**-****-****"},
		{"phone without separators", finding.CategoryCustomerData, finding.KindPhone, "01000000000", "010********"},
		{"phone with newline", finding.CategoryCustomerData, finding.KindPhone, "010-0000\n0000", "010-*********"},
		{"rrn", finding.CategoryCustomerData, finding.KindRRN, "000000-0000000", "******-*******"},
		{"rrn ignores input", finding.CategoryCustomerData, finding.KindRRN, "anything", "******-*******"},
		{"email", finding.CategoryCustomerData, finding.KindEmail, "dev-only-fake@example.invalid", "****@****"},
		{"card", finding.CategoryCustomerData, finding.KindCard, "0000-0000-0000-0000", "****-****-****-****"},
		{"address", finding.CategoryCustomerData, finding.KindAddress, "fake road 1", "****"},
		{"customer data without kind", finding.CategoryCustomerData, "", "dev-only-fake-value", "****"},
		{"customer data unknown kind", finding.CategoryCustomerData, "bogus", "dev-only-fake-value", "****"},
		{"customer kind under secret category", finding.CategoryOther, finding.KindPhone, "010-0000-0000", "****"},
		{"unknown category", "bogus", finding.KindPassword, "dev-only-fake-password", "****"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := finding.Preview(tt.c, tt.k, tt.value)
			if got != tt.want {
				t.Errorf("Preview(%q, %q, %q) = %q, want %q", tt.c, tt.k, tt.value, got, tt.want)
			}
		})
	}
}

func TestPreview_NeverContainsDigitsOfCustomerData(t *testing.T) {
	tests := []struct {
		name  string
		k     finding.Kind
		value string
	}{
		{"phone", finding.KindPhone, "010-0001-0002"},
		{"phone no separators", finding.KindPhone, "01000010002"},
		{"phone country code", finding.KindPhone, "+82-10-0003-0004"},
		{"rrn", finding.KindRRN, "000101-0000002"},
		{"card", finding.KindCard, "0001-0002-0003-0004"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := finding.Preview(finding.CategoryCustomerData, tt.k, tt.value)
			digits := 0
			for _, r := range got {
				if unicode.IsDigit(r) {
					digits++
				}
			}
			maxDigits := 0
			if tt.k == finding.KindPhone {
				maxDigits = 3
			}
			if digits > maxDigits {
				t.Errorf("Preview(customer-data, %q, %q) = %q, has %d digits, want at most %d", tt.k, tt.value, got, digits, maxDigits)
			}
			for _, leak := range []string{"0001", "0002", "0003", "0004", "10002", "10-0003"} {
				if strings.Contains(got, leak) {
					t.Errorf("Preview(customer-data, %q, %q) = %q, contains %q", tt.k, tt.value, got, leak)
				}
			}
		})
	}
}

func TestParseCategory(t *testing.T) {
	for _, s := range []string{"partner-session", "storage-credential", "ssh-credential", "customer-data", "other-secret"} {
		got, err := finding.ParseCategory(s)
		if err != nil || string(got) != s {
			t.Errorf("ParseCategory(%q) = %q, %v, want %q, nil", s, got, err, s)
		}
	}
}

func TestParseCategory_Unknown(t *testing.T) {
	for _, s := range []string{"", "Customer-Data", "customer-data ", "bogus"} {
		if got, err := finding.ParseCategory(s); err == nil {
			t.Errorf("ParseCategory(%q) = %q, nil, want an error", s, got)
		}
	}
}

func TestParseKind(t *testing.T) {
	for _, s := range []string{"phone", "rrn", "email", "card", "address", "password", "token", "account", "host", "url", "private-key", "key-file", "other"} {
		got, err := finding.ParseKind(s)
		if err != nil || string(got) != s {
			t.Errorf("ParseKind(%q) = %q, %v, want %q, nil", s, got, err, s)
		}
	}
	for _, s := range []string{"", "Phone", "secret", "phone "} {
		if got, err := finding.ParseKind(s); err == nil {
			t.Errorf("ParseKind(%q) = %q, nil, want an error", s, got)
		}
	}
}
