// Package auth runs the OpenID Connect login and holds the identity types it
// produces: the signed-in user, its roles, the client secret, and the state of
// one login attempt.
package auth

import (
	"errors"
	"slices"
)

// Role is a permission level granted by the identity provider.
type Role string

// The roles this application knows. Any other role name from the identity
// provider is ignored.
const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// ErrNoRole means the identity provider gave the user neither role.
var ErrNoRole = errors.New("user has no role in this application")

// User is a signed-in person as the identity provider describes them.
type User struct {
	Subject string
	Name    string
	Roles   []Role
}

// Has reports whether u may do what r allows. RoleAdmin includes RoleUser.
func (u User) Has(r Role) bool {
	if slices.Contains(u.Roles, RoleAdmin) {
		return true
	}
	return slices.Contains(u.Roles, r)
}

// rolesFromClaim keeps the known roles in names and drops the rest. It returns
// ErrNoRole when none is known.
func rolesFromClaim(names []string) ([]Role, error) {
	var roles []Role
	for _, n := range names {
		r := Role(n)
		if r != RoleUser && r != RoleAdmin {
			continue
		}
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		return nil, ErrNoRole
	}
	return roles, nil
}
