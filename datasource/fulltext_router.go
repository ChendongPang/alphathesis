package datasource

import (
	"context"
	"strings"
)

// RoutingFullTextFetcher dispatches to PDF for URLs ending in ".pdf" and to
// HTML for everything else. This lets us use the Python pyadapter (pdfplumber)
// for CNInfo announcements while keeping the fast Go fetcher for SEC EDGAR HTML.
type RoutingFullTextFetcher struct {
	PDF  FullTextFetcher // handles .pdf URLs (e.g. pyadapter Client)
	HTML FullTextFetcher // handles HTML/plain-text URLs (e.g. us.FullTextFetcher)
}

func (r *RoutingFullTextFetcher) FetchText(ctx context.Context, sourceURL string) (string, error) {
	if strings.Contains(strings.ToLower(sourceURL), ".pdf") {
		return r.PDF.FetchText(ctx, sourceURL)
	}
	return r.HTML.FetchText(ctx, sourceURL)
}

// Compile-time check.
var _ FullTextFetcher = (*RoutingFullTextFetcher)(nil)
