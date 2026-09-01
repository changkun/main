// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type service struct {
	log    *log.Logger
	llm    *llmClient
	github *githubClient
}

type ideaRequest struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	Augmented string `json:"augmented"`
}

type ideaResponse struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message,omitempty"`
	Content  string `json:"content,omitempty"`
	Filename string `json:"filename,omitempty"`
}

func (s *service) handlePost(w http.ResponseWriter, r *http.Request) {
	var req ideaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		s.jsonError(w, "content is required", http.StatusBadRequest)
		return
	}

	// Accept immediately, process in background.
	go s.processIdea(req)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ideaResponse{
		OK:      true,
		Message: "idea accepted, publishing in background",
	})
}

func (s *service) handleImprove(w http.ResponseWriter, r *http.Request) {
	var req ideaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		s.jsonError(w, "content is required", http.StatusBadRequest)
		return
	}

	improved, err := s.llm.improveContent(r.Context(), req.Content)
	if err != nil {
		s.log.Printf("content improvement failed: %v", err)
		s.jsonError(w, "content improvement failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ideaResponse{OK: true, Content: improved})
}

func (s *service) processIdea(req ideaRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Fetch linked content if the idea contains URLs.
	enriched := req.Content
	if urls := extractURLs(req.Content); len(urls) > 0 {
		var refs strings.Builder
		for _, u := range urls {
			s.log.Printf("fetching linked content: %s", u)
			text, err := fetchURL(ctx, u)
			if err != nil {
				s.log.Printf("failed to fetch %s: %v", u, err)
				continue
			}
			ref, err := renderPrompt(promptReference, referenceData{URL: u, Text: text})
			if err != nil {
				s.log.Printf("failed to render reference for %s: %v", u, err)
				continue
			}
			refs.WriteString("\n\n")
			refs.WriteString(ref)
		}
		if refs.Len() > 0 {
			enriched += refs.String()
		}
	}

	// Generate title in original language if not provided.
	if req.Title == "" {
		s.log.Printf("generating title for idea...")
		title, err := s.llm.generateTitle(ctx, enriched)
		if err != nil {
			s.log.Printf("title generation failed: %v", err)
			req.Title = "Untitled"
		} else {
			req.Title = title
		}
		s.log.Printf("generated title: %s", req.Title)
	}

	// Detect language, polish, and translate title+content in one LLM call.
	s.log.Printf("detecting language, polishing, and translating...")
	tr, err := s.llm.detectAndTranslate(ctx, req.Title, req.Content)
	var lang string
	var titleEn, titleZh, contentEn, contentZh string
	fallbackTranslate := func(cause error) {
		s.log.Printf("detect+translate unavailable, falling back to separate translation: %v", cause)
		lang = detectLang(req.Content)
		if lang == "en" {
			titleEn = req.Title
			contentEn = req.Content
			titleZh, err = s.llm.translateTitle(ctx, req.Title, "zh")
			if err != nil {
				s.log.Printf("title translation fallback failed: %v", err)
				titleZh = req.Title
			}
			contentZh, err = s.llm.translateContent(ctx, req.Content, "zh")
			if err != nil {
				s.log.Printf("content translation fallback failed: %v", err)
				contentZh = req.Content
			}
		} else {
			titleZh = req.Title
			contentZh = req.Content
			titleEn, err = s.llm.translateTitle(ctx, req.Title, "en")
			if err != nil {
				s.log.Printf("title translation fallback failed: %v", err)
				titleEn = req.Title
			}
			contentEn, err = s.llm.translateContent(ctx, req.Content, "en")
			if err != nil {
				s.log.Printf("content translation fallback failed: %v", err)
				contentEn = req.Content
			}
		}
	}
	if err != nil {
		fallbackTranslate(err)
	} else {
		sourceLang := detectLang(req.Content)
		tr = normalizeTranslateResult(tr, sourceLang)
		if !isUsableTranslateResult(tr) {
			fallbackTranslate(fmt.Errorf("invalid detect+translate payload"))
		} else {
			lang = tr.Lang
			if lang == "en" {
				titleEn = tr.PolishedTitle
				titleZh = tr.TranslatedTitle
				contentEn = tr.PolishedContent
				contentZh = tr.TranslatedContent
			} else {
				titleZh = tr.PolishedTitle
				titleEn = tr.TranslatedTitle
				contentZh = tr.PolishedContent
				contentEn = tr.TranslatedContent
			}
		}
	}
	s.log.Printf("detected language: %s", lang)

	// Generate short slug via LLM, fall back to mechanical slugify.
	now := time.Now()
	s.log.Printf("generating short slug...")
	slug, err := s.llm.generateSlug(ctx, titleEn)
	if err != nil {
		s.log.Printf("LLM slug generation failed, using fallback: %v", err)
		slug = slugify(titleEn)
	}
	s.log.Printf("slug: %s", slug)
	filename := fmt.Sprintf("%s-%s.md", now.Format("2006-01-02"), slug)

	// Augment in original language.
	augmented := req.Augmented
	var augmentedEn, augmentedZh string
	if augmented == "" {
		s.log.Printf("augmenting idea: %s", req.Title)
		augmentedSource, augErr := s.llm.augment(ctx, lang, req.Title, enriched)
		if augErr != nil {
			s.log.Printf("LLM augmentation failed, publishing without augmentation: %v", augErr)
		} else {
			augmented = augmentedSource
		}
	} else {
		s.log.Printf("using provided augmented content for: %s", req.Title)
	}

	// Translate augmented content.
	if augmented != "" {
		otherLang := "zh"
		if lang == "zh" {
			otherLang = "en"
		}
		if lang == "en" {
			augmentedEn = augmented
		} else {
			augmentedZh = augmented
		}
		s.log.Printf("translating augmented content to %s...", otherLang)
		translatedAugmented, err := s.llm.translateContent(ctx, augmented, otherLang)
		if err != nil {
			s.log.Printf("augmented translation failed: %v", err)
		} else {
			if lang == "en" {
				augmentedZh = translatedAugmented
			} else {
				augmentedEn = translatedAugmented
			}
		}
	}

	llmGenerated := req.Augmented == "" && augmented != ""
	md := buildMarkdown(bilingualContent{
		date:         now,
		slug:         slug,
		titleEn:      titleEn,
		titleZh:      titleZh,
		contentEn:    contentEn,
		contentZh:    contentZh,
		augmentedEn:  augmentedEn,
		augmentedZh:  augmentedZh,
		llmGenerated: llmGenerated,
	})

	filePath := "content/ideas/" + filename
	commitMsg := sanitizeCommitMsg(fmt.Sprintf("ideas: %s", titleEn))
	if err := s.github.createFile(ctx, filePath, md, commitMsg); err != nil {
		s.log.Printf("GitHub commit failed: %v", err)
		return
	}
	s.log.Printf("idea published: %s", filename)
}

type bilingualContent struct {
	date         time.Time
	slug         string
	titleEn      string
	titleZh      string
	contentEn    string
	contentZh    string
	augmentedEn  string
	augmentedZh  string
	llmGenerated bool
}

func buildMarkdown(c bilingualContent) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("date: %s\n", c.date.Format("2006-01-02T15:04:05")))
	b.WriteString(fmt.Sprintf("slug: %q\n", c.slug))
	b.WriteString(fmt.Sprintf("title: %q\n", c.titleEn))
	b.WriteString(fmt.Sprintf("title_zh: %q\n", c.titleZh))
	b.WriteString("---\n\n")

	// English block.
	b.WriteString("{{% en %}}\n")
	b.WriteString(c.contentEn)
	if c.augmentedEn != "" {
		b.WriteString("\n\n{{% augmented %}}\n")
		if c.llmGenerated {
			b.WriteString("*The following content is generated by LLMs and may contain inaccuracies.*\n\n")
		}
		b.WriteString(c.augmentedEn)
		b.WriteString("\n{{% /augmented %}}\n")
	}
	b.WriteString("{{% /en %}}\n\n")

	// Chinese block.
	b.WriteString("{{% zh %}}\n")
	b.WriteString(c.contentZh)
	if c.augmentedZh != "" {
		b.WriteString("\n\n{{% augmented %}}\n")
		if c.llmGenerated {
			b.WriteString("*以下内容由 LLM 生成，可能包含不准确之处。*\n\n")
		}
		b.WriteString(c.augmentedZh)
		b.WriteString("\n{{% /augmented %}}\n")
	}
	b.WriteString("{{% /zh %}}\n")

	return b.String()
}

// detectLang guesses whether text is primarily Chinese or English
// by checking if more than half the non-space runes are CJK.
func detectLang(s string) string {
	var cjk, total int
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		total++
		if unicode.Is(unicode.Han, r) {
			cjk++
		}
	}
	if total > 0 && cjk*2 > total {
		return "zh"
	}
	return "en"
}

var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	var ascii strings.Builder
	for _, r := range s {
		if r <= unicode.MaxASCII {
			ascii.WriteRune(r)
		}
	}
	s = nonAlphanumRe.ReplaceAllString(ascii.String(), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "idea"
	}
	return s
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"{}|\\^` + "`" + `\[\]]+`)

func extractURLs(s string) []string {
	matches := urlRe.FindAllString(s, 5) // limit to 5 URLs
	return matches
}

func fetchURL(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ChangkunIdeasBot/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024)) // 32KB max
	if err != nil {
		return "", err
	}

	// Strip HTML tags for a rough plain-text extraction.
	text := stripHTMLTags(string(body))
	// Collapse whitespace.
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 4096 {
		text = text[:4096]
	}
	return text, nil
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTMLTags(s string) string {
	return htmlTagRe.ReplaceAllString(s, " ")
}

func (s *service) jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ideaResponse{OK: false, Message: msg})
}

func normalizeTranslateResult(tr *translateResult, sourceLang string) *translateResult {
	if tr == nil {
		return nil
	}
	n := *tr
	n.Lang = strings.TrimSpace(n.Lang)
	n.PolishedTitle = strings.TrimSpace(n.PolishedTitle)
	n.PolishedContent = strings.TrimSpace(n.PolishedContent)
	n.TranslatedTitle = strings.TrimSpace(n.TranslatedTitle)
	n.TranslatedContent = strings.TrimSpace(n.TranslatedContent)

	if sourceLang == "en" || sourceLang == "zh" {
		n.Lang = sourceLang
	}

	polishedLang := detectLang(n.PolishedContent)
	translatedLang := detectLang(n.TranslatedContent)
	if n.Lang == "en" && polishedLang == "zh" && translatedLang == "en" {
		n.PolishedTitle, n.TranslatedTitle = n.TranslatedTitle, n.PolishedTitle
		n.PolishedContent, n.TranslatedContent = n.TranslatedContent, n.PolishedContent
	}
	if n.Lang == "zh" && polishedLang == "en" && translatedLang == "zh" {
		n.PolishedTitle, n.TranslatedTitle = n.TranslatedTitle, n.PolishedTitle
		n.PolishedContent, n.TranslatedContent = n.TranslatedContent, n.PolishedContent
	}
	return &n
}

func isUsableTranslateResult(tr *translateResult) bool {
	if tr == nil {
		return false
	}
	if tr.Lang != "en" && tr.Lang != "zh" {
		return false
	}
	if !isLikelyTitle(tr.PolishedTitle) || !isLikelyTitle(tr.TranslatedTitle) {
		return false
	}
	if strings.TrimSpace(tr.PolishedContent) == "" || strings.TrimSpace(tr.TranslatedContent) == "" {
		return false
	}
	return true
}

func isLikelyTitle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "\r\n") {
		return false
	}
	if len([]rune(s)) > 120 {
		return false
	}
	return true
}
