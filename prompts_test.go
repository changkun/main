// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// sampleData is representative input for every prompt, so the whole set can be
// rendered in one pass.
var sampleData = map[string]any{
	promptAugment:          augmentData{Lang: "en"},
	promptTitle:            nil,
	promptImprove:          nil,
	promptDetectTranslate:  nil,
	promptSlug:             nil,
	promptTranslateTitle:   languageData{Language: "Chinese"},
	promptTranslateContent: languageData{Language: "Chinese"},
	promptIdeaInput:        ideaInputData{Title: "T", Content: "C"},
	promptReference:        referenceData{URL: "https://example.com", Text: "body"},
}

// TestPromptNamesCoverFiles keeps prompts/ and promptNames in step. A template
// added to the directory but never named here is dead weight nobody renders,
// and a name without a file panics the binary at startup.
func TestPromptNamesCoverFiles(t *testing.T) {
	entries, err := fs.Glob(promptFS, "prompts/*.tmpl")
	if err != nil {
		t.Fatalf("glob embedded prompts: %v", err)
	}

	var files []string
	for _, e := range entries {
		files = append(files, strings.TrimPrefix(e, "prompts/"))
	}
	slices.Sort(files)

	names := slices.Clone(promptNames)
	slices.Sort(names)

	if !slices.Equal(files, names) {
		t.Fatalf("prompts/ holds %v, promptNames lists %v", files, names)
	}
	for _, n := range names {
		if _, ok := sampleData[n]; !ok {
			t.Errorf("%s has no entry in sampleData, so nothing renders it", n)
		}
	}
}

// TestRenderPromptAll renders every prompt. A field renamed in Go but not in
// the template fails only on execution, so this is what catches it before a
// live post does.
func TestRenderPromptAll(t *testing.T) {
	for _, name := range promptNames {
		t.Run(name, func(t *testing.T) {
			got, err := renderPrompt(name, sampleData[name])
			if err != nil {
				t.Fatalf("renderPrompt() error: %v", err)
			}
			if strings.TrimSpace(got) == "" {
				t.Fatal("renderPrompt() = empty")
			}
			if strings.Contains(got, "{{") || strings.Contains(got, "}}") {
				t.Fatalf("renderPrompt() left template syntax unrendered:\n%s", got)
			}
			if strings.HasSuffix(got, "\n") {
				t.Fatal("renderPrompt() kept the file's trailing newline")
			}
		})
	}
}

// TestAugmentPrompt covers the one template with branches. Both switches are
// independent, so all four combinations are checked.
func TestAugmentPrompt(t *testing.T) {
	const (
		zhDirective = "You MUST write your ENTIRE response in Chinese"
		enDirective = "Write in the same language as the original content."
		webBlock    = "You have access to web search and web fetch tools"
		verifyWords = "search for and verify link to the canonical source"
		plainWords  = "For other references, link to the canonical source"
	)

	tests := []struct {
		name    string
		data    augmentData
		want    []string
		notWant []string
	}{
		{
			name:    "english without web tools",
			data:    augmentData{Lang: "en"},
			want:    []string{enDirective, plainWords},
			notWant: []string{zhDirective, webBlock, verifyWords},
		},
		{
			name:    "english with web tools",
			data:    augmentData{Lang: "en", WebTools: true},
			want:    []string{enDirective, webBlock, verifyWords},
			notWant: []string{zhDirective, plainWords},
		},
		{
			name:    "chinese without web tools",
			data:    augmentData{Lang: "zh"},
			want:    []string{zhDirective, plainWords},
			notWant: []string{enDirective, webBlock, verifyWords},
		},
		{
			name:    "chinese with web tools",
			data:    augmentData{Lang: "zh", WebTools: true},
			want:    []string{zhDirective, webBlock, verifyWords},
			notWant: []string{enDirective, plainWords},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderPrompt(promptAugment, tt.data)
			if err != nil {
				t.Fatalf("renderPrompt() error: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpectedly present: %q", w)
				}
			}
		})
	}
}

// TestRenderPromptFailsLoudly checks that a broken render is an error rather
// than a prompt with "<no value>" quietly baked into it, which the model would
// answer as if it were an instruction.
func TestRenderPromptFailsLoudly(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		data any
	}{
		{name: "unknown template", tmpl: "no-such.tmpl", data: nil},
		{name: "missing data", tmpl: promptTranslateTitle, data: nil},
		{name: "wrong data shape", tmpl: promptIdeaInput, data: languageData{Language: "Chinese"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderPrompt(tt.tmpl, tt.data)
			if err == nil {
				t.Fatalf("renderPrompt() = %q, want an error", got)
			}
			if got != "" {
				t.Fatalf("renderPrompt() = %q on error, want the empty string", got)
			}
		})
	}
}

// TestLanguageName pins the mapping the translation prompts read. Anything
// other than "zh" is English, matching the two languages the blog publishes.
func TestLanguageName(t *testing.T) {
	for code, want := range map[string]string{"zh": "Chinese", "en": "English", "": "English", "de": "English"} {
		if got := languageName(code); got != want {
			t.Errorf("languageName(%q) = %q, want %q", code, got, want)
		}
	}
}

// TestIdeaInputPrompt pins the shape every idea reaches the model in: the
// title and body must stay on separate labelled lines, not run together.
func TestIdeaInputPrompt(t *testing.T) {
	got, err := renderPrompt(promptIdeaInput, ideaInputData{Title: "Some Title", Content: "line one\nline two"})
	if err != nil {
		t.Fatalf("renderPrompt() error: %v", err)
	}
	want := "Title: Some Title\n\nContent:\nline one\nline two"
	if got != want {
		t.Fatalf("renderPrompt() = %q, want %q", got, want)
	}
}

// TestRenderPromptKeepsContentWhitespace covers the two templates that close
// on user content. Only the template file's own trailing newline may go: an
// author's blank lines are part of what they wrote, and stripping them would
// silently reformat every idea that ends with one.
func TestRenderPromptKeepsContentWhitespace(t *testing.T) {
	got, err := renderPrompt(promptIdeaInput, ideaInputData{Title: "T", Content: "body\n\n"})
	if err != nil {
		t.Fatalf("renderPrompt() error: %v", err)
	}
	if want := "Title: T\n\nContent:\nbody\n\n"; got != want {
		t.Fatalf("renderPrompt() = %q, want %q", got, want)
	}

	got, err = renderPrompt(promptReference, referenceData{URL: "https://example.com", Text: "page\n\n"})
	if err != nil {
		t.Fatalf("renderPrompt() error: %v", err)
	}
	if !strings.HasSuffix(got, "page\n\n") {
		t.Fatalf("renderPrompt() = %q, want it to end with the fetched text unchanged", got)
	}
}
