package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EvidenceStore handles evidence_snippets.
type EvidenceStore struct {
	pool *pgxpool.Pool
}

func NewEvidenceStore(db *DB) *EvidenceStore {
	return &EvidenceStore{pool: db.pool}
}

// CreateEvidenceSnippetParams holds the LLM judge output for one evidence snippet.
type CreateEvidenceSnippetParams struct {
	AssumptionID  int64
	CandidateID   int64
	SnippetText   string
	JudgeStage    string
	Relevance     float64
	Confidence    float64
	Stance        string
	Impact        float64
	SourceWeight  float64
	NoveltyWeight float64
	PublishedAt   *time.Time
	Reason        string
	LLMModel      string
}

// ------------------------------------------------------------
// EvidenceSnippet
// ------------------------------------------------------------

// CreateEvidenceSnippet upserts an evidence snippet by (assumption_id, candidate_id, judge_stage).
// If the same snippet already exists from a prior run it is overwritten with fresh values.
func (s *EvidenceStore) CreateEvidenceSnippet(ctx context.Context, p CreateEvidenceSnippetParams) (*EvidenceSnippet, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO evidence_snippets
		    (assumption_id, candidate_id, snippet_text, judge_stage,
		     relevance, confidence, stance, impact,
		     source_weight, novelty_weight, published_at, reason, llm_model)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (assumption_id, candidate_id, judge_stage) DO UPDATE
		    SET snippet_text   = EXCLUDED.snippet_text,
		        relevance      = EXCLUDED.relevance,
		        confidence     = EXCLUDED.confidence,
		        stance         = EXCLUDED.stance,
		        impact         = EXCLUDED.impact,
		        source_weight  = EXCLUDED.source_weight,
		        novelty_weight = EXCLUDED.novelty_weight,
		        published_at   = EXCLUDED.published_at,
		        reason         = EXCLUDED.reason,
		        llm_model      = EXCLUDED.llm_model
		RETURNING `+evidenceSnippetColumns,
		p.AssumptionID, p.CandidateID, p.SnippetText, p.JudgeStage,
		p.Relevance, p.Confidence, p.Stance, p.Impact,
		p.SourceWeight, p.NoveltyWeight, p.PublishedAt, p.Reason, p.LLMModel,
	)
	es, err := scanEvidenceSnippet(row)
	if err != nil {
		return nil, fmt.Errorf("create evidence snippet: %w", err)
	}
	return es, nil
}

// GetEvidenceSnippet returns one evidence snippet by ID.
func (s *EvidenceStore) GetEvidenceSnippet(ctx context.Context, id int64) (*EvidenceSnippet, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+evidenceSnippetColumns+` FROM evidence_snippets WHERE id = $1`, id)
	es, err := scanEvidenceSnippet(row)
	if err != nil {
		return nil, fmt.Errorf("get evidence snippet: %w", err)
	}
	return es, nil
}

// ListEvidenceSnippetsByAssumptionAndDate returns all snippets for an assumption
// created within a given calendar day (UTC). Used by ScoreEngine to compute
// daily_effect from today's evidence.
func (s *EvidenceStore) ListEvidenceSnippetsByAssumptionAndDate(ctx context.Context, assumptionID int64, runDate time.Time) ([]*EvidenceSnippet, error) {
	dayStart := truncateToDay(runDate)
	dayEnd := dayStart.Add(24 * time.Hour)

	rows, err := s.pool.Query(ctx, `
		SELECT `+evidenceSnippetColumns+`
		FROM evidence_snippets
		WHERE assumption_id = $1
		  AND created_at >= $2
		  AND created_at <  $3
		ORDER BY id ASC`,
		assumptionID, dayStart, dayEnd,
	)
	if err != nil {
		return nil, fmt.Errorf("list snippets by assumption and date: %w", err)
	}
	defer rows.Close()
	return collectEvidenceSnippetRows(rows)
}

// ListEvidenceSnippetsByCandidate returns all snippets produced for a candidate.
// Used when building the daily report to explain which evidence drove score changes.
func (s *EvidenceStore) ListEvidenceSnippetsByCandidate(ctx context.Context, candidateID int64) ([]*EvidenceSnippet, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+evidenceSnippetColumns+`
		FROM evidence_snippets
		WHERE candidate_id = $1
		ORDER BY assumption_id, judge_stage`,
		candidateID,
	)
	if err != nil {
		return nil, fmt.Errorf("list snippets by candidate: %w", err)
	}
	defer rows.Close()
	return collectEvidenceSnippetRows(rows)
}

// ListTopEvidenceSnippetsByAssumption returns the most impactful snippets for an
// assumption ordered by |impact| × confidence descending. Used in daily reports.
func (s *EvidenceStore) ListTopEvidenceSnippetsByAssumption(ctx context.Context, assumptionID int64, limit int) ([]*EvidenceSnippet, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+evidenceSnippetColumnsWithSource+`
		FROM evidence_snippets es
		JOIN relevant_candidates rc ON rc.id = es.candidate_id
		WHERE es.assumption_id = $1
		ORDER BY ABS(es.impact) * es.confidence DESC
		LIMIT $2`,
		assumptionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list top evidence snippets: %w", err)
	}
	defer rows.Close()
	return collectEvidenceSnippetRows(rows)
}

// ------------------------------------------------------------
// Scan helpers
// ------------------------------------------------------------

// evidenceSnippetColumns is used for plain queries without a JOIN.
// CandidateSource, CandidateURL, and CandidateTitle are empty strings.
const evidenceSnippetColumns = `id, assumption_id, candidate_id,
	'' AS candidate_source, '' AS candidate_url, '' AS candidate_title,
	snippet_text, judge_stage,
	relevance, confidence, stance, impact,
	source_weight, novelty_weight, published_at, reason, llm_model, created_at`

// evidenceSnippetColumnsWithSource is used for queries that JOIN relevant_candidates.
const evidenceSnippetColumnsWithSource = `es.id, es.assumption_id, es.candidate_id,
	rc.source AS candidate_source, rc.source_url AS candidate_url, rc.title AS candidate_title,
	es.snippet_text, es.judge_stage,
	es.relevance, es.confidence, es.stance, es.impact,
	es.source_weight, es.novelty_weight, es.published_at, es.reason, es.llm_model, es.created_at`

// evidenceSnippetColumnsUnqualified is used in the outer SELECT of DISTINCT ON subqueries,
// where table aliases from the inner query are no longer in scope.
const evidenceSnippetColumnsUnqualified = `id, assumption_id, candidate_id,
	candidate_source, candidate_url, candidate_title,
	snippet_text, judge_stage,
	relevance, confidence, stance, impact,
	source_weight, novelty_weight, published_at, reason, llm_model, created_at`

func scanEvidenceSnippet(row pgx.Row) (*EvidenceSnippet, error) {
	var es EvidenceSnippet
	err := row.Scan(
		&es.ID, &es.AssumptionID, &es.CandidateID,
		&es.CandidateSource, &es.CandidateURL, &es.CandidateTitle,
		&es.SnippetText, &es.JudgeStage,
		&es.Relevance, &es.Confidence, &es.Stance, &es.Impact,
		&es.SourceWeight, &es.NoveltyWeight, &es.PublishedAt, &es.Reason, &es.LLMModel, &es.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan evidence snippet: %w", err)
	}
	return &es, nil
}

func scanEvidenceSnippetRow(rows pgx.Rows) (*EvidenceSnippet, error) {
	var es EvidenceSnippet
	err := rows.Scan(
		&es.ID, &es.AssumptionID, &es.CandidateID,
		&es.CandidateSource, &es.CandidateURL, &es.CandidateTitle,
		&es.SnippetText, &es.JudgeStage,
		&es.Relevance, &es.Confidence, &es.Stance, &es.Impact,
		&es.SourceWeight, &es.NoveltyWeight, &es.PublishedAt, &es.Reason, &es.LLMModel, &es.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan evidence snippet row: %w", err)
	}
	return &es, nil
}

// ListEvidenceSnippetsByThesis returns evidence snippets for all assumptions in a
// thesis. One snippet per candidate (the highest-impact one), ordered by
// creation time desc. Used for the evidence feed in the UI.
func (s *EvidenceStore) ListEvidenceSnippetsByThesis(ctx context.Context, thesisID int64, limit int) ([]*EvidenceSnippet, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+evidenceSnippetColumnsUnqualified+`
		FROM (
			SELECT DISTINCT ON (es.candidate_id) `+evidenceSnippetColumnsWithSource+`
			FROM evidence_snippets es
			JOIN relevant_candidates rc ON rc.id = es.candidate_id
			JOIN assumptions a ON a.id = es.assumption_id
			WHERE a.thesis_id = $1
			ORDER BY es.candidate_id, ABS(es.impact) * es.confidence DESC
		) sub
		ORDER BY created_at DESC
		LIMIT $2`,
		thesisID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list evidence snippets by thesis: %w", err)
	}
	defer rows.Close()
	return collectEvidenceSnippetRows(rows)
}

// ListEvidenceSnippetsByThesisAndDate returns evidence snippets for a thesis
// from a specific calendar day. One snippet per candidate (highest impact),
// ordered by |impact|*confidence desc. Used for report detail pages.
func (s *EvidenceStore) ListEvidenceSnippetsByThesisAndDate(ctx context.Context, thesisID int64, runDate time.Time, limit int) ([]*EvidenceSnippet, error) {
	dayStart := truncateToDay(runDate)
	dayEnd := dayStart.Add(24 * time.Hour)
	rows, err := s.pool.Query(ctx, `
		SELECT `+evidenceSnippetColumnsUnqualified+`
		FROM (
			SELECT DISTINCT ON (es.candidate_id) `+evidenceSnippetColumnsWithSource+`
			FROM evidence_snippets es
			JOIN relevant_candidates rc ON rc.id = es.candidate_id
			JOIN assumptions a ON a.id = es.assumption_id
			WHERE a.thesis_id = $1
			  AND es.created_at >= $2
			  AND es.created_at <  $3
			ORDER BY es.candidate_id, ABS(es.impact) * es.confidence DESC
		) sub
		ORDER BY ABS(impact) * confidence DESC
		LIMIT $4`,
		thesisID, dayStart, dayEnd, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list evidence snippets by thesis and date: %w", err)
	}
	defer rows.Close()
	return collectEvidenceSnippetRows(rows)
}

// CountEvidenceSnippetsByThesisAndDate returns the number of evidence snippets
// for a thesis on a specific calendar day.
func (s *EvidenceStore) CountEvidenceSnippetsByThesisAndDate(ctx context.Context, thesisID int64, runDate time.Time) (int, error) {
	dayStart := truncateToDay(runDate)
	dayEnd := dayStart.Add(24 * time.Hour)
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM evidence_snippets es
		JOIN assumptions a ON a.id = es.assumption_id
		WHERE a.thesis_id = $1
		  AND es.created_at >= $2
		  AND es.created_at <  $3`,
		thesisID, dayStart, dayEnd,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count evidence snippets by thesis and date: %w", err)
	}
	return count, nil
}

func collectEvidenceSnippetRows(rows pgx.Rows) ([]*EvidenceSnippet, error) {
	var snippets []*EvidenceSnippet
	for rows.Next() {
		es, err := scanEvidenceSnippetRow(rows)
		if err != nil {
			return nil, err
		}
		snippets = append(snippets, es)
	}
	return snippets, rows.Err()
}

func truncateToDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
