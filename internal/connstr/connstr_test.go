package connstr_test

import (
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/SH-Hong1/secret-externalizer/internal/connstr"
	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// part is a component as the tests compare it: its kind and the text of
// its span.
type part struct {
	Kind finding.Kind
	Text string
}

func TestSplit(t *testing.T) {
	const (
		host    = finding.KindHost
		port    = finding.KindPort
		account = finding.KindAccount
		pw      = finding.KindPassword
	)
	tests := []struct {
		in   string
		want []part
	}{
		{"jdbc:mysql://db.invalid:3306/shop", []part{{host, "db.invalid"}, {port, "3306"}}},
		{
			"jdbc:mysql://db.invalid/shop?user=shop_app&password=dev-only-fake-pw",
			[]part{{host, "db.invalid"}, {account, "shop_app"}, {pw, "dev-only-fake-pw"}},
		},
		{
			"jdbc:mysql://shop_app:dev-only-fake-pw@db.invalid:3306/shop",
			[]part{{account, "shop_app"}, {pw, "dev-only-fake-pw"}, {host, "db.invalid"}, {port, "3306"}},
		},
		{"jdbc:oracle:thin:@db.invalid:1521:ORCL", []part{{host, "db.invalid"}, {port, "1521"}}},
		{
			"jdbc:oracle:thin:shop_app/dev-only-fake-pw@db.invalid:1521:ORCL",
			[]part{{account, "shop_app"}, {pw, "dev-only-fake-pw"}, {host, "db.invalid"}, {port, "1521"}},
		},
		{"jdbc:oracle:thin:@//db.invalid:1521/svc", []part{{host, "db.invalid"}, {port, "1521"}}},
		{
			"jdbc:sqlserver://db.invalid:1433;databaseName=shop;user=sa;password=dev-only-fake-pw",
			[]part{{host, "db.invalid"}, {port, "1433"}, {account, "sa"}, {pw, "dev-only-fake-pw"}},
		},
		{"jdbc:postgresql://[::1]:5432/shop", []part{{host, "::1"}, {port, "5432"}}},
		{"deploy@host.invalid", []part{{account, "deploy"}, {host, "host.invalid"}}},
		{"deploy@host.invalid:2222", []part{{account, "deploy"}, {host, "host.invalid"}, {port, "2222"}}},
		{"deploy@host.invalid:/srv/app", []part{{account, "deploy"}, {host, "host.invalid"}}},
		{"jdbc:mysql://${db.host}:${db.port}/shop", nil},
		{"jdbc:mysql://${db.host}:3306/shop", []part{{port, "3306"}}},
		{"jdbc:h2:mem:test", nil},
		{
			"jdbc:sqlserver://db.invalid\\inst;user=sa;password={dev;only&fake}",
			[]part{{host, "db.invalid"}, {account, "sa"}, {pw, "dev;only&fake"}},
		},
		{
			"jdbc:mysql://a.invalid:3306,b.invalid:3307/shop",
			[]part{{host, "a.invalid"}, {port, "3306"}, {host, "b.invalid"}, {port, "3307"}},
		},
		{
			"jdbc:oracle:thin:shop_app/dev-only-fake-pw@(DESCRIPTION=(ADDRESS=(HOST=db.invalid)))",
			[]part{{account, "shop_app"}, {pw, "dev-only-fake-pw"}},
		},
		{"jdbc:mysql://${db.host:localhost}:3306/shop", []part{{port, "3306"}}},
		{"jdbc:mysql://db.invalid:port/shop", nil},
		{"jdbc:mysql://[::1/shop", nil},
		{"jdbc:mysql://\xa9@db.invalid", nil},
		{
			"jdbc:postgresql://db.invalid/shop?user=a_user&sslpassword=dev-only-fake-ssl",
			[]part{{host, "db.invalid"}, {account, "a_user"}, {pw, "dev-only-fake-ssl"}},
		},
		{
			"jdbc:mysql://db.invalid/shop?trustCertificateKeyStorePassword=dev-only-fake-ts&clientCertificateKeyStorePassword=dev-only-fake-cs",
			[]part{{host, "db.invalid"}, {pw, "dev-only-fake-ts"}, {pw, "dev-only-fake-cs"}},
		},
		{
			"jdbc:sqlserver://db.invalid;accessToken=dev-only-fake-token;keyStoreSecret=dev-only-fake-secret;pwd=dev-only-fake-pwd",
			[]part{{host, "db.invalid"}, {pw, "dev-only-fake-token"}, {pw, "dev-only-fake-secret"}, {pw, "dev-only-fake-pwd"}},
		},
		{"jdbc:mysql://u:12#ab@db.invalid/x", nil},
		{"jdbc:mysql://u:12?ab@db.invalid/x", nil},
		{"jdbc:mysql://u:12/ab@db.invalid/x", nil},
		{"not a url", nil},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			value := []byte(tt.in)
			parts := connstr.Split(value)
			var got []part
			end := 0
			for _, p := range parts {
				if p.Span.Start < end || p.Span.End <= p.Span.Start || p.Span.End > len(value) {
					t.Fatalf("Split(%q) = %+v, want ordered, non-empty, non-overlapping spans inside the value", tt.in, parts)
				}
				end = p.Span.End
				got = append(got, part{Kind: p.Kind, Text: string(value[p.Span.Start:p.Span.End])})
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Split(%q) (-want +got):\n%s", tt.in, diff)
			}
		})
	}
}

func FuzzSplit(f *testing.F) {
	for _, s := range []string{
		"jdbc:mysql://shop_app:dev-only-fake-pw@db.invalid:3306/shop",
		"jdbc:mysql://db.invalid/shop?user=shop_app&password=dev-only-fake-pw",
		"jdbc:oracle:thin:shop_app/dev-only-fake-pw@db.invalid:1521:ORCL",
		"jdbc:oracle:thin:@//db.invalid:1521/svc",
		"jdbc:sqlserver://db.invalid:1433;user=sa;password=dev-only-fake-pw",
		"jdbc:postgresql://[::1]:5432/shop",
		"jdbc:mysql://${db.host:${x}}:3306/shop",
		"deploy@host.invalid:2222",
		"jdbc:mysql://한글:비밀@호스트:3306/db",
		"",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, value []byte) {
		end := 0
		for _, p := range connstr.Split(value) {
			s := p.Span
			if s.Start < end || s.End <= s.Start || s.End > len(value) {
				t.Fatalf("Split(%q) gave span %+v after %d, want ordered non-empty spans inside the value", value, s, end)
			}
			if !utf8.RuneStart(value[s.Start]) || (s.End < len(value) && !utf8.RuneStart(value[s.End])) {
				t.Fatalf("Split(%q) gave span %+v, want it on character boundaries", value, s)
			}
			end = s.End
		}
	})
}
