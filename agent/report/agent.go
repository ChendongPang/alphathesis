package report

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"alphathesis/agent"
	"alphathesis/client"
	"alphathesis/datasource"
	"alphathesis/engine"
	"alphathesis/store"
)

// Agent generates the daily markdown thesis monitoring report.
type Agent struct {
	llm   agent.ChatCompleter
	model string
}

func New(llm agent.ChatCompleter, model string) *Agent {
	return &Agent{llm: llm, model: model}
}

// AssumptionResult is the per-assumption data the Agent needs for the report.
type AssumptionResult struct {
	Key          string
	Text         string
	ScoreBefore  float64
	ScoreAfter   float64
	DailyEffect  float64
	PosCount     int
	NegCount     int
	NeutralCount int
	TopSnippets  []*store.EvidenceSnippet
}

// Input is everything the Agent needs to produce a daily report.
type Input struct {
	Thesis            *store.Thesis
	RunDate           time.Time
	ThesisScoreBefore float64
	ThesisScoreAfter  float64
	Results           []AssumptionResult
	MarketCtx         engine.MarketContext
}

// Output is the structured report produced by the Agent.
type Output struct {
	Title          string
	Summary        string
	MarkdownReport string
	AlertLevel     string
}

// Generate calls the LLM to produce a daily report from pre-loaded score and
// evidence data.
func (a *Agent) Generate(ctx context.Context, input Input) (*Output, error) {
	alertLevel := computeAlertLevel(input.ThesisScoreAfter-input.ThesisScoreBefore, input.MarketCtx.AlertLevel)

	temperature := 0.3
	maxTokens := 1200

	resp, err := a.llm.CreateChatCompletion(ctx, client.ChatCompletionRequest{
		Model: a.model,
		Messages: []client.ChatMessage{
			{Role: "system", Content: systemPrompt()},
			{Role: "user", Content: buildUserMessage(input, alertLevel)},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Extra: map[string]any{
			"response_format": map[string]any{"type": "json_object"},
			"chat_template_kwargs": map[string]any{"enable_thinking": false},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("report agent llm call: %w", err)
	}

	msg, err := agent.FirstChoiceMessage(resp)
	if err != nil {
		return nil, err
	}
	content, err := agent.ChatContentString(msg.Content)
	if err != nil {
		return nil, err
	}

	out, err := parseOutput(content)
	if err != nil {
		return nil, fmt.Errorf("parse report output: %w", err)
	}
	if out.AlertLevel == "" {
		out.AlertLevel = alertLevel
	}
	return out, nil
}

func systemPrompt() string {
	return `你是 ReportAgent，负责生成每日 thesis 监控报告。

根据提供的 thesis 信息、分数变化、证据片段和市场数据，生成结构化报告。

要求：
- title：简洁的报告标题，包含公司名、日期
- summary：2-3 句话，直接说明今日最重要的发现
- markdown_body：完整的 Markdown 报告，包含分数变化、关键证据（news 和 event 分开展示）、市场表现；对每条证据，若提供了 url 和 title 字段，必须使用 title 字段的原文作为链接文字，格式为 [title原文](url)，严禁用 assumption key 或自己编造的文字替换 title；没有 url 字段的证据直接展示文本
- alert_level：high / medium / low / none，基于提供的数据

只返回一个 JSON object，不要 markdown 或解释：
{
  "title": "...",
  "summary": "...",
  "markdown_body": "...",
  "alert_level": "high|medium|low|none"
}`
}

func buildUserMessage(input Input, alertLevel string) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "thesis: %s (%s) — %s\n", input.Thesis.CompanyName, input.Thesis.Symbol, input.Thesis.Direction)
	fmt.Fprintf(&sb, "core_claim: %s\n", input.Thesis.CoreClaim)
	fmt.Fprintf(&sb, "date: %s\n\n", input.RunDate.Format("2006-01-02"))

	fmt.Fprintf(&sb, "thesis_score: %.3f → %.3f (%+.3f)\n",
		input.ThesisScoreBefore, input.ThesisScoreAfter,
		input.ThesisScoreAfter-input.ThesisScoreBefore)
	fmt.Fprintf(&sb, "alert_level_suggested: %s\n\n", alertLevel)

	if input.MarketCtx.Symbol != "" {
		fmt.Fprintf(&sb, "market:\n")
		fmt.Fprintf(&sb, "  stock (%s): open=%.2f close=%.2f return=%+.2f%%\n",
			input.MarketCtx.Symbol,
			input.MarketCtx.StockOpen, input.MarketCtx.StockClose,
			input.MarketCtx.StockReturn*100)
		fmt.Fprintf(&sb, "  %s: return=%+.2f%%\n",
			input.MarketCtx.MarketETF, input.MarketCtx.MarketReturn*100)
		fmt.Fprintf(&sb, "  relative_return: %+.2f%%\n\n", input.MarketCtx.RelativeReturn*100)
	}

	sb.WriteString("assumptions:\n")
	for _, r := range input.Results {
		fmt.Fprintf(&sb, "  - key: %s\n    text: %s\n", r.Key, r.Text)
		fmt.Fprintf(&sb, "    score: %.3f → %.3f (%+.3f)  evidence: +%d -%d ~%d\n",
			r.ScoreBefore, r.ScoreAfter, r.ScoreAfter-r.ScoreBefore,
			r.PosCount, r.NegCount, r.NeutralCount)
		var news, events []*store.EvidenceSnippet
		for _, s := range r.TopSnippets {
			if datasource.IsNewsSource(s.CandidateSource) {
				news = append(news, s)
			} else {
				events = append(events, s)
			}
		}
		for i, s := range news {
			if i >= 2 {
				break
			}
			fmt.Fprintf(&sb, "    news[%d]: [%s impact=%.2f] %s\n",
				i+1, s.Stance, s.Impact, truncateRunes(s.SnippetText, 80))
		}
		for i, s := range events {
			if i >= 2 {
				break
			}
			fmt.Fprintf(&sb, "    event[%d]: [%s impact=%.2f] %s\n",
				i+1, s.Stance, s.Impact, truncateRunes(s.SnippetText, 80))
		}
	}

	return sb.String()
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

type rawOutput struct {
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	MarkdownBody string `json:"markdown_body"`
	AlertLevel   string `json:"alert_level"`
}

func parseOutput(content string) (*Output, error) {
	var raw rawOutput
	if err := json.Unmarshal([]byte(agent.ExtractJSONObject(content)), &raw); err != nil {
		return nil, fmt.Errorf("decode report json: %w", err)
	}

	level := strings.ToLower(strings.TrimSpace(raw.AlertLevel))
	switch level {
	case "high", "medium", "low", "none":
	default:
		level = "none"
	}

	return &Output{
		Title:          strings.TrimSpace(raw.Title),
		Summary:        strings.TrimSpace(raw.Summary),
		MarkdownReport: strings.TrimSpace(raw.MarkdownBody),
		AlertLevel:     level,
	}, nil
}

// computeAlertLevel returns the more severe of the market alert and the
// score-delta-based alert.
func computeAlertLevel(scoreDelta float64, marketAlertLevel string) string {
	rank := map[string]int{"none": 0, "low": 1, "medium": 2, "high": 3}
	levels := []string{"none", "low", "medium", "high"}

	abs := math.Abs(scoreDelta)
	var scoreRank int
	switch {
	case abs >= 0.10:
		scoreRank = 3
	case abs >= 0.05:
		scoreRank = 2
	case abs >= 0.01:
		scoreRank = 1
	}

	mr := rank[marketAlertLevel]
	if scoreRank > mr {
		return levels[scoreRank]
	}
	return levels[mr]
}
