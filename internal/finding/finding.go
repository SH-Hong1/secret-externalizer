// Package finding defines what the scan reports about a hard-coded secret or a
// piece of customer data, and masks everything that could reveal the value.
// None of its types has a field for a raw value: only a fingerprint, a masked
// preview, a location and, for secrets, a few masked surrounding lines.
package finding

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrUnknown means a category or kind name is not one the package defines.
var ErrUnknown = errors.New("unknown name")

// Category groups findings by what a leaked value gives access to.
type Category string

// The categories a finding can have.
const (
	CategoryPartnerSession Category = "partner-session"
	CategoryStorage        Category = "storage-credential"
	CategorySSH            Category = "ssh-credential"
	CategoryCustomerData   Category = "customer-data"
	CategoryOther          Category = "other-secret"
)

// ParseCategory returns the category named s. It fails with ErrUnknown for
// anything else.
func ParseCategory(s string) (Category, error) {
	switch c := Category(s); c {
	case CategoryPartnerSession, CategoryStorage, CategorySSH, CategoryCustomerData, CategoryOther:
		return c, nil
	}
	return "", fmt.Errorf("category %q: %w", s, ErrUnknown)
}

// Kind says what a detected value is.
type Kind string

// The kinds of customer data.
const (
	KindPhone   Kind = "phone"
	KindRRN     Kind = "rrn"
	KindEmail   Kind = "email"
	KindCard    Kind = "card"
	KindAddress Kind = "address"
)

// The kinds of secret value.
const (
	KindPassword   Kind = "password"
	KindToken      Kind = "token"
	KindAccount    Kind = "account"
	KindHost       Kind = "host"
	KindPort       Kind = "port"
	KindURL        Kind = "url"
	KindPrivateKey Kind = "private-key"
	KindKeyFile    Kind = "key-file"
	KindOther      Kind = "other"
)

// ParseKind returns the kind named s. It fails with ErrUnknown for anything
// else.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindPhone, KindRRN, KindEmail, KindCard, KindAddress,
		KindPassword, KindToken, KindAccount, KindHost, KindPort, KindURL, KindPrivateKey, KindKeyFile, KindOther:
		return k, nil
	}
	return "", fmt.Errorf("kind %q: %w", s, ErrUnknown)
}

// Detection names the tool and the rule that reported a location.
type Detection struct {
	Tool, Rule string
}

// Span is a range of bytes in a file's Text. End is not part of the range.
type Span struct {
	Start, End int
}

// Location is one place a value was found. Context holds the lines around the
// value with every detected value masked; it is empty for customer data.
// ContextFirstLine is the file line number of Context[0].
type Location struct {
	StartLine, StartCol int
	EndLine, EndCol     int
	Context             []string
	ContextFirstLine    int
	FileHash            string
	Detections          []Detection
}

// Finding is a detected value, identified by its fingerprint. It never holds
// the value itself.
type Finding struct {
	Fingerprint string
	File        string
	Category    Category
	Kind        Kind
	Preview     string
	HasOriginal bool
	Locations   []Location
}

const (
	maskText      = "****"
	previewPrefix = 4
	previewMinLen = 8
)

// Preview returns the masked form of value that is safe to store and show.
// A secret of at least eight characters keeps its first four characters;
// shorter secrets, keys and key files show nothing. Customer data shows only a
// format: a phone number keeps its leading digit group, and every other kind
// is a fixed string. An unknown category or kind gives "****".
func Preview(c Category, k Kind, value string) string {
	switch c {
	case CategoryCustomerData:
		return customerPreview(k, value)
	case CategoryPartnerSession, CategoryStorage, CategorySSH, CategoryOther:
		return secretPreview(k, value)
	}
	return maskText
}

func customerPreview(k Kind, value string) string {
	switch k {
	case KindPhone:
		return phonePreview(value)
	case KindRRN:
		return "******-*******"
	case KindEmail:
		return "****@****"
	case KindCard:
		return "****-****-****-****"
	case KindAddress, KindPassword, KindToken, KindAccount, KindHost, KindPort, KindURL, KindPrivateKey, KindKeyFile, KindOther:
		return maskText
	}
	return maskText
}

func secretPreview(k Kind, value string) string {
	switch k {
	case KindPassword, KindToken, KindAccount, KindHost, KindPort, KindURL, KindOther:
	case KindPhone, KindRRN, KindEmail, KindCard, KindAddress, KindPrivateKey, KindKeyFile:
		return maskText
	default:
		return maskText
	}
	if utf8.RuneCountInString(value) < previewMinLen {
		return maskText
	}
	var head strings.Builder
	n := 0
	for _, r := range value {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return maskText
		}
		head.WriteRune(r)
		if n++; n == previewPrefix {
			break
		}
	}
	return head.String() + maskText
}

// phonePreview keeps at most the first three digits of the leading digit
// group, together with a leading "+" and the separators, and replaces every
// other character with "*".
func phonePreview(value string) string {
	const keepDigits = 3
	var out strings.Builder
	started, done, kept := false, false, 0
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			if done || kept == keepDigits {
				out.WriteByte('*')
				break
			}
			started = true
			kept++
			out.WriteRune(r)
		case r == '+' && !started && !done:
			started = true
			out.WriteRune(r)
		case r == '-' || r == ' ' || r == '.' || r == '(' || r == ')' || r == '/':
			if started {
				done = true
			}
			out.WriteRune(r)
		default:
			out.WriteByte('*')
		}
	}
	return out.String()
}
