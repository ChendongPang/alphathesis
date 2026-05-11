package us

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"alphathesis/datasource"
)

const (
	defaultMaxRawBytes  = 5 * 1024 * 1024 // 5 MB raw response cap
	defaultMaxTextChars = 500_000          // ~500 K chars of extracted text
)

// FullTextFetcher fetches plain text from US source URLs.
// For SEC EDGAR HTML filings this works reliably; for news article URLs it is
// best-effort — paywalled or JS-rendered pages will return an error and the
// caller should fall back to summary mode.
type FullTextFetcher struct {
	http         datasource.HTTPClient
	maxRawBytes  int
	maxTextChars int
}

type FullTextFetcherOption func(*FullTextFetcher)

func WithFullTextHTTPClient(c datasource.HTTPClient) FullTextFetcherOption {
	return func(f *FullTextFetcher) {
		f.http = datasource.DefaultHTTPClient(c)
	}
}

func WithFullTextMaxRawBytes(n int) FullTextFetcherOption {
	return func(f *FullTextFetcher) {
		if n > 0 {
			f.maxRawBytes = n
		}
	}
}

func WithFullTextMaxTextChars(n int) FullTextFetcherOption {
	return func(f *FullTextFetcher) {
		if n > 0 {
			f.maxTextChars = n
		}
	}
}

func NewFullTextFetcher(opts ...FullTextFetcherOption) *FullTextFetcher {
	f := &FullTextFetcher{
		http:         http.DefaultClient,
		maxRawBytes:  defaultMaxRawBytes,
		maxTextChars: defaultMaxTextChars,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(f)
		}
	}
	return f
}

// FetchText fetches sourceURL and returns extracted plain text.
// HTML and XHTML responses are stripped to text. text/plain is returned
// as-is. All other content types (PDF, binary, …) return an error so the
// caller can fall back to summary mode.
func (f *FullTextFetcher) FetchText(ctx context.Context, sourceURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", fmt.Errorf("full text fetch: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 AlphaThesis/1.0")
	req.Header.Set("Accept", "text/html,text/plain,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate")

	resp, err := f.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("full text fetch %s: %w", sourceURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return "", fmt.Errorf("full text fetch %s: http %s", sourceURL, resp.Status)
	}

	raw, err := datasource.ReadResponseBody(resp, f.maxRawBytes)
	if err != nil {
		return "", fmt.Errorf("full text fetch %s: read body: %w", sourceURL, err)
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(ct, "text/html"), strings.Contains(ct, "xhtml"):
		text := htmlToText(raw, f.maxTextChars)
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("full text fetch %s: no text extracted from HTML", sourceURL)
		}
		return text, nil
	case strings.Contains(ct, "text/plain"):
		text := string(raw)
		if len(text) > f.maxTextChars {
			text = text[:f.maxTextChars]
		}
		return strings.TrimSpace(text), nil
	case ct == "":
		// Content-Type absent — try HTML extraction, fall through on empty result
		text := htmlToText(raw, f.maxTextChars)
		if strings.TrimSpace(text) != "" {
			return text, nil
		}
		return "", fmt.Errorf("full text fetch %s: unsupported or unknown content type", sourceURL)
	default:
		return "", fmt.Errorf("full text fetch %s: unsupported content type %q", sourceURL, ct)
	}
}

// ------------------------------------------------------------
// HTML → text extractor (no external deps)
// ------------------------------------------------------------

// htmlToText extracts visible text from HTML. It:
//   - skips <script>, <style> blocks and HTML comments entirely
//   - emits a newline for block-level elements (p, div, h1–h6, br, tr, li, …)
//   - strips all other tags
//   - decodes common named and numeric HTML entities
//   - collapses runs of whitespace and caps output at maxChars
func htmlToText(raw []byte, maxChars int) string {
	lower := bytes.ToLower(raw)
	var sb strings.Builder
	sb.Grow(min(len(raw)/3, maxChars))

	i := 0
	n := len(raw)

	for i < n {
		if sb.Len() >= maxChars {
			break
		}

		// HTML comment <!-- ... -->
		if i+4 <= n && lower[i] == '<' && lower[i+1] == '!' && lower[i+2] == '-' && lower[i+3] == '-' {
			end := bytes.Index(lower[i+4:], []byte("-->"))
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}

		if raw[i] != '<' {
			sb.WriteByte(raw[i])
			i++
			continue
		}

		// Find the closing '>' of this tag.
		closeAngle := bytes.IndexByte(raw[i:], '>')
		if closeAngle < 0 {
			// Malformed tag — emit and move on.
			sb.WriteByte(raw[i])
			i++
			continue
		}

		tagContent := bytes.TrimSpace(lower[i+1 : i+closeAngle])
		tagName := extractTagName(tagContent)

		switch tagName {
		case "script", "style":
			// Skip to closing tag.
			closing := []byte("</" + tagName + ">")
			end := bytes.Index(lower[i+closeAngle+1:], closing)
			if end < 0 {
				i = n
			} else {
				i = i + closeAngle + 1 + end + len(closing)
			}
			continue
		case "br", "p", "/p", "div", "/div",
			"h1", "/h1", "h2", "/h2", "h3", "/h3",
			"h4", "/h4", "h5", "/h5", "h6", "/h6",
			"tr", "/tr", "li", "/li",
			"blockquote", "/blockquote", "pre", "/pre":
			sb.WriteByte('\n')
		}

		i += closeAngle + 1
	}

	return collapseSpace(decodeEntities(sb.String()), maxChars)
}

// extractTagName returns the lowercase tag name from already-lowercased tag
// content (the bytes between '<' and '>'), e.g. "div", "/p", "br".
func extractTagName(content []byte) string {
	end := len(content)
	for j, b := range content {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			end = j
			break
		}
	}
	return strings.TrimRight(string(content[:end]), "/")
}

// ------------------------------------------------------------
// Entity decoding
// ------------------------------------------------------------

var (
	htmlEntities = strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&apos;", "'",
		"&nbsp;", " ",
		"&#160;", " ",
		"&mdash;", "—",
		"&ndash;", "–",
		"&ldquo;", "“",
		"&rdquo;", "”",
		"&lsquo;", "‘",
		"&rsquo;", "’",
		"&hellip;", "…",
		"&copy;", "©",
		"&reg;", "®",
		"&trade;", "™",
	)

	numericEntityRE = regexp.MustCompile(`&#([xX][0-9a-fA-F]+|[0-9]+);`)
)

func decodeEntities(s string) string {
	s = htmlEntities.Replace(s)
	return numericEntityRE.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-1] // strip &# and ;
		var (
			cp  int64
			err error
		)
		if strings.HasPrefix(strings.ToLower(inner), "x") {
			cp, err = strconv.ParseInt(inner[1:], 16, 32)
		} else {
			cp, err = strconv.ParseInt(inner, 10, 32)
		}
		if err != nil || cp < 0 || cp > 0x10FFFF {
			return m
		}
		return string(rune(cp))
	})
}

// ------------------------------------------------------------
// Whitespace normalisation
// ------------------------------------------------------------

var (
	multiNewlines = regexp.MustCompile(`\n{3,}`)
	multiSpaces   = regexp.MustCompile(`[ \t\r]+`)
)

func collapseSpace(s string, maxChars int) string {
	s = multiSpaces.ReplaceAllString(s, " ")
	// Trim spaces adjacent to newlines.
	s = strings.ReplaceAll(s, " \n", "\n")
	s = strings.ReplaceAll(s, "\n ", "\n")
	s = multiNewlines.ReplaceAllString(s, "\n\n")
	s = strings.TrimFunc(s, unicode.IsSpace)
	if len(s) > maxChars {
		// Trim at a character boundary.
		for maxChars > 0 && !isRuneStart(s[maxChars]) {
			maxChars--
		}
		s = s[:maxChars]
	}
	return s
}

// isRuneStart reports whether b is the first byte of a UTF-8 sequence.
func isRuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

// Compile-time interface check.
var _ datasource.FullTextFetcher = (*FullTextFetcher)(nil)
