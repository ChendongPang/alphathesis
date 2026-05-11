package manual

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"alphathesis/datasource"
	"alphathesis/store"
)

type ManualEvidenceInput struct {
	Symbol      string
	Title       string
	Summary     string
	SourceURL   string
	PublishedAt *time.Time
	RawText     string
}

func NewManualEvidenceCandidate(input ManualEvidenceInput) (store.CreateJobCandidateParams, error) {
	title := strings.TrimSpace(input.Title)
	summary := strings.TrimSpace(input.Summary)
	rawText := strings.TrimSpace(input.RawText)
	if title == "" && summary == "" && rawText == "" {
		return store.CreateJobCandidateParams{}, errors.New("manual evidence requires title, summary, or raw text")
	}
	if summary == "" {
		summary = rawText
	}
	if title == "" {
		title = "Manual evidence"
	}

	payload := map[string]any{
		"title":        title,
		"summary":      summary,
		"raw_text":     rawText,
		"source_url":   strings.TrimSpace(input.SourceURL),
		"published_at": input.PublishedAt,
	}
	return store.CreateJobCandidateParams{
		Source:      datasource.SourceManual,
		SourceID:    manualSourceID(input),
		SourceURL:   strings.TrimSpace(input.SourceURL),
		Symbol:      strings.TrimSpace(strings.ToUpper(input.Symbol)),
		Title:       title,
		Summary:     summary,
		PublishedAt: input.PublishedAt,
		RawPayload:  datasource.MarshalRaw(payload),
	}, nil
}

func manualSourceID(input ManualEvidenceInput) string {
	parts := []string{
		strings.TrimSpace(strings.ToUpper(input.Symbol)),
		strings.TrimSpace(input.SourceURL),
		strings.TrimSpace(input.Title),
		strings.TrimSpace(input.Summary),
		strings.TrimSpace(input.RawText),
	}
	if input.PublishedAt != nil {
		parts = append(parts, input.PublishedAt.UTC().Format(time.RFC3339))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}
