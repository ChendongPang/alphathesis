package runner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pgvector/pgvector-go"
	"alphathesis/agent/dedup"
	"alphathesis/agent/evidence"
	"alphathesis/agent/relevance"
	reportagent "alphathesis/agent/report"
	"alphathesis/client"
	"alphathesis/datasource"
	eng "alphathesis/engine"
	"alphathesis/engine/rag"
	"alphathesis/store"
)

// ------------------------------------------------------------
// Step 1: init
// ------------------------------------------------------------

// stepInit embeds any assumption that doesn't yet have a vector for the
// current embed model, then reloads assumptions so subsequent steps have
// fresh embeddings in jc.
func (r *Runner) stepInit(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	t, err := r.deps.ThesisStore.GetThesis(ctx, job.ThesisID)
	if err != nil {
		return fmt.Errorf("get thesis: %w", err)
	}
	if t == nil {
		return fmt.Errorf("thesis %d not found", job.ThesisID)
	}
	jc.thesis = t

	assumptions, err := r.deps.ThesisStore.GetAssumptionsWithEmbeddings(ctx, job.ThesisID)
	if err != nil {
		return fmt.Errorf("get assumptions: %w", err)
	}

	if err := r.deps.AssumptionEmbedder.EmbedAssumptions(ctx, assumptions); err != nil {
		// Non-fatal: log and continue. Missing embeddings → summary-only mode.
		r.log.Warn("assumption embedding failed, will use summary mode", "err", err)
	}

	// Reload to pick up newly stored embeddings.
	jc.assumptions, err = r.deps.ThesisStore.GetAssumptionsWithEmbeddings(ctx, job.ThesisID)
	if err != nil {
		return fmt.Errorf("reload assumptions: %w", err)
	}
	return nil
}

// ------------------------------------------------------------
// Step 2: fetch_candidates
// ------------------------------------------------------------

// stepFetchCandidates calls every registered CandidateFetcher and batch-inserts
// results into job_candidates. Failures from individual fetchers are logged but
// do not abort the step — partial data is better than no data.
func (r *Runner) stepFetchCandidates(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	if err := r.ensureContext(ctx, job, jc); err != nil {
		return err
	}

	since := job.RunDate.Add(-r.cfg.FetchLookback)

	thesisMarket := jc.thesis.Market
	// HK stocks use the same data sources as CN (AKShare news, CNInfo events).
	fetchMarket := thesisMarket
	if fetchMarket == "hk" {
		fetchMarket = "cn"
	}
	for _, mf := range r.deps.Fetchers {
		if mf.Market != "" && mf.Market != fetchMarket {
			continue
		}
		limit := r.cfg.NewsLimit
		if mf.Type == "event" {
			limit = r.cfg.EventLimit
		}
		fetchInput := datasource.CandidateFetchInput{
			Symbol: jc.thesis.Symbol,
			Since:  &since,
			Limit:  limit,
		}
		candidates, err := mf.Fetcher.FetchCandidates(ctx, fetchInput)
		if err != nil {
			r.log.Warn("fetcher error", "err", err)
			continue
		}
		if len(candidates) == 0 {
			continue
		}
		if err := r.deps.JobStore.CreateJobCandidates(ctx, job.ID, candidates); err != nil {
			r.log.Warn("insert job candidates failed", "err", err)
		}
	}
	return nil
}

// ------------------------------------------------------------
// Step 3: relevance_judge
// ------------------------------------------------------------

// stepRelevanceJudge runs the RelevanceJudge on every pending job_candidate.
// Relevant ones are promoted to relevant_candidates with their matched assumptions.
func (r *Runner) stepRelevanceJudge(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	if err := r.ensureContext(ctx, job, jc); err != nil {
		return err
	}

	pending, err := r.deps.JobStore.ListJobCandidatesByStatus(ctx, job.ID, store.RelevanceStatusPending)
	if err != nil {
		return fmt.Errorf("list pending candidates: %w", err)
	}

	assumptionHints := make([]relevance.AssumptionHint, len(jc.assumptions))
	for i, a := range jc.assumptions {
		assumptionHints[i] = relevance.AssumptionHint{
			ID:            a.ID,
			Key:           a.AssumptionKey,
			Text:          a.Text,
			EvidenceHints: a.EvidenceHints,
		}
	}

	for _, c := range pending {
		judgment, err := r.deps.RelevanceJudge.Judge(ctx, relevance.Input{
			Title:       c.Title,
			Summary:     c.Summary,
			CoreClaim:   jc.thesis.CoreClaim,
			Assumptions: assumptionHints,
		})
		if err != nil {
			r.log.Warn("relevance judge error", "candidate_id", c.ID, "err", err)
			_ = r.deps.JobStore.UpdateJobCandidateStatus(ctx, c.ID, store.RelevanceStatusError)
			continue
		}

		if !judgment.Relevant {
			_ = r.deps.JobStore.UpdateJobCandidateStatus(ctx, c.ID, store.RelevanceStatusIrrelevant)
			continue
		}

		snapshot, _ := json.Marshal(judgment.MatchedAssumptions)
		rc, err := r.deps.CandidateStore.CreateRelevantCandidate(ctx, store.CreateRelevantCandidateParams{
			Source:                     c.Source,
			SourceID:                   c.SourceID,
			SourceURL:                  c.SourceURL,
			Symbol:                     c.Symbol,
			Title:                      c.Title,
			Summary:                    c.Summary,
			NormalizedText:             c.Title + "\n" + c.Summary,
			LLMModel:                   r.cfg.LLMModel,
			PublishedAt:                c.PublishedAt,
			MatchedAssumptionsSnapshot: snapshot,
		})
		if err != nil {
			r.log.Warn("create relevant candidate failed", "candidate_id", c.ID, "err", err)
			_ = r.deps.JobStore.UpdateJobCandidateStatus(ctx, c.ID, store.RelevanceStatusError)
			continue
		}

		caParams := make([]store.CreateCandidateAssumptionParams, 0, len(judgment.MatchedAssumptions))
		for _, ma := range judgment.MatchedAssumptions {
			caParams = append(caParams, store.CreateCandidateAssumptionParams{
				CandidateID:   rc.ID,
				AssumptionID:  ma.AssumptionID,
				Relevance:     ma.Relevance,
				Confidence:    ma.Confidence,
				InitialImpact: ma.InitialImpact,
				Reason:        ma.Reason,
				LLMModel:      r.cfg.LLMModel,
			})
		}
		if err := r.deps.CandidateStore.CreateCandidateAssumptions(ctx, caParams); err != nil {
			r.log.Warn("create candidate assumptions failed", "candidate_id", c.ID, "err", err)
		}

		_ = r.deps.JobStore.UpdateJobCandidateStatus(ctx, c.ID, store.RelevanceStatusRelevant)
	}
	return nil
}

// ------------------------------------------------------------
// Step 4: dedup_judge
// ------------------------------------------------------------

// stepDedupJudge embeds each relevant candidate that hasn't been deduped yet,
// finds historically similar candidates via vector search, and classifies the
// new candidate as duplicate / event_update / new_event.
func (r *Runner) stepDedupJudge(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	candidates, err := r.deps.CandidateStore.ListRelevantCandidatesByThesisAndDedupStatus(
		ctx, job.ThesisID, store.DedupStatusUnknown,
	)
	if err != nil {
		return fmt.Errorf("list unknown-dedup candidates: %w", err)
	}

	for _, c := range candidates {
		resp, err := r.deps.Embedder.CreateEmbedding(ctx, client.EmbeddingRequest{
			Model: r.deps.EmbedModel,
			Input: []string{c.NormalizedText},
		})
		if err != nil || len(resp.Data) == 0 {
			r.log.Warn("embed candidate for dedup failed", "id", c.ID, "err", err)
			continue
		}

		vec := pgvector.NewVector(toFloat32Slice(resp.Data[0].Embedding))
		if err := r.deps.CandidateStore.UpdateRelevantCandidateEmbedding(ctx, c.ID, vec, r.deps.EmbedModel); err != nil {
			r.log.Warn("store candidate embedding failed", "id", c.ID, "err", err)
		}

		similar, err := r.deps.CandidateStore.SearchSimilarCandidates(ctx, vec, r.cfg.DedupSearchLimit)
		if err != nil {
			r.log.Warn("search similar candidates failed", "id", c.ID, "err", err)
			continue
		}

		dedupInput := dedup.Input{
			Candidate: dedup.CandidateInfo{
				Title:          c.Title,
				Summary:        c.Summary,
				NormalizedText: c.NormalizedText,
				PublishedAt:    c.PublishedAt,
			},
		}
		for _, s := range similar {
			if s.ID == c.ID {
				continue // skip self
			}
			dedupInput.SimilarCandidates = append(dedupInput.SimilarCandidates, dedup.SimilarCandidate{
				ID:          s.ID,
				Title:       s.Title,
				Summary:     s.Summary,
				PublishedAt: s.PublishedAt,
			})
		}
		judgment, err := r.deps.DedupJudge.Judge(ctx, dedupInput)
		if err != nil {
			r.log.Warn("dedup judge error", "id", c.ID, "err", err)
			continue
		}

		if err := r.deps.CandidateStore.UpdateDedupStatus(ctx, store.UpdateDedupStatusParams{
			ID:            c.ID,
			DedupStatus:   judgment.Status,
			DuplicateOfID: judgment.RelatedID,
		}); err != nil {
			r.log.Warn("update dedup status failed", "id", c.ID, "err", err)
		}
	}
	return nil
}

// ------------------------------------------------------------
// Step 5: evidence_judge
// ------------------------------------------------------------

// stepEvidenceJudge processes each relevant, non-duplicate candidate:
//  1. Optionally fetches the full article text and runs RAG to find top chunks.
//  2. Calls EvidenceJudge (RAG mode if chunks found, summary mode otherwise).
//  3. Persists evidence snippets and marks the candidate as done.
func (r *Runner) stepEvidenceJudge(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	if err := r.ensureContext(ctx, job, jc); err != nil {
		return err
	}

	// Build the assumption input map for RAG (only assumptions that have embeddings).
	ragInputByID := make(map[int64]rag.AssumptionInput, len(jc.assumptions))
	for _, a := range jc.assumptions {
		if a.Embedding != nil {
			ragInputByID[a.ID] = rag.AssumptionInput{
				ID:        a.ID,
				Embedding: float32ToFloat64(a.Embedding.Slice()),
			}
		}
	}

	assumptionByID := make(map[int64]*store.Assumption, len(jc.assumptions))
	for _, a := range jc.assumptions {
		assumptionByID[a.ID] = a
	}

	// Use assumption-level tracking instead of the global evidence_status flag so
	// that candidates shared across theses are still evaluated for each thesis's
	// own assumptions even if they were already processed for a prior thesis.
	candidates, err := r.deps.CandidateStore.ListCandidatesWithUnprocessedAssumptions(
		ctx, job.ThesisID,
	)
	if err != nil {
		return fmt.Errorf("list candidates with unprocessed assumptions: %w", err)
	}

	for _, c := range candidates {
		// Determine which assumptions matched this candidate.
		caLinks, err := r.deps.CandidateStore.ListCandidateAssumptionsByCandidate(ctx, c.ID)
		if err != nil {
			r.log.Warn("list candidate assumptions failed", "id", c.ID, "err", err)
			_ = r.deps.CandidateStore.UpdateEvidenceStatus(ctx, c.ID, store.EvidenceStatusError)
			continue
		}

		// Build per-assumption RAG inputs for this candidate's matched assumptions.
		// Only include assumptions that belong to the current thesis AND have no
		// evidence snippet yet (the query already filters at the candidate level,
		// but a single candidate may have some assumptions processed and others not).
		var matchedAssumptions []*store.Assumption
		var matchedRAGInputs []rag.AssumptionInput
		for _, ca := range caLinks {
			if a, ok := assumptionByID[ca.AssumptionID]; ok {
				matchedAssumptions = append(matchedAssumptions, a)
				if ri, ok := ragInputByID[ca.AssumptionID]; ok {
					matchedRAGInputs = append(matchedRAGInputs, ri)
				}
			}
		}

		// RAG: fetch full text and recall top chunks per assumption.
		topChunks := make(map[int64][]string)
		if r.deps.TextFetcher != nil && len(matchedRAGInputs) > 0 && c.SourceURL != "" {
			if fullText, err := r.deps.TextFetcher.FetchText(ctx, c.SourceURL); err == nil && fullText != "" {
				if chunks, err := r.deps.RAGEngine.Recall(ctx, fullText, matchedRAGInputs, r.cfg.RAGTopK); err == nil {
					topChunks = chunks
				} else {
					r.log.Warn("rag recall failed, falling back to summary", "id", c.ID, "err", err)
				}
			}
		}

		// Build evidence judge input.
		targets := make([]evidence.AssumptionTarget, len(matchedAssumptions))
		for i, a := range matchedAssumptions {
			targets[i] = evidence.AssumptionTarget{
				ID:            a.ID,
				Key:           a.AssumptionKey,
				Text:          a.Text,
				EvidenceHints: a.EvidenceHints,
				TopChunks:     topChunks[a.ID],
			}
		}

		judgment, err := r.deps.EvidenceJudge.Judge(ctx, evidence.Input{
			Title:       c.Title,
			Summary:     c.Summary,
			PublishedAt: c.PublishedAt,
			Source:      c.Source,
			Assumptions: targets,
		})
		if err != nil {
			r.log.Warn("evidence judge error", "id", c.ID, "err", err)
			_ = r.deps.CandidateStore.UpdateEvidenceStatus(ctx, c.ID, store.EvidenceStatusError)
			continue
		}

		sw := sourceWeight(c.Source)
		nw := noveltyWeight(c.DedupStatus)
		for _, ev := range judgment.Evidences {
			if _, err := r.deps.EvidenceStore.CreateEvidenceSnippet(ctx, store.CreateEvidenceSnippetParams{
				AssumptionID:  ev.AssumptionID,
				CandidateID:   c.ID,
				SnippetText:   ev.SnippetText,
				JudgeStage:    ev.JudgeStage,
				Relevance:     ev.Relevance,
				Confidence:    ev.Confidence,
				Stance:        ev.Stance,
				Impact:        ev.Impact,
				SourceWeight:  sw,
				NoveltyWeight: nw,
				PublishedAt:   c.PublishedAt,
				Reason:        ev.Reason,
				LLMModel:      r.cfg.LLMModel,
			}); err != nil {
				r.log.Warn("create evidence snippet failed", "id", c.ID, "assumption", ev.AssumptionKey, "err", err)
			}
		}

		_ = r.deps.CandidateStore.UpdateEvidenceStatus(ctx, c.ID, store.EvidenceStatusDone)
	}
	return nil
}

// ------------------------------------------------------------
// Step 6: score_update
// ------------------------------------------------------------

// stepScoreUpdate aggregates today's evidence snippets into assumption and
// thesis scores, then atomically writes the results to the DB.
func (r *Runner) stepScoreUpdate(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	if err := r.ensureContext(ctx, job, jc); err != nil {
		return err
	}

	assumptionInputs := make([]eng.AssumptionInput, 0, len(jc.assumptions))
	for _, a := range jc.assumptions {
		snippets, err := r.deps.EvidenceStore.ListEvidenceSnippetsByAssumptionAndDate(
			ctx, a.ID, job.RunDate,
		)
		if err != nil {
			r.log.Warn("list evidence snippets failed", "assumption", a.ID, "err", err)
			continue
		}

		snippetInputs := make([]eng.SnippetInput, len(snippets))
		for i, s := range snippets {
			snippetInputs[i] = eng.SnippetInput{
				ID:            s.ID,
				Relevance:     s.Relevance,
				Confidence:    s.Confidence,
				Impact:        s.Impact,
				SourceWeight:  s.SourceWeight,
				NoveltyWeight: s.NoveltyWeight,
			}
		}
		assumptionInputs = append(assumptionInputs, eng.AssumptionInput{
			AssumptionID: a.ID,
			CurrentScore: a.CurrentScore,
			Importance:   a.Importance,
			Snippets:     snippetInputs,
		})
	}

	aOutputs, tOutput := r.deps.ScoreEngine.Compute(assumptionInputs)

	// Get thesis score-before from the thesis itself (confidence_score).
	scoreBefore := jc.thesis.ConfidenceScore

	aResults := make([]store.AssumptionScoreResult, len(aOutputs))
	for i, out := range aOutputs {
		aResults[i] = store.AssumptionScoreResult{
			AssumptionID:          out.AssumptionID,
			ScoreBefore:           out.ScoreBefore,
			ScoreAfter:            out.ScoreAfter,
			DailyEffect:           out.DailyEffect,
			PositiveEvidenceCount: out.PositiveEvidenceCount,
			NegativeEvidenceCount: out.NegativeEvidenceCount,
			NeutralEvidenceCount:  out.NeutralEvidenceCount,
			TopEvidenceSnippetIDs: out.TopEvidenceSnippetIDs,
			Reason:                "daily evidence",
		}
	}

	return r.deps.ScoreStore.SaveScoreResults(ctx, store.SaveScoreResultsParams{
		ThesisID:    job.ThesisID,
		RunDate:     job.RunDate,
		Assumptions: aResults,
		Thesis: store.ThesisScoreResult{
			ScoreBefore:           scoreBefore,
			ScoreAfter:            tOutput.ScoreAfter,
			AssumptionCount:       tOutput.AssumptionCount,
			StrongestAssumptionID: tOutput.StrongestAssumptionID,
			WeakestAssumptionID:   tOutput.WeakestAssumptionID,
			ChangedAssumptionIDs:  tOutput.ChangedAssumptionIDs,
			Reason:                "daily evidence",
		},
	})
}

// ------------------------------------------------------------
// Step 7: market_context
// ------------------------------------------------------------

// stepMarketContext fetches today's price quotes for the stock, the broad
// market ETF, and (if mapped) the sector ETF, then computes relative returns.
// Price fetch errors are non-fatal — zero prices produce alert_level="none".
func (r *Runner) stepMarketContext(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	if err := r.ensureContext(ctx, job, jc); err != nil {
		return err
	}

	symbol := jc.thesis.Symbol
	marketETF := r.deps.MarketEngine.MarketETFForMarket(jc.thesis.Market)
	sectorETF := r.deps.MarketEngine.SectorETFForSymbol(symbol)

	fetch := func(sym string) eng.PriceQuote {
		if sym == "" {
			return eng.PriceQuote{}
		}
		q, err := r.deps.PriceQuotes.FetchQuote(ctx, sym)
		if err != nil {
			r.log.Warn("price fetch failed", "symbol", sym, "err", err)
			return eng.PriceQuote{Symbol: sym}
		}
		return q
	}

	jc.marketCtx = r.deps.MarketEngine.Compute(eng.ComputeInput{
		Stock:  fetch(symbol),
		Market: fetch(marketETF),
		Sector: fetch(sectorETF),
	})
	return nil
}

// ------------------------------------------------------------
// Step 8: report_generate
// ------------------------------------------------------------

// stepReportGenerate collects score results and evidence snippets for today,
// calls the ReportAgent to produce a markdown report, and persists it.
func (r *Runner) stepReportGenerate(ctx context.Context, job *store.JobRun, jc *jobContext) error {
	if err := r.ensureContext(ctx, job, jc); err != nil {
		return err
	}

	thesisHistory, err := r.deps.ScoreStore.GetThesisScoreHistory(ctx, job.ThesisID, job.RunDate)
	if err != nil || thesisHistory == nil {
		r.log.Warn("thesis score history not found, using defaults", "err", err)
		thesisHistory = &store.ThesisScoreHistory{
			ScoreBefore: jc.thesis.ConfidenceScore,
			ScoreAfter:  jc.thesis.ConfidenceScore,
		}
	}

	aHistories, err := r.deps.ScoreStore.ListAssumptionScoreHistoriesByThesis(ctx, job.ThesisID, job.RunDate)
	if err != nil {
		r.log.Warn("assumption score histories failed", "err", err)
	}

	aHistByID := make(map[int64]*store.AssumptionScoreHistory, len(aHistories))
	for _, h := range aHistories {
		aHistByID[h.AssumptionID] = h
	}

	results := make([]reportagent.AssumptionResult, 0, len(jc.assumptions))
	for _, a := range jc.assumptions {
		h := aHistByID[a.ID]
		if h == nil {
			continue
		}
		snippets, _ := r.deps.EvidenceStore.ListTopEvidenceSnippetsByAssumption(
			ctx, a.ID, r.cfg.TopSnippetsPerAssumption,
		)
		results = append(results, reportagent.AssumptionResult{
			Key:          a.AssumptionKey,
			Text:         a.Text,
			ScoreBefore:  h.ScoreBefore,
			ScoreAfter:   h.ScoreAfter,
			DailyEffect:  h.DailyEffect,
			PosCount:     h.PositiveEvidenceCount,
			NegCount:     h.NegativeEvidenceCount,
			NeutralCount: h.NeutralEvidenceCount,
			TopSnippets:  snippets,
		})
	}

	out, err := r.deps.Reporter.Generate(ctx, reportagent.Input{
		Thesis:            jc.thesis,
		RunDate:           job.RunDate,
		ThesisScoreBefore: thesisHistory.ScoreBefore,
		ThesisScoreAfter:  thesisHistory.ScoreAfter,
		Results:           results,
		MarketCtx:         jc.marketCtx,
	})
	if err != nil {
		return fmt.Errorf("report generate: %w", err)
	}

	marketJSON, _ := json.Marshal(jc.marketCtx)
	_, err = r.deps.ScoreStore.CreateDailyReport(ctx, store.CreateDailyReportParams{
		UserID:            jc.thesis.UserID,
		ThesisID:          job.ThesisID,
		RunDate:           job.RunDate,
		Title:             out.Title,
		ThesisScoreBefore: thesisHistory.ScoreBefore,
		ThesisScoreAfter:  thesisHistory.ScoreAfter,
		Summary:           out.Summary,
		MarkdownReport:    out.MarkdownReport,
		MarketContext:     marketJSON,
		AlertLevel:        out.AlertLevel,
	})
	return err
}

// ------------------------------------------------------------
// Helpers
// ------------------------------------------------------------

func sourceWeight(source string) float64 {
	switch source {
	case datasource.SourceUSOfficialEventSEC, datasource.SourceCNOfficialEventCNInfo:
		return 1.0
	case datasource.SourceManual:
		return 0.8
	default: // news sources
		return 0.6
	}
}

func noveltyWeight(dedupStatus string) float64 {
	switch dedupStatus {
	case store.DedupStatusNewEvent:
		return 1.0
	case store.DedupStatusEventUpdate:
		return 0.5
	default:
		return 0.25 // unknown or duplicate treated conservatively
	}
}

func toFloat32Slice(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x)
	}
	return out
}

func float32ToFloat64(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}
