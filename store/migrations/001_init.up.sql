-- Enable pgvector extension (must be installed on the PostgreSQL server).
CREATE EXTENSION IF NOT EXISTS vector;

-- ============================================================
-- Users & Thesis
-- ============================================================

CREATE TABLE users (
    id         BIGSERIAL    PRIMARY KEY,
    email      TEXT         NOT NULL UNIQUE,
    name       TEXT         NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE theses (
    id               BIGSERIAL        PRIMARY KEY,
    user_id          BIGINT           NOT NULL REFERENCES users(id),
    symbol           TEXT             NOT NULL,
    company_name     TEXT             NOT NULL DEFAULT '',
    direction        TEXT             NOT NULL,
    raw_text         TEXT             NOT NULL,
    core_claim       TEXT             NOT NULL,
    status           TEXT             NOT NULL DEFAULT 'active',
    confidence_score DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    llm_model        TEXT             NOT NULL DEFAULT '',
    parser_version   TEXT             NOT NULL DEFAULT '',
    version          INT              NOT NULL DEFAULT 1,
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ
);

CREATE INDEX idx_theses_user_id    ON theses(user_id);
CREATE INDEX idx_theses_active     ON theses(user_id) WHERE deleted_at IS NULL;

CREATE TABLE assumptions (
    id              BIGSERIAL        PRIMARY KEY,
    thesis_id       BIGINT           NOT NULL REFERENCES theses(id),
    assumption_key  TEXT             NOT NULL,
    text            TEXT             NOT NULL,
    type            TEXT             NOT NULL DEFAULT 'other',
    verifiable      BOOLEAN          NOT NULL DEFAULT true,
    importance      DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    current_score   DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    evidence_hints  TEXT[]           NOT NULL DEFAULT '{}',
    -- vector dimension left open; add a fixed-dim index in a later migration
    -- once the embedding model is chosen.
    embedding       vector,
    embedding_model TEXT             NOT NULL DEFAULT '',
    status          TEXT             NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE INDEX idx_assumptions_thesis_id        ON assumptions(thesis_id);
CREATE INDEX idx_assumptions_thesis_active    ON assumptions(thesis_id) WHERE status = 'active';

-- ============================================================
-- Job
-- ============================================================

CREATE TABLE job_runs (
    id             BIGSERIAL   PRIMARY KEY,
    thesis_id      BIGINT      NOT NULL REFERENCES theses(id),
    thesis_version INT         NOT NULL,
    run_date       DATE        NOT NULL,
    job_type       TEXT        NOT NULL,
    status         TEXT        NOT NULL DEFAULT 'queued',
    current_step   TEXT        NOT NULL DEFAULT 'init',
    retry_count    INT         NOT NULL DEFAULT 0,
    error_message  TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ
);

CREATE INDEX idx_job_runs_thesis_id ON job_runs(thesis_id);
CREATE INDEX idx_job_runs_status    ON job_runs(status);

CREATE TABLE job_candidates (
    id               BIGSERIAL   PRIMARY KEY,
    job_id           BIGINT      NOT NULL REFERENCES job_runs(id),
    source           TEXT        NOT NULL,
    source_id        TEXT        NOT NULL,
    source_url       TEXT        NOT NULL DEFAULT '',
    symbol           TEXT        NOT NULL DEFAULT '',
    title            TEXT        NOT NULL DEFAULT '',
    summary          TEXT        NOT NULL DEFAULT '',
    published_at     TIMESTAMPTZ,
    raw_payload      JSONB       NOT NULL DEFAULT '{}',
    relevance_status TEXT        NOT NULL DEFAULT 'pending',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (job_id, source, source_id)
);

CREATE INDEX idx_job_candidates_status ON job_candidates(job_id, relevance_status);

-- ============================================================
-- Candidate / Evidence
-- ============================================================

CREATE TABLE relevant_candidates (
    id                           BIGSERIAL   PRIMARY KEY,
    source                       TEXT        NOT NULL,
    source_id                    TEXT        NOT NULL,
    source_url                   TEXT        NOT NULL DEFAULT '',
    symbol                       TEXT        NOT NULL DEFAULT '',
    title                        TEXT        NOT NULL DEFAULT '',
    summary                      TEXT        NOT NULL DEFAULT '',
    normalized_text              TEXT        NOT NULL DEFAULT '',
    embedding                    vector,
    embedding_model              TEXT        NOT NULL DEFAULT '',
    dedup_status                 TEXT        NOT NULL DEFAULT 'unknown',
    duplicate_of_id              BIGINT      REFERENCES relevant_candidates(id),
    event_group_id               TEXT,
    matched_assumptions_snapshot JSONB       NOT NULL DEFAULT '{}',
    llm_model                    TEXT        NOT NULL DEFAULT '',
    published_at                 TIMESTAMPTZ,
    evidence_status              TEXT        NOT NULL DEFAULT 'pending',
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source, source_id)
);

CREATE INDEX idx_relevant_candidates_dedup    ON relevant_candidates(dedup_status);
CREATE INDEX idx_relevant_candidates_evidence ON relevant_candidates(evidence_status);
CREATE INDEX idx_relevant_candidates_symbol   ON relevant_candidates(symbol);

CREATE TABLE candidate_assumptions (
    id             BIGSERIAL        PRIMARY KEY,
    candidate_id   BIGINT           NOT NULL REFERENCES relevant_candidates(id),
    assumption_id  BIGINT           NOT NULL REFERENCES assumptions(id),
    relevance      DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    confidence     DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    initial_impact DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    reason         TEXT             NOT NULL DEFAULT '',
    llm_model      TEXT             NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (candidate_id, assumption_id)
);

CREATE INDEX idx_candidate_assumptions_candidate  ON candidate_assumptions(candidate_id);
CREATE INDEX idx_candidate_assumptions_assumption ON candidate_assumptions(assumption_id);

CREATE TABLE candidate_chunks (
    id              BIGSERIAL        PRIMARY KEY,
    candidate_id    BIGINT           NOT NULL REFERENCES relevant_candidates(id),
    chunk_index     INT              NOT NULL,
    text            TEXT             NOT NULL DEFAULT '',
    embedding       vector,
    embedding_model TEXT             NOT NULL DEFAULT '',
    token_count     INT              NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (candidate_id, chunk_index)
);

CREATE INDEX idx_candidate_chunks_candidate ON candidate_chunks(candidate_id);

CREATE TABLE evidence_snippets (
    id             BIGSERIAL        PRIMARY KEY,
    assumption_id  BIGINT           NOT NULL REFERENCES assumptions(id),
    candidate_id   BIGINT           NOT NULL REFERENCES relevant_candidates(id),
    snippet_text   TEXT             NOT NULL DEFAULT '',
    judge_stage    TEXT             NOT NULL,
    relevance      DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    confidence     DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    stance         TEXT             NOT NULL,
    impact         DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    source_weight  DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    novelty_weight DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    published_at   TIMESTAMPTZ,
    reason         TEXT             NOT NULL DEFAULT '',
    llm_model      TEXT             NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (assumption_id, candidate_id, judge_stage)
);

CREATE INDEX idx_evidence_snippets_assumption ON evidence_snippets(assumption_id);
CREATE INDEX idx_evidence_snippets_candidate  ON evidence_snippets(candidate_id);
CREATE INDEX idx_evidence_snippets_created_at ON evidence_snippets(created_at);

-- ============================================================
-- Score / Report
-- ============================================================

CREATE TABLE assumption_score_history (
    id                       BIGSERIAL        PRIMARY KEY,
    assumption_id            BIGINT           NOT NULL REFERENCES assumptions(id),
    thesis_id                BIGINT           NOT NULL REFERENCES theses(id),
    run_date                 DATE             NOT NULL,
    score_before             DOUBLE PRECISION NOT NULL,
    score_after              DOUBLE PRECISION NOT NULL,
    score_delta              DOUBLE PRECISION NOT NULL,
    daily_effect             DOUBLE PRECISION NOT NULL,
    positive_evidence_count  INT              NOT NULL DEFAULT 0,
    negative_evidence_count  INT              NOT NULL DEFAULT 0,
    neutral_evidence_count   INT              NOT NULL DEFAULT 0,
    top_evidence_snippet_ids BIGINT[]         NOT NULL DEFAULT '{}',
    reason                   TEXT             NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (assumption_id, run_date)
);

CREATE INDEX idx_assumption_score_history_thesis ON assumption_score_history(thesis_id, run_date);

CREATE TABLE thesis_score_history (
    id                      BIGSERIAL        PRIMARY KEY,
    thesis_id               BIGINT           NOT NULL REFERENCES theses(id),
    run_date                DATE             NOT NULL,
    score_before            DOUBLE PRECISION NOT NULL,
    score_after             DOUBLE PRECISION NOT NULL,
    score_delta             DOUBLE PRECISION NOT NULL,
    assumption_count        INT              NOT NULL DEFAULT 0,
    strongest_assumption_id BIGINT           REFERENCES assumptions(id),
    weakest_assumption_id   BIGINT           REFERENCES assumptions(id),
    changed_assumption_ids  BIGINT[]         NOT NULL DEFAULT '{}',
    reason                  TEXT             NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (thesis_id, run_date)
);

CREATE INDEX idx_thesis_score_history_thesis ON thesis_score_history(thesis_id);

CREATE TABLE daily_reports (
    id                  BIGSERIAL        PRIMARY KEY,
    user_id             BIGINT           NOT NULL REFERENCES users(id),
    thesis_id           BIGINT           NOT NULL REFERENCES theses(id),
    run_date            DATE             NOT NULL,
    title               TEXT             NOT NULL DEFAULT '',
    thesis_score_before DOUBLE PRECISION NOT NULL,
    thesis_score_after  DOUBLE PRECISION NOT NULL,
    thesis_score_delta  DOUBLE PRECISION NOT NULL,
    summary             TEXT             NOT NULL DEFAULT '',
    markdown_report     TEXT             NOT NULL DEFAULT '',
    market_context      JSONB            NOT NULL DEFAULT '{}',
    alert_level         TEXT             NOT NULL DEFAULT 'none',
    created_at          TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (thesis_id, run_date)
);

CREATE INDEX idx_daily_reports_user    ON daily_reports(user_id);
CREATE INDEX idx_daily_reports_thesis  ON daily_reports(thesis_id);
