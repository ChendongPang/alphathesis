-- Drop in reverse dependency order.
DROP TABLE IF EXISTS daily_reports;
DROP TABLE IF EXISTS thesis_score_history;
DROP TABLE IF EXISTS assumption_score_history;
DROP TABLE IF EXISTS evidence_snippets;
DROP TABLE IF EXISTS candidate_chunks;
DROP TABLE IF EXISTS candidate_assumptions;
DROP TABLE IF EXISTS relevant_candidates;
DROP TABLE IF EXISTS job_candidates;
DROP TABLE IF EXISTS job_runs;
DROP TABLE IF EXISTS assumptions;
DROP TABLE IF EXISTS theses;
DROP TABLE IF EXISTS users;
DROP EXTENSION IF EXISTS vector;
