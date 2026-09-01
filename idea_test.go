package main

import "testing"

func TestDetectLang(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "english text",
			input: "This is a simple English sentence about programming.",
			want:  "en",
		},
		{
			name:  "chinese text",
			input: "这是一个关于编程的简单中文句子。",
			want:  "zh",
		},
		{
			name:  "mostly english with some chinese",
			input: "This is mostly English with a few 中文 words mixed in for testing purposes.",
			want:  "en",
		},
		{
			name:  "mostly chinese with some english",
			input: "这主要是中文内容，只有少量English单词混在其中。",
			want:  "zh",
		},
		{
			name:  "empty string",
			input: "",
			want:  "en",
		},
		{
			name:  "only punctuation and spaces",
			input: "   ... !!! ???   ",
			want:  "en",
		},
		{
			name:  "mixed markdown with english",
			input: "## Title\n\nSome **bold** text and [a link](https://example.com).",
			want:  "en",
		},
		{
			name:  "mixed markdown with chinese",
			input: "## 标题\n\n这是一段包含**加粗**文本的中文内容，用于测试语言检测功能。",
			want:  "zh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectLang(tt.input)
			if got != tt.want {
				t.Errorf("detectLang(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeTranslateResult(t *testing.T) {
	t.Run("forces source language and swaps inverted payload", func(t *testing.T) {
		tr := &translateResult{
			Lang:              "en",
			PolishedTitle:     "English Title",
			PolishedContent:   "This is an English paragraph.",
			TranslatedTitle:   "中文标题",
			TranslatedContent: "这是一段中文内容。",
		}

		got := normalizeTranslateResult(tr, "zh")
		if got.Lang != "zh" {
			t.Fatalf("lang = %q, want zh", got.Lang)
		}
		if got.PolishedTitle != "中文标题" || got.PolishedContent != "这是一段中文内容。" {
			t.Fatalf("polished fields not swapped: %+v", got)
		}
		if got.TranslatedTitle != "English Title" || got.TranslatedContent != "This is an English paragraph." {
			t.Fatalf("translated fields not swapped: %+v", got)
		}
	})
}

func TestIsUsableTranslateResult(t *testing.T) {
	tests := []struct {
		name string
		in   *translateResult
		want bool
	}{
		{
			name: "valid payload",
			in: &translateResult{
				Lang:              "zh",
				PolishedTitle:     "中文标题",
				PolishedContent:   "这是原文。",
				TranslatedTitle:   "English Title",
				TranslatedContent: "This is the translation.",
			},
			want: true,
		},
		{
			name: "content in title",
			in: &translateResult{
				Lang:              "zh",
				PolishedTitle:     "This is a very long content block pretending to be a title and it clearly exceeds the expected title length limits used by the service for a safe payload check.",
				PolishedContent:   "这是原文。",
				TranslatedTitle:   "English Title",
				TranslatedContent: "This is the translation.",
			},
			want: false,
		},
		{
			name: "missing content",
			in: &translateResult{
				Lang:              "en",
				PolishedTitle:     "English Title",
				PolishedContent:   "",
				TranslatedTitle:   "中文标题",
				TranslatedContent: "中文内容",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUsableTranslateResult(tt.in); got != tt.want {
				t.Fatalf("isUsableTranslateResult() = %v, want %v", got, tt.want)
			}
		})
	}
}
