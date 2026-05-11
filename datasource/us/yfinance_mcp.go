package us

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"alphathesis/datasource"
	"alphathesis/engine"
	"alphathesis/store"
)

// MCPToolCaller is the subset of client.MCPClient used by data source fetchers.
type MCPToolCaller interface {
	CallToolText(ctx context.Context, name string, arguments map[string]any) (string, error)
}

// YFinanceMCPClient fetches US stock news and daily price quotes from the
// local yfinance MCP server (scripts/yfinance_mcp_server.py).
//
// It implements:
//   - datasource.CandidateFetcher  (news → job_candidates)
//   - dailyQuoteFetcher            (OHLC → market context)
type YFinanceMCPClient struct {
	mcp MCPToolCaller
}

func NewYFinanceMCPClient(mcp MCPToolCaller) (*YFinanceMCPClient, error) {
	if mcp == nil {
		return nil, errors.New("yfinance mcp caller is required")
	}
	return &YFinanceMCPClient{mcp: mcp}, nil
}

// FetchCandidates implements datasource.CandidateFetcher.
func (c *YFinanceMCPClient) FetchCandidates(ctx context.Context, input datasource.CandidateFetchInput) ([]store.CreateJobCandidateParams, error) {
	limit := datasource.LimitOrDefault(input.Limit, 30)
	return c.FetchNews(ctx, input.Symbol, input.Since, limit)
}

func (c *YFinanceMCPClient) FetchNews(ctx context.Context, symbol string, since *time.Time, limit int) ([]store.CreateJobCandidateParams, error) {
	symbol = strings.TrimSpace(strings.ToUpper(symbol))
	if symbol == "" {
		return nil, datasource.ErrMissingSymbol
	}

	text, err := c.mcp.CallToolText(ctx, "get_news", map[string]any{
		"symbol": symbol,
		"limit":  limit,
	})
	if err != nil {
		return nil, fmt.Errorf("yfinance mcp get_news: %w", err)
	}

	var items []yfinanceNewsItem
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		return nil, fmt.Errorf("yfinance mcp decode news: %w (raw: %.200s)", err, text)
	}

	var candidates []store.CreateJobCandidateParams
	for _, item := range items {
		if item.URL == "" && item.Title == "" {
			continue
		}
		pubAt := parseYFTime(item.PublishedAt)
		if since != nil && pubAt != nil && pubAt.Before(*since) {
			continue
		}
		sourceID := datasource.FirstNonEmpty(item.URL, item.Title+"|"+item.PublishedAt)
		candidates = append(candidates, store.CreateJobCandidateParams{
			Source:      datasource.SourceUSNewsYFinance,
			SourceID:    sourceID,
			SourceURL:   item.URL,
			Symbol:      symbol,
			Title:       item.Title,
			Summary:     item.Summary,
			PublishedAt: pubAt,
			RawPayload:  datasource.MarshalRaw(item),
		})
	}
	return candidates, nil
}

// FetchDailyQuote implements the dailyQuoteFetcher interface used by
// routingPriceQuoteFetcher in cmd/server and cmd/runner.
func (c *YFinanceMCPClient) FetchDailyQuote(ctx context.Context, symbol string, tradingDay *time.Time) (engine.PriceQuote, error) {
	symbol = strings.TrimSpace(strings.ToUpper(symbol))
	if symbol == "" {
		return engine.PriceQuote{}, datasource.ErrMissingSymbol
	}

	text, err := c.mcp.CallToolText(ctx, "get_daily_quote", map[string]any{
		"symbol": symbol,
	})
	if err != nil {
		return engine.PriceQuote{}, fmt.Errorf("yfinance mcp get_daily_quote: %w", err)
	}

	var resp yfinanceDailyQuote
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return engine.PriceQuote{}, fmt.Errorf("yfinance mcp decode quote: %w", err)
	}
	if resp.Error != "" {
		return engine.PriceQuote{}, fmt.Errorf("yfinance mcp: %s", resp.Error)
	}

	if tradingDay != nil {
		want := tradingDay.Format("2006-01-02")
		if resp.Date != want {
			return engine.PriceQuote{}, fmt.Errorf("yfinance quote date %s != requested %s", resp.Date, want)
		}
	}

	return engine.PriceQuote{
		Symbol: symbol,
		Open:   resp.Open,
		Close:  resp.Close,
	}, nil
}

// ------------------------------------------------------------
// Internal types
// ------------------------------------------------------------

type yfinanceNewsItem struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Publisher   string `json:"publisher"`
	PublishedAt string `json:"published_at"`
	Summary     string `json:"summary"`
}

type yfinanceDailyQuote struct {
	Symbol string  `json:"symbol"`
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	Close  float64 `json:"close"`
	Error  string  `json:"error"`
}

func parseYFTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
