// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"log"
	"net/http"
	"os"
	"strings"

	"latere.ai/x/pkg/authkit"
	"latere.ai/x/pkg/jwtauth"
)

// latereVerifier accepts the RS256 access tokens that auth.latere.ai issues to
// the blog compose box through browser PKCE.
//
// Signature, issuer and expiry come from the JWKS document. Posting rights do
// not: any latere account can mint a token for any client, so a valid
// signature only proves *who* is calling. The allowlist decides whether that
// principal may write to the blog.
type latereVerifier struct {
	auth    *authkit.JWT
	allowed map[string]bool // lowercased email or principal id (sub)
	log     *log.Logger
}

// newLatereVerifier builds a verifier from the environment. It returns nil
// when AUTH_ALLOWED_PRINCIPALS is unset: with no allowlist there is no safe
// answer, so latere tokens are refused outright rather than trusted wholesale.
func newLatereVerifier(l *log.Logger) *latereVerifier {
	allowed := principalSet(os.Getenv("AUTH_ALLOWED_PRINCIPALS"))
	if len(allowed) == 0 {
		l.Println("AUTH_ALLOWED_PRINCIPALS is unset, latere tokens will be rejected")
		return nil
	}

	issuer := strings.TrimRight(cmp.Or(os.Getenv("AUTH_URL"), "https://auth.latere.ai"), "/")
	jwks := cmp.Or(os.Getenv("AUTH_JWKS_URL"), issuer+"/.well-known/jwks.json")
	l.Printf("latere auth enabled: issuer=%s principals=%d", issuer, len(allowed))

	return &latereVerifier{
		auth:    authkit.NewJWT(jwtauth.New(jwtauth.Config{JWKSURL: jwks, Issuer: issuer}), nil),
		allowed: allowed,
		log:     l,
	}
}

// principalSet parses a comma-separated list of emails and principal ids.
func principalSet(s string) map[string]bool {
	set := map[string]bool{}
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			set[p] = true
		}
	}
	return set
}

// allow reports whether r carries a latere token belonging to an allowlisted
// principal.
//
// authkit.JWT decides identity: it reads the Bearer header and validates the
// signature, issuer and expiry. The allowlist decides authority, and stays
// here, because who may write to this blog is not something a token can say.
//
// A nil receiver denies everything. newLatereVerifier returns nil when no
// allowlist is configured, and that must fail closed rather than panic.
func (v *latereVerifier) allow(r *http.Request) bool {
	if v == nil {
		return false
	}
	id, err := v.auth.Authenticate(r)
	if err != nil {
		return false
	}
	if v.allowed[strings.ToLower(id.Email)] || v.allowed[strings.ToLower(id.Sub)] {
		return true
	}
	v.log.Printf("latere principal not allowed: sub=%s email=%s client=%s",
		id.Sub, id.Email, id.ClientID)
	return false
}

// auth admits one credential: a latere access token carried as a Bearer.
// Everything else is rejected.
func auth(latere *latereVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ideas/ping" {
			next.ServeHTTP(w, r)
			return
		}

		if latere.allow(r) {
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}
