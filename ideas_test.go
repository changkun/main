// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ideasEnv is the minimum configuration newIdeasService accepts.
func ideasEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LLM_BASE_URL", "https://lux.example")
	t.Setenv("LLM_API_KEY", "key")
	t.Setenv("GIT_TOKEN", "token")
}

func discard() *log.Logger { return log.New(io.Discard, "", 0) }

// TestNewIdeasServiceNeedsCredentials keeps the site independent of the API.
// The binary serves changkun.de first and answers /ideas/* second, so a host
// without credentials must start rather than exit.
func TestNewIdeasServiceNeedsCredentials(t *testing.T) {
	tests := []struct {
		name  string
		unset string
	}{
		{name: "no api key", unset: "LLM_API_KEY"},
		{name: "no git token", unset: "GIT_TOKEN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ideasEnv(t)
			t.Setenv(tt.unset, "")
			if svc := newIdeasService(discard()); svc != nil {
				t.Fatalf("newIdeasService() = %v, want nil without %s", svc, tt.unset)
			}
		})
	}
}

// TestNewIdeasServiceDefaultsGateway keeps LLM_BASE_URL optional: luxsdk
// names the platform's live Lux deployment, so a key alone mounts the API.
func TestNewIdeasServiceDefaultsGateway(t *testing.T) {
	ideasEnv(t)
	t.Setenv("LLM_BASE_URL", "")
	if svc := newIdeasService(discard()); svc == nil {
		t.Fatal("newIdeasService() = nil, want a service on the default gateway")
	}
}

func TestNewIdeasServiceReadsRepo(t *testing.T) {
	tests := []struct {
		name        string
		repo        string
		wantOwner   string
		wantRepo    string
		wantService bool
	}{
		{name: "default", repo: "", wantOwner: "changkun", wantRepo: "blog", wantService: true},
		{name: "override", repo: "acme/notes", wantOwner: "acme", wantRepo: "notes", wantService: true},
		{name: "no slash", repo: "notes"},
		{name: "no owner", repo: "/notes"},
		{name: "no name", repo: "acme/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ideasEnv(t)
			t.Setenv("GIT_REPO", tt.repo)

			svc := newIdeasService(discard())
			if (svc != nil) != tt.wantService {
				t.Fatalf("newIdeasService() = %v, want service: %v", svc, tt.wantService)
			}
			if svc == nil {
				return
			}
			if svc.github.owner != tt.wantOwner || svc.github.repo != tt.wantRepo {
				t.Errorf("repo = %s/%s, want %s/%s",
					svc.github.owner, svc.github.repo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

// TestIdeasServiceModelsAreNativeNames pins the bare default names. Lux
// resolves a name exactly and never strips a prefix, so a default must be a
// name the platform serves as written. Provider-prefixed defaults once
// shipped for four weeks and failed on the first post that reached a healthy
// gateway; a deployment that needs another name sets it in the environment.
func TestIdeasServiceModelsAreNativeNames(t *testing.T) {
	ideasEnv(t)
	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_TITLE_MODEL", "")

	svc := newIdeasService(discard())
	if svc == nil {
		t.Fatal("newIdeasService() = nil, want a service")
	}
	for _, m := range []struct{ name, model string }{
		{"LLM_MODEL", svc.llm.model},
		{"LLM_TITLE_MODEL", svc.llm.titleModel},
	} {
		if m.model == "" {
			t.Errorf("the %s default is empty", m.name)
		}
		if strings.Contains(m.model, "/") {
			t.Errorf("the %s default is %q, want a bare model name", m.name, m.model)
		}
	}
}

// TestIdeasHandlerUnconfigured pins what an unconfigured deployment answers.
// Leaving the subtree unmounted would hand /ideas/post to the file server and
// return the site's 404 page, which reads as a wrong URL rather than a
// missing credential.
func TestIdeasHandlerUnconfigured(t *testing.T) {
	h := ideasHandler(nil, discard())

	for _, path := range []string{"/ideas/ping", "/ideas/post", "/ideas/improve"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("POST %s = %d, want %d", path, w.Code, http.StatusServiceUnavailable)
		}
	}
}

// TestIdeasHandlerAllowsComposeBox covers the browser path: the compose box is
// served from changkun.de and calls the API cross-origin, so a missing CORS
// header reaches the page as a network error and hides the real status.
func TestIdeasHandlerAllowsComposeBox(t *testing.T) {
	tests := []struct {
		name string
		svc  *service
	}{
		{name: "configured", svc: &service{log: discard()}},
		{name: "unconfigured", svc: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := ideasHandler(tt.svc, discard())

			r := httptest.NewRequest(http.MethodOptions, "/ideas/post", nil)
			r.Header.Set("Origin", "https://changkun.de")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://changkun.de" {
				t.Errorf("Access-Control-Allow-Origin = %q, want the site origin", got)
			}
			if w.Code != http.StatusNoContent {
				t.Errorf("preflight status = %d, want %d", w.Code, http.StatusNoContent)
			}
		})
	}
}

// TestIdeasHandlerRejectsUnauthenticated checks the configured mount still
// closes: only /ideas/ping is public.
func TestIdeasHandlerRejectsUnauthenticated(t *testing.T) {
	h := ideasHandler(&service{log: discard()}, discard())

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ideas/post", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /ideas/post = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ideas/ping", nil))
	if w.Code != http.StatusOK {
		t.Errorf("GET /ideas/ping = %d, want %d", w.Code, http.StatusOK)
	}
}
