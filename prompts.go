// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// Prompt names, one per file in prompts/. Every template the service can
// render is listed here, and TestPromptNamesCoverFiles fails if the directory
// and this list ever disagree.
const (
	promptAugment          = "augment.tmpl"
	promptTitle            = "title.tmpl"
	promptImprove          = "improve.tmpl"
	promptDetectTranslate  = "detect_translate.tmpl"
	promptSlug             = "slug.tmpl"
	promptTranslateTitle   = "translate_title.tmpl"
	promptTranslateContent = "translate_content.tmpl"
	promptIdeaInput        = "idea_input.tmpl"
	promptReference        = "reference.tmpl"
)

// promptNames is the full set, for iteration in tests.
var promptNames = []string{
	promptAugment, promptTitle, promptImprove, promptDetectTranslate,
	promptSlug, promptTranslateTitle, promptTranslateContent,
	promptIdeaInput, promptReference,
}

// promptFS carries the templates into the binary, so a deployed server needs
// no files beside it and cannot drift from the prompts it was built with.
//
//go:embed prompts/*.tmpl
var promptFS embed.FS

// prompts is parsed once at process start. A template that does not parse is a
// mistake in this repository, not a condition to recover from at request time,
// so it stops the binary before it can serve.
//
// missingkey=error covers map-shaped data. Every prompt below is rendered from
// a typed struct, where a bad field name is already an execution error; the
// option is what keeps that true if a map is ever passed.
var prompts = template.Must(
	template.New("prompts").Option("missingkey=error").ParseFS(promptFS, "prompts/*.tmpl"),
)

// augmentData drives augment.tmpl. Lang is the source language of the idea
// ("en" or "zh"); WebTools reports whether the request carries web search and
// fetch tools, which changes both the instructions and the citation wording.
type augmentData struct {
	Lang     string
	WebTools bool
}

// languageData names a target language in prose, e.g. "Chinese".
type languageData struct {
	Language string
}

// ideaInputData is the user message an idea is presented in.
type ideaInputData struct {
	Title   string
	Content string
}

// referenceData is one fetched URL appended to an idea for context.
type referenceData struct {
	URL  string
	Text string
}

// renderPrompt executes the named template.
//
// Exactly one trailing newline is dropped: the one every template file ends
// with, as text files should. Trimming further would eat blank lines an author
// wrote at the end of an idea, since idea_input.tmpl and reference.tmpl close
// on user content rather than on our own prose.
func renderPrompt(name string, data any) (string, error) {
	var b strings.Builder
	if err := prompts.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("render prompt %s: %w", name, err)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// languageName maps a language code to the name used inside a prompt.
func languageName(code string) string {
	if code == "zh" {
		return "Chinese"
	}
	return "English"
}
