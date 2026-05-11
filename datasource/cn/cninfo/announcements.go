package cninfo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"alphathesis/datasource"
	"alphathesis/store"
)

const (
	defaultQueryURL   = "https://www.cninfo.com.cn/new/hisAnnouncement/query"
	defaultSearchURL  = "https://www.cninfo.com.cn/new/information/topSearch/query"
	defaultDocBaseURL = "https://static.cninfo.com.cn"
	defaultLimit      = 20
)

// CNInfoClient fetches official company announcements from 巨潮资讯网 (cninfo.com.cn),
// the CSRC-designated disclosure platform for A-share listed companies.
// It is the CN equivalent of the US SEC EDGAR datasource.
type CNInfoClient struct {
	queryURL   string
	searchURL  string
	docBaseURL string
	http       datasource.HTTPClient
	categories map[string]bool
}

type CNInfoOption func(*CNInfoClient)

// WithCNInfoURLs overrides the query, topSearch, and static-doc base URLs.
func WithCNInfoURLs(queryURL, searchURL, docBaseURL string) CNInfoOption {
	return func(c *CNInfoClient) {
		if strings.TrimSpace(queryURL) != "" {
			c.queryURL = strings.TrimRight(strings.TrimSpace(queryURL), "/")
		}
		if strings.TrimSpace(searchURL) != "" {
			c.searchURL = strings.TrimRight(strings.TrimSpace(searchURL), "/")
		}
		if strings.TrimSpace(docBaseURL) != "" {
			c.docBaseURL = strings.TrimRight(strings.TrimSpace(docBaseURL), "/")
		}
	}
}

func WithCNInfoHTTPClient(httpClient datasource.HTTPClient) CNInfoOption {
	return func(c *CNInfoClient) {
		c.http = datasource.DefaultHTTPClient(httpClient)
	}
}

// WithCNInfoCategories restricts fetched announcements to the provided CNInfo
// category codes (e.g. "category_ndbg_szsh" for annual reports). Passing no
// categories disables filtering and returns all announcement types.
func WithCNInfoCategories(categories ...string) CNInfoOption {
	return func(c *CNInfoClient) {
		c.categories = normalizeCategories(categories)
	}
}

func NewCNInfoClient(opts ...CNInfoOption) *CNInfoClient {
	c := &CNInfoClient{
		queryURL:   defaultQueryURL,
		searchURL:  defaultSearchURL,
		docBaseURL: defaultDocBaseURL,
		http:       http.DefaultClient,
		categories: nil,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// FetchCandidates fetches recent CNInfo announcements. Prefer OrgID via
// input.CIK when available; otherwise it resolves OrgID from input.Symbol via
// CNInfo's topSearch API.
func (c *CNInfoClient) FetchCandidates(ctx context.Context, input datasource.CandidateFetchInput) ([]store.CreateJobCandidateParams, error) {
	orgID := strings.TrimSpace(input.CIK)
	if orgID == "" {
		if strings.TrimSpace(input.Symbol) == "" {
			return nil, datasource.ErrMissingSymbol
		}
		resolved, err := c.ResolveOrgID(ctx, input.Symbol)
		if err != nil {
			return nil, err
		}
		orgID = resolved.OrgID
	}
	return c.FetchAnnouncements(ctx, input.Symbol, orgID, input.Since, input.Limit)
}

// ResolveOrgID resolves a stock code to a CNInfo OrgID via the topSearch API.
func (c *CNInfoClient) ResolveOrgID(ctx context.Context, symbol string) (OrgSearchResult, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return OrgSearchResult{}, datasource.ErrMissingSymbol
	}
	form := url.Values{}
	form.Set("keyWord", symbol)
	form.Set("maxNum", "10")

	var rows []OrgSearchResult
	if err := c.post(ctx, c.searchURL, form, &rows); err != nil {
		return OrgSearchResult{}, err
	}
	for _, row := range rows {
		if row.Code == symbol && row.OrgID != "" {
			return row, nil
		}
	}
	if len(rows) > 0 && rows[0].OrgID != "" {
		return rows[0], nil
	}
	return OrgSearchResult{}, fmt.Errorf("cninfo orgId for symbol %q not found", symbol)
}

// FetchAnnouncements fetches recent announcements for the given orgID.
// symbol is used for tagging candidates and resolving exchange metadata; it may
// be empty if orgID is known. since filters out announcements published before
// the given time.
func (c *CNInfoClient) FetchAnnouncements(ctx context.Context, symbol, orgID string, since *time.Time, limit int) ([]store.CreateJobCandidateParams, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, errors.New("cninfo orgId is required")
	}
	symbol = strings.TrimSpace(symbol)
	col, plate := columnAndPlate(symbol)

	startDate := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	if since != nil {
		startDate = since.Format("2006-01-02")
	}
	endDate := time.Now().Format("2006-01-02")

	max := datasource.LimitOrDefault(limit, defaultLimit)

	form := url.Values{}
	form.Set("pageNum", "1")
	form.Set("pageSize", fmt.Sprintf("%d", max))
	form.Set("column", col)
	form.Set("tabName", "fulltext")
	form.Set("plate", plate)
	form.Set("stock", stockParam(symbol, orgID))
	form.Set("searchkey", "")
	form.Set("secid", "")
	form.Set("category", c.categoryParam())
	form.Set("trade", "")
	form.Set("seDate", startDate+"~"+endDate)
	form.Set("sortName", "")
	form.Set("sortType", "")
	form.Set("isHLtitle", "true")

	var res announcementsResponse
	if err := c.post(ctx, c.queryURL, form, &res); err != nil {
		return nil, err
	}

	candidates := make([]store.CreateJobCandidateParams, 0, len(res.Announcements))
	for _, ann := range res.Announcements {
		if !c.allowCategory(ann.CategoryID) {
			continue
		}
		publishedAt := parseMillis(ann.AnnouncementTime)
		if since != nil && publishedAt != nil && publishedAt.Before(*since) {
			continue
		}
		sym := datasource.FirstNonEmpty(symbol, ann.SecCode)
		candidates = append(candidates, store.CreateJobCandidateParams{
			Source:      datasource.SourceCNOfficialEventCNInfo,
			SourceID:    ann.AnnouncementID,
			SourceURL:   c.docURL(ann.AdjunctURL),
			Symbol:      sym,
			Title:       ann.AnnouncementTitle,
			Summary:     buildSummary(ann),
			PublishedAt: publishedAt,
			RawPayload: datasource.MarshalRaw(map[string]any{
				"announcementId":    ann.AnnouncementID,
				"announcementTitle": ann.AnnouncementTitle,
				"announcementTime":  ann.AnnouncementTime,
				"adjunctUrl":        ann.AdjunctURL,
				"adjunctType":       ann.AdjunctType,
				"categoryId":        ann.CategoryID,
				"secCode":           ann.SecCode,
				"secName":           ann.SecName,
				"orgId":             datasource.FirstNonEmpty(ann.OrgID, orgID),
			}),
		})
	}
	return candidates, nil
}

func (c *CNInfoClient) allowCategory(categoryID string) bool {
	if len(c.categories) == 0 {
		return true
	}
	return c.categories[strings.ToLower(strings.TrimSpace(categoryID))]
}

func (c *CNInfoClient) categoryParam() string {
	if len(c.categories) == 0 {
		return ""
	}
	cats := make([]string, 0, len(c.categories))
	for cat := range c.categories {
		cats = append(cats, cat)
	}
	return strings.Join(cats, ";")
}

func (c *CNInfoClient) docURL(adjunctURL string) string {
	if adjunctURL == "" {
		return ""
	}
	return c.docBaseURL + "/" + strings.TrimLeft(adjunctURL, "/")
}

func (c *CNInfoClient) post(ctx context.Context, endpoint string, form url.Values, out any) error {
	body := bytes.NewBufferString(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Origin", "https://www.cninfo.com.cn")
	req.Header.Set("Referer", "https://www.cninfo.com.cn/new/commonUrl/pageOfSearch?url=disclosure/list/search")
	req.Header.Set("User-Agent", "Mozilla/5.0 AlphaThesis/1.0")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cninfo request: %w", err)
	}
	return datasource.DecodeJSONResponse(resp, out)
}

// OrgSearchResult is returned by ResolveOrgID.
type OrgSearchResult struct {
	Code     string `json:"code"`
	OrgID    string `json:"orgId"`
	Name     string `json:"zwjc"`
	Category string `json:"category"`
}

type announcementsResponse struct {
	Announcements []announcementItem `json:"announcements"`
	HasMore       bool               `json:"hasMore"`
	TotalCount    int                `json:"totalRecordNum"`
}

type announcementItem struct {
	AnnouncementID    string `json:"announcementId"`
	AnnouncementTitle string `json:"announcementTitle"`
	AnnouncementTime  int64  `json:"announcementTime"` // milliseconds since epoch
	AdjunctURL        string `json:"adjunctUrl"`
	AdjunctType       string `json:"adjunctType"`
	CategoryID        string `json:"categoryId"`
	SecCode           string `json:"secCode"`
	SecName           string `json:"secName"`
	OrgID             string `json:"orgId"`
}

func buildSummary(ann announcementItem) string {
	s := "CNInfo announcement"
	if ann.SecName != "" {
		s += " by " + ann.SecName
	}
	if ann.SecCode != "" {
		s += " (" + ann.SecCode + ")"
	}
	if ann.AdjunctType != "" {
		s += ", type " + ann.AdjunctType
	}
	if ann.CategoryID != "" {
		s += ", category " + ann.CategoryID
	}
	return s
}

func parseMillis(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).In(time.Local)
	return &t
}

// columnAndPlate derives CNInfo exchange column and plate from the A-share stock code.
func columnAndPlate(symbol string) (column, plate string) {
	switch {
	case strings.HasPrefix(symbol, "6"):
		return "sse", "sh"
	case strings.HasPrefix(symbol, "8"), strings.HasPrefix(symbol, "4"):
		return "bjse", "bj"
	default:
		return "szse", "sz"
	}
}

func stockParam(symbol, orgID string) string {
	symbol = strings.TrimSpace(symbol)
	orgID = strings.TrimSpace(orgID)
	if symbol == "" {
		return orgID
	}
	if orgID == "" {
		return symbol
	}
	return symbol + "," + orgID
}

func normalizeCategories(cats []string) map[string]bool {
	out := make(map[string]bool, len(cats))
	for _, cat := range cats {
		norm := strings.ToLower(strings.TrimSpace(cat))
		if norm != "" {
			out[norm] = true
		}
	}
	return out
}
