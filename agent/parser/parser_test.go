package parser

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"alphathesis/client"
)

func TestThesisParserAgentParseWithLocalQwen3(t *testing.T) {
	if os.Getenv("RUN_LOCAL_VLLM") != "1" {
		t.Skip("set RUN_LOCAL_VLLM=1 to run against local vLLM/Qwen3")
	}

	parser := newLocalQwen3ParserForTest(t)

	parsed, err := parser.Parse(context.Background(), `我看空贵州茅台，认为未来三年高端白酒需求会持续萎缩，渠道库存压力会压低出厂价预期，估值会回到普通消费品水平。`)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "enable-auto-tool-choice") {
			t.Skip("local vLLM was not started with tool calling enabled; restart with --enable-auto-tool-choice and a suitable --tool-call-parser")
		}
		t.Fatalf("Parse() error = %v", err)
	}
	t.Logf("parsed thesis:\n%s", parsed.PrettyString())

	if !isMoutaiParsed(parsed) {
		t.Fatalf("parsed target = symbol %q company %q, want Kweichow Moutai", parsed.Symbol, parsed.CompanyName)
	}
	assertChineseBearishThesis(t, parsed)
}

func TestThesisParserAgentParseWithLocalQwen3ObscureChinaBearish(t *testing.T) {
	if os.Getenv("RUN_LOCAL_VLLM") != "1" {
		t.Skip("set RUN_LOCAL_VLLM=1 to run against local vLLM/Qwen3")
	}

	parser := newLocalQwen3ParserForTest(t)

	parsed, err := parser.Parse(context.Background(), `我看空汇洁股份。这家公司太依赖线下内衣零售渠道，品牌老化，年轻消费者认知弱，线上流量成本又越来越高。如果消费继续分层，它的收入增长和利润率都可能被挤压，股价大概率会继续下跌。`)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "enable-auto-tool-choice") {
			t.Skip("local vLLM was not started with tool calling enabled; restart with --enable-auto-tool-choice and a suitable --tool-call-parser")
		}
		t.Fatalf("Parse() error = %v", err)
	}
	t.Logf("parsed obscure China bearish thesis:\n%s", parsed.PrettyString())

	if !isHuijieParsed(parsed) {
		t.Fatalf("parsed target = symbol %q company %q, want Huijie Group / 汇洁股份", parsed.Symbol, parsed.CompanyName)
	}
	assertChineseBearishThesis(t, parsed)
}

func TestThesisParserAgentParseWithLocalQwen3AndSymbolTool(t *testing.T) {
	if os.Getenv("RUN_LOCAL_VLLM_TOOLS") != "1" {
		t.Skip("set RUN_LOCAL_VLLM_TOOLS=1 to run local vLLM/Qwen3 tool-calling test")
	}

	baseURL := getenvDefault("VLLM_BASE_URL", "http://localhost:8000/v1")
	model := getenvDefault("VLLM_MODEL", "Qwen/Qwen3-8B")
	apiKey := os.Getenv("VLLM_API_KEY")

	llm, err := client.NewVLLMClient(baseURL, apiKey, client.WithTimeout(2*time.Minute))
	if err != nil {
		t.Fatalf("NewVLLMClient() error = %v", err)
	}
	resolver, cleanup := newYFinanceMCPResolverForTest(t)
	defer cleanup()

	p, err := NewThesisParserAgent(llm, model, WithHKUSSymbolResolver(resolver))
	if err != nil {
		t.Fatalf("NewThesisParserAgent() error = %v", err)
	}

	parsed, err := p.Parse(context.Background(), `我看好 Kura Sushi USA，认为它的小型回转寿司门店模式还能继续在美国二三线城市扩张，同店销售会恢复，利润率会改善。`)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	t.Logf("parsed thesis with tool:\n%s", parsed.PrettyString())

	if !isKuraSushiParsed(parsed) {
		t.Fatalf("parsed target = symbol %q company %q, want Kura Sushi USA", parsed.Symbol, parsed.CompanyName)
	}
}

func TestYFinanceMCPSymbolResolver(t *testing.T) {
	if os.Getenv("RUN_YFINANCE_MCP") != "1" {
		t.Skip("set RUN_YFINANCE_MCP=1 to run against yfinance MCP")
	}

	resolver, cleanup := newYFinanceMCPResolverForTest(t)
	defer cleanup()

	query := getenvDefault("YFINANCE_MCP_QUERY", "Kura Sushi USA")
	resolution, err := resolver.ResolveSymbol(context.Background(), query)
	if err != nil {
		t.Fatalf("ResolveSymbol() error = %v", err)
	}
	t.Logf("yfinance MCP resolution:\n%s", marshalToolResult(resolution))
	if !strings.Contains(resolution.RawText, "KRUS") && !strings.Contains(strings.ToLower(resolution.RawText), "kura sushi") {
		t.Fatalf("resolution raw text does not look like Kura Sushi result: %s", resolution.RawText)
	}
}

// ------------------------------------------------------------
// Test helpers
// ------------------------------------------------------------

func getenvDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func newLocalQwen3ParserForTest(t *testing.T) *ThesisParserAgent {
	t.Helper()
	baseURL := getenvDefault("VLLM_BASE_URL", "http://localhost:8000/v1")
	model := getenvDefault("VLLM_MODEL", "Qwen/Qwen3-8B")
	apiKey := os.Getenv("VLLM_API_KEY")

	llm, err := client.NewVLLMClient(baseURL, apiKey, client.WithTimeout(2*time.Minute))
	if err != nil {
		t.Fatalf("NewVLLMClient() error = %v", err)
	}
	p, err := NewThesisParserAgent(llm, model)
	if err != nil {
		t.Fatalf("NewThesisParserAgent() error = %v", err)
	}
	return p
}

func newYFinanceMCPResolverForTest(t *testing.T) (*MCPSymbolResolver, func()) {
	t.Helper()
	command := getenvDefault("YFINANCE_MCP_COMMAND", "docker")
	args := strings.Fields(getenvDefault("YFINANCE_MCP_ARGS", "run -i --rm narumi/yfinance-mcp"))
	toolName := getenvDefault("YFINANCE_MCP_TOOL", "yfinance_search")

	if _, err := exec.LookPath(command); err != nil {
		t.Skipf("MCP command %q not found: %v", command, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	mcpClient, err := client.NewMCPStdioClient(ctx, command, args, client.WithMCPCommandTerminateDuration(3*time.Second))
	if err != nil {
		cancel()
		t.Fatalf("NewMCPStdioClient(%q, %#v) error = %v", command, args, err)
	}

	resolver, err := NewMCPSymbolResolver(mcpClient, toolName)
	if err != nil {
		cancel()
		_ = mcpClient.Close()
		t.Fatalf("NewMCPSymbolResolver() error = %v", err)
	}
	return resolver, func() {
		_ = resolver.Close()
		cancel()
	}
}

func containsCJK(value string) bool {
	for _, r := range value {
		if r >= '一' && r <= '鿿' {
			return true
		}
	}
	return false
}

func isMoutaiParsed(parsed *ParsedThesis) bool {
	symbol := strings.ToUpper(strings.TrimSpace(parsed.Symbol))
	companyName := strings.ToUpper(strings.TrimSpace(parsed.CompanyName))
	switch symbol {
	case "600519", "600519.SH", "600519.SS", "KWEICHOW MOUTAI":
		return true
	}
	return strings.Contains(companyName, "KWEICHOW MOUTAI") || strings.Contains(companyName, "MOUTAI") ||
		strings.Contains(parsed.CompanyName, "贵州茅台") || strings.Contains(parsed.CompanyName, "茅台")
}

func isHuijieParsed(parsed *ParsedThesis) bool {
	symbol := strings.ToUpper(strings.TrimSpace(parsed.Symbol))
	companyName := strings.ToUpper(strings.TrimSpace(parsed.CompanyName))
	switch symbol {
	case "002763", "002763.SZ":
		return true
	}
	return strings.Contains(companyName, "HUIJIE") || strings.Contains(parsed.CompanyName, "汇洁")
}

func isKuraSushiParsed(parsed *ParsedThesis) bool {
	symbol := strings.ToUpper(strings.TrimSpace(parsed.Symbol))
	companyName := strings.ToUpper(strings.TrimSpace(parsed.CompanyName))
	if symbol == "KRUS" {
		return true
	}
	return strings.Contains(companyName, "KURA SUSHI")
}

func assertChineseBearishThesis(t *testing.T, parsed *ParsedThesis) {
	t.Helper()
	if parsed.Direction != DirectionBearish {
		t.Fatalf("Direction = %q, want bearish", parsed.Direction)
	}
	if !containsCJK(parsed.CoreClaim) {
		t.Fatalf("CoreClaim = %q, want Chinese output for Chinese thesis", parsed.CoreClaim)
	}
	if len(parsed.Assumptions) < 3 {
		t.Fatalf("assumption count = %d, want at least 3", len(parsed.Assumptions))
	}
	var total float64
	for _, a := range parsed.Assumptions {
		if a.Key == "" {
			t.Fatalf("empty assumption key in %#v", a)
		}
		if a.Text == "" {
			t.Fatalf("empty assumption text in %#v", a)
		}
		if !containsCJK(a.Text) {
			t.Fatalf("assumption text = %q, want Chinese output for Chinese thesis", a.Text)
		}
		total += a.Importance
	}
	if total < 0.999 || total > 1.001 {
		t.Fatalf("importance total = %f, want 1", total)
	}
}
