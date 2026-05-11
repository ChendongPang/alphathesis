package evidence

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

const (
	StageSummary = "summary"
	StageRAG     = "rag"

	StanceSupport    = "support"
	StanceContradict = "contradict"
	StanceNeutral    = "neutral"
)

// Judge extracts evidence snippets from a candidate article and scores each
// against a set of thesis assumptions.
type Judge struct {
	llm   agent.ChatCompleter
	model string
}

// NewJudge creates an EvidenceJudge.
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

// AssumptionTarget is one assumption the judge must evaluate.
type AssumptionTarget struct {
	ID            int64
	Key           string
	Text          string
	EvidenceHints []string
	// TopChunks, when non-empty, triggers RAG mode for this assumption.
	// The caller retrieves these via vector search before calling Judge.
	TopChunks []string
}

// Input is everything the judge needs for one candidate article.
type Input struct {
	Title       string
	Summary     string
	PublishedAt *time.Time
	Source      string
	Assumptions []AssumptionTarget
}

// AssumptionEvidence is the evidence judgment for one (candidate, assumption) pair.
type AssumptionEvidence struct {
	AssumptionID  int64
	AssumptionKey string
	SnippetText   string
	// JudgeStage is "summary" or "rag".
	JudgeStage string
	Relevance  float64
	Confidence float64
	// Stance is "support", "contradict", or "neutral".
	Stance string
	// Impact is [-1, 1]; positive = supporting evidence, negative = contradicting.
	Impact float64
	Reason string
}

// Judgment is the full evidence output for one candidate.
type Judgment struct {
	Evidences []AssumptionEvidence
}

// ------------------------------------------------------------
// Judge
// ------------------------------------------------------------

// Judge produces evidence snippets for all assumptions in input.
// Assumptions that have TopChunks set are judged in RAG mode (one LLM call
// each); the rest are batched into a single summary-mode call.
func (j *Judge) Judge(ctx context.Context, input Input) (*Judgment, error) {
	if len(input.Assumptions) == 0 {
		return nil, errors.New("at least one assumption is required")
	}

	var summaryTargets, ragTargets []AssumptionTarget
	for _, a := range input.Assumptions {
		if len(a.TopChunks) > 0 {
			ragTargets = append(ragTargets, a)
		} else {
			summaryTargets = append(summaryTargets, a)
		}
	}

	var evidences []AssumptionEvidence

	if len(summaryTargets) > 0 {
		evs, err := j.judgeSummaryBatch(ctx, input, summaryTargets)
		if err != nil {
			return nil, err
		}
		evidences = append(evidences, evs...)
	}

	for _, target := range ragTargets {
		ev, err := j.judgeRAGSingle(ctx, input, target)
		if err != nil {
			return nil, err
		}
		evidences = append(evidences, ev)
	}

	return &Judgment{Evidences: evidences}, nil
}

// ------------------------------------------------------------
// LLM calls
// ------------------------------------------------------------

func (j *Judge) judgeSummaryBatch(ctx context.Context, input Input, targets []AssumptionTarget) ([]AssumptionEvidence, error) {
	temperature := 0.1
	maxTokens := 1500

	resp, err := j.llm.CreateChatCompletion(ctx, client.ChatCompletionRequest{
		Model: j.model,
		Messages: []client.ChatMessage{
			{Role: "system", Content: systemPrompt()},
			{Role: "user", Content: buildSummaryMessage(input, targets)},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Extra: map[string]any{
			"response_format": map[string]any{"type": "json_object"},
			"chat_template_kwargs": map[string]any{"enable_thinking": false},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("evidence judge summary llm call: %w", err)
	}

	msg, err := agent.FirstChoiceMessage(resp)
	if err != nil {
		return nil, err
	}
	content, err := agent.ChatContentString(msg.Content)
	if err != nil {
		return nil, err
	}

	evs, err := parseEvidences(content, StageSummary, targets)
	if err != nil {
		return nil, fmt.Errorf("parse evidence judgment (summary): %w", err)
	}
	return evs, nil
}

func (j *Judge) judgeRAGSingle(ctx context.Context, input Input, target AssumptionTarget) (AssumptionEvidence, error) {
	temperature := 0.1
	maxTokens := 800

	resp, err := j.llm.CreateChatCompletion(ctx, client.ChatCompletionRequest{
		Model: j.model,
		Messages: []client.ChatMessage{
			{Role: "system", Content: systemPrompt()},
			{Role: "user", Content: buildRAGMessage(input, target)},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Extra: map[string]any{
			"response_format": map[string]any{"type": "json_object"},
			"chat_template_kwargs": map[string]any{"enable_thinking": false},
		},
	})
	if err != nil {
		return AssumptionEvidence{}, fmt.Errorf("evidence judge rag llm call: %w", err)
	}

	msg, err := agent.FirstChoiceMessage(resp)
	if err != nil {
		return AssumptionEvidence{}, err
	}
	content, err := agent.ChatContentString(msg.Content)
	if err != nil {
		return AssumptionEvidence{}, err
	}

	evs, err := parseEvidences(content, StageRAG, []AssumptionTarget{target})
	if err != nil {
		return AssumptionEvidence{}, fmt.Errorf("parse evidence judgment (rag): %w", err)
	}
	if len(evs) == 0 {
		return AssumptionEvidence{
			AssumptionID:  target.ID,
			AssumptionKey: target.Key,
			JudgeStage:    StageRAG,
			Stance:        StanceNeutral,
			Reason:        "no evidence extracted by llm",
		}, nil
	}
	return evs[0], nil
}

// ------------------------------------------------------------
// Prompts
// ------------------------------------------------------------

func systemPrompt() string {
	return `你是 EvidenceJudgeAgent。

任务：从文章中，针对每个 assumption，提取最有价值的证据片段，并评估其影响。

对每个 assumption，你需要：
1. snippet_text：从文章中直接引用最相关的原文，限 2-3 句话，不要自行总结或改写。
2. stance（立场）：
   - support: 文章内容支持该 assumption（利好证据）
   - contradict: 文章内容反驳该 assumption（利空证据）
   - neutral: 涉及但无明确方向
3. impact [-1, 1]：正数=支持，负数=反驳，绝对值越大影响越强。符号必须与 stance 一致。
4. relevance [0, 1]：文章与该 assumption 的相关程度。
5. confidence [0, 1]：你对此判断的确信程度。

只返回一个 JSON object，不要 markdown 或解释：
{
  "evidences": [
    {
      "assumption_key": "对应 assumption 的 key",
      "snippet_text": "直接引用的原文",
      "relevance": 0.0,
      "confidence": 0.0,
      "stance": "support" 或 "contradict" 或 "neutral",
      "impact": 0.0,
      "reason": "简短说明判断依据"
    }
  ]
}

注意：
- 每个 assumption 只返回一条最有价值的证据条目
- 如果文章与某 assumption 完全无关，relevance 填 0，stance 填 "neutral"，impact 填 0`
}

func buildSummaryMessage(input Input, targets []AssumptionTarget) string {
	var sb strings.Builder
	sb.WriteString("article:\n")
	sb.WriteString("  title: ")
	sb.WriteString(input.Title)
	sb.WriteString("\n  summary: ")
	sb.WriteString(input.Summary)
	if input.PublishedAt != nil {
		sb.WriteString("\n  published_at: ")
		sb.WriteString(input.PublishedAt.Format("2006-01-02"))
	}
	if input.Source != "" {
		sb.WriteString("\n  source: ")
		sb.WriteString(input.Source)
	}
	sb.WriteString("\n\nassumptions:\n")
	for _, a := range targets {
		sb.WriteString("  - key: ")
		sb.WriteString(a.Key)
		sb.WriteString("\n    text: ")
		sb.WriteString(a.Text)
		if len(a.EvidenceHints) > 0 {
			sb.WriteString("\n    evidence_hints: ")
			sb.WriteString(strings.Join(a.EvidenceHints, "; "))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func buildRAGMessage(input Input, target AssumptionTarget) string {
	var sb strings.Builder
	sb.WriteString("article:\n")
	sb.WriteString("  title: ")
	sb.WriteString(input.Title)
	if input.PublishedAt != nil {
		sb.WriteString("\n  published_at: ")
		sb.WriteString(input.PublishedAt.Format("2006-01-02"))
	}
	if input.Source != "" {
		sb.WriteString("\n  source: ")
		sb.WriteString(input.Source)
	}
	sb.WriteString("\n\nrelevant chunks:\n")
	for i, chunk := range target.TopChunks {
		fmt.Fprintf(&sb, "  [%d] %s\n", i+1, chunk)
	}
	sb.WriteString("\nassumptions:\n")
	sb.WriteString("  - key: ")
	sb.WriteString(target.Key)
	sb.WriteString("\n    text: ")
	sb.WriteString(target.Text)
	if len(target.EvidenceHints) > 0 {
		sb.WriteString("\n    evidence_hints: ")
		sb.WriteString(strings.Join(target.EvidenceHints, "; "))
	}
	sb.WriteString("\n")
	return sb.String()
}

// ------------------------------------------------------------
// Parse & normalize
// ------------------------------------------------------------

type rawEvidence struct {
	AssumptionKey string  `json:"assumption_key"`
	SnippetText   string  `json:"snippet_text"`
	Relevance     float64 `json:"relevance"`
	Confidence    float64 `json:"confidence"`
	Stance        string  `json:"stance"`
	Impact        float64 `json:"impact"`
	Reason        string  `json:"reason"`
}

type rawResponse struct {
	Evidences []rawEvidence `json:"evidences"`
}

func parseEvidences(content, stage string, targets []AssumptionTarget) ([]AssumptionEvidence, error) {
	var raw rawResponse
	if err := json.Unmarshal([]byte(agent.ExtractJSONObject(content)), &raw); err != nil {
		return nil, fmt.Errorf("decode evidence judgment json: %w", err)
	}

	keyToID := make(map[string]int64, len(targets))
	for _, t := range targets {
		keyToID[t.Key] = t.ID
	}

	var result []AssumptionEvidence
	seen := make(map[string]bool)
	for _, e := range raw.Evidences {
		key := strings.TrimSpace(e.AssumptionKey)
		id, ok := keyToID[key]
		if !ok {
			continue // hallucinated key
		}
		if seen[key] {
			continue // take first occurrence only
		}
		seen[key] = true

		stance := normalizeStance(strings.TrimSpace(e.Stance))
		impact := normalizeImpact(agent.ClampFloat(e.Impact, -1, 1), stance)
		result = append(result, AssumptionEvidence{
			AssumptionID:  id,
			AssumptionKey: key,
			SnippetText:   strings.TrimSpace(e.SnippetText),
			JudgeStage:    stage,
			Relevance:     agent.ClampFloat(e.Relevance, 0, 1),
			Confidence:    agent.ClampFloat(e.Confidence, 0, 1),
			Stance:        stance,
			Impact:        impact,
			Reason:        strings.TrimSpace(e.Reason),
		})
	}
	return result, nil
}

func normalizeStance(s string) string {
	switch s {
	case StanceSupport, StanceContradict, StanceNeutral:
		return s
	default:
		return StanceNeutral
	}
}

// normalizeImpact flips the sign when impact and stance disagree.
func normalizeImpact(impact float64, stance string) float64 {
	switch stance {
	case StanceSupport:
		if impact < 0 {
			return -impact
		}
	case StanceContradict:
		if impact > 0 {
			return -impact
		}
	}
	return impact
}
