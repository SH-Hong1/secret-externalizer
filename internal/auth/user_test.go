package auth_test

import (
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
)

func TestUser_Has(t *testing.T) {
	tests := []struct {
		name      string
		roles     []auth.Role
		wantUser  bool
		wantAdmin bool
	}{
		{"user", []auth.Role{auth.RoleUser}, true, false},
		{"admin", []auth.Role{auth.RoleAdmin}, true, true},
		{"none", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := auth.User{Subject: "sub-1", Roles: tt.roles}
			if got := u.Has(auth.RoleUser); got != tt.wantUser {
				t.Errorf("User{Roles: %v}.Has(RoleUser) = %v, want %v", tt.roles, got, tt.wantUser)
			}
			if got := u.Has(auth.RoleAdmin); got != tt.wantAdmin {
				t.Errorf("User{Roles: %v}.Has(RoleAdmin) = %v, want %v", tt.roles, got, tt.wantAdmin)
			}
		})
	}
}
