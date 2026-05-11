package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"alphathesis/agent"
	"alphathesis/client"
)

// Judge deduplicates relevant candidates by comparing a new article against
// historically similar ones found via vector search.
type Judge struct {
	llm   agent.ChatCompleter
	model string
}

// NewJudge creates a DedupJudge.
func NewJudge(llm agent.ChatCompleter, model string) (*Judge, error) {
	if llm == nil {
		return nil, errors.New("llm client is required")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("model is required")
	}
	return &Judge{llm: llm, model: strings.TrimSpace(model)}, nil
}

// ------------------------------------------------------------
// Input / Output types
// ------------------------------------------------------------

// CandidateInfo describes the new article being classified.
type CandidateInfo struct {
	Title          string
	Summary        string
	NormalizedText string
	PublishedAt    *time.Time
}

// SimilarCandidate is one historically similar article returned by vector search.
// The LLM uses title/summary/published_at to compare — it never sees embeddings.
type SimilarCandidate struct {
	ID          int64
	Title       string
	Summary     string
	PublishedAt *time.Time
}

// Input groups everything the LLM needs to make a dedup judgment.
type Input struct {
	Candidate        CandidateInfo
	SimilarCandidates []SimilarCandidate
}

// Judgment is the dedup result for one candidate.
type Judgment struct {
	// Status is one of: duplicate, event_update, new_event.
	Status string
	// RelatedID is the ID of the most closely related historical candidate.
	// Non-nil for duplicate and event_update; nil for new_event.
	RelatedID *int64
	Reason    string
}

// ------------------------------------------------------------
// Judge
// ------------------------------------------------------------

// Judge classifies the candidate. When SimilarCandidates is empty the result
// is always new_event without calling the LLM.
func (j *Judge) Judge(ctx context.Context, input Input) (*Judgment, error) {
	if strings.TrimSpace(input.Candidate.Title) == "" && strings.TrimSpace(input.Candidate.Summary) == "" {
		return nil, errors.New("candidate title and summary are both empty")
	}

	// No similar candidates → trivially a new event, skip the LLM call.
	if len(input.SimilarCandidates) == 0 {
		return &Judgment{Status: "new_event", Reason: "no similar historical candidates found"}, nil
	}

	temperature := 0.1
	maxTokens := 800

	resp, err := j.llm.CreateChatCompletion(ctx, client.ChatCompletionRequest{
		Model: j.model,
		Messages: []client.ChatMessage{
			{Role: "system", Content: systemPrompt()},
			{Role: "user", Content: buildUserMessage(input)},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Extra: map[string]interface{}{
			"response_format": map[string]interface{}{"type": "json_object"},
			"chat_template_kwargs": map[string]interface{}{"enable_thinking": false},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("dedup judge llm call: %w", err)
	}

	msg, err := agent.FirstChoiceMessage(resp)
	if err != nil {
		return nil, err
	}
	content, err := agent.ChatContentString(msg.Content)
	if err != nil {
		return nil, err
	}

	judgment, err := parseJudgment(content)
	if err != nil {
		return nil, fmt.Errorf("parse dedup judgment: %w", err)
	}

	normalizeJudgment(judgment, input.SimilarCandidates)
	return judgment, nil
}

// ------------------------------------------------------------
// Prompt
// ------------------------------------------------------------

func systemPrompt() string {
	return `你是 DedupJudgeAgent。

任务：判断一篇新文章是否和历史文章重复。

三种结果：
- duplicate: 新文章和某篇历史文章报道的是同一个事件，内容实质相同（换了措辞或不同来源的转载）。
- event_update: 新文章是某个已有事件的后续更新（例如：公告发布 → 分析师解读 → 后续跟踪报道）。
- new_event: 全新事件，与任何历史文章都不相关。

判断依据：
- 比较事件核心（什么公司、什么事情、大致时间），不要因为措辞不同就判为 new_event。
- 如果发布时间差距超过 7 天且事件完全相同，通常是 event_update 而非 duplicate。
- 如果是不同事件（例如不同季度的财报、不同产品发布），即使公司相同也应判为 new_event。

只返回一个 JSON object，不要 markdown 或解释：
{
  "status": "duplicate" 或 "event_update" 或 "new_event",
  "related_candidate_id": 123,
  "reason": "简短说明"
}

说明：
- related_candidate_id: 如果是 duplicate 或 event_update，填写最相关的历史文章 ID；如果是 new_event，填 null。`
}

func buildUserMessage(input Input) string {
	var sb strings.Builder

	sb.WriteString("new article:\n")
	sb.WriteString("  title: ")
	sb.WriteString(input.Candidate.Title)
	sb.WriteString("\n  summary: ")
	sb.WriteString(input.Candidate.Summary)
	if input.Candidate.PublishedAt != nil {
		sb.WriteString("\n  published_at: ")
		sb.WriteString(input.Candidate.PublishedAt.Format("2006-01-02"))
	}

	sb.WriteString("\n\nhistorical similar articles:\n")
	for _, s := range input.SimilarCandidates {
		sb.WriteString(fmt.Sprintf("  - id: %d\n", s.ID))
		sb.WriteString("    title: ")
		sb.WriteString(s.Title)
		sb.WriteString("\n    summary: ")
		sb.WriteString(s.Summary)
		if s.PublishedAt != nil {
			sb.WriteString("\n    published_at: ")
			sb.WriteString(s.PublishedAt.Format("2006-01-02"))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// ------------------------------------------------------------
// Parse & normalize
// ------------------------------------------------------------

type rawJudgment struct {
	Status             string  `json:"status"`
	RelatedCandidateID *int64  `json:"related_candidate_id"`
	Reason             string  `json:"reason"`
}

func parseJudgment(content string) (*Judgment, error) {
	var raw rawJudgment
	if err := json.Unmarshal([]byte(agent.ExtractJSONObject(content)), &raw); err != nil {
		return nil, fmt.Errorf("decode dedup judgment json: %w", err)
	}
	return &Judgment{
		Status:    strings.TrimSpace(raw.Status),
		RelatedID: raw.RelatedCandidateID,
		Reason:    strings.TrimSpace(raw.Reason),
	}, nil
}

func normalizeJudgment(j *Judgment, similar []SimilarCandidate) {
	// Validate and normalize status.
	switch j.Status {
	case "duplicate", "event_update", "new_event":
		// valid
	default:
		// Unknown status → treat as new_event to avoid discarding evidence.
		j.Status = "new_event"
		j.RelatedID = nil
		return
	}

	// For new_event, RelatedID must be nil.
	if j.Status == "new_event" {
		j.RelatedID = nil
		return
	}

	// For duplicate/event_update, RelatedID must reference a known similar candidate.
	if j.RelatedID == nil {
		// LLM forgot to fill it in — pick the first similar candidate.
		j.RelatedID = &similar[0].ID
		return
	}
	for _, s := range similar {
		if s.ID == *j.RelatedID {
			return // valid reference
		}
	}
	// LLM hallucinated an ID — fall back to first similar candidate.
	j.RelatedID = &similar[0].ID
}
