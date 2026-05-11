package us

import (
	"context"
	"net/http"
	"testing"
	"time"

	"alphathesis/datasource"
)

func TestSECFetchCandidatesByTicker(t *testing.T) {
	httpClient := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("User-Agent") == "" {
			t.Fatal("missing user agent")
		}
		switch req.URL.Path {
		case "/files/company_tickers.json":
			return jsonResponse(`{
			"0": {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."}
		}`), nil
		case "/submissions/CIK0000320193.json":
			return jsonResponse(`{
			"cik": "0000320193",
			"name": "Apple Inc.",
			"tickers": ["AAPL"],
			"filings": {
				"recent": {
					"accessionNumber": ["0001111111-26-000001", "0000320193-26-000001", "0002222222-26-000001"],
					"filingDate": ["2026-05-06", "2026-05-05", "2026-05-04"],
					"reportDate": ["2026-05-01", "2026-03-31", "2026-04-30"],
					"form": ["4", "10-Q", "144"],
					"primaryDocument": ["form4.xml", "aapl-20260331.htm", "primary_doc.xml"],
					"primaryDocDescription": ["FORM 4", "10-Q quarterly report", ""]
				}
			}
		}`), nil
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
			return nil, nil
		}
	})

	client, err := NewSECClient(
		"AlphaThesis test contact@example.com",
		WithSECBaseURLs("https://data.test", "https://www.test"),
		WithSECHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	candidates, err := client.FetchCandidates(context.Background(), datasource.CandidateFetchInput{
		Symbol: "AAPL",
		Since:  &since,
		Limit:  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d", len(candidates))
	}
	c := candidates[0]
	if c.Source != datasource.SourceUSOfficialEventSEC {
		t.Fatalf("Source = %q", c.Source)
	}
	if c.SourceID != "0000320193-26-000001" {
		t.Fatalf("SourceID = %q", c.SourceID)
	}
	if c.SourceURL != "https://www.test/Archives/edgar/data/320193/000032019326000001/aapl-20260331.htm" {
		t.Fatalf("SourceURL = %q", c.SourceURL)
	}
	if c.Symbol != "AAPL" {
		t.Fatalf("Symbol = %q", c.Symbol)
	}
}

func TestSECFetchCandidatesWithCustomForms(t *testing.T) {
	httpClient := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/submissions/CIK0000320193.json":
			return jsonResponse(`{
			"cik": "0000320193",
			"name": "Apple Inc.",
			"tickers": ["AAPL"],
			"filings": {
				"recent": {
					"accessionNumber": ["0001111111-26-000001", "0000320193-26-000001"],
					"filingDate": ["2026-05-06", "2026-05-05"],
					"reportDate": ["2026-05-01", "2026-03-31"],
					"form": ["4", "10-Q"],
					"primaryDocument": ["form4.xml", "aapl-20260331.htm"],
					"primaryDocDescription": ["FORM 4", "10-Q quarterly report"]
				}
			}
		}`), nil
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
			return nil, nil
		}
	})

	client, err := NewSECClient(
		"AlphaThesis test contact@example.com",
		WithSECBaseURLs("https://data.test", "https://www.test"),
		WithSECHTTPClient(httpClient),
		WithSECForms("4"),
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := client.FetchCandidates(context.Background(), datasource.CandidateFetchInput{
		CIK:    "0000320193",
		Symbol: "AAPL",
		Limit:  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d", len(candidates))
	}
	if candidates[0].SourceID != "0001111111-26-000001" {
		t.Fatalf("SourceID = %q", candidates[0].SourceID)
	}
}
