// Package keyfile finds key stores, private keys, and certificates kept as
// files in a project folder.
package keyfile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
	"github.com/SH-Hong1/secret-externalizer/internal/scan"
	"github.com/SH-Hong1/secret-externalizer/internal/textfile"
)

const (
	name = "keyfile"
	// version changes whenever what the detector reports changes.
	version = "1"
	// headLen is how much of each file is searched for a PEM marker.
	headLen = 4096
)

// The rules a result can come from.
const (
	ruleName      = "key-file-name"
	ruleExtension = "key-file-extension"
	rulePrivate   = "pem-private-key"
	ruleCert      = "pem-certificate"
)

// Scanner finds key files. Build it with New.
type Scanner struct {
	privateKey, certificate *regexp.Regexp
}

// New returns a Scanner.
func New() *Scanner {
	return &Scanner{
		// A marker counts only as a whole line, so a source file that merely
		// mentions a private key, or holds a marker inside a string literal,
		// is not a key file.
		privateKey:  regexp.MustCompile(`(?m)^-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----\r?$`),
		certificate: regexp.MustCompile(`(?m)^-----BEGIN CERTIFICATE-----\r?$`),
	}
}

// Name returns "keyfile".
func (*Scanner) Name() string { return name }

// Version returns the version of the detector.
func (*Scanner) Version(context.Context) (string, error) { return version, nil }

// Scan walks dir, an absolute path, and reports every regular file that is a
// key file: one named like an SSH private key (id_rsa, id_dsa, id_ecdsa,
// id_ed25519), one with a key store extension (.jks, .p12, .pfx, .keystore,
// .ppk), or one whose first 4 KiB hold a PEM private key or certificate
// marker line, leaving out excludes, paths relative to dir with forward
// slashes. A result covers the whole file and holds no value. Files that
// cannot be read are left out; the walk that lists the files to scan counts
// them.
func (s *Scanner) Scan(ctx context.Context, dir string, excludes []string) ([]scan.Raw, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("scanning %s: the folder is not an absolute path", name)
	}
	var raws []scan.Raw
	_, err := textfile.Walk(dir, excludes, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rule, err := s.match(dir, rel)
		if err != nil {
			return err
		}
		if rule != "" {
			raws = append(raws, scan.Raw{
				Tool: name, Rule: rule, Category: finding.CategorySSH, Kind: finding.KindKeyFile,
				File: rel, Offset: 0, EndOffset: -1,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s for key files: %w", dir, err)
	}
	return raws, nil
}

// match returns the rule that makes the file rel under dir a key file, or
// "". A file that is gone, cannot be read, or is no longer a regular file
// matches no rule by its content.
func (s *Scanner) match(dir, rel string) (string, error) {
	base := strings.ToLower(path.Base(rel))
	switch base {
	case "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		return ruleName, nil
	}
	switch path.Ext(base) {
	case ".jks", ".p12", ".pfx", ".keystore", ".ppk":
		return ruleExtension, nil
	}
	head, err := textfile.ReadHead(dir, rel, headLen)
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, textfile.ErrNotRegular) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case s.privateKey.Match(head):
		return rulePrivate, nil
	case s.certificate.Match(head):
		return ruleCert, nil
	}
	return "", nil
}
