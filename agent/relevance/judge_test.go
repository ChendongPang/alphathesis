package relevance

import (
	"context"
	"testing"
	"time"

	"alphathesis/client"
)

// ------------------------------------------------------------
// Integration tests — require a local vLLM server
// ------------------------------------------------------------

// newTestJudge connects to the local vLLM server and returns a Judge ready to
// use. The test is skipped automatically when vLLM is not reachable.
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

// appleThesisAssumptions is a reusable set of assumptions for Apple iPhone thesis tests.
var appleThesisAssumptions = []AssumptionHint{
	{
		ID:            1,
		Key:           "iphone_sales_growth",
		Text:          "iPhone unit sales will grow year-over-year driven by emerging market demand",
		EvidenceHints: []string{"iPhone unit sales data", "emerging market revenue breakdown"},
	},
	{
		ID:            2,
		Key:           "services_margin",
		Text:          "Apple Services segment gross margin will remain above 70%",
		EvidenceHints: []string{"services revenue and margin data"},
	},
}

const appleCoreClaim = "Apple's iPhone business will continue to grow driven by emerging market expansion and product cycle upgrades"

func TestJudge_RelevantArticle(t *testing.T) {
	judge := newTestJudge(t)

	input := Input{
		Title:       "Apple Reports Record Q4 Revenue, iPhone Sales Surge 15%",
		Summary:     "Apple Inc. reported record quarterly revenue of $89.5 billion for Q4 2024, driven by a 15% increase in iPhone sales. CEO Tim Cook cited strong demand in emerging markets and the iPhone 16 lineup as key growth drivers.",
		CoreClaim:   appleCoreClaim,
		Assumptions: appleThesisAssumptions,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	judgment, err := judge.Judge(ctx, input)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}

	t.Logf("relevant=%v reason=%s", judgment.Relevant, judgment.Reason)
	for _, m := range judgment.MatchedAssumptions {
		t.Logf("  matched: id=%d key=%s relevance=%.2f confidence=%.2f impact=%.2f | %s",
			m.AssumptionID, m.AssumptionKey, m.Relevance, m.Confidence, m.InitialImpact, m.Reason)
	}

	if !judgment.Relevant {
		t.Error("expected article about iPhone sales growth to be relevant")
	}

	var matched *MatchedAssumption
	for i := range judgment.MatchedAssumptions {
		if judgment.MatchedAssumptions[i].AssumptionKey == "iphone_sales_growth" {
			matched = &judgment.MatchedAssumptions[i]
			break
		}
	}
	if matched == nil {
		t.Fatal("expected iphone_sales_growth to be matched")
	}
	if matched.AssumptionID != 1 {
		t.Errorf("AssumptionID: got %d, want 1", matched.AssumptionID)
	}
	if matched.InitialImpact <= 0 {
		t.Errorf("expected positive impact for supporting article, got %.2f", matched.InitialImpact)
	}
}

func TestJudge_IrrelevantArticle(t *testing.T) {
	judge := newTestJudge(t)

	input := Input{
		Title:       "Federal Reserve Holds Interest Rates Steady at 5.25%",
		Summary:     "The Federal Reserve kept its benchmark interest rate unchanged at 5.25%-5.5% on Wednesday, citing continued progress on inflation. Officials signaled caution about the pace of future rate cuts.",
		CoreClaim:   appleCoreClaim,
		Assumptions: appleThesisAssumptions,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	judgment, err := judge.Judge(ctx, input)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}

	t.Logf("relevant=%v reason=%s", judgment.Relevant, judgment.Reason)

	if judgment.Relevant {
		t.Error("expected Fed rate news to be irrelevant to iPhone thesis")
	}
}

func TestJudge_NegativeArticle(t *testing.T) {
	judge := newTestJudge(t)

	input := Input{
		Title:       "Apple iPhone Sales Disappoint in China, Market Share Falls to 5-Year Low",
		Summary:     "Apple's iPhone shipments in China dropped 18% year-over-year in Q3, as consumers shifted to domestic brands Huawei and Xiaomi. Analysts cut iPhone unit estimates citing weakening demand across emerging markets.",
		CoreClaim:   appleCoreClaim,
		Assumptions: appleThesisAssumptions,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	judgment, err := judge.Judge(ctx, input)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}

	t.Logf("relevant=%v reason=%s", judgment.Relevant, judgment.Reason)
	for _, m := range judgment.MatchedAssumptions {
		t.Logf("  matched: key=%s impact=%.2f | %s", m.AssumptionKey, m.InitialImpact, m.Reason)
	}

	if !judgment.Relevant {
		t.Error("expected article about iPhone decline to be relevant")
	}
	for _, m := range judgment.MatchedAssumptions {
		if m.AssumptionKey == "iphone_sales_growth" && m.InitialImpact >= 0 {
			t.Errorf("expected negative impact for contradicting article, got %.2f", m.InitialImpact)
		}
	}
}

// ------------------------------------------------------------
// Unit tests — no LLM required
// ------------------------------------------------------------

func TestNormalizeJudgment_FilterHallucinatedKey(t *testing.T) {
	hints := []AssumptionHint{
		{ID: 1, Key: "real_key"},
	}
	j := &Judgment{
		Relevant: true,
		MatchedAssumptions: []MatchedAssumption{
			{AssumptionKey: "real_key", Relevance: 0.9, Confidence: 0.8, InitialImpact: 0.5},
			{AssumptionKey: "hallucinated_key", Relevance: 0.7},
		},
	}
	normalizeJudgment(j, hints)

	if len(j.MatchedAssumptions) != 1 {
		t.Fatalf("expected 1 matched assumption, got %d", len(j.MatchedAssumptions))
	}
	if j.MatchedAssumptions[0].AssumptionKey != "real_key" {
		t.Errorf("wrong key: %s", j.MatchedAssumptions[0].AssumptionKey)
	}
}

func TestNormalizeJudgment_FillsAssumptionID(t *testing.T) {
	hints := []AssumptionHint{
		{ID: 42, Key: "key_a"},
		{ID: 99, Key: "key_b"},
	}
	j := &Judgment{
		MatchedAssumptions: []MatchedAssumption{
			{AssumptionKey: "key_b"},
			{AssumptionKey: "key_a"},
		},
	}
	normalizeJudgment(j, hints)

	for _, m := range j.MatchedAssumptions {
		switch m.AssumptionKey {
		case "key_a":
			if m.AssumptionID != 42 {
				t.Errorf("key_a: want ID 42, got %d", m.AssumptionID)
			}
		case "key_b":
			if m.AssumptionID != 99 {
				t.Errorf("key_b: want ID 99, got %d", m.AssumptionID)
			}
		}
	}
}

func TestNormalizeJudgment_ClampsFloats(t *testing.T) {
	hints := []AssumptionHint{{ID: 1, Key: "k"}}
	j := &Judgment{
		MatchedAssumptions: []MatchedAssumption{
			{AssumptionKey: "k", Relevance: 1.5, Confidence: -0.3, InitialImpact: 2.0},
		},
	}
	normalizeJudgment(j, hints)

	m := j.MatchedAssumptions[0]
	if m.Relevance != 1.0 {
		t.Errorf("Relevance: want 1.0, got %.2f", m.Relevance)
	}
	if m.Confidence != 0.0 {
		t.Errorf("Confidence: want 0.0, got %.2f", m.Confidence)
	}
	if m.InitialImpact != 1.0 {
		t.Errorf("InitialImpact: want 1.0, got %.2f", m.InitialImpact)
	}
}

func TestNormalizeJudgment_OverridesRelevantFlag(t *testing.T) {
	hints := []AssumptionHint{{ID: 1, Key: "k"}}

	// LLM said relevant=true but matched nothing valid.
	j := &Judgment{
		Relevant: true,
		MatchedAssumptions: []MatchedAssumption{
			{AssumptionKey: "fake_key"},
		},
	}
	normalizeJudgment(j, hints)
	if j.Relevant {
		t.Error("expected Relevant=false when all matched keys are invalid")
	}

	// LLM said relevant=false but did match something.
	j2 := &Judgment{
		Relevant: false,
		MatchedAssumptions: []MatchedAssumption{
			{AssumptionKey: "k"},
		},
	}
	normalizeJudgment(j2, hints)
	if !j2.Relevant {
		t.Error("expected Relevant=true when a valid key was matched")
	}
}
