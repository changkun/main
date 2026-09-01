// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"latere.ai/x/pkg/authkit"
	"latere.ai/x/pkg/jwtauth"
)

const testKid = "test-key"

// authFixture is a latere auth service stood up in-process: an RSA key, the
// JWKS document served over HTTP, and a minter for tokens signed by that key.
type authFixture struct {
	key    *rsa.PrivateKey
	issuer string
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("cannot generate key: %v", err)
	}

	f := &authFixture{key: key}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": testKid,
			"alg": "RS256",
			"use": "sig",
			"n":   b64(key.N.Bytes()),
			"e":   b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	f.issuer = srv.URL
	return f
}

// token mints an access token shaped like the one auth.latere.ai issues to
// the changkun-blog PKCE client (see buildSession in the auth service).
func (f *authFixture) token(t *testing.T, claims map[string]any) string {
	t.Helper()

	payload := map[string]any{
		"sub":            "principal-1",
		"iss":            f.issuer,
		"exp":            time.Now().Add(time.Hour).Unix(),
		"principal_type": "user",
		"client_id":      "changkun-blog",
	}
	maps.Copy(payload, claims)

	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": testKid, "typ": "JWT"})
	if err != nil {
		t.Fatalf("cannot encode header: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("cannot encode payload: %v", err)
	}

	signing := b64(header) + "." + b64(body)
	sum := crypto.SHA256.New()
	sum.Write([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum.Sum(nil))
	if err != nil {
		t.Fatalf("cannot sign token: %v", err)
	}
	return signing + "." + b64(sig)
}

func (f *authFixture) verifier(allowed string) *latereVerifier {
	return &latereVerifier{
		auth: authkit.NewJWT(jwtauth.New(jwtauth.Config{
			JWKSURL: f.issuer + "/.well-known/jwks.json",
			Issuer:  f.issuer,
		}), nil),
		allowed: principalSet(allowed),
		log:     log.New(io.Discard, "", 0),
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func doAuth(v *latereVerifier, bearer string) *httptest.ResponseRecorder {
	h := auth(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodPost, "/ideas/post", nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestAuthLatereToken covers the browser compose path: the blog's login SDK
// runs PKCE against auth.latere.ai and sends the resulting RS256 access token
// as a Bearer. Before latere validation existed this returned 401.
func TestAuthLatereToken(t *testing.T) {
	f := newAuthFixture(t)

	tests := []struct {
		name    string
		allowed string
		claims  map[string]any
		want    int
	}{
		{
			name:    "allowlisted email",
			allowed: "hi@changkun.de",
			claims:  map[string]any{"email": "hi@changkun.de"},
			want:    http.StatusOK,
		},
		{
			name:    "allowlisted email is case-insensitive",
			allowed: "hi@changkun.de",
			claims:  map[string]any{"email": "Hi@Changkun.de"},
			want:    http.StatusOK,
		},
		{
			name:    "allowlisted principal id",
			allowed: "principal-1",
			claims:  map[string]any{"email": "someone@example.com"},
			want:    http.StatusOK,
		},
		{
			name:    "valid signature but foreign principal",
			allowed: "hi@changkun.de",
			claims:  map[string]any{"email": "stranger@example.com", "sub": "principal-2"},
			want:    http.StatusUnauthorized,
		},
		{
			name:    "expired token",
			allowed: "hi@changkun.de",
			claims: map[string]any{
				"email": "hi@changkun.de",
				"exp":   time.Now().Add(-time.Minute).Unix(),
			},
			want: http.StatusUnauthorized,
		},
		{
			name:    "foreign issuer",
			allowed: "hi@changkun.de",
			claims:  map[string]any{"email": "hi@changkun.de", "iss": "https://evil.example.com"},
			want:    http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := doAuth(f.verifier(tt.allowed), f.token(t, tt.claims)).Code
			if got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAuthForgedSignature rejects a token that carries the right claims but
// was signed by a key the JWKS document does not publish.
func TestAuthForgedSignature(t *testing.T) {
	f := newAuthFixture(t)

	forger := newAuthFixture(t)
	forger.issuer = f.issuer // claim the real issuer, sign with the wrong key

	tok := forger.token(t, map[string]any{"email": "hi@changkun.de"})
	if got := doAuth(f.verifier("hi@changkun.de"), tok).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", got, http.StatusUnauthorized)
	}
}

// TestAuthForeignClient pins what the verifier does and does not check. The
// allowlist gates the principal, never the client the token was minted for, so
// an allowlisted person reaches the API from any latere client.
func TestAuthForeignClient(t *testing.T) {
	f := newAuthFixture(t)

	tok := f.token(t, map[string]any{
		"email":     "hi@changkun.de",
		"client_id": "some-other-client",
	})
	if got := doAuth(f.verifier("hi@changkun.de"), tok).Code; got != http.StatusOK {
		t.Fatalf("status = %d, want %d", got, http.StatusOK)
	}
}

// TestAuthMalformedHeader covers the header parsing authkit.JWT now owns. A
// missing header, a scheme other than Bearer, and a bare prefix must all be
// refused rather than reaching the validator with a nonsense token.
func TestAuthMalformedHeader(t *testing.T) {
	f := newAuthFixture(t)
	v := f.verifier("hi@changkun.de")

	for _, h := range []string{"", "Bearer", "Bearer ", "bearer sometoken", "Basic dXNlcjpwdw==", "Token abc"} {
		t.Run(fmt.Sprintf("%q", h), func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/ideas/post", nil)
			if h != "" {
				r.Header.Set("Authorization", h)
			}
			w := httptest.NewRecorder()
			auth(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestAuthNoLoginFallback pins the removal of the login.changkun.de paths: an
// opaque Bearer and a cookie/query credential are verified against nothing, so
// both must fail rather than reach the handler.
func TestAuthNoLoginFallback(t *testing.T) {
	f := newAuthFixture(t)
	v := f.verifier("hi@changkun.de")

	if got := doAuth(v, "cli-token").Code; got != http.StatusUnauthorized {
		t.Fatalf("opaque bearer status = %d, want %d", got, http.StatusUnauthorized)
	}

	h := auth(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodPost, "/ideas/post?token=cli-token", nil)
	r.AddCookie(&http.Cookie{Name: "token", Value: "cli-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cookie/query status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestAuthDisabledLatere pins the fail-closed default: with no allowlist
// configured newLatereVerifier yields nil and latere tokens are refused.
func TestAuthDisabledLatere(t *testing.T) {
	f := newAuthFixture(t)
	t.Setenv("AUTH_ALLOWED_PRINCIPALS", "")

	v := newLatereVerifier(log.New(io.Discard, "", 0))
	if v != nil {
		t.Fatalf("newLatereVerifier() = %v, want nil without an allowlist", v)
	}
	tok := f.token(t, map[string]any{"email": "hi@changkun.de"})
	if got := doAuth(v, tok).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", got, http.StatusUnauthorized)
	}
}

// TestNewLatereVerifier checks the environment wiring, including the default
// issuer and the derived JWKS URL.
func TestNewLatereVerifier(t *testing.T) {
	t.Setenv("AUTH_ALLOWED_PRINCIPALS", " HI@changkun.de , principal-1 ,")
	t.Setenv("AUTH_URL", "https://auth.latere.ai/")

	v := newLatereVerifier(log.New(io.Discard, "", 0))
	if v == nil {
		t.Fatal("newLatereVerifier() = nil, want a verifier")
	}
	if !v.allowed["hi@changkun.de"] || !v.allowed["principal-1"] || len(v.allowed) != 2 {
		t.Fatalf("allowed = %v", v.allowed)
	}
}

// TestAuthPingIsPublic keeps the health check reachable without credentials.
func TestAuthPingIsPublic(t *testing.T) {
	h := auth(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ideas/ping", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}
