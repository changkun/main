// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"latere.ai/x/pkg/luxsdk"
	"latere.ai/x/pkg/sanitize"
)

// gateway stands in for the Lux gateway: it records the request it received
// and answers with the blocks the test names.
type gateway struct {
	srv  *httptest.Server
	got  map[string]any
	fail bool
}

func newGateway(t *testing.T, blocks []map[string]any) *gateway {
	t.Helper()

	g := &gateway{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.got = map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&g.got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if g.fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream refused"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "m", "blocks": blocks})
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gateway) client() *llmClient {
	return &llmClient{
		lux:        luxsdk.New(g.srv.URL, luxsdk.WithAPIKey("lux_test")),
		model:      "anthropic/claude-sonnet-4-5-20250929",
		titleModel: "anthropic/claude-haiku-4-5-20251001",
		log:        log.New(io.Discard, "", 0),
	}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// TestCompleteSendsOneExchange pins the request shape every prompt-driven task
// shares: the prompt as system, the caller's text as the one user turn, and a
// bounded reply.
func TestCompleteSendsOneExchange(t *testing.T) {
	g := newGateway(t, []map[string]any{text(" ok ")})

	got, err := g.client().complete(context.Background(), "some-model", "system prompt", "user text", maxTokensShort)
	if err != nil {
		t.Fatalf("complete() error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("complete() = %q, want ok with surrounding space trimmed", got)
	}
	if g.got["model"] != "some-model" {
		t.Fatalf("model = %v", g.got["model"])
	}
	if g.got["max_tokens"] != float64(maxTokensShort) {
		t.Fatalf("max_tokens = %v, want %d", g.got["max_tokens"], maxTokensShort)
	}

	system, _ := g.got["system"].([]any)
	if len(system) != 1 {
		t.Fatalf("system = %#v, want one block", g.got["system"])
	}
	if b, _ := system[0].(map[string]any); b["text"] != "system prompt" {
		t.Fatalf("system block = %#v", system[0])
	}
	msgs, _ := g.got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want one turn", g.got["messages"])
	}
}

// TestAugmentAsksForGrounding covers the reason server tools exist here: the
// augmentation must ask the provider to search and fetch, or every citation is
// something the model recalled rather than read.
func TestAugmentAsksForGrounding(t *testing.T) {
	g := newGateway(t, []map[string]any{text("augmented")})

	got, err := g.client().augment(context.Background(), "en", "A Title", "some idea")
	if err != nil {
		t.Fatalf("augment() error: %v", err)
	}
	if got != "augmented" {
		t.Fatalf("augment() = %q", got)
	}

	tools, _ := g.got["server_tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("server_tools = %#v, want the fetch tool", g.got["server_tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "web_fetch_20250910" || tool["name"] != "web_fetch" {
		t.Fatalf("server_tools[0] = %#v", tool)
	}
	search, _ := g.got["web_search"].(map[string]any)
	if search["context_size"] != "medium" {
		t.Fatalf("web_search = %#v, want context_size medium", g.got["web_search"])
	}

	system, _ := g.got["system"].([]any)
	b, _ := system[0].(map[string]any)
	if !strings.Contains(b["text"].(string), "web search and web fetch tools") {
		t.Fatalf("system prompt does not mention the tools it was given:\n%v", b["text"])
	}
}

// TestAugmentFallsBackUngrounded keeps a provider without those tools from
// costing the post entirely. The answer is worse and it still arrives.
func TestAugmentFallsBackUngrounded(t *testing.T) {
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body)
		if _, grounded := body["server_tools"]; grounded {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"tools unsupported"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":  "m",
			"blocks": []map[string]any{text("plain augmentation")},
		})
	}))
	defer srv.Close()

	c := &llmClient{
		lux:   luxsdk.New(srv.URL, luxsdk.WithAPIKey("lux_test")),
		model: "m",
		log:   log.New(io.Discard, "", 0),
	}
	got, err := c.augment(context.Background(), "en", "T", "C")
	if err != nil {
		t.Fatalf("augment() error: %v", err)
	}
	if got != "plain augmentation" {
		t.Fatalf("augment() = %q", got)
	}
	if len(seen) != 2 {
		t.Fatalf("gateway saw %d requests, want a grounded attempt then a plain one", len(seen))
	}
	if _, grounded := seen[1]["server_tools"]; grounded {
		t.Fatal("the fallback request still asked for tools")
	}
}

// TestAnswerText covers the one piece of response handling with a judgement in
// it. Text between tool calls is the model thinking aloud, so only the last
// block is the answer; with no tool there is nothing to think aloud between.
func TestAnswerText(t *testing.T) {
	tests := []struct {
		name   string
		blocks []luxsdk.Block
		want   string
	}{
		{
			name:   "single text block",
			blocks: []luxsdk.Block{{Type: luxsdk.BlockText, Text: " hello "}},
			want:   "hello",
		},
		{
			name: "no tool use joins every block",
			blocks: []luxsdk.Block{
				{Type: luxsdk.BlockText, Text: "part one "},
				{Type: luxsdk.BlockText, Text: "part two"},
			},
			want: "part one part two",
		},
		{
			name: "after a tool call only the last block is the answer",
			blocks: []luxsdk.Block{
				{Type: luxsdk.BlockText, Text: "I need to search for this."},
				{Type: luxsdk.BlockToolUse, ToolUse: &luxsdk.ToolUse{ID: "t1", Name: "web_fetch"}},
				{Type: luxsdk.BlockText, Text: "Let me verify."},
				{Type: luxsdk.BlockToolUse, ToolUse: &luxsdk.ToolUse{ID: "t2", Name: "web_fetch"}},
				{Type: luxsdk.BlockText, Text: "The final answer."},
			},
			want: "The final answer.",
		},
		{
			name: "thinking is not the answer",
			blocks: []luxsdk.Block{
				{Type: luxsdk.BlockThinking, Text: "hmm"},
				{Type: luxsdk.BlockText, Text: "the answer"},
			},
			want: "the answer",
		},
		{
			name:   "no blocks",
			blocks: nil,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := answerText(tt.blocks); got != tt.want {
				t.Fatalf("answerText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDetectAndTranslateRepairsReply covers the decode path end to end: the
// model answers with the right keys inside a fence, with raw newlines in the
// multi-line values, and the result still has to come out whole.
func TestDetectAndTranslateRepairsReply(t *testing.T) {
	reply := "```json\n{\n  \"lang\": \"en\",\n  \"polished_title\": \"A Title\",\n" +
		"  \"polished_content\": \"Line one.\n\nLine two.\",\n" +
		"  \"translated_title\": \"\u6807\u9898\",\n" +
		"  \"translated_content\": \"\u7b2c\u4e00\u884c\u3002\n\n\u7b2c\u4e8c\u884c\u3002\"\n}\n```"
	g := newGateway(t, []map[string]any{text(reply)})

	got, err := g.client().detectAndTranslate(context.Background(), "T", "C")
	if err != nil {
		t.Fatalf("detectAndTranslate() error: %v", err)
	}
	want := translateResult{
		Lang:              "en",
		PolishedTitle:     "A Title",
		PolishedContent:   "Line one.\n\nLine two.",
		TranslatedTitle:   "\u6807\u9898",
		TranslatedContent: "\u7b2c\u4e00\u884c\u3002\n\n\u7b2c\u4e8c\u884c\u3002",
	}
	if *got != want {
		t.Fatalf("detectAndTranslate()\n got: %+v\nwant: %+v", *got, want)
	}
}

// TestDetectAndTranslateRejectsUnknownLanguage keeps a reply the blog cannot
// publish from reaching the markdown builder.
func TestDetectAndTranslateRejectsUnknownLanguage(t *testing.T) {
	g := newGateway(t, []map[string]any{text(`{"lang":"fr","polished_title":"t","polished_content":"c","translated_title":"t","translated_content":"c"}`)})

	if _, err := g.client().detectAndTranslate(context.Background(), "T", "C"); err == nil {
		t.Fatal("detectAndTranslate() = nil error, want one for an unsupported language")
	}
}

// TestGenerateSurfacesGatewayErrors keeps a failed call from looking like an
// empty answer, which would publish a post with a blank section.
func TestGenerateSurfacesGatewayErrors(t *testing.T) {
	g := newGateway(t, nil)
	g.fail = true

	if _, err := g.client().complete(context.Background(), "m", "s", "u", maxTokensShort); err == nil {
		t.Fatal("complete() = nil error, want the upstream failure surfaced")
	}
}

// TestHasSlugChar covers the gate in front of sanitize.Slug: the placeholder
// it would otherwise return is a container-name default, not a permalink.
func TestHasSlugChar(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{in: "expertise-risk-control", want: true},
		{in: "PBO Methods", want: true},
		{in: "42", want: true},
		{in: "", want: false},
		{in: "   ", want: false},
		{in: "!!! ??? ---", want: false},
		{in: "中文标题", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := hasSlugChar(tt.in); got != tt.want {
				t.Fatalf("hasSlugChar(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestSlugFromModelOutput pins the permalink shape. The previous cleanup
// deleted every character outside [a-z0-9-], so a spaced answer collapsed into
// one unreadable word; sanitize.Slug turns each run into a single dash.
func TestSlugFromModelOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "already a slug", raw: "expertise-risk-control", want: "expertise-risk-control"},
		{name: "spaces become dashes", raw: "expertise risk control", want: "expertise-risk-control"},
		{name: "surrounding whitespace", raw: "  reward-hacking\n", want: "reward-hacking"},
		{name: "uppercase is lowered", raw: "PBO Methods", want: "pbo-methods"},
		{name: "punctuation runs collapse", raw: "llms: bottlenecks!!", want: "llms-bottlenecks"},
		{name: "quoted answer", raw: `"language-vs-visual-ai"`, want: "language-vs-visual-ai"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !hasSlugChar(tt.raw) {
				t.Fatalf("hasSlugChar(%q) = false, want true", tt.raw)
			}
			if got := sanitize.Slug(tt.raw, slugMaxLen); got != tt.want {
				t.Fatalf("Slug(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestSlugIsBounded keeps a permalink from inheriting a runaway answer.
func TestSlugIsBounded(t *testing.T) {
	got := sanitize.Slug(strings.Repeat("word ", 200), slugMaxLen)
	if len(got) > slugMaxLen {
		t.Fatalf("Slug() = %q, %d bytes, want at most %d", got, len(got), slugMaxLen)
	}
	if strings.HasSuffix(got, "-") {
		t.Fatalf("Slug() = %q, want no trailing dash", got)
	}
}

// TestPromptTasks covers the thin tasks in one pass: each renders its prompt,
// sends it as system, and returns the reply.
//
// Two things are worth pinning per task. Which model it picks, since the cheap
// one handles the short jobs and the expensive one the long ones. And which
// token bound it sends, because the wrong one here does not fail — it returns
// a reply cut off mid-sentence, and the post publishes that way.
func TestPromptTasks(t *testing.T) {
	const (
		bigModel   = "anthropic/claude-sonnet-4-5-20250929"
		smallModel = "anthropic/claude-haiku-4-5-20251001"
	)

	tests := []struct {
		name      string
		reply     string
		call      func(*llmClient) (string, error)
		wantModel string
		wantMax   int
		want      string
		wantIn    string // a phrase the system prompt must carry
	}{
		{
			name:      "generateTitle",
			reply:     " A Short Title ",
			call:      func(c *llmClient) (string, error) { return c.generateTitle(context.Background(), "some idea") },
			wantModel: smallModel,
			wantMax:   maxTokensShort,
			want:      "A Short Title",
			wantIn:    "concise noun phrase",
		},
		{
			name:      "improveContent rewrites a whole body",
			reply:     "improved text",
			call:      func(c *llmClient) (string, error) { return c.improveContent(context.Background(), "improvd text") },
			wantModel: smallModel,
			wantMax:   maxTokensLong,
			want:      "improved text",
			wantIn:    "Fix typos",
		},
		{
			name:  "generateSlug",
			reply: "Expertise Risk Control",
			call: func(c *llmClient) (string, error) {
				return c.generateSlug(context.Background(), "Expertise as Risk Control")
			},
			wantModel: smallModel,
			wantMax:   maxTokensShort,
			want:      "expertise-risk-control",
			wantIn:    "URL slug",
		},
		{
			name:      "translateTitle",
			reply:     "标题",
			call:      func(c *llmClient) (string, error) { return c.translateTitle(context.Background(), "Title", "zh") },
			wantModel: smallModel,
			wantMax:   maxTokensShort,
			want:      "标题",
			wantIn:    "Translate the following title to Chinese",
		},
		{
			name:      "translateContent carries a whole document",
			reply:     "内容",
			call:      func(c *llmClient) (string, error) { return c.translateContent(context.Background(), "Content", "zh") },
			wantModel: smallModel,
			wantMax:   maxTokensLong,
			want:      "内容",
			wantIn:    "Translate the following text to Chinese",
		},
		{
			name:      "augment uses the capable model",
			reply:     "deep dive",
			call:      func(c *llmClient) (string, error) { return c.augment(context.Background(), "en", "T", "C") },
			wantModel: bigModel,
			wantMax:   maxTokensLong,
			want:      "deep dive",
			wantIn:    "intellectual companion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGateway(t, []map[string]any{text(tt.reply)})

			got, err := tt.call(g.client())
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("= %q, want %q", got, tt.want)
			}
			if g.got["model"] != tt.wantModel {
				t.Fatalf("model = %v, want %s", g.got["model"], tt.wantModel)
			}
			if g.got["max_tokens"] != float64(tt.wantMax) {
				t.Fatalf("max_tokens = %v, want %d", g.got["max_tokens"], tt.wantMax)
			}
			system, _ := g.got["system"].([]any)
			b, _ := system[0].(map[string]any)
			if !strings.Contains(b["text"].(string), tt.wantIn) {
				t.Fatalf("system prompt missing %q:\n%v", tt.wantIn, b["text"])
			}
		})
	}
}

// TestGenerateSlugRejectsUnusableAnswer keeps a post from publishing under a
// placeholder permalink when the model answers with nothing usable.
func TestGenerateSlugRejectsUnusableAnswer(t *testing.T) {
	g := newGateway(t, []map[string]any{text("!!! ???")})

	if _, err := g.client().generateSlug(context.Background(), "A Title"); err == nil {
		t.Fatal("generateSlug() = nil error, want one")
	}
}
