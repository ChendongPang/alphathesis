package evidence

import (
	"context"
	"testing"
	"time"

	"alphathesis/client"
)

// ------------------------------------------------------------
// Integration tests — require a local vLLM server
// ------------------------------------------------------------

func newTestJudge(t *testing.T) *Judge {
	t.Helper()
	llm, err := client.NewVLLMClient("", "", client.WithoutDebugLog(), client.WithTimeout(10*time.Second))
	if err != nil {
		t.Fatalf("create vllm client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	models, err := llm.ListModels(ctx)
	if err != nil {
		t.Skipf("vllm not available: %v", err)
	}
	if len(models.Data) == 0 {
		t.Skip("no models loaded in vllm")
	}

	model := models.Data[0].ID
	t.Logf("using model: %s", model)

	judge, err := NewJudge(llm, model)
	if err != nil {
		t.Fatalf("create judge: %v", err)
	}
	return judge
}

var appleAssumptions = []AssumptionTarget{
	{
		ID:            1,
		Key:           "iphone_sales_growth",
		Text:          "iPhone unit sales will grow year-over-year driven by emerging market demand",
		EvidenceHints: []string{"iPhone unit sales data", "emerging market revenue"},
	},
	{
		ID:            2,
		Key:           "services_margin",
		Text:          "Apple Services segment gross margin will remain above 70%",
		EvidenceHints: []string{"services revenue and margin data"},
	},
}

func logJudgment(t *testing.T, j *Judgment) {
	t.Helper()
	for _, e := range j.Evidences {
		t.Logf("  [%s] key=%s stage=%s stance=%s impact=%.2f confidence=%.2f relevance=%.2f",
			e.AssumptionKey, e.AssumptionKey, e.JudgeStage, e.Stance, e.Impact, e.Confidence, e.Relevance)
		t.Logf("    snippet: %s", e.SnippetText)
		t.Logf("    reason:  %s", e.Reason)
	}
}

func TestJudge_SummaryMode_SupportingArticle(t *testing.T) {
	judge := newTestJudge(t)

	input := Input{
		Title:       "Apple Reports Record Q4 Revenue, iPhone Sales Surge 15% in Emerging Markets",
		Summary:     "Apple Inc. reported record quarterly revenue of $89.5 billion for Q4 2024, driven by a 15% increase in iPhone shipments. CEO Tim Cook highlighted India and Southeast Asia as the fastest-growing regions. Services revenue hit $24.2 billion with gross margin of 74%.",
		Assumptions: appleAssumptions,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	judgment, err := judge.Judge(ctx, input)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	logJudgment(t, judgment)

	if len(judgment.Evidences) == 0 {
		t.Fatal("expected at least one evidence")
	}

	byKey := indexByKey(judgment.Evidences)

	if e, ok := byKey["iphone_sales_growth"]; ok {
		if e.AssumptionID != 1 {
			t.Errorf("iphone_sales_growth AssumptionID: got %d, want 1", e.AssumptionID)
		}
		if e.Stance != StanceSupport {
			t.Errorf("expected support stance, got %s", e.Stance)
		}
		if e.Impact <= 0 {
			t.Errorf("expected positive impact, got %.2f", e.Impact)
		}
		if e.JudgeStage != StageSummary {
			t.Errorf("expected summary stage, got %s", e.JudgeStage)
		}
	} else {
		t.Error("expected evidence for iphone_sales_growth")
	}
}

func TestJudge_SummaryMode_ContradictingArticle(t *testing.T) {
	judge := newTestJudge(t)

	input := Input{
		Title:   "Apple iPhone Sales Slump in China and India as Local Rivals Gain Share",
		Summary: "Apple's iPhone shipments fell 18% year-over-year in Q3, with declines in China and India as Huawei and Xiaomi captured market share. Analysts slashed full-year iPhone unit estimates by 12 million units.",
		Assumptions: []AssumptionTarget{
			appleAssumptions[0], // iphone_sales_growth only
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	judgment, err := judge.Judge(ctx, input)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	logJudgment(t, judgment)

	byKey := indexByKey(judgment.Evidences)
	e, ok := byKey["iphone_sales_growth"]
	if !ok {
		t.Fatal("expected evidence for iphone_sales_growth")
	}
	if e.Stance != StanceContradict {
		t.Errorf("expected contradict stance, got %s", e.Stance)
	}
	if e.Impact >= 0 {
		t.Errorf("expected negative impact for contradicting article, got %.2f", e.Impact)
	}
}

func TestJudge_RAGMode(t *testing.T) {
	judge := newTestJudge(t)

	// Simulate pre-retrieved chunks from vector search.
	chunks := []string{
		"Apple's iPhone shipments in India reached 8.5 million units in Q3, up 22% year-over-year, making India the fastest-growing iPhone market globally.",
		"The company attributed growth to the expanded network of Apple Stores in Tier-2 cities and trade-in programs that lowered the effective purchase price.",
	}

	input := Input{
		Title: "Apple Doubles Down on India Expansion as iPhone Demand Hits Record",
		Assumptions: []AssumptionTarget{
			{
				ID:            1,
				Key:           "iphone_sales_growth",
				Text:          "iPhone unit sales will grow year-over-year driven by emerging market demand",
				EvidenceHints: []string{"iPhone unit sales data", "emerging market revenue"},
				TopChunks:     chunks,
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	judgment, err := judge.Judge(ctx, input)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	logJudgment(t, judgment)

	if len(judgment.Evidences) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(judgment.Evidences))
	}
	e := judgment.Evidences[0]
	if e.JudgeStage != StageRAG {
		t.Errorf("expected rag stage, got %s", e.JudgeStage)
	}
	if e.Stance != StanceSupport {
		t.Errorf("expected support stance, got %s", e.Stance)
	}
	if e.SnippetText == "" {
		t.Error("expected non-empty snippet text")
	}
}

func TestJudge_MixedMode(t *testing.T) {
	judge := newTestJudge(t)

	// iphone_sales_growth → summary mode (no chunks)
	// services_margin → RAG mode (has chunks)
	input := Input{
		Title:   "Apple Q4 Results: iPhone Growth and Services Margins Beat Estimates",
		Summary: "Apple beat Q4 estimates with iPhone sales up 12% and Services gross margin at 73.5%.",
		Assumptions: []AssumptionTarget{
			{
				ID:            1,
				Key:           "iphone_sales_growth",
				Text:          "iPhone unit sales will grow year-over-year",
				EvidenceHints: []string{"iPhone unit sales"},
			},
			{
				ID:        2,
				Key:       "services_margin",
				Text:      "Apple Services gross margin will remain above 70%",
				TopChunks: []string{"Apple Services segment reported gross margin of 73.5% in Q4 2024, up from 70.8% a year ago, driven by higher-margin subscription revenue."},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	judgment, err := judge.Judge(ctx, input)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	logJudgment(t, judgment)

	byKey := indexByKey(judgment.Evidences)

	if e, ok := byKey["iphone_sales_growth"]; ok {
		if e.JudgeStage != StageSummary {
			t.Errorf("iphone_sales_growth: expected summary stage, got %s", e.JudgeStage)
		}
	} else {
		t.Error("expected evidence for iphone_sales_growth")
	}

	if e, ok := byKey["services_margin"]; ok {
		if e.JudgeStage != StageRAG {
			t.Errorf("services_margin: expected rag stage, got %s", e.JudgeStage)
		}
	} else {
		t.Error("expected evidence for services_margin")
	}
}

// ------------------------------------------------------------
// Unit tests — no LLM required
// ------------------------------------------------------------

func TestNormalizeStance(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"support", StanceSupport},
		{"contradict", StanceContradict},
		{"neutral", StanceNeutral},
		{"unknown", StanceNeutral},
		{"", StanceNeutral},
		{"SUPPORT", StanceNeutral}, // case-sensitive
	}
	for _, c := range cases {
		got := normalizeStance(c.input)
		if got != c.want {
			t.Errorf("normalizeStance(%q): got %q, want %q", c.input, got, c.want)
		}
	}
}

func TestNormalizeImpact_SignConsistency(t *testing.T) {
	cases := []struct {
		impact float64
		stance string
		want   float64
	}{
		// support requires positive impact
		{0.7, StanceSupport, 0.7},
		{-0.7, StanceSupport, 0.7},
		// contradict requires negative impact
		{-0.6, StanceContradict, -0.6},
		{0.6, StanceContradict, -0.6},
		// neutral: no sign correction
		{0.2, StanceNeutral, 0.2},
		{-0.2, StanceNeutral, -0.2},
	}
	for _, c := range cases {
		got := normalizeImpact(c.impact, c.stance)
		if got != c.want {
			t.Errorf("normalizeImpact(%.1f, %s): got %.1f, want %.1f", c.impact, c.stance, got, c.want)
		}
	}
}

func TestParseEvidences_FilterHallucinatedKey(t *testing.T) {
	targets := []AssumptionTarget{
		{ID: 1, Key: "real_key"},
	}
	content := `{"evidences":[
		{"assumption_key":"real_key","snippet_text":"foo","relevance":0.8,"confidence":0.9,"stance":"support","impact":0.6,"reason":"ok"},
		{"assumption_key":"fake_key","snippet_text":"bar","relevance":0.5,"confidence":0.5,"stance":"support","impact":0.5,"reason":"hallucinated"}
	]}`

	evs, err := parseEvidences(content, StageSummary, targets)
	if err != nil {
		t.Fatalf("parseEvidences: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(evs))
	}
	if evs[0].AssumptionKey != "real_key" {
		t.Errorf("wrong key: %s", evs[0].AssumptionKey)
	}
	if evs[0].AssumptionID != 1 {
		t.Errorf("AssumptionID: got %d, want 1", evs[0].AssumptionID)
	}
}

func TestParseEvidences_DeduplicatesKey(t *testing.T) {
	targets := []AssumptionTarget{{ID: 1, Key: "k"}}
	content := `{"evidences":[
		{"assumption_key":"k","snippet_text":"first","stance":"support","impact":0.5},
		{"assumption_key":"k","snippet_text":"second","stance":"support","impact":0.8}
	]}`

	evs, err := parseEvidences(content, StageSummary, targets)
	if err != nil {
		t.Fatalf("parseEvidences: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 evidence (deduplicated), got %d", len(evs))
	}
	if evs[0].SnippetText != "first" {
		t.Errorf("expected first occurrence to win, got %q", evs[0].SnippetText)
	}
}

func TestParseEvidences_ClampsAndNormalizesValues(t *testing.T) {
	targets := []AssumptionTarget{{ID: 1, Key: "k"}}
	content := `{"evidences":[
		{"assumption_key":"k","snippet_text":"x","relevance":2.0,"confidence":-0.5,"stance":"support","impact":-0.8}
	]}`

	evs, err := parseEvidences(content, StageSummary, targets)
	if err != nil {
		t.Fatalf("parseEvidences: %v", err)
	}
	e := evs[0]
	if e.Relevance != 1.0 {
		t.Errorf("Relevance: want 1.0, got %.2f", e.Relevance)
	}
	if e.Confidence != 0.0 {
		t.Errorf("Confidence: want 0.0, got %.2f", e.Confidence)
	}
	// stance=support + impact=-0.8 → impact should be flipped to +0.8
	if e.Impact != 0.8 {
		t.Errorf("Impact: want 0.8 (flipped), got %.2f", e.Impact)
	}
}

// ------------------------------------------------------------
// Helpers
// ------------------------------------------------------------

func indexByKey(evs []AssumptionEvidence) map[string]AssumptionEvidence {
	m := make(map[string]AssumptionEvidence, len(evs))
	for _, e := range evs {
		m[e.AssumptionKey] = e
	}
	return m
}
