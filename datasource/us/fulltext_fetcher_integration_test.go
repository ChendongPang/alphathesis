package us

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFullTextFetcherIntegration_SECEdgar tests FullTextFetcher.FetchText against a real
// SEC EDGAR HTML page. Set RUN_FULLTEXT=1 to enable; override the URL with FULLTEXT_TEST_URL.
func TestFullTextFetcherIntegration_SECEdgar(t *testing.T) {
	if os.Getenv("RUN_FULLTEXT") != "1" {
		t.Skip("set RUN_FULLTEXT=1 to run full-text fetcher integration tests")
	}

	// NVIDIA's SEC EDGAR 10-K filing list — stable, public, always HTML.
	testURL := getenvDefault("FULLTEXT_TEST_URL",
		"https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=0001045810&type=10-K&dateb=&owner=include&count=10")

	fetcher := NewFullTextFetcher()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	text, err := fetcher.FetchText(ctx, testURL)
	if err != nil {
		t.Fatalf("FetchText(%q) error = %v", testURL, err)
	}

	t.Logf("fetched %d chars from %s", len(text), testURL)
	t.Logf("text preview: %s", responsePreview(text))

	if len(text) < 200 {
		t.Errorf("expected at least 200 chars of plain text, got %d", len(text))
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "10-k") && !strings.Contains(lower, "nvidia") && !strings.Contains(lower, "filing") {
		t.Errorf("SEC EDGAR page should mention 10-K, NVIDIA, or filing; preview: %.400s", text)
	}
}
