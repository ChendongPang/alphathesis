package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"alphathesis/agent"
	"alphathesis/client"
)

const defaultParserVersion = "thesis_parser_v1"

// ThesisParserAgent turns free-form investment thesis text into structured data.
type ThesisParserAgent struct {
	llm           agent.ChatCompleter
	model         string
	parserVersion string
	cnResolver    SymbolResolver // AKShare — used when market=cn
	hkUsResolver  SymbolResolver // yfinance — used when market=hk or us
}

// ThesisParserOption customizes ThesisParserAgent.
type ThesisParserOption func(*ThesisParserAgent)

// WithParserVersion sets the parser prompt/version marker.
func WithParserVersion(version string) ThesisParserOption {
	return func(a *ThesisParserAgent) {
		if strings.TrimSpace(version) != "" {
			a.parserVersion = strings.TrimSpace(version)
		}
	}
}

// WithCNSymbolResolver sets the resolver used for A-share (cn market) lookups.
func WithCNSymbolResolver(resolver SymbolResolver) ThesisParserOption {
	return func(a *ThesisParserAgent) {
		a.cnResolver = resolver
	}
}

// WithHKUSSymbolResolver sets the resolver used for HK and US market lookups.
func WithHKUSSymbolResolver(resolver SymbolResolver) ThesisParserOption {
	return func(a *ThesisParserAgent) {
		a.hkUsResolver = resolver
	}
}

// NewThesisParserAgent creates a parser agent using a chat-completion model.
func NewThesisParserAgent(llm agent.ChatCompleter, model string, opts ...ThesisParserOption) (*ThesisParserAgent, error) {
	if llm == nil {
		return nil, errors.New("llm client is required")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("model is required")
	}
	a := &ThesisParserAgent{
		llm:           llm,
		model:         strings.TrimSpace(model),
		parserVersion: defaultParserVersion,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	return a, nil
}

// Parse parses raw thesis text into a normalized ParsedThesis.
//
// Flow:
//  1. Small LLM call to detect market (cn/hk/us) and produce a search query.
//  2. Route to the appropriate resolver: AKShare for cn, yfinance for hk/us.
//  3. Full LLM call with resolution results injected to produce the final JSON.
func (a *ThesisParserAgent) Parse(ctx context.Context, rawText string) (*ParsedThesis, error) {
	if strings.TrimSpace(rawText) == "" {
		return nil, errors.New("raw thesis text is required")
	}

	temperature := 0.1
	maxTokens := 1600
	messages := []client.ChatMessage{
		{Role: "system", Content: thesisParserSystemPrompt(a.parserVersion)},
		{Role: "user", Content: rawText},
	}

	if a.cnResolver != nil || a.hkUsResolver != nil {
		resolution, err := a.detectAndResolve(ctx, rawText, temperature)
		if err != nil {
			return nil, err
		}
		if resolution != nil {
			messages = append(messages, client.ChatMessage{
				Role:    "user",
				Content: "股票代码搜索结果（供参考，优先使用）：\n" + marshalToolResult(resolution),
			})
		}
	}

	finalMessage, err := a.requestFinalJSON(ctx, messages, temperature, maxTokens)
	if err != nil {
		return nil, err
	}
	content, err := agent.ChatContentString(finalMessage.Content)
	if err != nil {
		return nil, err
	}
	var parsed ParsedThesis
	if err := json.Unmarshal([]byte(agent.ExtractJSONObject(content)), &parsed); err != nil {
		return nil, fmt.Errorf("decode thesis parser json: %w", err)
	}
	if err := parsed.Normalize(); err != nil {
		return nil, fmt.Errorf("normalize parsed thesis: %w", err)
	}
	return &parsed, nil
}

type marketQueryInfo struct {
	Market      string `json:"market"`
	SearchQuery string `json:"search_query"`
}

// detectAndResolve asks the LLM to identify market + search query, then calls
// the matching resolver (AKShare for cn, yfinance for hk/us).
func (a *ThesisParserAgent) detectAndResolve(ctx context.Context, rawText string, temperature float64) (*SymbolResolution, error) {
	maxTokens := 80
	resp, err := a.llm.CreateChatCompletion(ctx, client.ChatCompletionRequest{
		Model: a.model,
		Messages: []client.ChatMessage{
			{Role: "system", Content: marketDetectSystemPrompt()},
			{Role: "user", Content: rawText},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Extra: map[string]interface{}{
			"response_format":      map[string]interface{}{"type": "json_object"},
			"chat_template_kwargs": map[string]interface{}{"enable_thinking": false},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("market detect llm call: %w", err)
	}
	msg, err := agent.FirstChoiceMessage(resp)
	if err != nil {
		return nil, err
	}
	content, err := agent.ChatContentString(msg.Content)
	if err != nil {
		return nil, err
	}
	var info marketQueryInfo
	if err := json.Unmarshal([]byte(agent.ExtractJSONObject(content)), &info); err != nil {
		return nil, fmt.Errorf("decode market detect json: %w", err)
	}
	info.Market = strings.ToLower(strings.TrimSpace(info.Market))
	info.SearchQuery = strings.TrimSpace(info.SearchQuery)
	if info.SearchQuery == "" {
		return nil, nil
	}

	var resolver SymbolResolver
	if info.Market == "cn" {
		resolver = a.cnResolver
	} else {
		resolver = a.hkUsResolver
	}
	if resolver == nil {
		return nil, nil
	}

	resolution, err := resolver.ResolveSymbol(ctx, info.SearchQuery)
	if err != nil {
		return &SymbolResolution{Query: info.SearchQuery, Source: "error", RawText: err.Error()}, nil
	}
	return resolution, nil
}

func (a *ThesisParserAgent) requestFinalJSON(ctx context.Context, messages []client.ChatMessage, temperature float64, maxTokens int) (client.ChatMessage, error) {
	resp, err := a.llm.CreateChatCompletion(ctx, client.ChatCompletionRequest{
		Model:       a.model,
		Messages:    messages,
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Extra: map[string]interface{}{
			"response_format":      map[string]interface{}{"type": "json_object"},
			"chat_template_kwargs": map[string]interface{}{"enable_thinking": false},
		},
	})
	if err != nil {
		return client.ChatMessage{}, fmt.Errorf("request final thesis json: %w", err)
	}
	return agent.FirstChoiceMessage(resp)
}

func marketDetectSystemPrompt() string {
	return `根据用户的投资 thesis，返回一个 JSON object，不要 markdown 或解释：
{"market": "cn | hk | us", "search_query": "公司名"}

规则：
- market: cn = 中国 A 股，hk = 香港上市，us = 美国上市
- search_query: market=cn 时使用中文公司名（如 西部矿业、贵州茅台）；market=hk 或 us 时使用英文公司名或 ticker（如 Xiaomi、NVIDIA、01810.HK）`
}

func thesisParserSystemPrompt(parserVersion string) string {
	return fmt.Sprintf(`你是 ThesisParserAgent %s。

你的任务：把用户输入的投资 thesis 解析成严格 JSON。
只返回一个 JSON object，不要返回 markdown、解释、注释或代码块。

语言规则非常重要：
- 如果用户 thesis 是中文，company_name、core_claim、assumptions[].text、evidence_hints 必须使用中文。
- 如果用户 thesis 是英文，这些自然语言字段使用英文。
- JSON 字段名必须保持英文。
- direction、type、key 必须保持英文。

返回 JSON schema：
{
  "symbol": "股票代码，优先使用搜索结果中的 symbol；若搜索结果为空则填写最合理的 hint",
  "company_name": "公司名称，跟随用户语言，例如中文输入时写 腾讯控股",
  "market": "us | cn | hk — 股票所在市场：cn 表示中国 A 股，hk 表示香港上市股票，us 表示美国股票",
  "direction": "bullish | bearish | neutral",
  "core_claim": "一句话总结 thesis，必须跟随用户语言",
  "assumptions": [
    {
      "key": "稳定的英文 snake_case 标识",
      "text": "可验证假设，必须跟随用户语言",
      "type": "business | financial | competitive | regulatory | technical | macro | valuation | other",
      "verifiable": true,
      "importance": 0.0,
      "evidence_hints": ["需要监控的具体证据，必须跟随用户语言"]
    }
  ]
}

规则：
- 输出 3 到 7 条 assumptions。
- 每条 assumption 的 text 必须不同，不得与其他条重复或高度相似。
- assumptions 必须具体、可被证据验证。
- importance 表示相对重要性，不需要加总为 1。
- 只有当 thesis 中包含有意义但无法直接验证的信念时，才使用 verifiable=false。
- 不要翻译用户 thesis 的自然语言内容到另一种语言。
- 不要输出 markdown fences、注释、解释或多余文本。`, parserVersion)
}
