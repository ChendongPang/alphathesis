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

const (
	JobTypeDailyRun    = "daily_run"
	JobTypeUpdateRerun = "update_rerun"
)

// JobStore handles job_runs and job_candidates.
type JobStore struct {
	pool *pgxpool.Pool
}

func NewJobStore(db *DB) *JobStore {
	return &JobStore{pool: db.pool}
}

// CreateJobCandidateParams holds the raw fetched data for one candidate.
type CreateJobCandidateParams struct {
	Source      string
	SourceID    string
	SourceURL   string
	Symbol      string
	Title       string
	Summary     string
	PublishedAt *time.Time
	RawPayload  json.RawMessage
}

// ------------------------------------------------------------
// JobRun
// ------------------------------------------------------------

// CreateJobRun enqueues a new job for a thesis. Starts at step=init, status=queued.
func (s *JobStore) CreateJobRun(ctx context.Context, thesisID int64, thesisVersion int, runDate time.Time, jobType string) (*JobRun, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO job_runs
		    (thesis_id, thesis_version, run_date, job_type, status, current_step, retry_count)
		VALUES ($1, $2, $3, $4, $5, $6, 0)
		RETURNING `+jobRunColumns,
		thesisID, thesisVersion, runDate, jobType, JobStatusQueued, JobStepInit,
	)
	return scanJobRun(row)
}

// GetJobRun returns a job run by ID.
func (s *JobStore) GetJobRun(ctx context.Context, id int64) (*JobRun, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+jobRunColumns+` FROM job_runs WHERE id = $1`, id)
	return scanJobRun(row)
}

// GetQueuedJobRuns returns all queued jobs ordered FIFO.
func (s *JobStore) GetQueuedJobRuns(ctx context.Context) ([]*JobRun, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+jobRunColumns+` FROM job_runs WHERE status = $1 ORDER BY created_at ASC`,
		JobStatusQueued,
	)
	if err != nil {
		return nil, fmt.Errorf("get queued jobs: %w", err)
	}
	defer rows.Close()
	return collectJobRunRows(rows)
}

// HasJobRunToday returns true if a job of the given type already exists for
// the thesis on runDate (same calendar date, UTC).
func (s *JobStore) HasJobRunToday(ctx context.Context, thesisID int64, jobType string, runDate time.Time) (bool, error) {
	day := runDate.UTC().Format("2006-01-02")
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM job_runs
		WHERE thesis_id = $1 AND job_type = $2
		  AND run_date::date = $3::date`,
		thesisID, jobType, day,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check job today: %w", err)
	}
	return count > 0, nil
}

// MarkJobRunStarted transitions a queued job to running.
func (s *JobStore) MarkJobRunStarted(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE job_runs
		SET status = $1, started_at = now(), updated_at = now()
		WHERE id = $2 AND status = $3`,
		JobStatusRunning, id, JobStatusQueued,
	)
	if err != nil {
		return fmt.Errorf("mark job started: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %d not found or not queued", id)
	}
	return nil
}

// AdvanceJobStep records the current checkpoint step without changing status.
// Call this after each step's results are safely written to the database.
func (s *JobStore) AdvanceJobStep(ctx context.Context, id int64, step string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE job_runs SET current_step = $1, updated_at = now() WHERE id = $2`,
		step, id,
	)
	if err != nil {
		return fmt.Errorf("advance job step: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %d not found", id)
	}
	return nil
}

// MarkJobRunSucceeded marks the job as done.
func (s *JobStore) MarkJobRunSucceeded(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE job_runs
		SET status = $1, current_step = $2, finished_at = now(), updated_at = now()
		WHERE id = $3`,
		JobStatusSucceeded, JobStepDone, id,
	)
	if err != nil {
		return fmt.Errorf("mark job succeeded: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %d not found", id)
	}
	return nil
}

// MarkJobRunFailed marks the job as failed and increments retry_count.
func (s *JobStore) MarkJobRunFailed(ctx context.Context, id int64, errMsg string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE job_runs
		SET status = $1, error_message = $2,
		    retry_count = retry_count + 1,
		    finished_at = now(), updated_at = now()
		WHERE id = $3`,
		JobStatusFailed, errMsg, id,
	)
	if err != nil {
		return fmt.Errorf("mark job failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %d not found", id)
	}
	return nil
}

// RequeueJobRun moves a failed job back to queued so the worker retries from
// its last checkpoint step.
func (s *JobStore) RequeueJobRun(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE job_runs
		SET status = $1, started_at = NULL, finished_at = NULL,
		    error_message = NULL, updated_at = now()
		WHERE id = $2 AND status = $3`,
		JobStatusQueued, id, JobStatusFailed,
	)
	if err != nil {
		return fmt.Errorf("requeue job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %d not found or not failed", id)
	}
	return nil
}

// CancelJobRunsForThesis cancels all queued and running jobs for a thesis.
// Call this when a thesis is updated so stale jobs are discarded.
func (s *JobStore) CancelJobRunsForThesis(ctx context.Context, thesisID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE job_runs
		SET status = $1, updated_at = now()
		WHERE thesis_id = $2 AND status = ANY($3)`,
		JobStatusCancelled, thesisID,
		[]string{JobStatusQueued, JobStatusRunning},
	)
	if err != nil {
		return fmt.Errorf("cancel jobs for thesis %d: %w", thesisID, err)
	}
	return nil
}

// RecoverRunningJobs resets any jobs stuck in running state back to queued.
// Call once on program startup to recover from a previous crash.
func (s *JobStore) RecoverRunningJobs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE job_runs
		SET status = $1, updated_at = now()
		WHERE status = $2`,
		JobStatusQueued, JobStatusRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("recover running jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ------------------------------------------------------------
// JobCandidate
// ------------------------------------------------------------

// CreateJobCandidates batch-inserts fetched candidates for a job.
// Duplicates (same job_id + source + source_id) are silently ignored.
func (s *JobStore) CreateJobCandidates(ctx context.Context, jobID int64, candidates []CreateJobCandidateParams) error {
	for _, c := range candidates {
		rawPayload := c.RawPayload
		if len(rawPayload) == 0 {
			rawPayload = json.RawMessage("{}")
		}
		_, err := s.pool.Exec(ctx, `
			INSERT INTO job_candidates
			    (job_id, source, source_id, source_url, symbol,
			     title, summary, published_at, raw_payload, relevance_status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (job_id, source, source_id) DO NOTHING`,
			jobID, c.Source, c.SourceID, c.SourceURL, c.Symbol,
			c.Title, c.Summary, c.PublishedAt, rawPayload, RelevanceStatusPending,
		)
		if err != nil {
			return fmt.Errorf("insert job candidate %q/%q: %w", c.Source, c.SourceID, err)
		}
	}
	return nil
}

// ListJobCandidatesByStatus returns all candidates for a job with the given relevance_status.
func (s *JobStore) ListJobCandidatesByStatus(ctx context.Context, jobID int64, status string) ([]*JobCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, job_id, source, source_id, source_url, symbol,
		       title, summary, published_at, raw_payload, relevance_status, created_at
		FROM job_candidates
		WHERE job_id = $1 AND relevance_status = $2
		ORDER BY id ASC`,
		jobID, status,
	)
	if err != nil {
		return nil, fmt.Errorf("list job candidates: %w", err)
	}
	defer rows.Close()

	var candidates []*JobCandidate
	for rows.Next() {
		c, err := scanJobCandidateRow(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// UpdateJobCandidateStatus sets relevance_status for a single candidate.
func (s *JobStore) UpdateJobCandidateStatus(ctx context.Context, id int64, status string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE job_candidates SET relevance_status = $1 WHERE id = $2`,
		status, id,
	)
	if err != nil {
		return fmt.Errorf("update candidate status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job candidate %d not found", id)
	}
	return nil
}

// ------------------------------------------------------------
// Scan helpers
// ------------------------------------------------------------

// jobRunColumns is the SELECT column list matching scanJobRun scan order.
const jobRunColumns = `id, thesis_id, thesis_version, run_date, job_type, status,
	current_step, retry_count, error_message,
	created_at, started_at, updated_at, finished_at`

func scanJobRun(row pgx.Row) (*JobRun, error) {
	var j JobRun
	err := row.Scan(
		&j.ID, &j.ThesisID, &j.ThesisVersion, &j.RunDate, &j.JobType, &j.Status,
		&j.CurrentStep, &j.RetryCount, &j.ErrorMessage,
		&j.CreatedAt, &j.StartedAt, &j.UpdatedAt, &j.FinishedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan job run: %w", err)
	}
	return &j, nil
}

func scanJobRunRow(rows pgx.Rows) (*JobRun, error) {
	var j JobRun
	err := rows.Scan(
		&j.ID, &j.ThesisID, &j.ThesisVersion, &j.RunDate, &j.JobType, &j.Status,
		&j.CurrentStep, &j.RetryCount, &j.ErrorMessage,
		&j.CreatedAt, &j.StartedAt, &j.UpdatedAt, &j.FinishedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan job run row: %w", err)
	}
	return &j, nil
}

func collectJobRunRows(rows pgx.Rows) ([]*JobRun, error) {
	var jobs []*JobRun
	for rows.Next() {
		j, err := scanJobRunRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func scanJobCandidateRow(rows pgx.Rows) (*JobCandidate, error) {
	var c JobCandidate
	err := rows.Scan(
		&c.ID, &c.JobID, &c.Source, &c.SourceID, &c.SourceURL, &c.Symbol,
		&c.Title, &c.Summary, &c.PublishedAt, &c.RawPayload, &c.RelevanceStatus, &c.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan job candidate row: %w", err)
	}
	return &c, nil
}
