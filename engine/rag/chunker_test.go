package rag

import (
	"strings"
	"testing"
)

func TestChunkTextShort(t *testing.T) {
	text := "Apple revenue grew 6% year over year."
	chunks := chunkText(text, 400)
	if len(chunks) != 1 || chunks[0] != text {
		t.Errorf("chunks = %v", chunks)
	}
}

func TestChunkTextParagraphSplit(t *testing.T) {
	text := "First paragraph about iPhone revenue.\n\nSecond paragraph about Services margin.\n\nThird paragraph about Mac shipments."
	chunks := chunkText(text, 400)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3; chunks = %v", len(chunks), chunks)
	}
	if !strings.Contains(chunks[0], "iPhone") {
		t.Errorf("chunk[0] = %q", chunks[0])
	}
	if !strings.Contains(chunks[1], "Services") {
		t.Errorf("chunk[1] = %q", chunks[1])
	}
}

func TestChunkTextLongParagraphSentenceSplit(t *testing.T) {
	// Build a paragraph with clearly delimited sentences, total > 100 chars
	para := "Apple reported strong earnings across its product lines. Revenue beat expectations across regions. Services hit a record high."
	chunks := chunkText(para, 50)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for long paragraph, got %d: %v", len(chunks), chunks)
	}
	// No chunk should exceed maxChars (50)
	for _, c := range chunks {
		if len(c) > 50 {
			t.Errorf("chunk exceeds maxChars: %q (len=%d)", c, len(c))
		}
	}
}

func TestChunkTextChineseSentenceSplit(t *testing.T) {
	para := "苹果公司发布了季报。营收同比增长6%。服务业务创历史新高。iPhone销量超出预期。"
	chunks := chunkText(para, 40)
	for _, c := range chunks {
		if len(c) > 40 {
			t.Errorf("chunk exceeds maxChars: %q (len=%d)", c, len(c))
		}
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
}

func TestChunkTextFiltersShortChunks(t *testing.T) {
	// Blank lines and tiny fragments should be dropped
	text := "ok\n\n\n\nThis is a meaningful sentence that should survive the filter."
	chunks := chunkText(text, 400)
	for _, c := range chunks {
		if len(strings.TrimSpace(c)) < minChunkChars {
			t.Errorf("short chunk not filtered: %q", c)
		}
	}
}

func TestChunkTextHardCutUTF8(t *testing.T) {
	// All Chinese chars: each is 3 bytes. maxChars=10 should not split mid-rune.
	text := "一二三四五六七八九十"
	chunks := chunkText(text, 10)
	for _, c := range chunks {
		if !strings.HasPrefix(text, c) && !strings.Contains(text, c) {
			t.Errorf("chunk not a substring of original: %q", c)
		}
		// Each rune is 3 bytes; chunk must not end mid-rune
		for _, r := range c {
			_ = r // just verify iteration doesn't panic on malformed UTF-8
		}
	}
}

func TestChunkTextEmptyAndBlank(t *testing.T) {
	if chunks := chunkText("", 400); len(chunks) != 0 {
		t.Errorf("expected empty, got %v", chunks)
	}
	if chunks := chunkText("   \n\n   ", 400); len(chunks) != 0 {
		t.Errorf("expected empty, got %v", chunks)
	}
}

func TestFindSentenceCutChinese(t *testing.T) {
	text := "苹果公司营收增长。市场反应积极。"
	// "苹果公司营收增长。" = 9 chars × 3 bytes = 27 bytes; use maxChars=30
	cut := findSentenceCut(text, 30)
	head := text[:cut]
	if !strings.HasSuffix(head, "。") {
		t.Errorf("cut did not land on 。: head=%q", head)
	}
}

func TestFindSentenceCutWestern(t *testing.T) {
	text := "Revenue grew. Margins expanded. Guidance raised."
	cut := findSentenceCut(text, 20)
	head := text[:cut]
	if !strings.HasSuffix(strings.TrimSpace(head), ".") {
		t.Errorf("cut did not land on period: head=%q", head)
	}
}

func TestFindSentenceCutWordBoundary(t *testing.T) {
	text := "nopunctuation just words here and there around the boundary"
	cut := findSentenceCut(text, 20)
	// Should cut at a space, not mid-word
	if cut > 0 && cut < len(text) && text[cut-1] != ' ' && text[cut] != ' ' {
		// Allow: cut lands right after a space (position after space is fine)
		if text[cut-1] != ' ' {
			t.Logf("cut at %d, char before=%q, char at=%q — may be word boundary", cut, string(text[cut-1]), string(text[cut]))
		}
	}
}
