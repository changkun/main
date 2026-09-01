// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"latere.ai/x/pkg/luxsdk"
)

// ideasPrefix is the subtree the ideas API owns. The routes below repeat it
// because the handler is mounted without stripping the prefix, which keeps the
// paths in this file the same as the ones a caller types.
const ideasPrefix = "/ideas/"

// newIdeasService builds the ideas API from the environment, and returns nil
// when it cannot.
//
// Serving changkun.de is the primary job of this binary and needs no
// configuration at all. The ideas API needs a model gateway and a GitHub
// token. Missing credentials therefore leave the API unbuilt rather than stop
// the process: a host that carries no secrets still serves the site.
func newIdeasService(l *log.Logger) *service {
	llmBaseURL := os.Getenv("LLM_BASE_URL")
	llmAPIKey := os.Getenv("LLM_API_KEY")
	gitToken := os.Getenv("GIT_TOKEN")
	if llmBaseURL == "" || llmAPIKey == "" || gitToken == "" {
		l.Println("ideas API is unconfigured, LLM_BASE_URL, LLM_API_KEY and GIT_TOKEN are required")
		return nil
	}

	gitRepo := cmp.Or(os.Getenv("GIT_REPO"), "changkun/blog")
	owner, repo, ok := strings.Cut(gitRepo, "/")
	if !ok || owner == "" || repo == "" {
		l.Printf("ideas API is unconfigured, GIT_REPO must be owner/repo, got %q", gitRepo)
		return nil
	}

	return &service{
		log: l,
		llm: &llmClient{
			lux:        luxsdk.New(llmBaseURL, luxsdk.WithAPIKey(llmAPIKey)),
			model:      cmp.Or(os.Getenv("LLM_MODEL"), "anthropic/claude-sonnet-4-5-20250929"),
			titleModel: cmp.Or(os.Getenv("LLM_TITLE_MODEL"), "anthropic/claude-haiku-4-5-20251001"),
			log:        l,
		},
		github: &githubClient{
			token: gitToken,
			owner: owner,
			repo:  repo,
			name:  cmp.Or(os.Getenv("GIT_COMMITTER_NAME"), "Changkun Ideas API Server"),
			email: cmp.Or(os.Getenv("GIT_COMMITTER_EMAIL"), "hi+ideas@changkun.de"),
		},
	}
}

// ideasHandler serves the ideas API under ideasPrefix.
//
// A nil service answers 503 on the whole subtree instead of leaving the paths
// to the file server, so a deployment that lost its credentials says so at the
// endpoint rather than returning the site's 404 page. Both forms keep the CORS
// wrapper, because the caller is the compose box on changkun.de and a browser
// reads a blocked response as a network error, not as the status it carries.
func ideasHandler(svc *service, l *log.Logger) http.Handler {
	r := http.NewServeMux()
	if svc == nil {
		r.HandleFunc(ideasPrefix, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "ideas API is not configured", http.StatusServiceUnavailable)
		})
		return cors(r)
	}

	r.HandleFunc("GET /ideas/ping", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "pong")
	})
	r.HandleFunc("POST /ideas/post", svc.handlePost)
	r.HandleFunc("POST /ideas/improve", svc.handleImprove)
	return cors(auth(newLatereVerifier(l), r))
}

// cors admits the compose box on changkun.de. The ideas API is reached from a
// page on the site itself, never from a third-party origin.
func cors(next http.Handler) http.Handler {
	allowed := map[string]bool{
		"https://changkun.de":     true,
		"https://www.changkun.de": true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
