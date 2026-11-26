//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package sso

import (
	"context"
	"errors"

	"github.com/coreos/go-oidc/v3/oidc"
)

// IDTokenVerifier checks a bearer ID token outside the browser flow, for a client that signed in
// elsewhere and presents the token on each request.
type IDTokenVerifier struct {
	v     *oidc.IDTokenVerifier
	claim string
}

// NewIDTokenVerifier discovers the issuer. The audience is the client id the token was issued to.
// groupsClaim defaults to "groups". As in NewOIDC, only RS256 is accepted.
func NewIDTokenVerifier(ctx context.Context, issuer, audience, groupsClaim string) (*IDTokenVerifier, error) {
	if issuer == "" || audience == "" {
		return nil, errors.New("sso: issuer and audience are required")
	}
	if groupsClaim == "" {
		groupsClaim = "groups"
	}
	prov, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &IDTokenVerifier{v: prov.Verifier(&oidc.Config{ClientID: audience, SupportedSigningAlgs: []string{oidc.RS256}}), claim: groupsClaim}, nil
}

// Verify checks signature, issuer, audience and expiry, then returns the session the token states.
func (t *IDTokenVerifier) Verify(ctx context.Context, raw string) (Session, error) {
	id, err := t.v.Verify(ctx, raw)
	if err != nil {
		return Session{}, errors.New("the ID token is not valid")
	}
	return sessionOf(id, t.claim)
}
