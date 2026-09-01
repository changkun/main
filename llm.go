// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"latere.ai/x/pkg/llmjson"
	"latere.ai/x/pkg/luxsdk"
	"latere.ai/x/pkg/sanitize"
)

// llmClient runs the service's prompts against a model.
//
// Every request goes through the Lux gateway's own dialect, so the wire shape
// is the same whatever model answers: one typed request, one typed response,
// and no branch here on whether the model behind the gateway speaks Anthropic
// or OpenAI.
type llmClient struct {
	lux        luxsdk.Caller
	model      string // for augmentation and translation
	titleModel string // for title, slug, and polish tasks
	log        *log.Logger
}

// Reply bounds, per task rather than one number for all of them.
//
// A bound is not optional: the gateway substitutes its own 4096 when a request
// omits one, so the choice is between picking a size and inheriting that. 4096
// is right for an answer of a few words and wrong for a bilingual deep dive,
// which it would cut off mid-sentence.
const (
	// maxTokensShort covers the tasks that answer in a phrase: a title, a
	// slug, a translated title.
	maxTokensShort = 1024

	// maxTokensLong covers the tasks that return whole documents —
	// augmentation, translation, and the polish-and-translate pass, which
	// carries both languages in one reply.
	maxTokensLong = 16384
)

func (c *llmClient) augment(ctx context.Context, sourceLang, title, content string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()

	prompt, err := renderPrompt(promptIdeaInput, ideaInputData{Title: title, Content: content})
	if err != nil {
		return "", err
	}

	// Ask the provider to search and fetch, so the citations point at pages
	// that exist. A model without those tools still answers, which is why
	// this is a first attempt rather than the only one.
	sysPrompt, err := renderPrompt(promptAugment, augmentData{Lang: sourceLang, WebTools: true})
	if err != nil {
		return "", err
	}
	grounded := &luxsdk.Request{
		Model:     c.model,
		System:    []luxsdk.Block{{Type: luxsdk.BlockText, Text: sysPrompt}},
		Messages:  []luxsdk.Message{luxsdk.UserText(prompt)},
		MaxTokens: new(int64(maxTokensLong)),
		WebSearch: &luxsdk.WebSearch{ContextSize: "medium"},
		ServerTools: []luxsdk.ServerTool{{
			Type:   "web_fetch_20250910",
			Name:   "web_fetch",
			Config: json.RawMessage(`{"max_uses":5}`),
		}},
	}
	result, err := c.generate(ctx, grounded)
	if err == nil {
		return result, nil
	}
	c.log.Printf("augment with web search failed, falling back to plain: %v", err)

	plainPrompt, err := renderPrompt(promptAugment, augmentData{Lang: sourceLang})
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.model, plainPrompt, prompt, maxTokensLong)
}

func (c *llmClient) generateTitle(ctx context.Context, content string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	system, err := renderPrompt(promptTitle, nil)
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.titleModel, system, content, maxTokensShort)
}

func (c *llmClient) improveContent(ctx context.Context, content string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	system, err := renderPrompt(promptImprove, nil)
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.titleModel, system, content, maxTokensLong)
}

type translateResult struct {
	Lang              string `json:"lang"`
	PolishedTitle     string `json:"polished_title"`
	PolishedContent   string `json:"polished_content"`
	TranslatedTitle   string `json:"translated_title"`
	TranslatedContent string `json:"translated_content"`
}

func (c *llmClient) detectAndTranslate(ctx context.Context, title, content string) (*translateResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	system, err := renderPrompt(promptDetectTranslate, nil)
	if err != nil {
		return nil, err
	}
	prompt, err := renderPrompt(promptIdeaInput, ideaInputData{Title: title, Content: content})
	if err != nil {
		return nil, err
	}
	raw, err := c.complete(ctx, c.titleModel, system, prompt, maxTokensLong)
	if err != nil {
		return nil, err
	}

	raw = llmjson.Unfence(raw)

	var result translateResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		// A model asked for JSON produces the right keys and the wrong
		// encoding often enough to be worth a second attempt.
		repaired := llmjson.Repair(raw)
		if err2 := json.Unmarshal([]byte(repaired), &result); err2 != nil {
			return nil, fmt.Errorf("parse translation response: %w (raw: %s)", err, raw)
		}
	}
	if result.Lang != "en" && result.Lang != "zh" {
		return nil, fmt.Errorf("unexpected language: %q", result.Lang)
	}
	return &result, nil
}

func (c *llmClient) generateSlug(ctx context.Context, titleEn string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	system, err := renderPrompt(promptSlug, nil)
	if err != nil {
		return "", err
	}
	raw, err := c.complete(ctx, c.titleModel, system, titleEn, maxTokensShort)
	if err != nil {
		return "", err
	}

	// sanitize.Slug substitutes a fixed placeholder when the input carries
	// nothing alphanumeric. That is a sensible default for a container name
	// and a bad one for a permalink, so the unusable case is rejected here
	// instead: a post must never publish under a slug the model did not
	// produce. Checking the input rather than comparing against the
	// placeholder also keeps a model that legitimately answers "task" from
	// being mistaken for a failure.
	if !hasSlugChar(raw) {
		return "", fmt.Errorf("no usable slug in model output %q", raw)
	}
	return sanitize.Slug(raw, slugMaxLen), nil
}

// slugMaxLen bounds the permalink. sanitize.Slug stops accumulating once the
// result reaches this length, so the bound is exact.
const slugMaxLen = 60

// hasSlugChar reports whether s carries a character sanitize.Slug will keep.
// It mirrors that function's own test, applied after the same lowercasing, so
// the two cannot disagree about what counts as empty.
func hasSlugChar(s string) bool {
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func (c *llmClient) translateTitle(ctx context.Context, title, targetLang string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	system, err := renderPrompt(promptTranslateTitle, languageData{Language: languageName(targetLang)})
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.titleModel, system, title, maxTokensShort)
}

func (c *llmClient) translateContent(ctx context.Context, content, targetLang string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	system, err := renderPrompt(promptTranslateContent, languageData{Language: languageName(targetLang)})
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.titleModel, system, content, maxTokensLong)
}

// complete runs one system-plus-user exchange and returns the reply text.
// maxTokens bounds the reply; pick it from the constants above by how much the
// task actually has to say.
func (c *llmClient) complete(ctx context.Context, model, system, user string, maxTokens int64) (string, error) {
	return c.generate(ctx, &luxsdk.Request{
		Model:     model,
		System:    []luxsdk.Block{{Type: luxsdk.BlockText, Text: system}},
		Messages:  []luxsdk.Message{luxsdk.UserText(user)},
		MaxTokens: new(maxTokens),
	})
}

// generate sends req and returns the model's answer.
//
// A dialect that cannot express part of the request reports it rather than
// dropping it silently, so a lost field is logged: an augmentation that asked
// for web search and did not get it is worth knowing about even though the
// answer still arrives.
func (c *llmClient) generate(ctx context.Context, req *luxsdk.Request) (string, error) {
	res, err := c.lux.Generate(ctx, req)
	if err != nil {
		return "", fmt.Errorf("generate: %w", err)
	}
	if len(res.Loss) > 0 {
		c.log.Printf("gateway could not carry %v for model %s", res.Loss, req.Model)
	}
	return answerText(res.Blocks), nil
}

// answerText pulls the reply out of a response's blocks.
//
// When the model used a tool, the text blocks between the calls are it
// thinking aloud — "I need to search for...", "Let me verify..." — and only
// the last one carries the answer the prompt asked for. Without a tool there
// is nothing to think aloud between, so every text block is part of the reply.
func answerText(blocks []luxsdk.Block) string {
	usedTool := false
	for _, b := range blocks {
		if b.Type != luxsdk.BlockText {
			usedTool = true
			break
		}
	}

	var text strings.Builder
	for _, b := range blocks {
		if b.Type != luxsdk.BlockText {
			continue
		}
		if usedTool {
			text.Reset()
		}
		text.WriteString(b.Text)
	}
	return strings.TrimSpace(text.String())
}
