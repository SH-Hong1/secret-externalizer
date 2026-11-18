package main

import (
	"context"
	"errors"

	"github.com/SH-Hong1/secret-externalizer/internal/auth"
	"github.com/SH-Hong1/secret-externalizer/internal/session"
)

// errLoginNotConfigured is returned by every method of unconfigured.
var errLoginNotConfigured = errors.New("login is not configured")

// unconfigured stands in for the identity provider and the session store
// until they are wired in. Every store call fails, so no request is ever
// treated as signed in: pages that need a login show the 503 page.
type unconfigured struct{}

// AuthCodeURL is never reached, because PutAttempt fails first.
func (unconfigured) AuthCodeURL(_, _, _ string) string { return "/" }

func (unconfigured) Exchange(context.Context, string, string, string) (auth.User, error) {
	return auth.User{}, errLoginNotConfigured
}

func (unconfigured) LogoutURL() string { return "/" }

func (unconfigured) Create(context.Context, auth.User) (session.ID, error) {
	return "", errLoginNotConfigured
}

func (unconfigured) Get(context.Context, session.ID) (session.Session, error) {
	return session.Session{}, errLoginNotConfigured
}

func (unconfigured) Delete(context.Context, session.ID) error { return errLoginNotConfigured }

func (unconfigured) PutAttempt(context.Context, auth.Attempt) (session.ID, error) {
	return "", errLoginNotConfigured
}

func (unconfigured) TakeAttempt(context.Context, session.ID) (auth.Attempt, error) {
	return auth.Attempt{}, errLoginNotConfigured
}
