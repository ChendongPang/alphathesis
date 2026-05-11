package pyadapter

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"alphathesis/datasource"
)

// FetchText retrieves the plain-text body of a PDF document via the pyadapter
// /fetch_text endpoint. It is intended for CNInfo announcements, which are
// served as PDF files that the Go HTML fetcher cannot parse.
//
// For HTML or plain-text URLs, callers should prefer the US FullTextFetcher
// which does not require the Python service.
func (c *Client) FetchText(ctx context.Context, sourceURL string) (string, error) {
	values := url.Values{}
	values.Set("url", sourceURL)

	var result fetchTextResponse
	if err := c.get(ctx, "/fetch_text", values, &result); err != nil {
		return "", fmt.Errorf("pyadapter fetch_text: %w", err)
	}
	if strings.TrimSpace(result.Text) == "" {
		return "", fmt.Errorf("pyadapter fetch_text: empty text for %s", sourceURL)
	}
	return result.Text, nil
}

type fetchTextResponse struct {
	Text      string `json:"text"`
	SourceURL string `json:"source_url"`
	CharCount int    `json:"char_count"`
}

// Compile-time check: pyadapter Client implements datasource.FullTextFetcher.
var _ datasource.FullTextFetcher = (*Client)(nil)
