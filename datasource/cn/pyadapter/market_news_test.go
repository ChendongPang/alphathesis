package pyadapter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFetchNews(t *testing.T) {
	client := NewClient(WithHTTPClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/news" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		if req.URL.Query().Get("symbol") != "000001" {
			t.Fatalf("symbol = %q", req.URL.Query().Get("symbol"))
		}
		return jsonResponse(`[{
			"source":"ak_news",
			"source_id":"http://finance.eastmoney.com/a/test.html",
			"source_url":"http://finance.eastmoney.com/a/test.html",
			"symbol":"000001",
			"title":"平安银行一季报",
			"summary":"平安银行净利润增长。",
			"published_at":"2026-04-25 10:12:35",
			"raw_payload":{"文章来源":"界面新闻","新闻标题":"平安银行一季报"}
		}]`), nil
	})))

	candidates, err := client.FetchNews(context.Background(), "000001", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d", len(candidates))
	}
	c := candidates[0]
	if c.Source != "cn_news_akshare" || c.Symbol != "000001" || c.Title == "" || c.Summary == "" {
		t.Fatalf("candidate = %#v", c)
	}
	if c.PublishedAt == nil {
		t.Fatal("PublishedAt is nil")
	}
}

func TestFetchDailyQuote(t *testing.T) {
	client := NewClient(WithHTTPClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/daily_quote" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		return jsonResponse(`{
			"symbol":"000001",
			"date":"2026-05-06",
			"open":11.5,
			"close":11.36,
			"high":11.5,
			"low":11.3,
			"raw_payload":{"日期":"2026-05-06"}
		}`), nil
	})))

	quote, err := client.FetchDailyQuote(context.Background(), "000001", nil)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Symbol != "000001" || quote.Open != 11.5 || quote.Close != 11.36 {
		t.Fatalf("quote = %#v", quote)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
