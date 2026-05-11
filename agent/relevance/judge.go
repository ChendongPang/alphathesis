package relevance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"alphathesis/agent"
	"alphathesis/client"
)

// Judge judges whether a news article or filing is relevant to any of a
// thesis's assumptions.
type Judge struct {
	llm   agent.ChatCompleter
	model string
}

// NewJudge creates a RelevanceJudge.
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

// Input is the data the LLM receives to make a relevance judgment.
type Input struct {
	Title       string
	Summary     string
	CoreClaim   string
	Assumptions []AssumptionHint
}

// AssumptionHint is a lightweight view of one assumption for the judge.
type AssumptionHint struct {
	ID            int64
	Key           string
	Text          string
	EvidenceHints []string
}

// Judgment is the parsed and normalized output of the relevance judge.
type Judgment struct {
	// Relevant is true when at least one assumption matched.
	Relevant           bool
	MatchedAssumptions []MatchedAssumption
	Reason             string
}

// MatchedAssumption is the relevance result for one matched assumption.
type MatchedAssumption struct {
	AssumptionID  int64
	AssumptionKey string
	// Relevance is how directly the article addresses this assumption [0, 1].
	Relevance float64
	// Confidence is the LLM's certainty in this judgment [0, 1].
	Confidence float64
	// InitialImpact is the expected direction: positive = support, negative =
	// contradict, near-zero = neutral. Range [-1, 1].
	InitialImpact float64
	Reason        string
}

// ------------------------------------------------------------
// Judge
// ------------------------------------------------------------

// Judge asks the LLM whether the article is relevant to the given thesis
// assumptions and returns a normalized judgment.
func (j *Judge) Judge(ctx context.Context, input Input) (*Judgment, error) {
	if strings.TrimSpace(input.Title) == "" && strings.TrimSpace(input.Summary) == "" {
		return nil, errors.New("article title and summary are both empty")
	}
	if len(input.Assumptions) == 0 {
		return nil, errors.New("at least one assumption is required")
	}

	temperature := 0.1
	maxTokens := 1200

	resp, err := j.llm.CreateChatCompletion(ctx, client.ChatCompletionRequest{
		Model: j.model,
		Messages: []client.ChatMessage{
			{Role: "system", Content: systemPrompt()},
			{Role: "user", Content: buildUserMessage(input)},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Extra: map[string]any{
			"response_format": map[string]any{"type": "json_object"},
			"chat_template_kwargs": map[string]any{"enable_thinking": false},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("relevance judge llm call: %w", err)
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
		return nil, fmt.Errorf("parse relevance judgment: %w", err)
	}

	normalizeJudgment(judgment, input.Assumptions)
	return judgment, nil
}

// ------------------------------------------------------------
// Prompt
// ------------------------------------------------------------

func systemPrompt() string {
	return `你是 RelevanceJudgeAgent。

任务：判断一篇文章（title + summary）是否和投资 thesis 的 assumptions 相关。

输入包含：
- article: 文章 title 和 summary
- core_claim: thesis 的核心主张
- assumptions: 若干个可验证假设，每个有 key、text、evidence_hints

判断规则：
- 支持或反驳某个 assumption 的文章，同样算相关。反驳证据（例如业绩大幅超预期、市场份额逆势增长）与支持证据同等重要，不要因为"方向不对"而判为不相关。
- 以下内容视为相关：财报数据（营收、利润、毛利率）、竞争对手动态、行业政策、价格数据（如碳酸锂、电池原材料）、融资/资本运作（可能影响估值）、市场渗透率数据。
- 以下内容才算不相关：纯粹的人事变动、与 thesis 所有假设均无任何关联的行业外新闻、单纯提及公司名但无实质内容。
- 遇到模棱两可的情况，倾向于判为相关（宁可多判）。
- 如果文章是英文，assumptions 是中文，仍需正确判断跨语言相关性。
- 一篇文章可以同时匹配多个 assumptions。

字段定义：
- relevance: 文章与该 assumption 的相关程度，0=几乎无关，1=直接相关
- confidence: 你对这个判断的确信程度，0=不确定，1=非常确定
- initial_impact: 文章对该 assumption 的初步影响方向和强度
    正数 = 支持 assumption（例如业绩超预期、市场份额增加）
    负数 = 反驳 assumption（例如竞争加剧、需求下滑）
    接近 0 = 中性或信息不足
    范围 [-1, 1]

只返回一个 JSON object，不要 markdown 或解释：
{
  "relevant": true 或 false,
  "matched_assumptions": [
    {
      "assumption_key": "对应 assumption 的 key",
      "relevance": 0.0,
      "confidence": 0.0,
      "initial_impact": 0.0,
      "reason": "简短说明"
    }
  ],
  "reason": "整体判断说明，如果不相关请说明原因"
}`
}

func buildUserMessage(input Input) string {
	var sb strings.Builder
	sb.WriteString("article:\n")
	sb.WriteString("  title: ")
	sb.WriteString(input.Title)
	sb.WriteString("\n  summary: ")
	sb.WriteString(input.Summary)
	sb.WriteString("\n\ncore_claim: ")
	sb.WriteString(input.CoreClaim)
	sb.WriteString("\n\nassumptions:\n")
	for _, a := range input.Assumptions {
		sb.WriteString("  - key: ")
		sb.WriteString(a.Key)
		sb.WriteString("\n    text: ")
		sb.WriteString(a.Text)
		if len(a.EvidenceHints) > 0 {
			sb.WriteString("\n    evidence_hints:")
			for _, h := range a.EvidenceHints {
				sb.WriteString("\n      - ")
				sb.WriteString(h)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// ------------------------------------------------------------
// Parse & normalize
// ------------------------------------------------------------

type rawJudgment struct {
	Relevant           bool `json:"relevant"`
	MatchedAssumptions []struct {
		AssumptionKey string  `json:"assumption_key"`
		Relevance     float64 `json:"relevance"`
		Confidence    float64 `json:"confidence"`
		InitialImpact float64 `json:"initial_impact"`
		Reason        string  `json:"reason"`
	} `json:"matched_assumptions"`
	Reason string `json:"reason"`
}

func parseJudgment(content string) (*Judgment, error) {
	var raw rawJudgment
	if err := json.Unmarshal([]byte(agent.ExtractJSONObject(content)), &raw); err != nil {
		return nil, fmt.Errorf("decode relevance judgment json (content: %.200s): %w", content, err)
	}
	j := &Judgment{
		Relevant: raw.Relevant,
		Reason:   strings.TrimSpace(raw.Reason),
	}
	for _, m := range raw.MatchedAssumptions {
		j.MatchedAssumptions = append(j.MatchedAssumptions, MatchedAssumption{
			AssumptionKey: strings.TrimSpace(m.AssumptionKey),
			Relevance:     m.Relevance,
			Confidence:    m.Confidence,
			InitialImpact: m.InitialImpact,
			Reason:        strings.TrimSpace(m.Reason),
		})
	}
	return j, nil
}

func normalizeJudgment(j *Judgment, hints []AssumptionHint) {
	keyToHint := make(map[string]AssumptionHint, len(hints))
	for _, h := range hints {
		keyToHint[h.Key] = h
	}

	filtered := j.MatchedAssumptions[:0]
	for _, m := range j.MatchedAssumptions {
		h, ok := keyToHint[m.AssumptionKey]
		if !ok {
			continue
		}
		m.AssumptionID = h.ID
		m.Relevance = agent.ClampFloat(m.Relevance, 0, 1)
		m.Confidence = agent.ClampFloat(m.Confidence, 0, 1)
		m.InitialImpact = agent.ClampFloat(m.InitialImpact, -1, 1)
		filtered = append(filtered, m)
	}
	j.MatchedAssumptions = filtered
	j.Relevant = len(j.MatchedAssumptions) > 0
}
