package parser

import (
	"math"
	"testing"
)

func TestParsedThesisNormalize(t *testing.T) {
	parsed := ParsedThesis{
		Symbol:    " nvda ",
		Direction: "Long",
		CoreClaim: "AI demand remains strong.",
		Assumptions: []ParsedAssumption{
			{
				Key:           "AI GPU Demand",
				Text:          "AI GPU demand keeps growing.",
				Type:          "business",
				Verifiable:    true,
				Importance:    2,
				EvidenceHints: []string{"Data center revenue", "data center revenue", "  "},
			},
			{
				Key:        "ai_gpu_demand",
				Text:       "Hyperscaler capex keeps growing.",
				Type:       "financial",
				Verifiable: true,
				Importance: 1,
			},
		},
	}

	if err := parsed.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if parsed.Symbol != "NVDA" {
		t.Fatalf("Symbol = %q, want NVDA", parsed.Symbol)
	}
	if parsed.Direction != DirectionBullish {
		t.Fatalf("Direction = %q, want %q", parsed.Direction, DirectionBullish)
	}
	if parsed.Assumptions[0].Key != "ai_gpu_demand" {
		t.Fatalf("first key = %q", parsed.Assumptions[0].Key)
	}
	if parsed.Assumptions[1].Key != "ai_gpu_demand_2" {
		t.Fatalf("duplicate key = %q", parsed.Assumptions[1].Key)
	}
	total := parsed.Assumptions[0].Importance + parsed.Assumptions[1].Importance
	if math.Abs(total-1) > 1e-9 {
		t.Fatalf("importance total = %f, want 1", total)
	}
	if len(parsed.Assumptions[0].EvidenceHints) != 1 {
		t.Fatalf("hints = %#v, want one deduped hint", parsed.Assumptions[0].EvidenceHints)
	}
}

func TestParsedThesisNormalizeEqualImportance(t *testing.T) {
	parsed := ParsedThesis{
		Symbol:    "TSLA",
		Direction: "bearish",
		CoreClaim: "Margins may compress.",
		Assumptions: []ParsedAssumption{
			{Text: "EV pricing pressure continues.", Verifiable: true},
			{Text: "Regulatory credits decline.", Verifiable: true},
		},
	}

	if err := parsed.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	for _, assumption := range parsed.Assumptions {
		if math.Abs(assumption.Importance-0.5) > 1e-9 {
			t.Fatalf("importance = %f, want 0.5", assumption.Importance)
		}
	}
}
