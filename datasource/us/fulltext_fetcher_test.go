package us

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ── FetchText integration with mock HTTP ──────────────────────────────────────

func TestFetchTextHTML(t *testing.T) {
	body := `<!DOCTYPE html>
<html>
<head><style>body{color:red}</style><script>alert(1)</script></head>
<body>
<h1>Apple Inc. 10-K Filing</h1>
<p>Revenue grew 6% year-over-year to <b>$391 billion</b>.</p>
<div>Services segment reached new highs.</div>
<ul><li>iPhone</li><li>Mac</li></ul>
</body>
</html>`

	client := NewFullTextFetcher(WithFullTextHTTPClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://www.sec.gov/Archives/edgar/data/320193/0001.htm" {
			t.Fatalf("unexpected URL %q", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})))

	text, err := client.FetchText(context.Background(), "https://www.sec.gov/Archives/edgar/data/320193/0001.htm")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Apple Inc. 10-K Filing") {
		t.Errorf("missing heading: %q", text)
	}
	if !strings.Contains(text, "391 billion") {
		t.Errorf("missing revenue text: %q", text)
	}
	if strings.Contains(text, "alert(1)") {
		t.Errorf("script content leaked into text: %q", text)
	}
	if strings.Contains(text, "color:red") {
		t.Errorf("style content leaked into text: %q", text)
	}
}

func TestFetchTextPlain(t *testing.T) {
	client := NewFullTextFetcher(WithFullTextHTTPClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("  quarterly earnings beat estimates  ")),
		}, nil
	})))

	text, err := client.FetchText(context.Background(), "https://example.com/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if text != "quarterly earnings beat estimates" {
		t.Errorf("text = %q", text)
	}
}

func TestFetchTextUnsupportedContentType(t *testing.T) {
	client := NewFullTextFetcher(WithFullTextHTTPClient(roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/pdf"}},
			Body:       io.NopCloser(strings.NewReader("%PDF-1.4")),
		}, nil
	})))

	_, err := client.FetchText(context.Background(), "https://static.cninfo.com.cn/doc.pdf")
	if err == nil {
		t.Fatal("expected error for PDF content type")
	}
}

func TestFetchTextHTTPError(t *testing.T) {
	client := NewFullTextFetcher(WithFullTextHTTPClient(roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})))

	_, err := client.FetchText(context.Background(), "https://example.com/missing")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestFetchTextEmptyHTML(t *testing.T) {
	client := NewFullTextFetcher(WithFullTextHTTPClient(roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("<html><head></head><body></body></html>")),
		}, nil
	})))

	_, err := client.FetchText(context.Background(), "https://example.com/empty.html")
	if err == nil {
		t.Fatal("expected error for empty HTML body")
	}
}

func TestFetchTextMaxChars(t *testing.T) {
	longBody := "<html><body><p>" + strings.Repeat("A", 10_000) + "</p></body></html>"
	client := NewFullTextFetcher(
		WithFullTextHTTPClient(roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader(longBody)),
			}, nil
		})),
		WithFullTextMaxTextChars(100),
	)

	text, err := client.FetchText(context.Background(), "https://example.com/long.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > 100 {
		t.Errorf("len(text) = %d, want ≤ 100", len(text))
	}
}

// ── htmlToText unit tests ──────────────────────────────────────────────────────

func TestHTMLToTextBasic(t *testing.T) {
	in := `<html><body><h1>Title</h1><p>Hello <b>world</b>.</p></body></html>`
	out := htmlToText([]byte(in), 10_000)
	if !strings.Contains(out, "Title") || !strings.Contains(out, "Hello") || !strings.Contains(out, "world") {
		t.Errorf("output = %q", out)
	}
}

func TestHTMLToTextSkipsScript(t *testing.T) {
	in := `<p>Visible</p><script>var x = 1; doEvil();</script><p>Also visible</p>`
	out := htmlToText([]byte(in), 10_000)
	if strings.Contains(out, "doEvil") {
		t.Errorf("script content leaked: %q", out)
	}
	if !strings.Contains(out, "Visible") || !strings.Contains(out, "Also visible") {
		t.Errorf("missing expected text: %q", out)
	}
}

func TestHTMLToTextSkipsStyle(t *testing.T) {
	in := `<style>body { color: red; }</style><p>Content</p>`
	out := htmlToText([]byte(in), 10_000)
	if strings.Contains(out, "color") {
		t.Errorf("style content leaked: %q", out)
	}
	if !strings.Contains(out, "Content") {
		t.Errorf("missing expected text: %q", out)
	}
}

func TestHTMLToTextSkipsComments(t *testing.T) {
	in := `<p>Real</p><!-- hidden comment --><p>Also real</p>`
	out := htmlToText([]byte(in), 10_000)
	if strings.Contains(out, "hidden") {
		t.Errorf("comment leaked: %q", out)
	}
}

func TestHTMLToTextBlockElements(t *testing.T) {
	in := `<p>First</p><p>Second</p><br><div>Third</div>`
	out := htmlToText([]byte(in), 10_000)
	// Block elements should produce newlines, so words should not be run together.
	if strings.Contains(out, "FirstSecond") || strings.Contains(out, "SecondThird") {
		t.Errorf("block elements not separated: %q", out)
	}
}

func TestHTMLToTextUppercaseTags(t *testing.T) {
	in := `<P>Para one</P><SCRIPT>secret()</SCRIPT><P>Para two</P>`
	out := htmlToText([]byte(in), 10_000)
	if strings.Contains(out, "secret") {
		t.Errorf("uppercase SCRIPT leaked: %q", out)
	}
	if !strings.Contains(out, "Para one") || !strings.Contains(out, "Para two") {
		t.Errorf("missing text: %q", out)
	}
}

// ── decodeEntities unit tests ──────────────────────────────────────────────────

func TestDecodeEntitiesNamed(t *testing.T) {
	cases := [][2]string{
		{"&amp;", "&"},
		{"&lt;", "<"},
		{"&gt;", ">"},
		{"&nbsp;", " "},
		{"&quot;", `"`},
		{"&mdash;", "—"},
		{"&hellip;", "…"},
	}
	for _, c := range cases {
		got := decodeEntities(c[0])
		if got != c[1] {
			t.Errorf("decodeEntities(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestDecodeEntitiesNumeric(t *testing.T) {
	cases := [][2]string{
		{"&#65;", "A"},        // decimal
		{"&#x41;", "A"},       // hex lowercase
		{"&#X41;", "A"},       // hex uppercase
		{"&#8212;", "—"},      // em dash
		{"&#160;", " "},       // non-breaking space
	}
	for _, c := range cases {
		got := decodeEntities(c[0])
		if got != c[1] {
			t.Errorf("decodeEntities(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// ── columnAndPlate (shared helper, already tested in sec test but verify) ─────

func TestSECFilingRoundTrip(t *testing.T) {
	// Simulate a realistic SEC 10-Q filing HTML snippet.
	body := `<html>
<head><title>Form 10-Q</title><style>.hide{display:none}</style></head>
<body>
<h1>APPLE INC.</h1>
<h2>FORM 10-Q</h2>
<p>For the quarterly period ended March&nbsp;29, 2025</p>
<table>
<tr><td>Net sales</td><td>&#36;95,359</td></tr>
<tr><td>Net income</td><td>&#36;24,780</td></tr>
</table>
<p>iPhone revenue increased 2% year&#x2011;over&#x2011;year.</p>
<script>// tracking pixel</script>
</body></html>`

	client := NewFullTextFetcher(WithFullTextHTTPClient(roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})))

	text, err := client.FetchText(context.Background(), "https://www.sec.gov/Archives/edgar/data/320193/000032019325000123/aapl-20250329.htm")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "APPLE INC") {
		t.Errorf("missing company name: %q", text)
	}
	if !strings.Contains(text, "Net sales") {
		t.Errorf("missing table content: %q", text)
	}
	if !strings.Contains(text, "95,359") {
		t.Errorf("missing numeric entity decoded value: %q", text)
	}
	if strings.Contains(text, "tracking pixel") {
		t.Errorf("script content leaked: %q", text)
	}
}
