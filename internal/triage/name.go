package triage

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidName means a placeholder name does not have the stored form.
var ErrInvalidName = errors.New("invalid placeholder name")

// maxNameLen is the longest name, in bytes, that ParseName accepts.
const maxNameLen = 100

// namePrefix is the first segment every rendered form adds, so a stored name
// may not start with it.
const namePrefix = "ext"

var nameForm = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*){1,4}$`)

// Name is a placeholder name without its prefix, such as "db.main.password":
// two to five segments of lower-case letters and digits, each starting with
// a letter, joined by dots, at most 100 bytes long.
type Name string

// ParseName returns s as a Name. It fails with ErrInvalidName when s does not
// have the stored form or starts with the "ext" segment that the rendered
// forms add.
func ParseName(s string) (Name, error) {
	switch {
	case len(s) > maxNameLen:
		return "", fmt.Errorf("%w: longer than %d bytes", ErrInvalidName, maxNameLen)
	case !nameForm.MatchString(s):
		return "", fmt.Errorf("%w: %q is not two to five dot-separated lower-case segments", ErrInvalidName, s)
	case strings.HasPrefix(s, namePrefix+"."):
		return "", fmt.Errorf("%w: %q starts with the %q prefix", ErrInvalidName, s, namePrefix)
	}
	return Name(s), nil
}

// Config returns the form used in configuration, XML and Java files, such as
// "ext.db.main.password".
func (n Name) Config() string { return namePrefix + "." + string(n) }

// Shell returns the form used in shell scripts, such as
// "EXT_DB_MAIN_PASSWORD".
func (n Name) Shell() string {
	return strings.ToUpper(strings.ReplaceAll(n.Config(), ".", "_"))
}
