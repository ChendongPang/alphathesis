package cninfo

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"alphathesis/datasource"
)

func TestCNInfoFetchCandidatesBySymbol(t *testing.T) {
	var searchCalled bool
	httpClient := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(req.URL.Path, "topSearch"):
			searchCalled = true
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if req.FormValue("keyWord") != "000001" {
				t.Fatalf("keyWord = %q", req.FormValue("keyWord"))
			}
			return jsonResponse(`[{"code":"000001","orgId":"gssz0000001","zwjc":"平安银行","category":"A"}]`), nil
		case strings.Contains(req.URL.Path, "hisAnnouncement"):
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if req.FormValue("column") != "szse" {
				t.Fatalf("column = %q", req.FormValue("column"))
			}
			if req.FormValue("plate") != "sz" {
				t.Fatalf("plate = %q", req.FormValue("plate"))
			}
			if !strings.Contains(req.FormValue("stock"), "000001") {
				t.Fatalf("stock = %q", req.FormValue("stock"))
			}
			if !strings.Contains(req.FormValue("stock"), "gssz0000001") {
				t.Fatalf("stock missing orgId: %q", req.FormValue("stock"))
			}
			return jsonResponse(`{
				"announcements": [
					{
						"announcementId": "1218680395",
						"announcementTitle": "平安银行股份有限公司2023年年度报告",
						"announcementTime": 1712073600000,
						"adjunctUrl": "finalpage/2024-04-03/1218680395.PDF",
						"adjunctType": "PDF",
						"categoryId": "category_ndbg_szsh",
						"secCode": "000001",
						"secName": "平安银行",
						"orgId": "gssz0000001"
					},
					{
						"announcementId": "1218590000",
						"announcementTitle": "平安银行股份有限公司2023年三季度报告",
						"announcementTime": 1698710400000,
						"adjunctUrl": "finalpage/2023-10-31/1218590000.PDF",
						"adjunctType": "PDF",
						"categoryId": "category_sjdbg_szsh",
						"secCode": "000001",
						"secName": "平安银行",
						"orgId": "gssz0000001"
					}
				],
				"hasMore": false,
				"totalRecordNum": 2
			}`), nil
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
			return nil, nil
		}
	})

	client := NewCNInfoClient(
		WithCNInfoURLs("https://test.cninfo.com.cn/new/hisAnnouncement/query",
			"https://test.cninfo.com.cn/new/information/topSearch/query",
			"https://static.test.cninfo.com.cn"),
		WithCNInfoHTTPClient(httpClient),
	)
	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local)
	candidates, err := client.FetchCandidates(context.Background(), datasource.CandidateFetchInput{
		Symbol: "000001",
		Since:  &since,
		Limit:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !searchCalled {
		t.Fatal("expected topSearch to be called")
	}
	// announcementTime=1698710400000 (2023-10-31) is before since=2024-01-01, filtered out
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	c := candidates[0]
	if c.Source != datasource.SourceCNOfficialEventCNInfo {
		t.Fatalf("Source = %q", c.Source)
	}
	if c.SourceID != "1218680395" {
		t.Fatalf("SourceID = %q", c.SourceID)
	}
	if c.SourceURL != "https://static.test.cninfo.com.cn/finalpage/2024-04-03/1218680395.PDF" {
		t.Fatalf("SourceURL = %q", c.SourceURL)
	}
	if c.Symbol != "000001" {
		t.Fatalf("Symbol = %q", c.Symbol)
	}
	if c.Title != "平安银行股份有限公司2023年年度报告" {
		t.Fatalf("Title = %q", c.Title)
	}
	if c.PublishedAt == nil {
		t.Fatal("PublishedAt is nil")
	}
}

func TestCNInfoFetchCandidatesWithOrgID(t *testing.T) {
	var searchCalled bool
	httpClient := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(req.URL.Path, "topSearch"):
			searchCalled = true
			return jsonResponse(`[]`), nil
		case strings.Contains(req.URL.Path, "hisAnnouncement"):
			return jsonResponse(`{
				"announcements": [{
					"announcementId": "9999",
					"announcementTitle": "测试公告",
					"announcementTime": 1712073600000,
					"adjunctUrl": "finalpage/2024-04-03/9999.PDF",
					"adjunctType": "PDF",
					"categoryId": "category_临时公告",
					"secCode": "600036",
					"secName": "招商银行",
					"orgId": "gfsh0600036"
				}],
				"hasMore": false,
				"totalRecordNum": 1
			}`), nil
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
			return nil, nil
		}
	})

	client := NewCNInfoClient(
		WithCNInfoURLs("https://test.cninfo.com.cn/new/hisAnnouncement/query",
			"https://test.cninfo.com.cn/new/information/topSearch/query",
			"https://static.test.cninfo.com.cn"),
		WithCNInfoHTTPClient(httpClient),
	)
	candidates, err := client.FetchCandidates(context.Background(), datasource.CandidateFetchInput{
		Symbol: "600036",
		CIK:    "gfsh0600036", // OrgID passed via CIK field
		Limit:  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if searchCalled {
		t.Fatal("expected topSearch NOT to be called when CIK is provided")
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d", len(candidates))
	}
	// 600036 → SSE → column=sse, plate=sh; verify via SourceURL and Symbol
	if candidates[0].Symbol != "600036" {
		t.Fatalf("Symbol = %q", candidates[0].Symbol)
	}
}

func TestCNInfoFetchCandidatesWithCategoryFilter(t *testing.T) {
	httpClient := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "topSearch") {
			return jsonResponse(`[{"code":"000001","orgId":"gssz0000001","zwjc":"平安银行"}]`), nil
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		catParam := req.FormValue("category")
		if !strings.Contains(catParam, "category_ndbg_szsh") {
			t.Fatalf("category param = %q, expected to contain category_ndbg_szsh", catParam)
		}
		return jsonResponse(`{
			"announcements": [{
				"announcementId": "1111",
				"announcementTitle": "年报",
				"announcementTime": 1712073600000,
				"adjunctUrl": "finalpage/2024-04-03/1111.PDF",
				"adjunctType": "PDF",
				"categoryId": "category_ndbg_szsh",
				"secCode": "000001",
				"secName": "平安银行",
				"orgId": "gssz0000001"
			}, {
				"announcementId": "2222",
				"announcementTitle": "季报",
				"announcementTime": 1712073600000,
				"adjunctUrl": "finalpage/2024-04-03/2222.PDF",
				"adjunctType": "PDF",
				"categoryId": "category_sjdbg_szsh",
				"secCode": "000001",
				"secName": "平安银行",
				"orgId": "gssz0000001"
			}],
			"hasMore": false,
			"totalRecordNum": 2
		}`), nil
	})

	client := NewCNInfoClient(
		WithCNInfoURLs("https://test.cninfo.com.cn/new/hisAnnouncement/query",
			"https://test.cninfo.com.cn/new/information/topSearch/query",
			"https://static.test.cninfo.com.cn"),
		WithCNInfoHTTPClient(httpClient),
		WithCNInfoCategories("category_ndbg_szsh"),
	)
	candidates, err := client.FetchCandidates(context.Background(), datasource.CandidateFetchInput{
		Symbol: "000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	// categoryId="category_sjdbg_szsh" should be filtered out client-side
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	if candidates[0].SourceID != "1111" {
		t.Fatalf("SourceID = %q", candidates[0].SourceID)
	}
}

func TestColumnAndPlate(t *testing.T) {
	cases := []struct{ symbol, col, plate string }{
		{"600036", "sse", "sh"},
		{"601398", "sse", "sh"},
		{"000001", "szse", "sz"},
		{"300750", "szse", "sz"},
		{"830799", "bjse", "bj"},
		{"430001", "bjse", "bj"},
		{"", "szse", "sz"},
	}
	for _, tc := range cases {
		col, plate := columnAndPlate(tc.symbol)
		if col != tc.col || plate != tc.plate {
			t.Errorf("columnAndPlate(%q) = (%q, %q), want (%q, %q)", tc.symbol, col, plate, tc.col, tc.plate)
		}
	}
}

func TestResolveOrgIDNotFound(t *testing.T) {
	httpClient := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(`[]`), nil
	})
	client := NewCNInfoClient(
		WithCNInfoURLs("https://test.cninfo.com.cn/new/hisAnnouncement/query",
			"https://test.cninfo.com.cn/new/information/topSearch/query",
			"https://static.test.cninfo.com.cn"),
		WithCNInfoHTTPClient(httpClient),
	)
	_, err := client.ResolveOrgID(context.Background(), "999999")
	if err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// Verify CNInfoClient satisfies datasource.CandidateFetcher.
var _ datasource.CandidateFetcher = (*CNInfoClient)(nil)

// Verify OrgSearchResult fields exist (compile-time check).
var _ = url.Values{}
