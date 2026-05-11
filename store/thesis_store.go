package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// ThesisStore handles users, theses, and assumptions.
type ThesisStore struct {
	pool *pgxpool.Pool
}

func NewThesisStore(db *DB) *ThesisStore {
	return &ThesisStore{pool: db.pool}
}

// CreateThesisParams holds inputs for creating a new thesis.
type CreateThesisParams struct {
	UserID        int64
	Symbol        string
	CompanyName   string
	Market        string
	Direction     string
	RawText       string
	CoreClaim     string
	LLMModel      string
	ParserVersion string
	Assumptions   []AssumptionParams
}

// AssumptionParams holds inputs for one assumption.
type AssumptionParams struct {
	AssumptionKey string
	Text          string
	Type          string
	Verifiable    bool
	Importance    float64
	EvidenceHints []string
}

// ------------------------------------------------------------
// User
// ------------------------------------------------------------

// GetOrCreateUser upserts a user by email and returns it.
func (s *ThesisStore) GetOrCreateUser(ctx context.Context, email, name string) (*User, error) {
	if email == "" {
		return nil, errors.New("email is required")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO users (email, name)
		VALUES ($1, $2)
		ON CONFLICT (email) DO UPDATE
		    SET name = EXCLUDED.name, updated_at = now()
		RETURNING id, email, name, password, created_at, updated_at`,
		email, name,
	)
	return scanUser(row)
}

// GetUser returns a user by ID.
func (s *ThesisStore) GetUser(ctx context.Context, id int64) (*User, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, email, name, password, created_at, updated_at FROM users WHERE id = $1`, id)
	return scanUser(row)
}

// GetUserByEmail returns a user by email, including password for auth.
func (s *ThesisStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, email, name, password, created_at, updated_at FROM users WHERE email = $1`, email)
	return scanUser(row)
}

// CreateUser inserts a new user with a plain-text password.
func (s *ThesisStore) CreateUser(ctx context.Context, email, name, password string) (*User, error) {
	if email == "" {
		return nil, errors.New("email is required")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO users (email, name, password)
		VALUES ($1, $2, $3)
		RETURNING id, email, name, password, created_at, updated_at`,
		email, name, password,
	)
	return scanUser(row)
}

// ------------------------------------------------------------
// Thesis
// ------------------------------------------------------------

// CreateThesis inserts a thesis and its assumptions in a single transaction.
// The new thesis starts at version=1 with status=active and score=0.5.
func (s *ThesisStore) CreateThesis(ctx context.Context, p CreateThesisParams) (*Thesis, error) {
	if p.UserID == 0 {
		return nil, errors.New("user_id is required")
	}
	if len(p.Assumptions) == 0 {
		return nil, errors.New("at least one assumption is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	thesis, err := insertThesis(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	if err := insertAssumptions(ctx, tx, thesis.ID, p.Assumptions); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create thesis: %w", err)
	}
	return thesis, nil
}

// GetThesis returns a thesis by ID regardless of deleted status.
func (s *ThesisStore) GetThesis(ctx context.Context, id int64) (*Thesis, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, symbol, company_name, market, direction, raw_text, core_claim,
		       status, confidence_score, llm_model, parser_version, version,
		       created_at, updated_at, deleted_at
		FROM theses WHERE id = $1`, id)
	return scanThesis(row)
}

// ListActiveThesesByUser returns non-deleted theses for a user, newest first.
func (s *ThesisStore) ListActiveThesesByUser(ctx context.Context, userID int64) ([]*Thesis, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, symbol, company_name, market, direction, raw_text, core_claim,
		       status, confidence_score, llm_model, parser_version, version,
		       created_at, updated_at, deleted_at
		FROM theses
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list theses: %w", err)
	}
	defer rows.Close()

	var theses []*Thesis
	for rows.Next() {
		t, err := scanThesisRow(rows)
		if err != nil {
			return nil, err
		}
		theses = append(theses, t)
	}
	return theses, rows.Err()
}

// ListAllActiveTheses returns all non-deleted theses across all users.
func (s *ThesisStore) ListAllActiveTheses(ctx context.Context) ([]*Thesis, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, symbol, company_name, market, direction, raw_text, core_claim,
		       status, confidence_score, llm_model, parser_version, version,
		       created_at, updated_at, deleted_at
		FROM theses
		WHERE deleted_at IS NULL
		ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all theses: %w", err)
	}
	defer rows.Close()

	var theses []*Thesis
	for rows.Next() {
		t, err := scanThesisRow(rows)
		if err != nil {
			return nil, err
		}
		theses = append(theses, t)
	}
	return theses, rows.Err()
}

// UpdateThesis bumps the thesis version, replaces its fields and assumptions atomically.
// Returns the new version number.
func (s *ThesisStore) UpdateThesis(ctx context.Context, id int64, p CreateThesisParams) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var newVersion int
	err = tx.QueryRow(ctx, `
		UPDATE theses
		SET symbol         = $1,
		    company_name   = $2,
		    market         = $3,
		    direction      = $4,
		    raw_text       = $5,
		    core_claim     = $6,
		    llm_model      = $7,
		    parser_version = $8,
		    version        = version + 1,
		    updated_at     = now()
		WHERE id = $9 AND deleted_at IS NULL
		RETURNING version`,
		p.Symbol, p.CompanyName, p.Market, p.Direction, p.RawText, p.CoreClaim,
		p.LLMModel, p.ParserVersion, id,
	).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("thesis %d not found or deleted", id)
		}
		return 0, fmt.Errorf("update thesis: %w", err)
	}

	// Mark all existing assumptions as deleted.
	_, err = tx.Exec(ctx,
		`UPDATE assumptions SET status = $1, updated_at = now() WHERE thesis_id = $2`,
		AssumptionStatusDeleted, id,
	)
	if err != nil {
		return 0, fmt.Errorf("delete old assumptions: %w", err)
	}

	if err := insertAssumptions(ctx, tx, id, p.Assumptions); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit update thesis: %w", err)
	}
	return newVersion, nil
}

// SoftDeleteThesis sets deleted_at on a thesis.
func (s *ThesisStore) SoftDeleteThesis(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE theses SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("soft delete thesis: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("thesis %d not found or already deleted", id)
	}
	return nil
}

// ------------------------------------------------------------
// Assumptions
// ------------------------------------------------------------

// GetAssumptions returns all active assumptions for a thesis.
func (s *ThesisStore) GetAssumptions(ctx context.Context, thesisID int64) ([]*Assumption, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, thesis_id, assumption_key, text, type, verifiable,
		       importance, current_score, evidence_hints,
		       embedding_model, status, created_at, updated_at
		FROM assumptions
		WHERE thesis_id = $1 AND status = $2
		ORDER BY importance DESC`,
		thesisID, AssumptionStatusActive,
	)
	if err != nil {
		return nil, fmt.Errorf("get assumptions: %w", err)
	}
	defer rows.Close()

	var assumptions []*Assumption
	for rows.Next() {
		a, err := scanAssumptionRow(rows)
		if err != nil {
			return nil, err
		}
		assumptions = append(assumptions, a)
	}
	return assumptions, rows.Err()
}

// GetAssumptionsWithEmbeddings returns active assumptions including their
// embedding vectors. Used by the evidence judge step to supply RAGEngine.
func (s *ThesisStore) GetAssumptionsWithEmbeddings(ctx context.Context, thesisID int64) ([]*Assumption, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, thesis_id, assumption_key, text, type, verifiable,
		       importance, current_score, evidence_hints,
		       embedding, embedding_model, status, created_at, updated_at
		FROM assumptions
		WHERE thesis_id = $1 AND status = $2
		ORDER BY importance DESC`,
		thesisID, AssumptionStatusActive,
	)
	if err != nil {
		return nil, fmt.Errorf("get assumptions with embeddings: %w", err)
	}
	defer rows.Close()

	var assumptions []*Assumption
	for rows.Next() {
		a, err := scanAssumptionWithEmbedding(rows)
		if err != nil {
			return nil, err
		}
		assumptions = append(assumptions, a)
	}
	return assumptions, rows.Err()
}

// UpdateAssumptionEmbedding sets the embedding vector and model for an assumption.
func (s *ThesisStore) UpdateAssumptionEmbedding(ctx context.Context, id int64, embedding pgvector.Vector, model string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE assumptions SET embedding = $1, embedding_model = $2, updated_at = now() WHERE id = $3`,
		embedding, model, id,
	)
	if err != nil {
		return fmt.Errorf("update assumption embedding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("assumption %d not found", id)
	}
	return nil
}

// UpdateAssumptionScore updates the current_score for an assumption.
func (s *ThesisStore) UpdateAssumptionScore(ctx context.Context, id int64, score float64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE assumptions SET current_score = $1, updated_at = now() WHERE id = $2`,
		score, id,
	)
	if err != nil {
		return fmt.Errorf("update assumption score: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("assumption %d not found", id)
	}
	return nil
}

// ------------------------------------------------------------
// Helpers
// ------------------------------------------------------------

func insertThesis(ctx context.Context, tx pgx.Tx, p CreateThesisParams) (*Thesis, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO theses
		    (user_id, symbol, company_name, market, direction, raw_text, core_claim,
		     status, confidence_score, llm_model, parser_version, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0.5, $9, $10, 1)
		RETURNING id, user_id, symbol, company_name, market, direction, raw_text, core_claim,
		          status, confidence_score, llm_model, parser_version, version,
		          created_at, updated_at, deleted_at`,
		p.UserID, p.Symbol, p.CompanyName, p.Market, p.Direction, p.RawText, p.CoreClaim,
		ThesisStatusActive, p.LLMModel, p.ParserVersion,
	)
	t, err := scanThesis(row)
	if err != nil {
		return nil, fmt.Errorf("insert thesis: %w", err)
	}
	return t, nil
}

func insertAssumptions(ctx context.Context, tx pgx.Tx, thesisID int64, params []AssumptionParams) error {
	for _, p := range params {
		_, err := tx.Exec(ctx, `
			INSERT INTO assumptions
			    (thesis_id, assumption_key, text, type, verifiable,
			     importance, current_score, evidence_hints, status)
			VALUES ($1, $2, $3, $4, $5, $6, 0.5, $7, $8)`,
			thesisID, p.AssumptionKey, p.Text, p.Type, p.Verifiable,
			p.Importance, p.EvidenceHints, AssumptionStatusActive,
		)
		if err != nil {
			return fmt.Errorf("insert assumption %q: %w", p.AssumptionKey, err)
		}
	}
	return nil
}

// scanUser scans a User from a pgx.Row (single-row query result).
func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Password, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}
	return &u, nil
}

// scanThesis scans a Thesis from a pgx.Row (single-row query result).
func scanThesis(row pgx.Row) (*Thesis, error) {
	var t Thesis
	var deletedAt *time.Time
	err := row.Scan(
		&t.ID, &t.UserID, &t.Symbol, &t.CompanyName, &t.Market, &t.Direction,
		&t.RawText, &t.CoreClaim, &t.Status, &t.ConfidenceScore,
		&t.LLMModel, &t.ParserVersion, &t.Version,
		&t.CreatedAt, &t.UpdatedAt, &deletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan thesis: %w", err)
	}
	t.DeletedAt = deletedAt
	return &t, nil
}

// scanThesisRow scans a Thesis from pgx.Rows (multi-row query result).
func scanThesisRow(rows pgx.Rows) (*Thesis, error) {
	var t Thesis
	var deletedAt *time.Time
	err := rows.Scan(
		&t.ID, &t.UserID, &t.Symbol, &t.CompanyName, &t.Market, &t.Direction,
		&t.RawText, &t.CoreClaim, &t.Status, &t.ConfidenceScore,
		&t.LLMModel, &t.ParserVersion, &t.Version,
		&t.CreatedAt, &t.UpdatedAt, &deletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan thesis row: %w", err)
	}
	t.DeletedAt = deletedAt
	return &t, nil
}

// scanAssumptionRow scans an Assumption from pgx.Rows (without embedding).
func scanAssumptionRow(rows pgx.Rows) (*Assumption, error) {
	var a Assumption
	err := rows.Scan(
		&a.ID, &a.ThesisID, &a.AssumptionKey, &a.Text, &a.Type,
		&a.Verifiable, &a.Importance, &a.CurrentScore, &a.EvidenceHints,
		&a.EmbeddingModel, &a.Status, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan assumption row: %w", err)
	}
	return &a, nil
}

// scanAssumptionWithEmbedding scans an Assumption including the embedding column.
func scanAssumptionWithEmbedding(rows pgx.Rows) (*Assumption, error) {
	var a Assumption
	var emb *pgvector.Vector
	err := rows.Scan(
		&a.ID, &a.ThesisID, &a.AssumptionKey, &a.Text, &a.Type,
		&a.Verifiable, &a.Importance, &a.CurrentScore, &a.EvidenceHints,
		&emb, &a.EmbeddingModel, &a.Status, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan assumption with embedding: %w", err)
	}
	if emb != nil && len(emb.Slice()) > 0 {
		a.Embedding = emb
	}
	return &a, nil
}
