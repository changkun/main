// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"latere.ai/x/pkg/luxsdk"
)

// scriptedGateway answers each call with the next reply in the script, so one
// server can stand in for a whole pipeline run. A call past the end of the
// script fails the way the real gateway fails.
type scriptedGateway struct {
	srv *httptest.Server

	mu      sync.Mutex
	replies []string
	calls   int
}

func newScriptedGateway(t *testing.T, replies ...string) *scriptedGateway {
	t.Helper()

	g := &scriptedGateway{replies: replies}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)

		g.mu.Lock()
		n := g.calls
		g.calls++
		g.mu.Unlock()

		if n >= len(g.replies) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"no reply scripted"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":  "m",
			"blocks": []map[string]any{{"type": "text", "text": g.replies[n]}},
		})
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *scriptedGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func (g *scriptedGateway) client() *llmClient {
	return &llmClient{
		lux:        luxsdk.New(g.srv.URL, luxsdk.WithAPIKey("lux_test")),
		model:      "test/model",
		titleModel: "test/title-model",
		log:        log.New(io.Discard, "", 0),
	}
}

// commit is one file the pipeline wrote to GitHub.
type commit struct {
	path    string
	message string
	content string
}

// fakeGitHub accepts the contents API calls createFile makes and records them.
type fakeGitHub struct {
	srv *httptest.Server

	mu      sync.Mutex
	commits []commit
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()

	f := &fakeGitHub{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body createFileRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode commit: %v", err)
		}
		content, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			t.Errorf("decode content: %v", err)
		}

		f.mu.Lock()
		f.commits = append(f.commits, commit{
			path:    strings.TrimPrefix(r.URL.Path, "/repos/changkun/blog/contents/"),
			message: body.Message,
			content: string(content),
		})
		f.mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) got() []commit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]commit(nil), f.commits...)
}

func newPipeline(t *testing.T, g *scriptedGateway) (*service, *fakeGitHub) {
	t.Helper()

	gh := newFakeGitHub(t)
	return &service{
		log: log.New(io.Discard, "", 0),
		llm: g.client(),
		github: &githubClient{
			token:   "t",
			owner:   "changkun",
			repo:    "blog",
			name:    "test",
			email:   "test@changkun.de",
			baseURL: gh.srv.URL,
		},
	}, gh
}

const zhIdea = "最近整理自己的经历，推断出下面几条适用于目前阶段的公理。"

// TestProcessIdeaWithholdsOnGatewayOutage reproduces the 2026-09-01 post. The
// Lux gateway's database ran out of connection slots, every call in the
// pipeline failed, every fallback substituted the source text, and the service
// committed a post titled "Untitled" whose English block held Chinese.
// Publishing is permanent, dated and public, so an outage must withhold.
func TestProcessIdeaWithholdsOnGatewayOutage(t *testing.T) {
	g := newScriptedGateway(t) // no replies: every call answers 500
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{Content: zhIdea})

	if got := gh.got(); len(got) != 0 {
		t.Fatalf("committed %d file(s) during an outage, want none: %+v", len(got), got)
	}
}

// TestProcessIdeaWithholdsOnEchoedTranslation covers the same defect one
// branch over: the gateway answers, and answers with the source text in both
// languages. That payload passes isUsableTranslateResult, so only a check
// after the branches meet catches it.
func TestProcessIdeaWithholdsOnEchoedTranslation(t *testing.T) {
	echoed, err := json.Marshal(translateResult{
		Lang:              "zh",
		PolishedTitle:     "公理",
		PolishedContent:   zhIdea,
		TranslatedTitle:   "公理",
		TranslatedContent: zhIdea,
	})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}

	g := newScriptedGateway(t, string(echoed))
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{Title: "公理", Content: zhIdea})

	if got := gh.got(); len(got) != 0 {
		t.Fatalf("committed %d untranslated file(s), want none: %+v", len(got), got)
	}
}

// TestProcessIdeaWithholdsWithoutTitle keeps the "Untitled" placeholder out of
// the blog: a post the model could not name is not one worth dating.
func TestProcessIdeaWithholdsWithoutTitle(t *testing.T) {
	g := newScriptedGateway(t) // the title call is the first, and it fails
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{Content: zhIdea})

	if got := g.count(); got != 1 {
		t.Errorf("made %d gateway calls, want 1: the run must stop at the title", got)
	}
	if got := gh.got(); len(got) != 0 {
		t.Fatalf("committed %d file(s) without a title, want none: %+v", len(got), got)
	}
}

// TestProcessIdeaPublishesBilingual walks the whole pipeline with a gateway
// that answers every step, and checks what reaches GitHub.
func TestProcessIdeaPublishesBilingual(t *testing.T) {
	translated, err := json.Marshal(translateResult{
		Lang:              "zh",
		PolishedTitle:     "三条公理",
		PolishedContent:   zhIdea,
		TranslatedTitle:   "Three Axioms",
		TranslatedContent: "Three axioms drawn from my own experience.",
	})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}

	g := newScriptedGateway(t,
		string(translated),                  // detect, polish and translate
		"three-axioms",                      // slug
		"An English rendering of the note.", // the augmented text, translated
	)
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{
		Title:     "三条公理",
		Content:   zhIdea,
		Augmented: "这是补充说明。",
	})

	got := gh.got()
	if len(got) != 1 {
		t.Fatalf("committed %d file(s), want 1: %+v", len(got), got)
	}
	if !strings.HasSuffix(got[0].path, "-three-axioms.md") {
		t.Errorf("path = %q, want the generated slug", got[0].path)
	}
	if got[0].message != "ideas: Three Axioms" {
		t.Errorf("message = %q, want the English title", got[0].message)
	}
	for _, want := range []string{"三条公理", "Three Axioms", "Three axioms drawn", "这是补充说明。"} {
		if !strings.Contains(got[0].content, want) {
			t.Errorf("the published markdown is missing %q:\n%s", want, got[0].content)
		}
	}
}
