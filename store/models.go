package store

import (
	"encoding/json"
	"time"

	"github.com/pgvector/pgvector-go"
)

// ------------------------------------------------------------
// Status / enum constants
// ------------------------------------------------------------

const (
	ThesisStatusActive  = "active"
	ThesisStatusArchived = "archived"

	JobStatusQueued    = "queued"
	JobStatusRunning   = "running"
	JobStatusSucceeded = "succeeded"
	JobStatusFailed    = "failed"
	JobStatusCancelled = "cancelled"

	JobStepInit           = "init"
	JobStepFetchCandidates = "fetch_candidates"
	JobStepRelevanceJudge = "relevance_judge"
	JobStepDedupJudge     = "dedup_judge"
	JobStepEvidenceJudge  = "evidence_judge"
	JobStepScoreUpdate    = "score_update"
	JobStepMarketContext  = "market_context"
	JobStepReportGenerate = "report_generate"
	JobStepDone           = "done"

	RelevanceStatusPending   = "pending"
	RelevanceStatusIrrelevant = "irrelevant"
	RelevanceStatusRelevant  = "relevant"
	RelevanceStatusError     = "error"

	DedupStatusUnknown     = "unknown"
	DedupStatusDuplicate   = "duplicate"
	DedupStatusEventUpdate = "event_update"
	DedupStatusNewEvent    = "new_event"

	EvidenceStatusPending = "pending"
	EvidenceStatusDone    = "done"
	EvidenceStatusSkipped = "skipped"
	EvidenceStatusError   = "error"

	JudgeStageSummary = "summary"
	JudgeStageRAG     = "rag"

	StanceSupport    = "support"
	StanceContradict = "contradict"
	StanceNeutral    = "neutral"

	AssumptionStatusActive  = "active"
	AssumptionStatusDeleted = "deleted"

	AlertLevelNone   = "none"
	AlertLevelLow    = "low"
	AlertLevelMedium = "medium"
	AlertLevelHigh   = "high"
)

// ------------------------------------------------------------
// Users & Thesis
// ------------------------------------------------------------

type User struct {
	ID        int64     `db:"id"`
	Email     string    `db:"email"`
	Name      string    `db:"name"`
	Password  string    `db:"password" json:"-"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

type Thesis struct {
	ID              int64      `db:"id"`
	UserID          int64      `db:"user_id"`
	Symbol          string     `db:"symbol"`
	CompanyName     string     `db:"company_name"`
	Market          string     `db:"market"`
	Direction       string     `db:"direction"`
	RawText         string     `db:"raw_text"`
	CoreClaim       string     `db:"core_claim"`
	Status          string     `db:"status"`
	ConfidenceScore float64    `db:"confidence_score"`
	LLMModel        string     `db:"llm_model"`
	ParserVersion   string     `db:"parser_version"`
	Version         int        `db:"version"`
	CreatedAt       time.Time  `db:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at"`
	DeletedAt       *time.Time `db:"deleted_at"`
}

type Assumption struct {
	ID             int64          `db:"id"`
	ThesisID       int64          `db:"thesis_id"`
	AssumptionKey  string         `db:"assumption_key"`
	Text           string         `db:"text"`
	Type           string         `db:"type"`
	Verifiable     bool           `db:"verifiable"`
	Importance     float64        `db:"importance"`
	CurrentScore   float64        `db:"current_score"`
	EvidenceHints  []string       `db:"evidence_hints"`
	Embedding      *pgvector.Vector `db:"embedding"`
	EmbeddingModel string         `db:"embedding_model"`
	Status         string         `db:"status"`
	CreatedAt      time.Time      `db:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at"`
}

// ------------------------------------------------------------
// Job
// ------------------------------------------------------------

type JobRun struct {
	ID             int64      `db:"id"`
	ThesisID       int64      `db:"thesis_id"`
	ThesisVersion  int        `db:"thesis_version"`
	RunDate        time.Time  `db:"run_date"`
	JobType        string     `db:"job_type"`
	Status         string     `db:"status"`
	CurrentStep    string     `db:"current_step"`
	RetryCount     int        `db:"retry_count"`
	ErrorMessage   *string    `db:"error_message"`
	CreatedAt      time.Time  `db:"created_at"`
	StartedAt      *time.Time `db:"started_at"`
	UpdatedAt      time.Time  `db:"updated_at"`
	FinishedAt     *time.Time `db:"finished_at"`
}

type JobCandidate struct {
	ID               int64           `db:"id"`
	JobID            int64           `db:"job_id"`
	Source           string          `db:"source"`
	SourceID         string          `db:"source_id"`
	SourceURL        string          `db:"source_url"`
	Symbol           string          `db:"symbol"`
	Title            string          `db:"title"`
	Summary          string          `db:"summary"`
	PublishedAt      *time.Time      `db:"published_at"`
	RawPayload       json.RawMessage `db:"raw_payload"`
	RelevanceStatus  string          `db:"relevance_status"`
	CreatedAt        time.Time       `db:"created_at"`
}

// ------------------------------------------------------------
// Candidate / Evidence
// ------------------------------------------------------------

type RelevantCandidate struct {
	ID                         int64           `db:"id"`
	Source                     string          `db:"source"`
	SourceID                   string          `db:"source_id"`
	SourceURL                  string          `db:"source_url"`
	Symbol                     string          `db:"symbol"`
	Title                      string          `db:"title"`
	Summary                    string          `db:"summary"`
	NormalizedText             string          `db:"normalized_text"`
	Embedding                  *pgvector.Vector `db:"embedding"`
	EmbeddingModel             string          `db:"embedding_model"`
	DedupStatus                string          `db:"dedup_status"`
	DuplicateOfID              *int64          `db:"duplicate_of_id"`
	EventGroupID               *string         `db:"event_group_id"`
	MatchedAssumptionsSnapshot json.RawMessage `db:"matched_assumptions_snapshot"`
	LLMModel                   string          `db:"llm_model"`
	PublishedAt                *time.Time      `db:"published_at"`
	EvidenceStatus             string          `db:"evidence_status"`
	CreatedAt                  time.Time       `db:"created_at"`
	UpdatedAt                  time.Time       `db:"updated_at"`
}

type CandidateAssumption struct {
	ID            int64     `db:"id"`
	CandidateID   int64     `db:"candidate_id"`
	AssumptionID  int64     `db:"assumption_id"`
	Relevance     float64   `db:"relevance"`
	Confidence    float64   `db:"confidence"`
	InitialImpact float64   `db:"initial_impact"`
	Reason        string    `db:"reason"`
	LLMModel      string    `db:"llm_model"`
	CreatedAt     time.Time `db:"created_at"`
}

type CandidateChunk struct {
	ID             int64            `db:"id"`
	CandidateID    int64            `db:"candidate_id"`
	ChunkIndex     int              `db:"chunk_index"`
	Text           string           `db:"text"`
	Embedding      *pgvector.Vector `db:"embedding"`
	EmbeddingModel string           `db:"embedding_model"`
	TokenCount     int              `db:"token_count"`
	CreatedAt      time.Time        `db:"created_at"`
}

type EvidenceSnippet struct {
	ID              int64      `db:"id"`
	AssumptionID    int64      `db:"assumption_id"`
	CandidateID     int64      `db:"candidate_id"`
	CandidateSource string     `db:"candidate_source"`
	CandidateURL    string     `db:"candidate_url"`
	CandidateTitle  string     `db:"candidate_title"`
	SnippetText     string     `db:"snippet_text"`
	JudgeStage      string     `db:"judge_stage"`
	Relevance       float64    `db:"relevance"`
	Confidence      float64    `db:"confidence"`
	Stance          string     `db:"stance"`
	Impact          float64    `db:"impact"`
	SourceWeight    float64    `db:"source_weight"`
	NoveltyWeight   float64    `db:"novelty_weight"`
	PublishedAt     *time.Time `db:"published_at"`
	Reason          string     `db:"reason"`
	LLMModel        string     `db:"llm_model"`
	CreatedAt       time.Time  `db:"created_at"`
}

// ------------------------------------------------------------
// Score / Report
// ------------------------------------------------------------

type AssumptionScoreHistory struct {
	ID                    int64     `db:"id"`
	AssumptionID          int64     `db:"assumption_id"`
	ThesisID              int64     `db:"thesis_id"`
	RunDate               time.Time `db:"run_date"`
	ScoreBefore           float64   `db:"score_before"`
	ScoreAfter            float64   `db:"score_after"`
	ScoreDelta            float64   `db:"score_delta"`
	DailyEffect           float64   `db:"daily_effect"`
	PositiveEvidenceCount int       `db:"positive_evidence_count"`
	NegativeEvidenceCount int       `db:"negative_evidence_count"`
	NeutralEvidenceCount  int       `db:"neutral_evidence_count"`
	TopEvidenceSnippetIDs []int64   `db:"top_evidence_snippet_ids"`
	Reason                string    `db:"reason"`
	CreatedAt             time.Time `db:"created_at"`
}

type ThesisScoreHistory struct {
	ID                   int64     `db:"id"`
	ThesisID             int64     `db:"thesis_id"`
	RunDate              time.Time `db:"run_date"`
	ScoreBefore          float64   `db:"score_before"`
	ScoreAfter           float64   `db:"score_after"`
	ScoreDelta           float64   `db:"score_delta"`
	AssumptionCount      int       `db:"assumption_count"`
	StrongestAssumptionID *int64   `db:"strongest_assumption_id"`
	WeakestAssumptionID  *int64    `db:"weakest_assumption_id"`
	ChangedAssumptionIDs []int64   `db:"changed_assumption_ids"`
	Reason               string    `db:"reason"`
	CreatedAt            time.Time `db:"created_at"`
}

type DailyReport struct {
	ID                int64           `db:"id"`
	UserID            int64           `db:"user_id"`
	ThesisID          int64           `db:"thesis_id"`
	RunDate           time.Time       `db:"run_date"`
	Title             string          `db:"title"`
	ThesisScoreBefore float64         `db:"thesis_score_before"`
	ThesisScoreAfter  float64         `db:"thesis_score_after"`
	ThesisScoreDelta  float64         `db:"thesis_score_delta"`
	Summary           string          `db:"summary"`
	MarkdownReport    string          `db:"markdown_report"`
	MarketContext     json.RawMessage `db:"market_context"`
	AlertLevel        string          `db:"alert_level"`
	CreatedAt         time.Time       `db:"created_at"`
}
