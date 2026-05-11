// Package runner orchestrates the full daily evidence pipeline for AlphaThesis.
// Each JobRun maps to exactly one thesis and executes a sequence of steps
// (fetch → relevance → dedup → evidence → score → market → report) with
// per-step checkpointing so that a crashed run resumes from the last safe point.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"alphathesis/agent/dedup"
	"alphathesis/agent/evidence"
	"alphathesis/agent/relevance"
	reportagent "alphathesis/agent/report"
	"alphathesis/datasource"
	eng "alphathesis/engine"
	"alphathesis/engine/rag"
	"alphathesis/store"
)

// PriceQuoteFetcher fetches today's OHLC data for a single symbol.
// Return a zero-value PriceQuote (empty Symbol) when data is unavailable;
// MarketContextEngine handles zeros gracefully (alert_level = "none").
type PriceQuoteFetcher interface {
	FetchQuote(ctx context.Context, symbol string) (eng.PriceQuote, error)
}

// NoOpPriceQuoteFetcher is a stub that always returns zero prices.
// Replace with a real implementation once a market data source is wired up.
type NoOpPriceQuoteFetcher struct{}

func (NoOpPriceQuoteFetcher) FetchQuote(_ context.Context, symbol string) (eng.PriceQuote, error) {
	return eng.PriceQuote{Symbol: symbol}, nil
}

// Config holds all tunable parameters for the Runner.
type Config struct {
	// LLMModel is the chat model served by vLLM (e.g. "Qwen/Qwen3-8B").
	LLMModel string
	// EmbedModel is the embedding model served by vLLM.
	EmbedModel string
	// RAGTopK is how many chunks to retrieve per assumption in RAG mode.
	RAGTopK int
	// DedupSearchLimit is how many similar candidates to retrieve for dedup.
	DedupSearchLimit int
	// FetchLookback is how far back to fetch candidates (default 48 h).
	FetchLookback time.Duration
	// NewsLimit caps how many news candidates to fetch per run (default 50).
	NewsLimit int
	// EventLimit caps how many event/filing candidates to fetch per run (default 50).
	EventLimit int
	// TopSnippetsPerAssumption controls how many snippets go into the report.
	TopSnippetsPerAssumption int
}

func (c *Config) defaults() {
	if c.RAGTopK <= 0 {
		c.RAGTopK = 3
	}
	if c.DedupSearchLimit <= 0 {
		c.DedupSearchLimit = 10
	}
	if c.FetchLookback <= 0 {
		c.FetchLookback = 48 * time.Hour
	}
	if c.NewsLimit <= 0 {
		c.NewsLimit = 50
	}
	if c.EventLimit <= 0 {
		c.EventLimit = 50
	}
	if c.TopSnippetsPerAssumption <= 0 {
		c.TopSnippetsPerAssumption = 3
	}
}

// MarketFetcher pairs a CandidateFetcher with the market and type it covers.
// Market should be "us", "cn", or "" (matches all markets).
// Type should be "news", "event", or "" (no per-type limit applied).
type MarketFetcher struct {
	Market  string
	Type    string
	Fetcher datasource.CandidateFetcher
}

// Deps groups all external collaborators the Runner needs.
type Deps struct {
	JobStore       *store.JobStore
	ThesisStore    *store.ThesisStore
	CandidateStore *store.CandidateStore
	EvidenceStore  *store.EvidenceStore
	ScoreStore     *store.ScoreStore

	// Fetchers supply raw candidate articles from each datasource.
	// Only fetchers whose Market matches the thesis market are called.
	Fetchers    []MarketFetcher
	// TextFetcher retrieves full article text for RAG. May be nil → summary-only.
	TextFetcher datasource.FullTextFetcher

	RelevanceJudge     *relevance.Judge
	DedupJudge         *dedup.Judge
	EvidenceJudge      *evidence.Judge
	AssumptionEmbedder *eng.AssumptionEmbedder
	RAGEngine          *rag.RAGEngine
	ScoreEngine        *eng.ScoreEngine
	MarketEngine       *eng.MarketContextEngine
	Reporter           *reportagent.Agent

	// Embedder + EmbedModel are used to embed candidates for dedup vector search.
	Embedder   rag.Embedder
	EmbedModel string

	PriceQuotes PriceQuoteFetcher
}

// jobContext carries in-memory state that is shared across steps within one
// job execution. Steps populate it lazily; ensureContext reloads if nil.
type jobContext struct {
	thesis      *store.Thesis
	assumptions []*store.Assumption // with embeddings
	marketCtx   eng.MarketContext
}

// Runner executes queued JobRuns one at a time, in FIFO order.
type Runner struct {
	log  *slog.Logger
	cfg  Config
	deps Deps
}

// New creates a Runner. The caller is responsible for ensuring all Deps fields
// are non-nil before starting the runner.
func New(log *slog.Logger, cfg Config, deps Deps) *Runner {
	cfg.defaults()
	if log == nil {
		log = slog.Default()
	}
	return &Runner{log: log, cfg: cfg, deps: deps}
}

// EnqueueAll creates a daily job for every active thesis that has not yet been
// run today. Safe to call multiple times — duplicate runs on the same date are
// skipped. Returns the number of newly enqueued jobs.
func (r *Runner) EnqueueAll(ctx context.Context) (int, error) {
	theses, err := r.deps.ThesisStore.ListAllActiveTheses(ctx)
	if err != nil {
		return 0, fmt.Errorf("list theses: %w", err)
	}
	now := time.Now()
	enqueued := 0
	for _, t := range theses {
		already, err := r.deps.JobStore.HasJobRunToday(ctx, t.ID, store.JobTypeDailyRun, now)
		if err != nil {
			return enqueued, fmt.Errorf("check job today (thesis %d): %w", t.ID, err)
		}
		if already {
			r.log.Info("skipping thesis — already has job today", "thesis_id", t.ID, "symbol", t.Symbol)
			continue
		}
		if _, err := r.deps.JobStore.CreateJobRun(ctx, t.ID, t.Version, now, store.JobTypeDailyRun); err != nil {
			return enqueued, fmt.Errorf("enqueue thesis %d: %w", t.ID, err)
		}
		r.log.Info("enqueued daily job", "thesis_id", t.ID, "symbol", t.Symbol)
		enqueued++
	}
	return enqueued, nil
}

// RecoverStuckJobs resets any jobs that were left in "running" state from a
// previous crash back to "queued". Call once at startup.
func (r *Runner) RecoverStuckJobs(ctx context.Context) error {
	n, err := r.deps.JobStore.RecoverRunningJobs(ctx)
	if err != nil {
		return fmt.Errorf("recover stuck jobs: %w", err)
	}
	if n > 0 {
		r.log.Info("recovered stuck jobs", "count", n)
	}
	return nil
}

// ProcessAll fetches all queued jobs and runs them sequentially.
func (r *Runner) ProcessAll(ctx context.Context) error {
	jobs, err := r.deps.JobStore.GetQueuedJobRuns(ctx)
	if err != nil {
		return fmt.Errorf("get queued jobs: %w", err)
	}
	r.log.Info("processing queued jobs", "count", len(jobs))
	for _, job := range jobs {
		if err := r.processJob(ctx, job); err != nil {
			r.log.Error("job failed", "job_id", job.ID, "thesis_id", job.ThesisID, "err", err)
			// Continue with remaining jobs even if one fails.
		}
	}
	return nil
}

func (r *Runner) processJob(ctx context.Context, job *store.JobRun) error {
	if err := r.deps.JobStore.MarkJobRunStarted(ctx, job.ID); err != nil {
		return fmt.Errorf("mark job started: %w", err)
	}
	r.log.Info("job started", "job_id", job.ID, "thesis_id", job.ThesisID, "step", job.CurrentStep)

	jc := &jobContext{}
	if err := r.runSteps(ctx, job, jc); err != nil {
		errMsg := err.Error()
		if markErr := r.deps.JobStore.MarkJobRunFailed(ctx, job.ID, errMsg); markErr != nil {
			r.log.Error("failed to mark job as failed", "job_id", job.ID, "err", markErr)
		}
		return err
	}

	return r.deps.JobStore.MarkJobRunSucceeded(ctx, job.ID)
}

// runSteps executes the pipeline steps in order, resuming from job.CurrentStep.
// Each step is responsible for its own idempotency so that retries are safe.
func (r *Runner) runSteps(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	type step struct {
		checkpoint string
		fn         func(ctx context.Context, job *store.JobRun, jc *jobContext) error
	}

	steps := []step{
		{store.JobStepInit, r.stepInit},
		{store.JobStepFetchCandidates, r.stepFetchCandidates},
		{store.JobStepRelevanceJudge, r.stepRelevanceJudge},
		{store.JobStepDedupJudge, r.stepDedupJudge},
		{store.JobStepEvidenceJudge, r.stepEvidenceJudge},
		{store.JobStepScoreUpdate, r.stepScoreUpdate},
		{store.JobStepMarketContext, r.stepMarketContext},
		{store.JobStepReportGenerate, r.stepReportGenerate},
	}

	// Find the index to start from based on the job's current checkpoint.
	startIdx := 0
	for i, s := range steps {
		if s.checkpoint == job.CurrentStep {
			startIdx = i
			break
		}
	}

	for i := startIdx; i < len(steps); i++ {
		s := steps[i]
		r.log.Info("running step", "job_id", job.ID, "step", s.checkpoint)

		if err := s.fn(ctx, job, jc); err != nil {
			return fmt.Errorf("step %s: %w", s.checkpoint, err)
		}

		// Advance checkpoint to the next step so that a crash between steps
		// resumes from the correct position.
		if i+1 < len(steps) {
			next := steps[i+1].checkpoint
			if advErr := r.deps.JobStore.AdvanceJobStep(ctx, job.ID, next); advErr != nil {
				r.log.Warn("advance job step failed", "job_id", job.ID, "next", next, "err", advErr)
			}
		}
	}
	return nil
}

// ProcessJobWithProgress runs the full pipeline for a single job, calling emit
// after each step so the caller can stream per-step progress to the user.
// Designed for use by the HTTP server when running jobs inline during an SSE stream.
func (r *Runner) ProcessJobWithProgress(ctx context.Context, job *store.JobRun, emit func(kind, text string)) error {
	if err := r.deps.JobStore.MarkJobRunStarted(ctx, job.ID); err != nil {
		return fmt.Errorf("mark job started: %w", err)
	}

	type stepDef struct {
		checkpoint string
		label      string
		fn         func(ctx context.Context, job *store.JobRun, jc *jobContext) error
		after      func(ctx context.Context, job *store.JobRun, jc *jobContext) string
	}

	jc := &jobContext{}

	steps := []stepDef{
		{
			checkpoint: store.JobStepInit,
			label:      "初始化假设向量",
			fn:         r.stepInit,
			after: func(ctx context.Context, job *store.JobRun, jc *jobContext) string {
				return fmt.Sprintf("已处理 %d 条 assumption 向量嵌入", len(jc.assumptions))
			},
		},
		{
			checkpoint: store.JobStepFetchCandidates,
			label:      "抓取候选文章",
			fn:         r.stepFetchCandidates,
			after: func(ctx context.Context, job *store.JobRun, jc *jobContext) string {
				pending, _ := r.deps.JobStore.ListJobCandidatesByStatus(ctx, job.ID, store.RelevanceStatusPending)
				return fmt.Sprintf("抓取 %d 条候选文章", len(pending))
			},
		},
		{
			checkpoint: store.JobStepRelevanceJudge,
			label:      "相关性判断",
			fn:         r.stepRelevanceJudge,
			after: func(ctx context.Context, job *store.JobRun, jc *jobContext) string {
				relevant, _ := r.deps.JobStore.ListJobCandidatesByStatus(ctx, job.ID, store.RelevanceStatusRelevant)
				irrelevant, _ := r.deps.JobStore.ListJobCandidatesByStatus(ctx, job.ID, store.RelevanceStatusIrrelevant)
				return fmt.Sprintf("相关 %d 条，过滤 %d 条", len(relevant), len(irrelevant))
			},
		},
		{
			checkpoint: store.JobStepDedupJudge,
			label:      "去重判断",
			fn:         r.stepDedupJudge,
			after: func(ctx context.Context, job *store.JobRun, jc *jobContext) string {
				newEvents, _ := r.deps.CandidateStore.ListRelevantCandidatesByThesisAndDedupStatus(ctx, job.ThesisID, store.DedupStatusNewEvent)
				updates, _ := r.deps.CandidateStore.ListRelevantCandidatesByThesisAndDedupStatus(ctx, job.ThesisID, store.DedupStatusEventUpdate)
				dups, _ := r.deps.CandidateStore.ListRelevantCandidatesByThesisAndDedupStatus(ctx, job.ThesisID, store.DedupStatusDuplicate)
				return fmt.Sprintf("新事件 %d 条，更新 %d 条，重复 %d 条", len(newEvents), len(updates), len(dups))
			},
		},
		{
			checkpoint: store.JobStepEvidenceJudge,
			label:      "生成证据片段",
			fn:         r.stepEvidenceJudge,
			after: func(ctx context.Context, job *store.JobRun, jc *jobContext) string {
				snips, _ := r.deps.EvidenceStore.ListEvidenceSnippetsByThesis(ctx, job.ThesisID, 500)
				return fmt.Sprintf("生成 %d 条证据片段", len(snips))
			},
		},
		{
			checkpoint: store.JobStepScoreUpdate,
			label:      "更新论题评分",
			fn:         r.stepScoreUpdate,
			after: func(ctx context.Context, job *store.JobRun, jc *jobContext) string {
				scoreBefore := int(jc.thesis.ConfidenceScore * 100)
				if t, err := r.deps.ThesisStore.GetThesis(ctx, job.ThesisID); err == nil && t != nil {
					return fmt.Sprintf("论题评分 %d → %d", scoreBefore, int(t.ConfidenceScore*100))
				}
				return "评分更新完成"
			},
		},
		{
			checkpoint: store.JobStepMarketContext,
			label:      "获取市场行情",
			fn:         r.stepMarketContext,
			after: func(ctx context.Context, job *store.JobRun, jc *jobContext) string {
				if jc.marketCtx.StockReturn != 0 {
					sign := "+"
					if jc.marketCtx.RelativeReturn < 0 {
						sign = ""
					}
					return fmt.Sprintf("涨跌幅 %+.2f%%，超额 %s%.2f%%",
						jc.marketCtx.StockReturn*100, sign, jc.marketCtx.RelativeReturn*100)
				}
				return "市场行情获取完成"
			},
		},
		{
			checkpoint: store.JobStepReportGenerate,
			label:      "生成日报",
			fn:         r.stepReportGenerate,
			after: func(ctx context.Context, job *store.JobRun, jc *jobContext) string {
				if dr, err := r.deps.ScoreStore.GetLatestDailyReport(ctx, job.ThesisID); err == nil && dr != nil {
					return fmt.Sprintf("日报《%s》已生成", dr.Title)
				}
				return "日报已生成"
			},
		},
	}

	for i, s := range steps {
		r.log.Debug("progress step starting", "job_id", job.ID, "step", s.checkpoint)
		emit("pending", s.label+" ...")

		if err := s.fn(ctx, job, jc); err != nil {
			errMsg := err.Error()
			if markErr := r.deps.JobStore.MarkJobRunFailed(ctx, job.ID, errMsg); markErr != nil {
				r.log.Error("failed to mark job as failed", "job_id", job.ID, "err", markErr)
			}
			return fmt.Errorf("step %s: %w", s.checkpoint, err)
		}

		detail := s.after(ctx, job, jc)
		r.log.Debug("progress step done", "job_id", job.ID, "step", s.checkpoint, "detail", detail)
		emit("ok", detail)

		if i+1 < len(steps) {
			next := steps[i+1].checkpoint
			if advErr := r.deps.JobStore.AdvanceJobStep(ctx, job.ID, next); advErr != nil {
				r.log.Warn("advance job step failed", "job_id", job.ID, "next", next, "err", advErr)
			}
		}
	}

	return r.deps.JobStore.MarkJobRunSucceeded(ctx, job.ID)
}

// ensureContext loads thesis and assumptions into jc if they are not already
// present. Call at the top of any step that needs them after a crash recovery.
func (r *Runner) ensureContext(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	if jc.thesis == nil {
		t, err := r.deps.ThesisStore.GetThesis(ctx, job.ThesisID)
		if err != nil {
			return fmt.Errorf("load thesis %d: %w", job.ThesisID, err)
		}
		if t == nil {
			return fmt.Errorf("thesis %d not found", job.ThesisID)
		}
		jc.thesis = t
	}
	if jc.assumptions == nil {
		assumptions, err := r.deps.ThesisStore.GetAssumptionsWithEmbeddings(ctx, job.ThesisID)
		if err != nil {
			return fmt.Errorf("load assumptions for thesis %d: %w", job.ThesisID, err)
		}
		jc.assumptions = assumptions
	}
	return nil
}
