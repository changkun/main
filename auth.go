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

	"latere.ai/x/pkg/authkit/jwt"
)

// latereVerifier accepts the RS256 access tokens that auth.latere.ai issues to
// the blog compose box through browser PKCE.
//
// Signature, issuer and expiry come from the JWKS document. Posting rights do
// not: any latere account can mint a token for any client, so a valid
// signature only proves *who* is calling. The allowlist decides whether that
// principal may write to the blog.
//
// The client list decides which login the token came from. A latere login
// token is addressed to the issuer, not to this API, so a token the same
// person handed to any other latere app verifies here just as well. Only the
// clients listed, by default the compose box's own, may reach the API.
type latereVerifier struct {
	auth    *jwt.Authenticator
	allowed map[string]bool // lowercased email or principal id (sub)
	clients map[string]bool // lowercased OAuth client ids
	log     *log.Logger
}

// defaultClient is the OAuth client the blog's compose box logs in with,
// named in login-sdk.js.
const defaultClient = "changkun-blog"

// newLatereVerifier builds a verifier from the environment. It returns nil
// when AUTH_ALLOWED_PRINCIPALS is unset: with no allowlist there is no safe
// answer, so latere tokens are refused outright rather than trusted wholesale.
func newLatereVerifier(l *log.Logger) *latereVerifier {
	allowed := principalSet(os.Getenv("AUTH_ALLOWED_PRINCIPALS"))
	if len(allowed) == 0 {
		l.Println("AUTH_ALLOWED_PRINCIPALS is unset, latere tokens will be rejected")
		return nil
	}
	clients := principalSet(cmp.Or(os.Getenv("AUTH_ALLOWED_CLIENTS"), defaultClient))

	issuer := strings.TrimRight(cmp.Or(os.Getenv("AUTH_URL"), "https://auth.latere.ai"), "/")
	jwks := cmp.Or(os.Getenv("AUTH_JWKS_URL"), issuer+"/.well-known/jwks.json")
	l.Printf("latere auth enabled: issuer=%s principals=%d clients=%d", issuer, len(allowed), len(clients))

	return &latereVerifier{
		auth:    jwt.NewAuthenticator(jwt.New(jwt.Config{JWKSURL: jwks, Issuer: issuer})),
		allowed: allowed,
		clients: clients,
		log:     l,
	}
}

// principalSet parses a comma-separated list of emails, principal ids or
// client ids.
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
// principal and minted for an allowlisted client.
//
// jwt.Authenticator decides identity: it reads the Bearer header and validates
// the signature, issuer and expiry. The allowlists decide authority, and stay
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
	if !v.clients[strings.ToLower(id.ClientID)] {
		v.log.Printf("latere client not allowed: sub=%s email=%s client=%s",
			id.Sub, id.Email, id.ClientID)
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
