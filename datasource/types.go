package datasource

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"alphathesis/store"
)

const (
	SourceUSNewsYFinance        = "us_news_yfinance"
	SourceUSOfficialEventSEC    = "us_official_sec"
	SourceCNNewsAKShare         = "cn_news_akshare"
	SourceCNOfficialEventCNInfo = "cn_official_cninfo"
	SourceManual                = "manual"
)

// IsNewsSource reports whether a source string represents a news article
// (as opposed to an official filing or announcement).
func IsNewsSource(source string) bool {
	return source == SourceUSNewsYFinance ||
		source == SourceCNNewsAKShare
}

// CandidateFetcher returns raw evidence candidates ready to be inserted into
// job_candidates. Market data intentionally uses a separate interface because
// price quotes explain context; they should not become evidence snippets.
type CandidateFetcher interface {
	FetchCandidates(ctx context.Context, input CandidateFetchInput) ([]store.CreateJobCandidateParams, error)
}

// FullTextFetcher retrieves the full plain-text body of a source document so
// the job runner can chunk and embed it for RAG-mode evidence judging.
// Implementations must return an error for unsupported content types (e.g.
// PDF) so the caller can fall back to summary mode gracefully.
type FullTextFetcher interface {
	FetchText(ctx context.Context, sourceURL string) (string, error)
}

type CandidateFetchInput struct {
	Symbol string
	CIK    string
	Since  *time.Time
	Limit  int
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func DefaultHTTPClient(c HTTPClient) HTTPClient {
	if c != nil {
		return c
	}
	return http.DefaultClient
}

func DecodeJSONResponse(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %s", resp.Status)
	}
	body, err := decodedBody(resp)
	if err != nil {
		return err
	}
	defer body.Close()
	dec := json.NewDecoder(body)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode json response: %w", err)
	}
	return nil
}

// ReadResponseBody reads the full body of resp, handling gzip content-encoding,
// and caps the read at maxBytes. It closes resp.Body.
func ReadResponseBody(resp *http.Response, maxBytes int) ([]byte, error) {
	defer resp.Body.Close()
	r, err := decodedBody(resp)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, int64(maxBytes)))
}

func decodedBody(resp *http.Response) (io.ReadCloser, error) {
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("decode gzip response: %w", err)
		}
		return reader, nil
	case "", "identity":
		return resp.Body, nil
	default:
		return nil, fmt.Errorf("unsupported content encoding %q", resp.Header.Get("Content-Encoding"))
	}
}

func MarshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func LimitOrDefault(limit, fallback int) int {
	if limit > 0 {
		return limit
	}
	return fallback
}

func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var ErrMissingSymbol = errors.New("symbol is required")
