package pyadapter

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPyadapterFetchTextIntegration tests Client.FetchText against a real CNInfo PDF via
// the running pyadapter Python server. Set RUN_PYADAPTER=1 to enable.
// Set CNINFO_TEST_URL to override the target PDF URL.
// Set PYADAPTER_URL to override the pyadapter base URL (default: http://127.0.0.1:8811).
func TestPyadapterFetchTextIntegration(t *testing.T) {
	if os.Getenv("RUN_PYADAPTER") != "1" {
		t.Skip("set RUN_PYADAPTER=1 to run pyadapter full-text fetcher integration tests")
	}

	cnInfoURL := os.Getenv("CNINFO_TEST_URL")
	if cnInfoURL == "" {
		t.Fatal("set CNINFO_TEST_URL to a CNInfo PDF announcement URL, " +
			"e.g. http://static.cninfo.com.cn/finalpage/2024-03-28/1219XXXXXXX.PDF")
	}

	var opts []Option
	if base := os.Getenv("PYADAPTER_URL"); base != "" {
		opts = append(opts, WithBaseURL(base))
	}
	client := NewClient(opts...)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	text, err := client.FetchText(ctx, cnInfoURL)
	if err != nil {
		t.Fatalf("FetchText(%q) error = %v", cnInfoURL, err)
	}

	t.Logf("fetched %d chars from %s", len(text), cnInfoURL)
	t.Logf("text preview: %s", previewText(text, 600))

	if len(strings.TrimSpace(text)) < 100 {
		t.Errorf("expected at least 100 chars of text, got %d", len(text))
	}
}

func previewText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
