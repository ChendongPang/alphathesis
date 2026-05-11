package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// CandidateStore handles relevant_candidates, candidate_assumptions, and candidate_chunks.
type CandidateStore struct {
	pool *pgxpool.Pool
}

func NewCandidateStore(db *DB) *CandidateStore {
	return &CandidateStore{pool: db.pool}
}

// CreateRelevantCandidateParams holds data for inserting a relevant candidate.
type CreateRelevantCandidateParams struct {
	Source                     string
	SourceID                   string
	SourceURL                  string
	Symbol                     string
	Title                      string
	Summary                    string
	NormalizedText             string
	LLMModel                   string
	PublishedAt                *time.Time
	MatchedAssumptionsSnapshot json.RawMessage
}

// CreateCandidateAssumptionParams holds data for one candidate↔assumption link.
type CreateCandidateAssumptionParams struct {
	CandidateID   int64
	AssumptionID  int64
	Relevance     float64
	Confidence    float64
	InitialImpact float64
	Reason        string
	LLMModel      string
}

// UpdateDedupStatusParams carries the dedup judge result for one candidate.
type UpdateDedupStatusParams struct {
	ID            int64
	DedupStatus   string
	DuplicateOfID *int64
	EventGroupID  *string
}

// CreateCandidateChunkParams holds data for one RAG chunk.
type CreateCandidateChunkParams struct {
	CandidateID int64
	ChunkIndex  int
	Text        string
	TokenCount  int
}

// ------------------------------------------------------------
// RelevantCandidate
// ------------------------------------------------------------

// CreateRelevantCandidate upserts a relevant candidate by (source, source_id).
// If the row already exists the normalized_text and llm_model fields are refreshed.
func (s *CandidateStore) CreateRelevantCandidate(ctx context.Context, p CreateRelevantCandidateParams) (*RelevantCandidate, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO relevant_candidates
		    (source, source_id, source_url, symbol, title, summary,
		     normalized_text, llm_model, published_at,
		     matched_assumptions_snapshot,
		     dedup_status, evidence_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (source, source_id) DO UPDATE
		    SET normalized_text             = EXCLUDED.normalized_text,
		        llm_model                   = EXCLUDED.llm_model,
		        matched_assumptions_snapshot = EXCLUDED.matched_assumptions_snapshot,
		        updated_at                  = now()
		RETURNING `+relevantCandidateColumns,
		p.Source, p.SourceID, p.SourceURL, p.Symbol, p.Title, p.Summary,
		p.NormalizedText, p.LLMModel, p.PublishedAt, p.MatchedAssumptionsSnapshot,
		DedupStatusUnknown, EvidenceStatusPending,
	)
	rc, err := scanRelevantCandidate(row)
	if err != nil {
		return nil, fmt.Errorf("create relevant candidate: %w", err)
	}
	return rc, nil
}

// GetRelevantCandidate returns a relevant candidate by ID.
func (s *CandidateStore) GetRelevantCandidate(ctx context.Context, id int64) (*RelevantCandidate, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+relevantCandidateColumns+` FROM relevant_candidates WHERE id = $1`, id)
	rc, err := scanRelevantCandidate(row)
	if err != nil {
		return nil, fmt.Errorf("get relevant candidate: %w", err)
	}
	return rc, nil
}

// ListRelevantCandidatesByDedupStatus returns candidates with the given dedup_status.
func (s *CandidateStore) ListRelevantCandidatesByDedupStatus(ctx context.Context, status string) ([]*RelevantCandidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+relevantCandidateColumns+` FROM relevant_candidates WHERE dedup_status = $1 ORDER BY id ASC`,
		status,
	)
	if err != nil {
		return nil, fmt.Errorf("list candidates by dedup status: %w", err)
	}
	defer rows.Close()
	return collectRelevantCandidateRows(rows)
}

// ListRelevantCandidatesByEvidenceStatus returns candidates for a specific thesis
// with the given evidence_status, joining through candidate_assumptions.
func (s *CandidateStore) ListRelevantCandidatesByEvidenceStatus(ctx context.Context, thesisID int64, status string) ([]*RelevantCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT `+prefixColumns("rc", relevantCandidateColumns)+`
		FROM relevant_candidates rc
		JOIN candidate_assumptions ca ON ca.candidate_id = rc.id
		JOIN assumptions a ON a.id = ca.assumption_id
		WHERE a.thesis_id = $1 AND rc.evidence_status = $2
		ORDER BY rc.id ASC`,
		thesisID, status,
	)
	if err != nil {
		return nil, fmt.Errorf("list candidates by evidence status: %w", err)
	}
	defer rows.Close()
	return collectRelevantCandidateRows(rows)
}

// ListCandidatesWithUnprocessedAssumptions returns all non-duplicate candidates
// linked to thesisID that have at least one assumption without an evidence
// snippet yet. This is used by the evidence judge step so that candidates
// processed for a previous thesis are still evaluated for any new thesis whose
// assumptions share the same underlying articles.
func (s *CandidateStore) ListCandidatesWithUnprocessedAssumptions(ctx context.Context, thesisID int64) ([]*RelevantCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT `+prefixColumns("rc", relevantCandidateColumns)+`
		FROM relevant_candidates rc
		JOIN candidate_assumptions ca ON ca.candidate_id = rc.id
		JOIN assumptions a ON a.id = ca.assumption_id
		LEFT JOIN evidence_snippets es
		       ON es.candidate_id = rc.id AND es.assumption_id = ca.assumption_id
		WHERE a.thesis_id = $1
		  AND rc.dedup_status != 'duplicate'
		  AND es.id IS NULL
		ORDER BY rc.id ASC`,
		thesisID,
	)
	if err != nil {
		return nil, fmt.Errorf("list candidates with unprocessed assumptions: %w", err)
	}
	defer rows.Close()
	return collectRelevantCandidateRows(rows)
}

// UpdateRelevantCandidateEmbedding sets the embedding for a candidate.
func (s *CandidateStore) UpdateRelevantCandidateEmbedding(ctx context.Context, id int64, embedding pgvector.Vector, model string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE relevant_candidates SET embedding = $1, embedding_model = $2, updated_at = now() WHERE id = $3`,
		embedding, model, id,
	)
	if err != nil {
		return fmt.Errorf("update candidate embedding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("relevant candidate %d not found", id)
	}
	return nil
}

// UpdateDedupStatus records the dedup judge result for a candidate.
func (s *CandidateStore) UpdateDedupStatus(ctx context.Context, p UpdateDedupStatusParams) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE relevant_candidates
		SET dedup_status    = $1,
		    duplicate_of_id = $2,
		    event_group_id  = $3,
		    updated_at      = now()
		WHERE id = $4`,
		p.DedupStatus, p.DuplicateOfID, p.EventGroupID, p.ID,
	)
	if err != nil {
		return fmt.Errorf("update dedup status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("relevant candidate %d not found", p.ID)
	}
	return nil
}

// UpdateEvidenceStatus sets evidence_status for a candidate.
func (s *CandidateStore) UpdateEvidenceStatus(ctx context.Context, id int64, status string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE relevant_candidates SET evidence_status = $1, updated_at = now() WHERE id = $2`,
		status, id,
	)
	if err != nil {
		return fmt.Errorf("update evidence status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("relevant candidate %d not found", id)
	}
	return nil
}

// ListRelevantCandidatesByThesisAndDedupStatus returns relevant candidates linked
// to a thesis (via candidate_assumptions → assumptions) that have the given
// dedup_status. Used by the dedup step to process only this job's candidates.
func (s *CandidateStore) ListRelevantCandidatesByThesisAndDedupStatus(ctx context.Context, thesisID int64, dedupStatus string) ([]*RelevantCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT `+prefixColumns("rc", relevantCandidateColumns)+`
		FROM relevant_candidates rc
		JOIN candidate_assumptions ca ON ca.candidate_id = rc.id
		JOIN assumptions a ON a.id = ca.assumption_id
		WHERE a.thesis_id = $1 AND rc.dedup_status = $2
		ORDER BY rc.id ASC`,
		thesisID, dedupStatus,
	)
	if err != nil {
		return nil, fmt.Errorf("list candidates by thesis and dedup status: %w", err)
	}
	defer rows.Close()
	return collectRelevantCandidateRows(rows)
}

// SearchSimilarCandidates finds the most embedding-similar candidates to the given
// vector. Used by DedupJudgeAgent to find historical candidates for comparison.
// Only candidates with a non-null embedding are considered.
func (s *CandidateStore) SearchSimilarCandidates(ctx context.Context, embedding pgvector.Vector, limit int) ([]*RelevantCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+relevantCandidateColumns+`
		FROM relevant_candidates
		WHERE embedding IS NOT NULL
		ORDER BY embedding <=> $1
		LIMIT $2`,
		embedding, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search similar candidates: %w", err)
	}
	defer rows.Close()
	return collectRelevantCandidateRows(rows)
}

// ------------------------------------------------------------
// CandidateAssumption
// ------------------------------------------------------------

// CreateCandidateAssumptions batch-inserts candidate↔assumption links.
// Duplicates (same candidate_id + assumption_id) are silently ignored.
func (s *CandidateStore) CreateCandidateAssumptions(ctx context.Context, params []CreateCandidateAssumptionParams) error {
	for _, p := range params {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO candidate_assumptions
			    (candidate_id, assumption_id, relevance, confidence,
			     initial_impact, reason, llm_model)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (candidate_id, assumption_id) DO NOTHING`,
			p.CandidateID, p.AssumptionID, p.Relevance, p.Confidence,
			p.InitialImpact, p.Reason, p.LLMModel,
		)
		if err != nil {
			return fmt.Errorf("insert candidate assumption (%d, %d): %w", p.CandidateID, p.AssumptionID, err)
		}
	}
	return nil
}

// ListCandidateAssumptionsByCandidate returns all assumption links for a candidate.
func (s *CandidateStore) ListCandidateAssumptionsByCandidate(ctx context.Context, candidateID int64) ([]*CandidateAssumption, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, candidate_id, assumption_id, relevance, confidence,
		       initial_impact, reason, llm_model, created_at
		FROM candidate_assumptions
		WHERE candidate_id = $1`,
		candidateID,
	)
	if err != nil {
		return nil, fmt.Errorf("list candidate assumptions: %w", err)
	}
	defer rows.Close()
	return collectCandidateAssumptionRows(rows)
}

// ListCandidateAssumptionsByAssumption returns all candidate links for an assumption.
func (s *CandidateStore) ListCandidateAssumptionsByAssumption(ctx context.Context, assumptionID int64) ([]*CandidateAssumption, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, candidate_id, assumption_id, relevance, confidence,
		       initial_impact, reason, llm_model, created_at
		FROM candidate_assumptions
		WHERE assumption_id = $1`,
		assumptionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list assumption candidates: %w", err)
	}
	defer rows.Close()
	return collectCandidateAssumptionRows(rows)
}

// ------------------------------------------------------------
// CandidateChunk
// ------------------------------------------------------------

// CreateCandidateChunks batch-inserts RAG chunks for a candidate.
// Duplicates (same candidate_id + chunk_index) are silently ignored.
func (s *CandidateStore) CreateCandidateChunks(ctx context.Context, params []CreateCandidateChunkParams) error {
	for _, p := range params {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO candidate_chunks
			    (candidate_id, chunk_index, text, token_count)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (candidate_id, chunk_index) DO NOTHING`,
			p.CandidateID, p.ChunkIndex, p.Text, p.TokenCount,
		)
		if err != nil {
			return fmt.Errorf("insert chunk %d for candidate %d: %w", p.ChunkIndex, p.CandidateID, err)
		}
	}
	return nil
}

// UpdateCandidateChunkEmbedding sets the embedding vector for one chunk.
func (s *CandidateStore) UpdateCandidateChunkEmbedding(ctx context.Context, id int64, embedding pgvector.Vector, model string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE candidate_chunks SET embedding = $1, embedding_model = $2 WHERE id = $3`,
		embedding, model, id,
	)
	if err != nil {
		return fmt.Errorf("update chunk embedding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("candidate chunk %d not found", id)
	}
	return nil
}

// ListChunksWithoutEmbedding returns chunks for a candidate that have no embedding yet.
func (s *CandidateStore) ListChunksWithoutEmbedding(ctx context.Context, candidateID int64) ([]*CandidateChunk, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, candidate_id, chunk_index, text, embedding_model, token_count, created_at
		FROM candidate_chunks
		WHERE candidate_id = $1 AND embedding IS NULL
		ORDER BY chunk_index ASC`,
		candidateID,
	)
	if err != nil {
		return nil, fmt.Errorf("list chunks without embedding: %w", err)
	}
	defer rows.Close()
	return collectCandidateChunkRows(rows)
}

// SearchSimilarChunks returns the top-k chunks for a candidate most similar to the
// given assumption embedding. Used by EvidenceJudgeAgent in RAG mode.
func (s *CandidateStore) SearchSimilarChunks(ctx context.Context, candidateID int64, assumptionEmbedding pgvector.Vector, limit int) ([]*CandidateChunk, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, candidate_id, chunk_index, text, embedding_model, token_count, created_at
		FROM candidate_chunks
		WHERE candidate_id = $1 AND embedding IS NOT NULL
		ORDER BY embedding <=> $2
		LIMIT $3`,
		candidateID, assumptionEmbedding, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search similar chunks: %w", err)
	}
	defer rows.Close()
	return collectCandidateChunkRows(rows)
}

// ------------------------------------------------------------
// Scan helpers
// ------------------------------------------------------------

// relevantCandidateColumns is the SELECT column list matching scanRelevantCandidate.
const relevantCandidateColumns = `id, source, source_id, source_url, symbol, title, summary,
	normalized_text, embedding, embedding_model,
	dedup_status, duplicate_of_id, event_group_id,
	matched_assumptions_snapshot, llm_model, published_at,
	evidence_status, created_at, updated_at`

// prefixColumns prepends a table alias to a column list for DISTINCT queries.
func prefixColumns(alias, columns string) string {
	// Simple approach: just return alias.* since we always select all columns.
	// This works because DISTINCT operates on the full row.
	return alias + ".*"
}

func scanRelevantCandidate(row pgx.Row) (*RelevantCandidate, error) {
	var rc RelevantCandidate
	err := row.Scan(
		&rc.ID, &rc.Source, &rc.SourceID, &rc.SourceURL, &rc.Symbol,
		&rc.Title, &rc.Summary, &rc.NormalizedText,
		&rc.Embedding, &rc.EmbeddingModel,
		&rc.DedupStatus, &rc.DuplicateOfID, &rc.EventGroupID,
		&rc.MatchedAssumptionsSnapshot, &rc.LLMModel, &rc.PublishedAt,
		&rc.EvidenceStatus, &rc.CreatedAt, &rc.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan relevant candidate: %w", err)
	}
	return &rc, nil
}

func scanRelevantCandidateRow(rows pgx.Rows) (*RelevantCandidate, error) {
	var rc RelevantCandidate
	err := rows.Scan(
		&rc.ID, &rc.Source, &rc.SourceID, &rc.SourceURL, &rc.Symbol,
		&rc.Title, &rc.Summary, &rc.NormalizedText,
		&rc.Embedding, &rc.EmbeddingModel,
		&rc.DedupStatus, &rc.DuplicateOfID, &rc.EventGroupID,
		&rc.MatchedAssumptionsSnapshot, &rc.LLMModel, &rc.PublishedAt,
		&rc.EvidenceStatus, &rc.CreatedAt, &rc.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan relevant candidate row: %w", err)
	}
	return &rc, nil
}

func collectRelevantCandidateRows(rows pgx.Rows) ([]*RelevantCandidate, error) {
	var candidates []*RelevantCandidate
	for rows.Next() {
		rc, err := scanRelevantCandidateRow(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, rc)
	}
	return candidates, rows.Err()
}

func scanCandidateAssumptionRow(rows pgx.Rows) (*CandidateAssumption, error) {
	var ca CandidateAssumption
	err := rows.Scan(
		&ca.ID, &ca.CandidateID, &ca.AssumptionID, &ca.Relevance, &ca.Confidence,
		&ca.InitialImpact, &ca.Reason, &ca.LLMModel, &ca.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan candidate assumption row: %w", err)
	}
	return &ca, nil
}

func collectCandidateAssumptionRows(rows pgx.Rows) ([]*CandidateAssumption, error) {
	var items []*CandidateAssumption
	for rows.Next() {
		ca, err := scanCandidateAssumptionRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, ca)
	}
	return items, rows.Err()
}

func scanCandidateChunkRow(rows pgx.Rows) (*CandidateChunk, error) {
	var cc CandidateChunk
	err := rows.Scan(
		&cc.ID, &cc.CandidateID, &cc.ChunkIndex, &cc.Text,
		&cc.EmbeddingModel, &cc.TokenCount, &cc.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan candidate chunk row: %w", err)
	}
	return &cc, nil
}

func collectCandidateChunkRows(rows pgx.Rows) ([]*CandidateChunk, error) {
	var chunks []*CandidateChunk
	for rows.Next() {
		cc, err := scanCandidateChunkRow(rows)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, cc)
	}
	return chunks, rows.Err()
}
