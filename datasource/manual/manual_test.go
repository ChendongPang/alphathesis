package manual

import (
	"testing"

	"alphathesis/datasource"
)

func TestNewManualEvidenceCandidate(t *testing.T) {
	c, err := NewManualEvidenceCandidate(ManualEvidenceInput{
		Symbol:  "nvda",
		Title:   "Customer channel check",
		RawText: "GPU lead times shortened materially.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != datasource.SourceManual {
		t.Fatalf("Source = %q", c.Source)
	}
	if c.Symbol != "NVDA" {
		t.Fatalf("Symbol = %q", c.Symbol)
	}
	if c.Summary != "GPU lead times shortened materially." {
		t.Fatalf("Summary = %q", c.Summary)
	}
	if c.SourceID == "" {
		t.Fatal("SourceID is empty")
	}
}
