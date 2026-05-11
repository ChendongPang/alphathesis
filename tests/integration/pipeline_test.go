//go:build integration

// Package integration_test runs an end-to-end pipeline test against a real
// local vLLM server and a real PostgreSQL database.
//
// Prerequisites:
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/alphathesis_test?sslmode=disable
//	vLLM chat server  running at VLLM_CHAT_URL  (default http://localhost:8000/v1)
//	vLLM embed server running at VLLM_EMBED_URL (default http://localhost:8001/v1)
//
// Optional overrides:
//
//	CHAT_MODEL   override the chat model ID   (default: first model from /v1/models)
//	EMBED_MODEL  override the embed model ID  (default: first model from /v1/models)
//
// Run with:
//
//	go test -v -tags integration -timeout 10m ./tests/integration/
package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"alphathesis/agent/dedup"
	"alphathesis/agent/evidence"
	"alphathesis/agent/parser"
	"alphathesis/agent/relevance"
	reportagent "alphathesis/agent/report"
	"alphathesis/client"
	"alphathesis/datasource"
	eng "alphathesis/engine"
	"alphathesis/engine/rag"
	"alphathesis/engine/runner"
	"alphathesis/store"
)

// ─── slog → testing.T handler ────────────────────────────────────────────────

type tlogHandler struct {
	t     *testing.T
	level slog.Level
	attrs []slog.Attr
}

func newTlogHandler(t *testing.T, level slog.Level) slog.Handler {
	t.Helper()
	return &tlogHandler{t: t, level: level}
}

func (h *tlogHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *tlogHandler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %s", r.Level, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
		return true
	})
	h.t.Log(sb.String())
	return nil
}

func (h *tlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cp := *h
	cp.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &cp
}

func (h *tlogHandler) WithGroup(name string) slog.Handler {
	cp := *h
	return &cp
}

// ─── vLLM connectivity helpers ───────────────────────────────────────────────

// vllmClient creates a VLLMClient and skips the test if the endpoint is
// unreachable or has no models loaded.
func vllmClient(t *testing.T, baseURL string, timeout time.Duration) (*client.VLLMClient, string) {
	t.Helper()
	c, err := client.NewVLLMClient(baseURL, "", client.WithDebugLog(os.Stderr), client.WithTimeout(timeout))
	if err != nil {
		t.Skipf("invalid vLLM URL %q: %v", baseURL, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	models, err := c.ListModels(ctx)
	if err != nil {
		t.Skipf("vLLM at %s not reachable: %v", baseURL, err)
	}
	if len(models.Data) == 0 {
		t.Skipf("vLLM at %s has no models loaded", baseURL)
	}

	model := models.Data[0].ID
	t.Logf("vLLM %s → model %q", baseURL, model)
	return c, model
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ─── Stub data sources ────────────────────────────────────────────────────────

// stubFetcher returns two deterministic news candidates so the pipeline always
// has something to judge without hitting external APIs.
type stubFetcher struct {
	t        *testing.T
	thesisID int64
}

func (f *stubFetcher) FetchCandidates(_ context.Context, in datasource.CandidateFetchInput) ([]store.CreateJobCandidateParams, error) {
	now := time.Now()
	f.t.Logf("[stub-fetcher] fetching for symbol=%s thesisID=%d", in.Symbol, f.thesisID)
	id := func(n int) string { return fmt.Sprintf("stub-%d-%d", f.thesisID, n) }

	// Vary specific financial facts with thesisID so each run produces a
	// semantically distinct article the dedup judge will classify as a new event,
	// not a duplicate of a previous run's identical story.
	revenue := 18.0 + float64((int(f.thesisID)-1)%6)*2 // 18, 20, 22, 24, 26, 28 B
	quarter := (int(f.thesisID)-1)%4 + 1                // Q1–Q4
	year := 2025 + int(f.thesisID-1)/4                  // 2025, 2026, …
	capex := 30 + int(f.thesisID-1)%5*10                // 30–70 %

	return []store.CreateJobCandidateParams{
		{
			Source:  datasource.SourceUSNewsYFinance,
			SourceID: id(1),
			Symbol:  in.Symbol,
			Title:   fmt.Sprintf("NVIDIA Reports Record $%.0fB Data Center Revenue in Q%d FY%d", revenue, quarter, year),
			Summary: fmt.Sprintf(
				"NVIDIA data center segment delivered $%.0fB revenue in Q%d FY%d, up %.0f%% year-over-year. "+
					"H100 and H200 GPU demand from AI training clusters exceeded supply. "+
					"Management raised next-quarter guidance reflecting continued hyperscaler AI infrastructure build-out.",
				revenue, quarter, year, revenue*5),
			PublishedAt: &now,
		},
		{
			Source:  datasource.SourceUSNewsYFinance,
			SourceID: id(2),
			Symbol:  in.Symbol,
			Title:   fmt.Sprintf("AWS Azure GCP Raise AI Datacenter Capex by %d%% to Absorb GPU Demand", capex),
			Summary: fmt.Sprintf(
				"The three major cloud providers collectively announced %d%% increases in GPU infrastructure "+
					"capital expenditure for the coming fiscal year. The expanded NVIDIA H100/H200 allocations "+
					"support growing AI workloads. Microsoft alone committed $%.0fB to new GPU-dense data centers.",
				capex, float64(capex)*0.8),
			PublishedAt: &now,
		},
	}, nil
}

// stubTextFetcher returns empty so the evidence judge always uses summary mode
// (candidates have no URL, so this is just a safety net).
type stubTextFetcher struct{}

func (s *stubTextFetcher) FetchText(_ context.Context, _ string) (string, error) { return "", nil }

// ─── Diagnostic helper ────────────────────────────────────────────────────────

// logPipelineState queries intermediate DB state to help diagnose failures.
func logPipelineState(t *testing.T, db *store.DB, jobID int64) {
	t.Helper()
	pool := db.Pool()
	ctx := context.Background()

	var totalJC, relJC, irrelJC int
	_ = pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE relevance_status='relevant'), count(*) FILTER (WHERE relevance_status='irrelevant') FROM job_candidates WHERE job_id=$1`, jobID,
	).Scan(&totalJC, &relJC, &irrelJC)
	t.Logf("  job_candidates: total=%d relevant=%d irrelevant=%d", totalJC, relJC, irrelJC)

	rows, err := pool.Query(ctx, `
		SELECT rc.source_id, rc.dedup_status, rc.evidence_status
		FROM relevant_candidates rc
		JOIN candidate_assumptions ca ON ca.candidate_id = rc.id
		JOIN assumptions a ON a.id = ca.assumption_id
		JOIN job_runs jr ON jr.thesis_id = a.thesis_id
		WHERE jr.id = $1
		ORDER BY rc.id`, jobID)
	if err != nil {
		t.Logf("  relevant_candidates query error: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var sid, ds, es string
		if err := rows.Scan(&sid, &ds, &es); err == nil {
			t.Logf("  relevant_candidate: source_id=%s dedup=%s evidence=%s", sid, ds, es)
		}
	}
}

// ─── Main integration test ────────────────────────────────────────────────────

func TestFullPipeline(t *testing.T) {
	dbDSN := os.Getenv("TEST_DATABASE_URL")
	if dbDSN == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	chatURL := envOr("VLLM_CHAT_URL", "http://localhost:8000/v1")
	embedURL := envOr("VLLM_EMBED_URL", "http://localhost:8001/v1")

	// Skip immediately if either vLLM endpoint is down.
	chatClient, detectedChatModel := vllmClient(t, chatURL, 120*time.Second)
	embedClient, detectedEmbedModel := vllmClient(t, embedURL, 60*time.Second)

	chatModel := envOr("CHAT_MODEL", detectedChatModel)
	embedModel := envOr("EMBED_MODEL", detectedEmbedModel)
	t.Logf("chat_model=%s  embed_model=%s", chatModel, embedModel)

	log := slog.New(newTlogHandler(t, slog.LevelDebug))
	ctx := context.Background()

	// ── Database ───────────────────────────────────────────────────────
	t.Log("─── connecting to database")
	db, err := store.NewDB(ctx, dbDSN)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(db.Close)

	thesisStore := store.NewThesisStore(db)
	jobStore := store.NewJobStore(db)
	candidateStore := store.NewCandidateStore(db)
	evidenceStore := store.NewEvidenceStore(db)
	scoreStore := store.NewScoreStore(db)

	// ── Agents ────────────────────────────────────────────────────────
	parserAgent, err := parser.NewThesisParserAgent(chatClient, chatModel)
	if err != nil {
		t.Fatalf("create parser agent: %v", err)
	}
	relevanceJudge, err := relevance.NewJudge(chatClient, chatModel)
	if err != nil {
		t.Fatalf("create relevance judge: %v", err)
	}
	dedupJudge, err := dedup.NewJudge(chatClient, chatModel)
	if err != nil {
		t.Fatalf("create dedup judge: %v", err)
	}
	evidenceJudge, err := evidence.NewJudge(chatClient, chatModel)
	if err != nil {
		t.Fatalf("create evidence judge: %v", err)
	}
	reporter := reportagent.New(chatClient, chatModel)

	// ── Engines ────────────────────────────────────────────────────────
	assumptionEmbedder := eng.NewAssumptionEmbedder(embedClient, embedModel, thesisStore)
	ragEngine := rag.New(embedClient, embedModel)
	scoreEngine := eng.NewScoreEngine(eng.ScoreConfig{})
	marketEngine := eng.NewMarketContextEngine(eng.MarketContextConfig{})

	// ══════════════════════════════════════════════════════════════════
	// Stage 1: parse and save thesis
	// ══════════════════════════════════════════════════════════════════
	t.Log("─── stage 1: parse thesis")

	const rawThesisText = `我认为 NVDA 会跑赢大盘。
AI 训练对 GPU 的需求在持续爆发，同时头部云厂商的资本开支依然很高，
这两个核心假设如果成立，NVDA 的营收和利润都有很大上行空间。`

	parsed, err := parserAgent.Parse(ctx, rawThesisText)
	if err != nil {
		t.Fatalf("parse thesis: %v", err)
	}
	if err := parsed.Normalize(); err != nil {
		t.Fatalf("normalize thesis: %v", err)
	}
	t.Logf("parsed: symbol=%s direction=%s core_claim=%q",
		parsed.Symbol, parsed.Direction, parsed.CoreClaim)
	t.Logf("assumptions (%d, importance_sum=%.2f):", len(parsed.Assumptions), importanceSum(parsed.Assumptions))
	for i, a := range parsed.Assumptions {
		t.Logf("  [%d] key=%q type=%s importance=%.2f verifiable=%v", i+1, a.Key, a.Type, a.Importance, a.Verifiable)
		t.Logf("       text: %s", a.Text)
	}

	user, err := thesisStore.GetOrCreateUser(ctx, "integration-test@example.local", "Integration Test")
	if err != nil {
		t.Fatalf("get/create user: %v", err)
	}

	assumptionParams := make([]store.AssumptionParams, len(parsed.Assumptions))
	for i, a := range parsed.Assumptions {
		assumptionParams[i] = store.AssumptionParams{
			AssumptionKey: a.Key,
			Text:          a.Text,
			Type:          a.Type,
			Verifiable:    a.Verifiable,
			Importance:    a.Importance,
			EvidenceHints: a.EvidenceHints,
		}
	}

	thesis, err := thesisStore.CreateThesis(ctx, store.CreateThesisParams{
		UserID:        user.ID,
		Symbol:        parsed.Symbol,
		CompanyName:   parsed.CompanyName,
		Market:        parsed.Market,
		Direction:     parsed.Direction,
		RawText:       rawThesisText,
		CoreClaim:     parsed.CoreClaim,
		LLMModel:      chatModel,
		ParserVersion: "thesis_parser_v1",
		Assumptions:   assumptionParams,
	})
	if err != nil {
		t.Fatalf("create thesis: %v", err)
	}
	t.Logf("thesis id=%d symbol=%s version=%d initial_score=%.4f",
		thesis.ID, thesis.Symbol, thesis.Version, thesis.ConfidenceScore)

	// ══════════════════════════════════════════════════════════════════
	// Stage 2: embed assumptions
	// ══════════════════════════════════════════════════════════════════
	t.Log("─── stage 2: embed assumptions")

	assumptions, err := thesisStore.GetAssumptionsWithEmbeddings(ctx, thesis.ID)
	if err != nil {
		t.Fatalf("get assumptions: %v", err)
	}
	if err := assumptionEmbedder.EmbedAssumptions(ctx, assumptions); err != nil {
		t.Fatalf("embed assumptions: %v", err)
	}
	t.Logf("embedded %d assumptions", len(assumptions))

	// ══════════════════════════════════════════════════════════════════
	// Stage 3: enqueue job
	// ══════════════════════════════════════════════════════════════════
	t.Log("─── stage 3: enqueue daily_run job")

	job, err := jobStore.CreateJobRun(ctx, thesis.ID, thesis.Version, time.Now(), store.JobTypeDailyRun)
	if err != nil {
		t.Fatalf("create job run: %v", err)
	}
	t.Logf("job id=%d status=%s step=%s", job.ID, job.Status, job.CurrentStep)

	// ══════════════════════════════════════════════════════════════════
	// Stage 4: run the full 8-step pipeline
	// ══════════════════════════════════════════════════════════════════
	t.Log("─── stage 4: run pipeline (init → fetch → relevance → dedup → evidence → score → market → report)")

	r := runner.New(log, runner.Config{
		LLMModel:                 chatModel,
		EmbedModel:               embedModel,
		RAGTopK:                  3,
		DedupSearchLimit:         10,
		FetchLookback:            48 * time.Hour,
		TopSnippetsPerAssumption: 3,
	}, runner.Deps{
		JobStore:       jobStore,
		ThesisStore:    thesisStore,
		CandidateStore: candidateStore,
		EvidenceStore:  evidenceStore,
		ScoreStore:     scoreStore,

		Fetchers:    []runner.MarketFetcher{{Fetcher: &stubFetcher{t: t, thesisID: thesis.ID}}},
		TextFetcher: &stubTextFetcher{},

		RelevanceJudge:     relevanceJudge,
		DedupJudge:         dedupJudge,
		EvidenceJudge:      evidenceJudge,
		AssumptionEmbedder: assumptionEmbedder,
		RAGEngine:          ragEngine,
		ScoreEngine:        scoreEngine,
		MarketEngine:       marketEngine,
		Reporter:           reporter,

		Embedder:   embedClient,
		EmbedModel: embedModel,

		PriceQuotes: runner.NoOpPriceQuoteFetcher{},
	})

	if err := r.ProcessAll(ctx); err != nil {
		t.Fatalf("ProcessAll: %v", err)
	}

	t.Log("─── pipeline intermediate state")
	logPipelineState(t, db, job.ID)

	// ══════════════════════════════════════════════════════════════════
	// Assertions
	// ══════════════════════════════════════════════════════════════════
	t.Log("─── assertions")

	// 1. Job succeeded.
	finishedJob, err := jobStore.GetJobRun(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	errMsg := "<nil>"
	if finishedJob.ErrorMessage != nil {
		errMsg = *finishedJob.ErrorMessage
	}
	t.Logf("job status=%s step=%s error=%s", finishedJob.Status, finishedJob.CurrentStep, errMsg)
	if finishedJob.Status != store.JobStatusSucceeded {
		t.Errorf("job.status = %q, want %q (step=%s error=%s)",
			finishedJob.Status, store.JobStatusSucceeded, finishedJob.CurrentStep, errMsg)
	}

	// 2. Evidence snippets created.
	finalAssumptions, err := thesisStore.GetAssumptionsWithEmbeddings(ctx, thesis.ID)
	if err != nil {
		t.Fatalf("get final assumptions: %v", err)
	}
	totalSnippets := 0
	for _, a := range finalAssumptions {
		snippets, err := evidenceStore.ListTopEvidenceSnippetsByAssumption(ctx, a.ID, 10)
		if err != nil {
			t.Errorf("list snippets for %q: %v", a.AssumptionKey, err)
			continue
		}
		t.Logf("assumption %q: snippets=%d current_score=%.4f", a.AssumptionKey, len(snippets), a.CurrentScore)
		for _, s := range snippets {
			t.Logf("  stance=%-10s impact=%+.2f source_w=%.1f novelty_w=%.1f: %q",
				s.Stance, s.Impact, s.SourceWeight, s.NoveltyWeight, truncate(s.SnippetText, 80))
		}
		totalSnippets += len(snippets)
	}
	if totalSnippets == 0 {
		t.Error("no evidence snippets created — evidence_judge may have failed or all candidates were irrelevant")
	}

	// 3. Thesis confidence score updated from initial 0.5.
	finalThesis, err := thesisStore.GetThesis(ctx, thesis.ID)
	if err != nil {
		t.Fatalf("get final thesis: %v", err)
	}
	t.Logf("thesis confidence_score: %.4f → %.4f", thesis.ConfidenceScore, finalThesis.ConfidenceScore)
	if finalThesis.ConfidenceScore == thesis.ConfidenceScore {
		t.Error("thesis confidence_score unchanged at 0.5 — score engine may not have run")
	}

	// 4. Daily report created with non-empty markdown.
	report, err := scoreStore.GetDailyReport(ctx, thesis.ID, job.RunDate)
	if err != nil {
		t.Fatalf("get daily report: %v", err)
	}
	if report == nil {
		t.Fatal("no daily report created")
	}
	t.Logf("daily report: title=%q alert=%s score %.4f→%.4f",
		report.Title, report.AlertLevel, report.ThesisScoreBefore, report.ThesisScoreAfter)
	if report.MarkdownReport == "" {
		t.Error("daily report markdown is empty")
	}

	t.Log("─── PASSED")
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func importanceSum(assumptions []parser.ParsedAssumption) float64 {
	var sum float64
	for _, a := range assumptions {
		sum += a.Importance
	}
	return sum
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
