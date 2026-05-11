package us

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"alphathesis/datasource"
	"alphathesis/store"
)

const (
	defaultSECDataBaseURL = "https://data.sec.gov"
	defaultSECSiteBaseURL = "https://www.sec.gov"
	defaultSECLimit       = 20
)

type SECClient struct {
	dataBaseURL string
	siteBaseURL string
	userAgent   string
	http        datasource.HTTPClient
	forms       map[string]bool
}

type SECOption func(*SECClient)

func WithSECBaseURLs(dataBaseURL, siteBaseURL string) SECOption {
	return func(c *SECClient) {
		if strings.TrimSpace(dataBaseURL) != "" {
			c.dataBaseURL = strings.TrimRight(strings.TrimSpace(dataBaseURL), "/")
		}
		if strings.TrimSpace(siteBaseURL) != "" {
			c.siteBaseURL = strings.TrimRight(strings.TrimSpace(siteBaseURL), "/")
		}
	}
}

func WithSECHTTPClient(httpClient datasource.HTTPClient) SECOption {
	return func(c *SECClient) {
		c.http = datasource.DefaultHTTPClient(httpClient)
	}
}

// WithSECForms restricts SEC recent filings to the provided form types. Form
// matching is case-insensitive. Passing no forms disables filtering.
func WithSECForms(forms ...string) SECOption {
	return func(c *SECClient) {
		c.forms = normalizeSECForms(forms)
	}
}

func NewSECClient(userAgent string, opts ...SECOption) (*SECClient, error) {
	if strings.TrimSpace(userAgent) == "" {
		return nil, errors.New("sec user agent is required")
	}
	c := &SECClient{
		dataBaseURL: defaultSECDataBaseURL,
		siteBaseURL: defaultSECSiteBaseURL,
		userAgent:   strings.TrimSpace(userAgent),
		http:        http.DefaultClient,
		forms:       normalizeSECForms([]string{"10-K", "10-Q", "8-K"}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c, nil
}

// FetchCandidates fetches recent SEC submissions. Prefer CIK when available;
// otherwise it resolves CIK from Symbol via SEC's company_tickers.json.
func (c *SECClient) FetchCandidates(ctx context.Context, input datasource.CandidateFetchInput) ([]store.CreateJobCandidateParams, error) {
	cik := strings.TrimSpace(input.CIK)
	if cik == "" {
		if strings.TrimSpace(input.Symbol) == "" {
			return nil, errors.New("cik or symbol is required")
		}
		resolved, err := c.ResolveCIKByTicker(ctx, input.Symbol)
		if err != nil {
			return nil, err
		}
		cik = resolved
	}
	return c.FetchRecentFilings(ctx, cik, input.Symbol, input.Since, input.Limit)
}

func (c *SECClient) ResolveCIKByTicker(ctx context.Context, symbol string) (string, error) {
	symbol = strings.TrimSpace(strings.ToUpper(symbol))
	if symbol == "" {
		return "", datasource.ErrMissingSymbol
	}

	endpoint := c.siteBaseURL + "/files/company_tickers.json"
	var res map[string]secCompanyTicker
	if err := c.get(ctx, endpoint, &res); err != nil {
		return "", err
	}
	for _, company := range res {
		if strings.EqualFold(company.Ticker, symbol) {
			return formatCIK(company.CIK), nil
		}
	}
	return "", fmt.Errorf("sec cik for ticker %q not found", symbol)
}

func (c *SECClient) FetchRecentFilings(ctx context.Context, cik, symbol string, since *time.Time, limit int) ([]store.CreateJobCandidateParams, error) {
	cik = formatCIKString(cik)
	if cik == "" {
		return nil, errors.New("cik is required")
	}
	symbol = strings.TrimSpace(strings.ToUpper(symbol))

	endpoint := c.dataBaseURL + "/submissions/CIK" + cik + ".json"
	var res secSubmissionsResponse
	if err := c.get(ctx, endpoint, &res); err != nil {
		return nil, err
	}

	max := datasource.LimitOrDefault(limit, defaultSECLimit)
	recent := res.Filings.Recent
	count := minSECFieldLen(recent)
	candidates := make([]store.CreateJobCandidateParams, 0, max)
	for i := 0; i < count && len(candidates) < max; i++ {
		filedAt := parseSECDate(recent.FilingDate[i])
		if since != nil && filedAt != nil && filedAt.Before(*since) {
			continue
		}
		accession := recent.AccessionNumber[i]
		form := recent.Form[i]
		if !c.allowForm(form) {
			continue
		}
		primaryDoc := recent.PrimaryDocument[i]
		description := recent.PrimaryDocDescription[i]
		title := strings.TrimSpace(form + " " + datasource.FirstNonEmpty(description, primaryDoc))
		if res.Name != "" {
			title = strings.TrimSpace(title + " filed by " + res.Name)
		}
		summary := fmt.Sprintf("SEC filing %s filed on %s", form, recent.FilingDate[i])
		if recent.ReportDate[i] != "" {
			summary += " for report date " + recent.ReportDate[i]
		}
		if description != "" {
			summary += ": " + description
		}

		candidates = append(candidates, store.CreateJobCandidateParams{
			Source:      datasource.SourceUSOfficialEventSEC,
			SourceID:    accession,
			SourceURL:   c.filingURL(cik, accession, primaryDoc),
			Symbol:      datasource.FirstNonEmpty(symbol, firstTicker(res.Tickers)),
			Title:       title,
			Summary:     summary,
			PublishedAt: filedAt,
			RawPayload: datasource.MarshalRaw(map[string]any{
				"accessionNumber":       accession,
				"filingDate":            recent.FilingDate[i],
				"reportDate":            recent.ReportDate[i],
				"form":                  form,
				"primaryDocument":       primaryDoc,
				"primaryDocDescription": description,
				"cik":                   cik,
				"name":                  res.Name,
			}),
		})
	}
	return candidates, nil
}

func (c *SECClient) allowForm(form string) bool {
	if len(c.forms) == 0 {
		return true
	}
	return c.forms[normalizeSECForm(form)]
}

func normalizeSECForms(forms []string) map[string]bool {
	out := make(map[string]bool, len(forms))
	for _, form := range forms {
		normalized := normalizeSECForm(form)
		if normalized != "" {
			out[normalized] = true
		}
	}
	return out
}

func normalizeSECForm(form string) string {
	return strings.ToUpper(strings.TrimSpace(form))
}

func (c *SECClient) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sec request: %w", err)
	}
	return datasource.DecodeJSONResponse(resp, out)
}

func (c *SECClient) filingURL(cik, accession, primaryDoc string) string {
	cikNoZeros := strings.TrimLeft(cik, "0")
	if cikNoZeros == "" {
		cikNoZeros = "0"
	}
	accessionNoDashes := strings.ReplaceAll(accession, "-", "")
	u, _ := url.Parse(c.siteBaseURL)
	u.Path = path.Join("Archives", "edgar", "data", cikNoZeros, accessionNoDashes, primaryDoc)
	return u.String()
}

type secCompanyTicker struct {
	CIK    int    `json:"cik_str"`
	Ticker string `json:"ticker"`
	Title  string `json:"title"`
}

type secSubmissionsResponse struct {
	CIK     string   `json:"cik"`
	Name    string   `json:"name"`
	Tickers []string `json:"tickers"`
	Filings struct {
		Recent secRecentFilings `json:"recent"`
	} `json:"filings"`
}

type secRecentFilings struct {
	AccessionNumber       []string `json:"accessionNumber"`
	FilingDate            []string `json:"filingDate"`
	ReportDate            []string `json:"reportDate"`
	Form                  []string `json:"form"`
	PrimaryDocument       []string `json:"primaryDocument"`
	PrimaryDocDescription []string `json:"primaryDocDescription"`
}

func minSECFieldLen(f secRecentFilings) int {
	n := len(f.AccessionNumber)
	for _, l := range []int{len(f.FilingDate), len(f.ReportDate), len(f.Form), len(f.PrimaryDocument), len(f.PrimaryDocDescription)} {
		if l < n {
			n = l
		}
	}
	return n
}

func parseSECDate(value string) *time.Time {
	if value == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil
	}
	return &t
}

func formatCIK(cik int) string {
	return fmt.Sprintf("%010d", cik)
}

func formatCIKString(cik string) string {
	cik = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(cik), "CIK"))
	if cik == "" {
		return ""
	}
	n, err := strconv.Atoi(cik)
	if err != nil {
		return cik
	}
	return formatCIK(n)
}

func firstTicker(tickers []string) string {
	if len(tickers) == 0 {
		return ""
	}
	return tickers[0]
}
