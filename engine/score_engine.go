package engine

import (
	"math"
	"sort"
)

const (
	DefaultLearningRate    = 0.2
	DefaultTopSnippetCount = 5
	ScoreChangeThreshold   = 0.01
)

// ScoreConfig controls ScoreEngine behaviour.
type ScoreConfig struct {
	// LearningRate scales how strongly today's evidence shifts an assumption score.
	// Defaults to DefaultLearningRate (0.2) when zero.
	LearningRate float64
	// TopSnippetCount is how many snippet IDs to record in the score history.
	// Defaults to DefaultTopSnippetCount (5) when zero.
	TopSnippetCount int
}

// ScoreEngine computes assumption scores and the thesis score from evidence
// snippets. It is a pure, stateless calculation — no DB or LLM calls.
type ScoreEngine struct {
	cfg ScoreConfig
}

// NewScoreEngine creates a ScoreEngine with the given config.
func NewScoreEngine(cfg ScoreConfig) *ScoreEngine {
	if cfg.LearningRate <= 0 {
		cfg.LearningRate = DefaultLearningRate
	}
	if cfg.TopSnippetCount <= 0 {
		cfg.TopSnippetCount = DefaultTopSnippetCount
	}
	return &ScoreEngine{cfg: cfg}
}

// ------------------------------------------------------------
// Input types
// ------------------------------------------------------------

// AssumptionInput is everything ScoreEngine needs for one assumption.
type AssumptionInput struct {
	AssumptionID int64
	// CurrentScore is the assumption's score before today's run (from DB).
	CurrentScore float64
	// Importance is the normalized weight of this assumption within the thesis.
	// All importances in a thesis must sum to 1.
	Importance float64
	Snippets   []SnippetInput
}

// SnippetInput carries the scoring-relevant fields of one evidence snippet.
type SnippetInput struct {
	ID            int64
	Relevance     float64
	Confidence    float64
	Impact        float64 // signed, [-1, 1]; positive = support, negative = contradict
	SourceWeight  float64 // SEC=1.0, manual=0.8, news=0.6
	NoveltyWeight float64 // new_event=1.0, event_update=0.5, duplicate=0
}

// ------------------------------------------------------------
// Output types
// ------------------------------------------------------------

// AssumptionScoreOutput is the result for one assumption after scoring.
type AssumptionScoreOutput struct {
	AssumptionID          int64
	ScoreBefore           float64
	ScoreAfter            float64
	DailyEffect           float64 // Σ evidence_effect
	PositiveEvidenceCount int
	NegativeEvidenceCount int
	NeutralEvidenceCount  int
	// TopEvidenceSnippetIDs holds up to TopSnippetCount snippet IDs sorted by
	// |evidence_effect| descending. Used in the daily report.
	TopEvidenceSnippetIDs []int64
}

// ThesisScoreOutput is the aggregate result for the whole thesis.
type ThesisScoreOutput struct {
	// ScoreBefore is Σ(assumption.CurrentScore × importance) before today.
	ScoreBefore           float64
	// ScoreAfter is Σ(assumption.ScoreAfter × importance) after today.
	ScoreAfter            float64
	AssumptionCount       int
	StrongestAssumptionID *int64  // highest ScoreAfter
	WeakestAssumptionID   *int64  // lowest ScoreAfter
	ChangedAssumptionIDs  []int64 // |ScoreAfter - ScoreBefore| > ScoreChangeThreshold
}

// ------------------------------------------------------------
// Core computation
// ------------------------------------------------------------

// Compute runs the scoring pipeline and returns per-assumption and thesis-level
// outputs. The caller is responsible for writing results to the database.
func (e *ScoreEngine) Compute(assumptions []AssumptionInput) ([]AssumptionScoreOutput, ThesisScoreOutput) {
	outputs := make([]AssumptionScoreOutput, len(assumptions))
	for i, a := range assumptions {
		outputs[i] = e.scoreAssumption(a)
	}
	thesis := e.scoreThesis(assumptions, outputs)
	return outputs, thesis
}

func (e *ScoreEngine) scoreAssumption(a AssumptionInput) AssumptionScoreOutput {
	type effectEntry struct {
		id     int64
		effect float64
	}

	var dailyEffect float64
	var posCount, negCount, neutralCount int
	entries := make([]effectEntry, 0, len(a.Snippets))

	for _, s := range a.Snippets {
		effect := s.Relevance * s.Confidence * s.Impact * s.SourceWeight * s.NoveltyWeight
		dailyEffect += effect
		entries = append(entries, effectEntry{id: s.ID, effect: effect})

		switch {
		case s.Impact > 0:
			posCount++
		case s.Impact < 0:
			negCount++
		default:
			neutralCount++
		}
	}

	newScore := clamp(a.CurrentScore+e.cfg.LearningRate*dailyEffect, 0, 1)

	// Sort by |effect| descending, keep top-N IDs for the report.
	sort.Slice(entries, func(i, j int) bool {
		return math.Abs(entries[i].effect) > math.Abs(entries[j].effect)
	})
	topN := e.cfg.TopSnippetCount
	if topN > len(entries) {
		topN = len(entries)
	}
	topIDs := make([]int64, topN)
	for i := range topN {
		topIDs[i] = entries[i].id
	}

	return AssumptionScoreOutput{
		AssumptionID:          a.AssumptionID,
		ScoreBefore:           a.CurrentScore,
		ScoreAfter:            newScore,
		DailyEffect:           dailyEffect,
		PositiveEvidenceCount: posCount,
		NegativeEvidenceCount: negCount,
		NeutralEvidenceCount:  neutralCount,
		TopEvidenceSnippetIDs: topIDs,
	}
}

func (e *ScoreEngine) scoreThesis(inputs []AssumptionInput, outputs []AssumptionScoreOutput) ThesisScoreOutput {
	if len(outputs) == 0 {
		return ThesisScoreOutput{}
	}

	importanceByID := make(map[int64]float64, len(inputs))
	for _, a := range inputs {
		importanceByID[a.AssumptionID] = a.Importance
	}

	var scoreBefore, scoreAfter float64
	var strongestID, weakestID *int64
	var strongestScore, weakestScore float64
	var changedIDs []int64

	for i, out := range outputs {
		imp := importanceByID[out.AssumptionID]
		scoreBefore += inputs[i].CurrentScore * imp
		scoreAfter += out.ScoreAfter * imp

		id := out.AssumptionID
		if strongestID == nil || out.ScoreAfter > strongestScore {
			strongestID = &id
			strongestScore = out.ScoreAfter
		}
		if weakestID == nil || out.ScoreAfter < weakestScore {
			weakestID = &id
			weakestScore = out.ScoreAfter
		}
		if math.Abs(out.ScoreAfter-out.ScoreBefore) > ScoreChangeThreshold {
			changedIDs = append(changedIDs, out.AssumptionID)
		}
	}

	return ThesisScoreOutput{
		ScoreBefore:           scoreBefore,
		ScoreAfter:            scoreAfter,
		AssumptionCount:       len(outputs),
		StrongestAssumptionID: strongestID,
		WeakestAssumptionID:   weakestID,
		ChangedAssumptionIDs:  changedIDs,
	}
}

// ------------------------------------------------------------
// Helpers
// ------------------------------------------------------------

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
