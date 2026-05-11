package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScoreStore handles assumption_score_history, thesis_score_history, and daily_reports.
type ScoreStore struct {
	pool *pgxpool.Pool
}

func NewScoreStore(db *DB) *ScoreStore {
	return &ScoreStore{pool: db.pool}
}

// AssumptionScoreResult is the ScoreEngine output for one assumption.
type AssumptionScoreResult struct {
	AssumptionID          int64
	ScoreBefore           float64
	ScoreAfter            float64
	DailyEffect           float64
	PositiveEvidenceCount int
	NegativeEvidenceCount int
	NeutralEvidenceCount  int
	TopEvidenceSnippetIDs []int64
	Reason                string
}

// ThesisScoreResult is the ScoreEngine output for the overall thesis.
type ThesisScoreResult struct {
	ScoreBefore           float64
	ScoreAfter            float64
	AssumptionCount       int
	StrongestAssumptionID *int64
	WeakestAssumptionID   *int64
	ChangedAssumptionIDs  []int64
	Reason                string
}

// SaveScoreResultsParams groups all score outputs for one job run.
type SaveScoreResultsParams struct {
	ThesisID    int64
	RunDate     time.Time
	Assumptions []AssumptionScoreResult
	Thesis      ThesisScoreResult
}

// CreateDailyReportParams holds the ReportAgent output.
type CreateDailyReportParams struct {
	UserID            int64
	ThesisID          int64
	RunDate           time.Time
	Title             string
	ThesisScoreBefore float64
	ThesisScoreAfter  float64
	Summary           string
	MarkdownReport    string
	MarketContext     json.RawMessage
	AlertLevel        string
}

// ------------------------------------------------------------
// Score results (transactional write)
// ------------------------------------------------------------

// SaveScoreResults atomically writes all score results for one job run:
//   - updates current_score on each assumption
//   - upserts one assumption_score_history row per assumption
//   - upserts one thesis_score_history row
//
// This is always called as a single transaction so the score state is never
// partially updated.
func (s *ScoreStore) SaveScoreResults(ctx context.Context, p SaveScoreResultsParams) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin score transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, a := range p.Assumptions {
		if err := updateAssumptionScoreInTx(ctx, tx, a.AssumptionID, a.ScoreAfter); err != nil {
			return err
		}
		if err := upsertAssumptionScoreHistory(ctx, tx, p.ThesisID, p.RunDate, a); err != nil {
			return err
		}
	}

	if err := upsertThesisScoreHistory(ctx, tx, p.ThesisID, p.RunDate, p.Thesis); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE theses SET confidence_score = $1, updated_at = now() WHERE id = $2`,
		p.Thesis.ScoreAfter, p.ThesisID,
	); err != nil {
		return fmt.Errorf("update thesis confidence_score: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit score results: %w", err)
	}
	return nil
}

// ------------------------------------------------------------
// AssumptionScoreHistory
// ------------------------------------------------------------

// GetAssumptionScoreHistory returns the score history for one assumption on a given day.
func (s *ScoreStore) GetAssumptionScoreHistory(ctx context.Context, assumptionID int64, runDate time.Time) (*AssumptionScoreHistory, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+assumptionScoreHistoryColumns+` FROM assumption_score_history WHERE assumption_id = $1 AND run_date = $2`,
		assumptionID, truncateToDay(runDate),
	)
	return scanAssumptionScoreHistory(row)
}

// ListAssumptionScoreHistoriesByThesis returns the score history for all assumptions
// of a thesis on a given day. Used by ScoreEngine to build the final thesis score.
func (s *ScoreStore) ListAssumptionScoreHistoriesByThesis(ctx context.Context, thesisID int64, runDate time.Time) ([]*AssumptionScoreHistory, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+assumptionScoreHistoryColumns+` FROM assumption_score_history WHERE thesis_id = $1 AND run_date = $2 ORDER BY assumption_id`,
		thesisID, truncateToDay(runDate),
	)
	if err != nil {
		return nil, fmt.Errorf("list assumption score histories: %w", err)
	}
	defer rows.Close()

	var histories []*AssumptionScoreHistory
	for rows.Next() {
		h, err := scanAssumptionScoreHistoryRow(rows)
		if err != nil {
			return nil, err
		}
		histories = append(histories, h)
	}
	return histories, rows.Err()
}

// ------------------------------------------------------------
// ThesisScoreHistory
// ------------------------------------------------------------

// GetThesisScoreHistory returns the thesis-level score history for a given day.
func (s *ScoreStore) GetThesisScoreHistory(ctx context.Context, thesisID int64, runDate time.Time) (*ThesisScoreHistory, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+thesisScoreHistoryColumns+` FROM thesis_score_history WHERE thesis_id = $1 AND run_date = $2`,
		thesisID, truncateToDay(runDate),
	)
	return scanThesisScoreHistory(row)
}

// ListThesisScoreHistory returns the score trend for a thesis, newest first.
func (s *ScoreStore) ListThesisScoreHistory(ctx context.Context, thesisID int64, limit int) ([]*ThesisScoreHistory, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+thesisScoreHistoryColumns+` FROM thesis_score_history WHERE thesis_id = $1 ORDER BY run_date DESC LIMIT $2`,
		thesisID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list thesis score history: %w", err)
	}
	defer rows.Close()

	var histories []*ThesisScoreHistory
	for rows.Next() {
		h, err := scanThesisScoreHistoryRow(rows)
		if err != nil {
			return nil, err
		}
		histories = append(histories, h)
	}
	return histories, rows.Err()
}

// ------------------------------------------------------------
// DailyReport
// ------------------------------------------------------------

// CreateDailyReport upserts the daily report for a thesis on a given day.
// If a report already exists for (thesis_id, run_date) it is overwritten.
func (s *ScoreStore) CreateDailyReport(ctx context.Context, p CreateDailyReportParams) (*DailyReport, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO daily_reports
		    (user_id, thesis_id, run_date, title,
		     thesis_score_before, thesis_score_after, thesis_score_delta,
		     summary, markdown_report, market_context, alert_level)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (thesis_id, run_date) DO UPDATE
		    SET title               = EXCLUDED.title,
		        thesis_score_before = EXCLUDED.thesis_score_before,
		        thesis_score_after  = EXCLUDED.thesis_score_after,
		        thesis_score_delta  = EXCLUDED.thesis_score_delta,
		        summary             = EXCLUDED.summary,
		        markdown_report     = EXCLUDED.markdown_report,
		        market_context      = EXCLUDED.market_context,
		        alert_level         = EXCLUDED.alert_level
		RETURNING `+dailyReportColumns,
		p.UserID, p.ThesisID, truncateToDay(p.RunDate), p.Title,
		p.ThesisScoreBefore, p.ThesisScoreAfter, p.ThesisScoreAfter-p.ThesisScoreBefore,
		p.Summary, p.MarkdownReport, p.MarketContext, p.AlertLevel,
	)
	dr, err := scanDailyReport(row)
	if err != nil {
		return nil, fmt.Errorf("create daily report: %w", err)
	}
	return dr, nil
}

// GetLatestDailyReport returns the most recent daily report for a thesis.
func (s *ScoreStore) GetLatestDailyReport(ctx context.Context, thesisID int64) (*DailyReport, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+dailyReportColumns+` FROM daily_reports WHERE thesis_id = $1 ORDER BY run_date DESC LIMIT 1`,
		thesisID,
	)
	return scanDailyReport(row)
}

// AssumptionEvidenceCounts holds cumulative positive/negative/neutral evidence
// counts for one assumption, aggregated across all run dates.
type AssumptionEvidenceCounts struct {
	AssumptionID int64
	PosCount     int
	NegCount     int
	NeutralCount int
}

// ListAssumptionEvidenceCounts returns cumulative evidence counts per assumption
// for a thesis, aggregated over all historical run dates.
func (s *ScoreStore) ListAssumptionEvidenceCounts(ctx context.Context, thesisID int64) ([]AssumptionEvidenceCounts, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT assumption_id,
		       SUM(positive_evidence_count)::int AS pos_count,
		       SUM(negative_evidence_count)::int AS neg_count,
		       SUM(neutral_evidence_count)::int  AS neutral_count
		FROM assumption_score_history
		WHERE thesis_id = $1
		GROUP BY assumption_id`,
		thesisID,
	)
	if err != nil {
		return nil, fmt.Errorf("list assumption evidence counts: %w", err)
	}
	defer rows.Close()

	var result []AssumptionEvidenceCounts
	for rows.Next() {
		var c AssumptionEvidenceCounts
		if err := rows.Scan(&c.AssumptionID, &c.PosCount, &c.NegCount, &c.NeutralCount); err != nil {
			return nil, fmt.Errorf("scan assumption evidence counts: %w", err)
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// GetDailyReportByID returns a daily report by its primary key.
func (s *ScoreStore) GetDailyReportByID(ctx context.Context, id int64) (*DailyReport, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+dailyReportColumns+` FROM daily_reports WHERE id = $1`, id)
	return scanDailyReport(row)
}

// GetDailyReport returns the daily report for a thesis on a given day.
func (s *ScoreStore) GetDailyReport(ctx context.Context, thesisID int64, runDate time.Time) (*DailyReport, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+dailyReportColumns+` FROM daily_reports WHERE thesis_id = $1 AND run_date = $2`,
		thesisID, truncateToDay(runDate),
	)
	return scanDailyReport(row)
}

// ListDailyReportsByUser returns all reports for a user's thesis, newest first.
func (s *ScoreStore) ListDailyReportsByUser(ctx context.Context, userID int64, thesisID int64, limit int) ([]*DailyReport, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+dailyReportColumns+` FROM daily_reports WHERE user_id = $1 AND thesis_id = $2 ORDER BY run_date DESC LIMIT $3`,
		userID, thesisID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list daily reports: %w", err)
	}
	defer rows.Close()

	var reports []*DailyReport
	for rows.Next() {
		dr, err := scanDailyReportRow(rows)
		if err != nil {
			return nil, err
		}
		reports = append(reports, dr)
	}
	return reports, rows.Err()
}

// ------------------------------------------------------------
// Transaction helpers
// ------------------------------------------------------------

func updateAssumptionScoreInTx(ctx context.Context, tx pgx.Tx, assumptionID int64, score float64) error {
	_, err := tx.Exec(ctx,
		`UPDATE assumptions SET current_score = $1, updated_at = now() WHERE id = $2`,
		score, assumptionID,
	)
	if err != nil {
		return fmt.Errorf("update assumption %d score in tx: %w", assumptionID, err)
	}
	return nil
}

func upsertAssumptionScoreHistory(ctx context.Context, tx pgx.Tx, thesisID int64, runDate time.Time, a AssumptionScoreResult) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO assumption_score_history
		    (assumption_id, thesis_id, run_date,
		     score_before, score_after, score_delta, daily_effect,
		     positive_evidence_count, negative_evidence_count, neutral_evidence_count,
		     top_evidence_snippet_ids, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (assumption_id, run_date) DO UPDATE
		    SET score_before             = EXCLUDED.score_before,
		        score_after              = EXCLUDED.score_after,
		        score_delta              = EXCLUDED.score_delta,
		        daily_effect             = EXCLUDED.daily_effect,
		        positive_evidence_count  = EXCLUDED.positive_evidence_count,
		        negative_evidence_count  = EXCLUDED.negative_evidence_count,
		        neutral_evidence_count   = EXCLUDED.neutral_evidence_count,
		        top_evidence_snippet_ids = EXCLUDED.top_evidence_snippet_ids,
		        reason                   = EXCLUDED.reason`,
		a.AssumptionID, thesisID, truncateToDay(runDate),
		a.ScoreBefore, a.ScoreAfter, a.ScoreAfter-a.ScoreBefore, a.DailyEffect,
		a.PositiveEvidenceCount, a.NegativeEvidenceCount, a.NeutralEvidenceCount,
		nonNilInt64Slice(a.TopEvidenceSnippetIDs), a.Reason,
	)
	if err != nil {
		return fmt.Errorf("upsert assumption score history for %d: %w", a.AssumptionID, err)
	}
	return nil
}

func upsertThesisScoreHistory(ctx context.Context, tx pgx.Tx, thesisID int64, runDate time.Time, t ThesisScoreResult) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO thesis_score_history
		    (thesis_id, run_date,
		     score_before, score_after, score_delta,
		     assumption_count, strongest_assumption_id, weakest_assumption_id,
		     changed_assumption_ids, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (thesis_id, run_date) DO UPDATE
		    SET score_before            = EXCLUDED.score_before,
		        score_after             = EXCLUDED.score_after,
		        score_delta             = EXCLUDED.score_delta,
		        assumption_count        = EXCLUDED.assumption_count,
		        strongest_assumption_id = EXCLUDED.strongest_assumption_id,
		        weakest_assumption_id   = EXCLUDED.weakest_assumption_id,
		        changed_assumption_ids  = EXCLUDED.changed_assumption_ids,
		        reason                  = EXCLUDED.reason`,
		thesisID, truncateToDay(runDate),
		t.ScoreBefore, t.ScoreAfter, t.ScoreAfter-t.ScoreBefore,
		t.AssumptionCount, t.StrongestAssumptionID, t.WeakestAssumptionID,
		nonNilInt64Slice(t.ChangedAssumptionIDs), t.Reason,
	)
	if err != nil {
		return fmt.Errorf("upsert thesis score history: %w", err)
	}
	return nil
}

// ------------------------------------------------------------
// Scan helpers
// ------------------------------------------------------------

const assumptionScoreHistoryColumns = `id, assumption_id, thesis_id, run_date,
	score_before, score_after, score_delta, daily_effect,
	positive_evidence_count, negative_evidence_count, neutral_evidence_count,
	top_evidence_snippet_ids, reason, created_at`

const thesisScoreHistoryColumns = `id, thesis_id, run_date,
	score_before, score_after, score_delta,
	assumption_count, strongest_assumption_id, weakest_assumption_id,
	changed_assumption_ids, reason, created_at`

const dailyReportColumns = `id, user_id, thesis_id, run_date, title,
	thesis_score_before, thesis_score_after, thesis_score_delta,
	summary, markdown_report, market_context, alert_level, created_at`

func scanAssumptionScoreHistory(row pgx.Row) (*AssumptionScoreHistory, error) {
	var h AssumptionScoreHistory
	err := row.Scan(
		&h.ID, &h.AssumptionID, &h.ThesisID, &h.RunDate,
		&h.ScoreBefore, &h.ScoreAfter, &h.ScoreDelta, &h.DailyEffect,
		&h.PositiveEvidenceCount, &h.NegativeEvidenceCount, &h.NeutralEvidenceCount,
		&h.TopEvidenceSnippetIDs, &h.Reason, &h.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan assumption score history: %w", err)
	}
	return &h, nil
}

func scanAssumptionScoreHistoryRow(rows pgx.Rows) (*AssumptionScoreHistory, error) {
	var h AssumptionScoreHistory
	err := rows.Scan(
		&h.ID, &h.AssumptionID, &h.ThesisID, &h.RunDate,
		&h.ScoreBefore, &h.ScoreAfter, &h.ScoreDelta, &h.DailyEffect,
		&h.PositiveEvidenceCount, &h.NegativeEvidenceCount, &h.NeutralEvidenceCount,
		&h.TopEvidenceSnippetIDs, &h.Reason, &h.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan assumption score history row: %w", err)
	}
	return &h, nil
}

func scanThesisScoreHistory(row pgx.Row) (*ThesisScoreHistory, error) {
	var h ThesisScoreHistory
	err := row.Scan(
		&h.ID, &h.ThesisID, &h.RunDate,
		&h.ScoreBefore, &h.ScoreAfter, &h.ScoreDelta,
		&h.AssumptionCount, &h.StrongestAssumptionID, &h.WeakestAssumptionID,
		&h.ChangedAssumptionIDs, &h.Reason, &h.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan thesis score history: %w", err)
	}
	return &h, nil
}

func scanThesisScoreHistoryRow(rows pgx.Rows) (*ThesisScoreHistory, error) {
	var h ThesisScoreHistory
	err := rows.Scan(
		&h.ID, &h.ThesisID, &h.RunDate,
		&h.ScoreBefore, &h.ScoreAfter, &h.ScoreDelta,
		&h.AssumptionCount, &h.StrongestAssumptionID, &h.WeakestAssumptionID,
		&h.ChangedAssumptionIDs, &h.Reason, &h.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan thesis score history row: %w", err)
	}
	return &h, nil
}

func scanDailyReport(row pgx.Row) (*DailyReport, error) {
	var dr DailyReport
	err := row.Scan(
		&dr.ID, &dr.UserID, &dr.ThesisID, &dr.RunDate, &dr.Title,
		&dr.ThesisScoreBefore, &dr.ThesisScoreAfter, &dr.ThesisScoreDelta,
		&dr.Summary, &dr.MarkdownReport, &dr.MarketContext, &dr.AlertLevel, &dr.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan daily report: %w", err)
	}
	return &dr, nil
}

func scanDailyReportRow(rows pgx.Rows) (*DailyReport, error) {
	var dr DailyReport
	err := rows.Scan(
		&dr.ID, &dr.UserID, &dr.ThesisID, &dr.RunDate, &dr.Title,
		&dr.ThesisScoreBefore, &dr.ThesisScoreAfter, &dr.ThesisScoreDelta,
		&dr.Summary, &dr.MarkdownReport, &dr.MarketContext, &dr.AlertLevel, &dr.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan daily report row: %w", err)
	}
	return &dr, nil
}

func nonNilInt64Slice(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}
