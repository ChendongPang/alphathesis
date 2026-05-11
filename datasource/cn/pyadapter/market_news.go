package pyadapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"alphathesis/datasource"
	"alphathesis/engine"
	"alphathesis/store"
)

const defaultBaseURL = "http://127.0.0.1:8811"

type Client struct {
	baseURL string
	http    datasource.HTTPClient
}

type Option func(*Client)

func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		if strings.TrimSpace(baseURL) != "" {
			c.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		}
	}
}

func WithHTTPClient(httpClient datasource.HTTPClient) Option {
	return func(c *Client) {
		c.http = datasource.DefaultHTTPClient(httpClient)
	}
}

func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL: defaultBaseURL,
		http:    http.DefaultClient,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

func (c *Client) FetchCandidates(ctx context.Context, input datasource.CandidateFetchInput) ([]store.CreateJobCandidateParams, error) {
	return c.FetchNews(ctx, input.Symbol, input.Limit)
}

func (c *Client) FetchNews(ctx context.Context, symbol string, limit int) ([]store.CreateJobCandidateParams, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, datasource.ErrMissingSymbol
	}
	values := url.Values{}
	values.Set("symbol", symbol)
	const maxNewsLimit = 100
	n := datasource.LimitOrDefault(limit, 50)
	if n > maxNewsLimit {
		n = maxNewsLimit
	}
	values.Set("limit", fmt.Sprintf("%d", n))

	var rows []newsResponse
	if err := c.get(ctx, "/news", values, &rows); err != nil {
		return nil, err
	}
	out := make([]store.CreateJobCandidateParams, 0, len(rows))
	for _, row := range rows {
		publishedAt := parseCNTime(row.PublishedAt)
		out = append(out, store.CreateJobCandidateParams{
			Source:      datasource.SourceCNNewsAKShare,
			SourceID:    datasource.FirstNonEmpty(row.SourceID, row.SourceURL, row.Title),
			SourceURL:   row.SourceURL,
			Symbol:      row.Symbol,
			Title:       row.Title,
			Summary:     row.Summary,
			PublishedAt: publishedAt,
			RawPayload:  datasource.MarshalRaw(row.RawPayload),
		})
	}
	return out, nil
}

func (c *Client) FetchDailyQuote(ctx context.Context, symbol string, tradingDay *time.Time) (engine.PriceQuote, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return engine.PriceQuote{}, datasource.ErrMissingSymbol
	}
	start, end := "19700101", "20500101"
	if tradingDay != nil {
		day := tradingDay.Format("20060102")
		start, end = day, day
	}
	values := url.Values{}
	values.Set("symbol", symbol)
	values.Set("start", start)
	values.Set("end", end)

	var quote dailyQuoteResponse
	if err := c.get(ctx, "/daily_quote", values, &quote); err != nil {
		return engine.PriceQuote{}, err
	}
	if quote.Open <= 0 || quote.Close <= 0 {
		return engine.PriceQuote{}, errors.New("akshare daily quote has non-positive open/close")
	}
	return engine.PriceQuote{Symbol: quote.Symbol, Open: quote.Open, Close: quote.Close}, nil
}

func (c *Client) get(ctx context.Context, path string, values url.Values, out any) error {
	endpoint := c.baseURL + path
	if len(values) > 0 {
		endpoint += "?" + values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("akshare adapter request: %w", err)
	}
	if err := datasource.DecodeJSONResponse(resp, out); err != nil {
		return fmt.Errorf("akshare adapter %s: %w", path, err)
	}
	return nil
}

type newsResponse struct {
	Source      string         `json:"source"`
	SourceID    string         `json:"source_id"`
	SourceURL   string         `json:"source_url"`
	Symbol      string         `json:"symbol"`
	Title       string         `json:"title"`
	Summary     string         `json:"summary"`
	PublishedAt string         `json:"published_at"`
	RawPayload  map[string]any `json:"raw_payload"`
}

type dailyQuoteResponse struct {
	Symbol     string         `json:"symbol"`
	Date       string         `json:"date"`
	Open       float64        `json:"open"`
	Close      float64        `json:"close"`
	High       *float64       `json:"high"`
	Low        *float64       `json:"low"`
	Volume     *float64       `json:"volume"`
	Amount     *float64       `json:"amount"`
	PctChange  *float64       `json:"pct_change"`
	Turnover   *float64       `json:"turnover"`
	RawPayload map[string]any `json:"raw_payload"`
}

func parseCNTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		t, err := time.ParseInLocation(layout, value, time.Local)
		if err == nil {
			return &t
		}
	}
	return nil
}
