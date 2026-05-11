package rag

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const minChunkChars = 30

// chunkText splits text into chunks using a paragraph-aware strategy.
//
// Priority:
//  1. Split on blank lines (\n\n) — block-level boundaries from htmlToText
//  2. If a paragraph exceeds maxChars, split at sentence boundaries
//  3. If a single sentence still exceeds maxChars, hard-cut at a UTF-8 boundary
//
// Chunks shorter than minChunkChars are dropped.
func chunkText(text string, maxChars int) []string {
	paragraphs := strings.Split(text, "\n\n")
	var chunks []string
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) <= maxChars {
			if len(p) >= minChunkChars && !isLowQualityChunk(p) {
				chunks = append(chunks, p)
			}
			continue
		}
		for _, c := range splitLong(p, maxChars) {
			c = strings.TrimSpace(c)
			if len(c) >= minChunkChars && !isLowQualityChunk(c) {
				chunks = append(chunks, c)
			}
		}
	}
	return chunks
}

// isLowQualityChunk returns true for chunks that are unlikely to contain
// meaningful evidence: boilerplate disclaimers and numeric-dense table rows.
func isLowQualityChunk(s string) bool {
	// Known Chinese financial boilerplate patterns.
	for _, pat := range []string{
		"郑重声明", "不构成投资建议", "风险自担", "与本站立场无关",
		"版权所有", "转载请注明", "免责声明",
	} {
		if strings.Contains(s, pat) {
			return true
		}
	}

	// Drop chunks where digits outnumber letters — typical of price/volume tables.
	var digits, letters int
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits++
		} else if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters > 0 && digits > letters*2 {
		return true
	}

	return false
}

// splitLong repeatedly cuts text into pieces no longer than maxChars,
// preferring sentence boundaries on each cut.
func splitLong(text string, maxChars int) []string {
	var chunks []string
	for len(text) > maxChars {
		cut := findSentenceCut(text, maxChars)
		if chunk := strings.TrimSpace(text[:cut]); chunk != "" {
			chunks = append(chunks, chunk)
		}
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}

// findSentenceCut returns the best byte offset to cut text at or before
// maxChars. It searches backwards from maxChars for:
//  1. Chinese/Japanese sentence terminators (。！？) — no trailing space needed
//  2. Western sentence terminators (.!?) followed by a space or end-of-text
//  3. A word boundary (space)
//  4. Hard UTF-8 boundary at maxChars
func findSentenceCut(text string, maxChars int) int {
	if maxChars >= len(text) {
		return len(text)
	}

	sub := text[:maxChars]

	// Chinese terminators — search backwards, return position after the terminator.
	for _, term := range []string{"？", "！", "。"} {
		if i := strings.LastIndex(sub, term); i >= 0 {
			return i + len(term)
		}
	}

	// Western terminators — must be followed by space or be at the cut boundary.
	for i := len(sub) - 1; i >= 0; i-- {
		b := sub[i]
		if b == '.' || b == '!' || b == '?' {
			next := i + 1
			if next >= len(text) || text[next] == ' ' || text[next] == '\n' {
				return next
			}
		}
	}

	// Word boundary.
	if i := strings.LastIndex(sub, " "); i > 0 {
		return i + 1
	}

	// Hard cut — step back to a valid UTF-8 rune start.
	for maxChars > 0 && !utf8.RuneStart(text[maxChars]) {
		maxChars--
	}
	return maxChars
}
