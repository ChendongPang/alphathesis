package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"alphathesis/client"
)

const defaultResolveSymbolToolName = "resolve_symbol"

// SymbolResolver resolves a company name or ticker hint into market symbols.
type SymbolResolver interface {
	ResolveSymbol(ctx context.Context, query string) (*SymbolResolution, error)
}

// SymbolResolution is returned to the LLM after a resolve_symbol tool call.
type SymbolResolution struct {
	Query      string            `json:"query"`
	Candidates []SymbolCandidate `json:"candidates"`
	Source     string            `json:"source"`
	RawText    string            `json:"raw_text,omitempty"`
}

// SymbolCandidate is one possible resolved security.
type SymbolCandidate struct {
	Symbol      string                 `json:"symbol"`
	CompanyName string                 `json:"company_name"`
	Exchange    string                 `json:"exchange,omitempty"`
	Currency    string                 `json:"currency,omitempty"`
	Score       float64                `json:"score,omitempty"`
	Raw         map[string]interface{} `json:"raw,omitempty"`
}

// MCPSymbolResolver calls an MCP search-like tool to resolve symbols.
type MCPSymbolResolver struct {
	mcpClient *client.MCPClient
	toolName  string
}

// NewMCPSymbolResolver creates a SymbolResolver backed by an MCP tool.
func NewMCPSymbolResolver(mcpClient *client.MCPClient, toolName string) (*MCPSymbolResolver, error) {
	if mcpClient == nil {
		return nil, errors.New("mcp client is required")
	}
	if strings.TrimSpace(toolName) == "" {
		return nil, errors.New("mcp symbol resolver tool name is required")
	}
	return &MCPSymbolResolver{
		mcpClient: mcpClient,
		toolName:  strings.TrimSpace(toolName),
	}, nil
}

// ResolveSymbol calls the configured MCP tool with a query.
func (r *MCPSymbolResolver) ResolveSymbol(ctx context.Context, query string) (*SymbolResolution, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("symbol query is required")
	}
	rawText, err := r.callSearchTool(ctx, r.toolName, query)
	if err != nil {
		return nil, err
	}
	return &SymbolResolution{
		Query:   query,
		Source:  "mcp:" + r.toolName,
		RawText: rawText,
	}, nil
}

func (r *MCPSymbolResolver) callSearchTool(ctx context.Context, toolName, query string) (string, error) {
	res, err := r.mcpClient.CallTool(ctx, toolName, map[string]any{
		"query":       query,
		"search_type": "quotes",
	})
	if err != nil {
		return "", err
	}
	rawText := client.MCPTextContent(res.Content)
	if res.IsError {
		return "", fmt.Errorf("mcp symbol resolver tool %q returned error: %s", toolName, rawText)
	}
	return rawText, nil
}

// Close closes the underlying MCP client.
func (r *MCPSymbolResolver) Close() error {
	if r == nil || r.mcpClient == nil {
		return nil
	}
	return r.mcpClient.Close()
}

// AKShareSymbolResolver resolves A-share symbols via the local AKShare HTTP adapter.
type AKShareSymbolResolver struct {
	baseURL string
	http    *http.Client
}

// NewAKShareSymbolResolver creates a resolver that queries the AKShare adapter at baseURL.
func NewAKShareSymbolResolver(baseURL string) *AKShareSymbolResolver {
	return &AKShareSymbolResolver{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

type akShareSearchResult struct {
	Symbol      string `json:"symbol"`
	CompanyName string `json:"company_name"`
	Exchange    string `json:"exchange"`
	Currency    string `json:"currency"`
}

// ResolveSymbol queries /search_symbol on the AKShare adapter with a Chinese company name.
func (r *AKShareSymbolResolver) ResolveSymbol(ctx context.Context, query string) (*SymbolResolution, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("symbol query is required")
	}
	u := r.baseURL + "/search_symbol?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build akshare search request: %w", err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("akshare search request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read akshare search response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("akshare search returned status %d: %s", resp.StatusCode, string(body))
	}
	var rows []akShareSearchResult
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode akshare search response: %w", err)
	}
	candidates := make([]SymbolCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, SymbolCandidate{
			Symbol:      row.Symbol,
			CompanyName: row.CompanyName,
			Exchange:    row.Exchange,
			Currency:    row.Currency,
		})
	}
	return &SymbolResolution{
		Query:      query,
		Candidates: candidates,
		Source:     "akshare",
	}, nil
}

func marshalToolResult(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}
