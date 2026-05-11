package us

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"alphathesis/datasource"
	"alphathesis/store"
)

func TestSECIntegrationFetchRecentFilings(t *testing.T) {
	if os.Getenv("RUN_SEC") != "1" {
		t.Skip("set RUN_SEC=1 to run SEC integration tests")
	}
	symbol := getenvDefault("SEC_TEST_SYMBOL", "AAPL")
	userAgent := getenvDefault("SEC_USER_AGENT", "AlphaThesis test contact@example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sec, err := NewSECClient(userAgent)
	if err != nil {
		t.Fatalf("NewSECClient() error = %v", err)
	}
	candidates, err := sec.FetchCandidates(ctx, datasource.CandidateFetchInput{
		Symbol: symbol,
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("FetchCandidates() error = %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("FetchCandidates() returned no candidates")
	}
	for i, c := range candidates {
		if c.Source != datasource.SourceUSOfficialEventSEC {
			t.Fatalf("candidate[%d].Source = %q", i, c.Source)
		}
		if c.SourceID == "" {
			t.Fatalf("candidate[%d].SourceID is empty", i)
		}
		if c.SourceURL == "" {
			t.Fatalf("candidate[%d].SourceURL is empty", i)
		}
		if c.Symbol != symbol {
			t.Fatalf("candidate[%d].Symbol = %q, want %q", i, c.Symbol, symbol)
		}
		if c.Title == "" || c.Summary == "" {
			t.Fatalf("candidate[%d] has empty title/summary: %#v", i, c)
		}
		if c.PublishedAt == nil {
			t.Fatalf("candidate[%d].PublishedAt is nil", i)
		}
		logSECCandidate(t, i, c)
	}
}

func logSECCandidate(t *testing.T, i int, c store.CreateJobCandidateParams) {
	t.Helper()
	t.Logf("SEC candidate[%d]: source_id=%s symbol=%s published_at=%v", i, c.SourceID, c.Symbol, c.PublishedAt)
	t.Logf("SEC candidate[%d]: title=%s", i, c.Title)
	t.Logf("SEC candidate[%d]: summary=%s", i, c.Summary)
	t.Logf("SEC candidate[%d]: url=%s", i, c.SourceURL)

	var raw map[string]any
	if err := json.Unmarshal(c.RawPayload, &raw); err != nil {
		t.Fatalf("candidate[%d] raw_payload decode error: %v", i, err)
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	t.Logf("SEC candidate[%d] raw_payload:\n%s", i, string(b))
}
