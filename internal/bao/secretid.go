package bao

import (
	"fmt"
	"os"
	"strings"
)

// ReadSecretIDFile reads an AppRole secret-id from the file at path, without
// surrounding white space. A missing file gives an error matching
// fs.ErrNotExist. The errors of os.ReadFile name the path and never the
// content.
func ReadSecretIDFile(path string) (SecretID, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own BAO_SECRET_ID_FILE setting
	if err != nil {
		return "", fmt.Errorf("reading the secret-id file: %w", err)
	}
	s := SecretID(strings.TrimSpace(string(b)))
	if s == "" {
		return "", fmt.Errorf("%q is empty", path)
	}
	return s, nil
}
