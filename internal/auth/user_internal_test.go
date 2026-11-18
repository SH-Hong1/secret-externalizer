package auth

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRolesFromClaim(t *testing.T) {
	tests := []struct {
		name    string
		names   []string
		want    []Role
		wantErr error
	}{
		{"user and unknown", []string{"user", "offline_access"}, []Role{RoleUser}, nil},
		{"admin and user", []string{"admin", "user"}, []Role{RoleAdmin, RoleUser}, nil},
		{"unknown only", []string{"offline_access"}, nil, ErrNoRole},
		{"nil", nil, nil, ErrNoRole},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rolesFromClaim(tt.names)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("rolesFromClaim(%q) error = %v, want %v", tt.names, err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("rolesFromClaim(%q) mismatch (-want +got):\n%s", tt.names, diff)
			}
		})
	}
}
