package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "http://localhost:8000/v1"

// VLLMClient calls vLLM's OpenAI-compatible HTTP API.
type VLLMClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	debugLog   io.Writer
}

// ClientOption customizes a VLLMClient.
type ClientOption func(*VLLMClient)

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *VLLMClient) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithTimeout sets the timeout on the default HTTP client.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *VLLMClient) {
		if timeout > 0 {
			c.httpClient.Timeout = timeout
		}
	}
}

// WithDebugLog writes full vLLM request and response payload logs to writer.
//
// Authorization headers are redacted, but request and response JSON bodies are
// logged in full. Pass os.Stdout or os.Stderr when debugging model behavior.
func WithDebugLog(writer io.Writer) ClientOption {
	return func(c *VLLMClient) {
		c.debugLog = writer
	}
}

// WithoutDebugLog disables vLLM request and response debug logging.
func WithoutDebugLog() ClientOption {
	return func(c *VLLMClient) {
		c.debugLog = nil
	}
}

// NewVLLMClient creates a client for a vLLM OpenAI-compatible endpoint.
//
// baseURL should normally look like http://host:8000/v1. If empty, the local
// vLLM default is used. apiKey may be empty when vLLM is launched without auth.
func NewVLLMClient(baseURL, apiKey string, opts ...ClientOption) (*VLLMClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse vllm base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid vllm base url %q: scheme and host are required", baseURL)
	}

	c := &VLLMClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// BaseURL returns the configured vLLM API base URL.
func (c *VLLMClient) BaseURL() string {
	return c.baseURL
}

// Model describes one model returned by /models.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
	Root    string `json:"root,omitempty"`
	Parent  string `json:"parent,omitempty"`
}

// ModelsResponse is the response from /models.
type ModelsResponse struct {
	Object string  `json:"object,omitempty"`
	Data   []Model `json:"data"`
}

// ModelResponse is the response from /models/{model}.
type ModelResponse = Model

// ChatMessage is an OpenAI-compatible chat message.
type ChatMessage struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

// ToolCall represents a tool/function call emitted by chat completions.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function,omitempty"`
}

// FunctionCall contains OpenAI-compatible function-call data.
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ChatCompletionRequest is the request body for /chat/completions.
type ChatCompletionRequest struct {
	Model            string                 `json:"model"`
	Messages         []ChatMessage          `json:"messages"`
	Temperature      *float64               `json:"temperature,omitempty"`
	TopP             *float64               `json:"top_p,omitempty"`
	MaxTokens        *int                   `json:"max_tokens,omitempty"`
	N                *int                   `json:"n,omitempty"`
	Stop             interface{}            `json:"stop,omitempty"`
	Stream           bool                   `json:"stream,omitempty"`
	PresencePenalty  *float64               `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64               `json:"frequency_penalty,omitempty"`
	User             string                 `json:"user,omitempty"`
	Tools            []Tool                 `json:"tools,omitempty"`
	ToolChoice       interface{}            `json:"tool_choice,omitempty"`
	Extra            map[string]interface{} `json:"-"`
}

// Tool describes a tool available to the model.
type Tool struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

// ToolDefinition describes an OpenAI-compatible function tool.
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// ChatCompletionResponse is the response from /chat/completions.
type ChatCompletionResponse struct {
	ID                string                 `json:"id"`
	Object            string                 `json:"object"`
	Created           int64                  `json:"created"`
	Model             string                 `json:"model"`
	Choices           []ChatCompletionChoice `json:"choices"`
	Usage             Usage                  `json:"usage,omitempty"`
	SystemFingerprint string                 `json:"system_fingerprint,omitempty"`
}

// ChatCompletionChoice contains one chat completion candidate.
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

// ChatCompletionStreamResponse is one SSE chunk from streaming chat completions.
type ChatCompletionStreamResponse struct {
	ID                string                       `json:"id"`
	Object            string                       `json:"object"`
	Created           int64                        `json:"created"`
	Model             string                       `json:"model"`
	Choices           []ChatCompletionStreamChoice `json:"choices"`
	Usage             *Usage                       `json:"usage,omitempty"`
	SystemFingerprint string                       `json:"system_fingerprint,omitempty"`
}

// ChatCompletionStreamChoice contains a streamed chat delta.
type ChatCompletionStreamChoice struct {
	Index        int         `json:"index"`
	Delta        ChatMessage `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

// CompletionRequest is the request body for /completions.
type CompletionRequest struct {
	Model            string                 `json:"model"`
	Prompt           interface{}            `json:"prompt"`
	Suffix           string                 `json:"suffix,omitempty"`
	MaxTokens        *int                   `json:"max_tokens,omitempty"`
	Temperature      *float64               `json:"temperature,omitempty"`
	TopP             *float64               `json:"top_p,omitempty"`
	N                *int                   `json:"n,omitempty"`
	Stream           bool                   `json:"stream,omitempty"`
	Logprobs         *int                   `json:"logprobs,omitempty"`
	Echo             *bool                  `json:"echo,omitempty"`
	Stop             interface{}            `json:"stop,omitempty"`
	PresencePenalty  *float64               `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64               `json:"frequency_penalty,omitempty"`
	User             string                 `json:"user,omitempty"`
	Extra            map[string]interface{} `json:"-"`
}

// CompletionResponse is the response from /completions.
type CompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"`
	Usage   Usage              `json:"usage,omitempty"`
}

// CompletionChoice contains one text completion candidate.
type CompletionChoice struct {
	Index        int         `json:"index"`
	Text         string      `json:"text"`
	Logprobs     interface{} `json:"logprobs,omitempty"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

// EmbeddingRequest is the request body for /embeddings.
type EmbeddingRequest struct {
	Model          string                 `json:"model"`
	Input          interface{}            `json:"input"`
	EncodingFormat string                 `json:"encoding_format,omitempty"`
	User           string                 `json:"user,omitempty"`
	Extra          map[string]interface{} `json:"-"`
}

// EmbeddingResponse is the response from /embeddings.
type EmbeddingResponse struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  Usage           `json:"usage,omitempty"`
}

// EmbeddingData contains one embedding vector.
type EmbeddingData struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

// TokenizeRequest is the request body for /tokenize.
type TokenizeRequest struct {
	Model            string                 `json:"model,omitempty"`
	Prompt           string                 `json:"prompt,omitempty"`
	Messages         []ChatMessage          `json:"messages,omitempty"`
	AddSpecialTokens *bool                  `json:"add_special_tokens,omitempty"`
	Extra            map[string]interface{} `json:"-"`
}

// TokenizeResponse is the response from /tokenize.
type TokenizeResponse struct {
	Count       int      `json:"count,omitempty"`
	Tokens      []int    `json:"tokens,omitempty"`
	TokenStrs   []string `json:"token_strs,omitempty"`
	MaxModelLen int      `json:"max_model_len,omitempty"`
}

// DetokenizeRequest is the request body for /detokenize.
type DetokenizeRequest struct {
	Model  string                 `json:"model,omitempty"`
	Tokens []int                  `json:"tokens"`
	Extra  map[string]interface{} `json:"-"`
}

// DetokenizeResponse is the response from /detokenize.
type DetokenizeResponse struct {
	Prompt string `json:"prompt,omitempty"`
	Text   string `json:"text,omitempty"`
}

// ScoreRequest is the request body for /score.
type ScoreRequest struct {
	Model          string                 `json:"model,omitempty"`
	Text1          interface{}            `json:"text_1"`
	Text2          interface{}            `json:"text_2"`
	EncodingFormat string                 `json:"encoding_format,omitempty"`
	Extra          map[string]interface{} `json:"-"`
}

// ScoreResponse is the response from /score.
type ScoreResponse struct {
	ID     string      `json:"id,omitempty"`
	Object string      `json:"object,omitempty"`
	Data   []ScoreData `json:"data,omitempty"`
	Model  string      `json:"model,omitempty"`
	Usage  Usage       `json:"usage,omitempty"`
}

// ScoreData contains one similarity or reranking score.
type ScoreData struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// RerankRequest is the request body for /rerank.
type RerankRequest struct {
	Model           string                 `json:"model,omitempty"`
	Query           string                 `json:"query"`
	Documents       []string               `json:"documents"`
	TopN            *int                   `json:"top_n,omitempty"`
	ReturnDocuments *bool                  `json:"return_documents,omitempty"`
	MaxChunksPerDoc *int                   `json:"max_chunks_per_doc,omitempty"`
	Extra           map[string]interface{} `json:"-"`
}

// RerankResponse is the response from /rerank.
type RerankResponse struct {
	ID      string         `json:"id,omitempty"`
	Results []RerankResult `json:"results,omitempty"`
	Model   string         `json:"model,omitempty"`
	Usage   Usage          `json:"usage,omitempty"`
}

// RerankResult contains one ranked document result.
type RerankResult struct {
	Index          int             `json:"index"`
	Document       *RerankDocument `json:"document,omitempty"`
	RelevanceScore float64         `json:"relevance_score"`
}

// RerankDocument contains a document returned by /rerank.
type RerankDocument struct {
	Text string `json:"text,omitempty"`
}

// Usage reports token usage when the server includes it.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

// APIError is returned for non-2xx responses from vLLM.
type APIError struct {
	StatusCode int
	Status     string
	Message    string
	Type       string
	Code       interface{}
	Body       string
}

func (e *APIError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = strings.TrimSpace(e.Body)
	}
	if message == "" {
		message = e.Status
	}
	return fmt.Sprintf("vllm api error: status=%d message=%s", e.StatusCode, message)
}

// DoJSON sends a raw JSON request to a vLLM endpoint and decodes the JSON response.
//
// path may be relative to BaseURL, such as "/chat/completions", or an absolute
// URL. Use this for vLLM extensions that do not yet have a typed helper here.
func (c *VLLMClient) DoJSON(ctx context.Context, method, path string, payload interface{}, out interface{}) error {
	return c.do(ctx, method, path, payload, out)
}

// ListModels calls GET /models.
func (c *VLLMClient) ListModels(ctx context.Context) (*ModelsResponse, error) {
	var out ModelsResponse
	if err := c.do(ctx, http.MethodGet, "/models", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetrieveModel calls GET /models/{model}.
func (c *VLLMClient) RetrieveModel(ctx context.Context, model string) (*ModelResponse, error) {
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("model is required")
	}
	var out ModelResponse
	if err := c.do(ctx, http.MethodGet, "/models/"+url.PathEscape(model), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateChatCompletion calls POST /chat/completions.
func (c *VLLMClient) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	req.Stream = false
	var out ChatCompletionResponse
	if err := c.do(ctx, http.MethodPost, "/chat/completions", req.withExtra(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StreamChatCompletion calls POST /chat/completions with stream=true.
func (c *VLLMClient) StreamChatCompletion(ctx context.Context, req ChatCompletionRequest, onChunk func(ChatCompletionStreamResponse) error) error {
	if onChunk == nil {
		return errors.New("onChunk callback is required")
	}
	req.Stream = true
	return c.stream(ctx, "/chat/completions", req.withExtra(), func(data []byte) error {
		var chunk ChatCompletionStreamResponse
		if err := json.Unmarshal(data, &chunk); err != nil {
			return fmt.Errorf("decode chat stream chunk: %w", err)
		}
		return onChunk(chunk)
	})
}

// CreateCompletion calls POST /completions.
func (c *VLLMClient) CreateCompletion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	req.Stream = false
	var out CompletionResponse
	if err := c.do(ctx, http.MethodPost, "/completions", req.withExtra(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StreamCompletion calls POST /completions with stream=true.
func (c *VLLMClient) StreamCompletion(ctx context.Context, req CompletionRequest, onChunk func(CompletionResponse) error) error {
	if onChunk == nil {
		return errors.New("onChunk callback is required")
	}
	req.Stream = true
	return c.stream(ctx, "/completions", req.withExtra(), func(data []byte) error {
		var chunk CompletionResponse
		if err := json.Unmarshal(data, &chunk); err != nil {
			return fmt.Errorf("decode completion stream chunk: %w", err)
		}
		return onChunk(chunk)
	})
}

// CreateEmbedding calls POST /embeddings.
func (c *VLLMClient) CreateEmbedding(ctx context.Context, req EmbeddingRequest) (*EmbeddingResponse, error) {
	var out EmbeddingResponse
	if err := c.do(ctx, http.MethodPost, "/embeddings", req.withExtra(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Tokenize calls POST /tokenize.
func (c *VLLMClient) Tokenize(ctx context.Context, req TokenizeRequest) (*TokenizeResponse, error) {
	var out TokenizeResponse
	if err := c.do(ctx, http.MethodPost, "/tokenize", req.withExtra(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Detokenize calls POST /detokenize.
func (c *VLLMClient) Detokenize(ctx context.Context, req DetokenizeRequest) (*DetokenizeResponse, error) {
	var out DetokenizeResponse
	if err := c.do(ctx, http.MethodPost, "/detokenize", req.withExtra(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Score calls POST /score.
func (c *VLLMClient) Score(ctx context.Context, req ScoreRequest) (*ScoreResponse, error) {
	var out ScoreResponse
	if err := c.do(ctx, http.MethodPost, "/score", req.withExtra(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Rerank calls POST /rerank.
func (c *VLLMClient) Rerank(ctx context.Context, req RerankRequest) (*RerankResponse, error) {
	var out RerankResponse
	if err := c.do(ctx, http.MethodPost, "/rerank", req.withExtra(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *VLLMClient) do(ctx context.Context, method, path string, payload interface{}, out interface{}) error {
	httpReq, err := c.newRequest(ctx, method, path, payload)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send vllm request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(resp.Body)
	c.logResponse(httpReq, resp, bodyBytes)
	if readErr != nil {
		return fmt.Errorf("read vllm response: %w", readErr)
	}

	if err := decodeAPIErrorBytes(resp, bodyBytes); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(bodyBytes, out); err != nil {
		return fmt.Errorf("decode vllm response: %w", err)
	}
	return nil
}

func (c *VLLMClient) stream(ctx context.Context, path string, payload interface{}, onData func([]byte) error) error {
	httpReq, err := c.newRequest(ctx, http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send vllm stream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		c.logResponse(httpReq, resp, bodyBytes)
		return decodeAPIErrorBytes(resp, bodyBytes)
	}
	c.logResponse(httpReq, resp, nil)
	if err := decodeAPIError(resp); err != nil {
		return err
	}
	return scanSSE(resp.Body, func(data []byte) error {
		c.logStreamChunk(httpReq, data)
		return onData(data)
	})
}

func (c *VLLMClient) newRequest(ctx context.Context, method, path string, payload interface{}) (*http.Request, error) {
	if c == nil {
		return nil, errors.New("vllm client is nil")
	}
	if c.httpClient == nil {
		return nil, errors.New("vllm http client is nil")
	}

	var body io.Reader
	var bodyBytes []byte
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode vllm request: %w", err)
		}
		bodyBytes = data
		body = bytes.NewReader(bodyBytes)
	}

	endpoint, err := c.endpoint(path)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create vllm request: %w", err)
	}
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	c.logRequest(httpReq, bodyBytes)
	return httpReq, nil
}

func (c *VLLMClient) endpoint(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("vllm request path is required")
	}
	if parsed, err := url.Parse(path); err == nil && parsed.IsAbs() {
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.baseURL + path, nil
}

func decodeAPIError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	return decodeAPIErrorBytes(resp, bodyBytes)
}

func decodeAPIErrorBytes(resp *http.Response, bodyBytes []byte) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       string(bodyBytes),
	}

	var envelope struct {
		Error struct {
			Message string      `json:"message"`
			Type    string      `json:"type"`
			Code    interface{} `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(bodyBytes, &envelope) == nil {
		apiErr.Message = envelope.Error.Message
		apiErr.Type = envelope.Error.Type
		apiErr.Code = envelope.Error.Code
	}
	return apiErr
}

func (c *VLLMClient) logRequest(req *http.Request, body []byte) {
	if c == nil || c.debugLog == nil {
		return
	}
	fmt.Fprintf(c.debugLog, "=== vLLM request ===\n")
	fmt.Fprintf(c.debugLog, "%s %s\n", req.Method, req.URL.String())
	fmt.Fprintf(c.debugLog, "headers: %s\n", mustJSON(sanitizeHeaders(req.Header)))
	fmt.Fprintf(c.debugLog, "body: %s\n", logBody(body))
}

func (c *VLLMClient) logResponse(req *http.Request, resp *http.Response, body []byte) {
	if c == nil || c.debugLog == nil {
		return
	}
	fmt.Fprintf(c.debugLog, "=== vLLM response ===\n")
	fmt.Fprintf(c.debugLog, "%s %s -> %s\n", req.Method, req.URL.String(), resp.Status)
	fmt.Fprintf(c.debugLog, "headers: %s\n", mustJSON(sanitizeHeaders(resp.Header)))
	if body == nil {
		fmt.Fprintf(c.debugLog, "body: <stream>\n")
		return
	}
	fmt.Fprintf(c.debugLog, "body: %s\n", logBody(body))
}

func (c *VLLMClient) logStreamChunk(req *http.Request, data []byte) {
	if c == nil || c.debugLog == nil {
		return
	}
	fmt.Fprintf(c.debugLog, "=== vLLM stream chunk ===\n")
	fmt.Fprintf(c.debugLog, "%s %s\n", req.Method, req.URL.String())
	fmt.Fprintf(c.debugLog, "data: %s\n", logBody(data))
}

func sanitizeHeaders(headers http.Header) map[string][]string {
	out := make(map[string][]string, len(headers))
	for key, values := range headers {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Proxy-Authorization") {
			out[key] = []string{"<redacted>"}
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}

func logBody(body []byte) string {
	if len(body) == 0 {
		return "<empty>"
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil {
		return pretty.String()
	}
	return string(body)
}

func mustJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

func scanSSE(r io.Reader, onData func([]byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if data == "" || data == "[DONE]" {
			return nil
		}
		return onData([]byte(data))
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read vllm stream: %w", err)
	}
	return flush()
}

func (r ChatCompletionRequest) withExtra() map[string]interface{} {
	return mergeJSONFields(r, r.Extra)
}

func (r CompletionRequest) withExtra() map[string]interface{} {
	return mergeJSONFields(r, r.Extra)
}

func (r EmbeddingRequest) withExtra() map[string]interface{} {
	return mergeJSONFields(r, r.Extra)
}

func (r TokenizeRequest) withExtra() map[string]interface{} {
	return mergeJSONFields(r, r.Extra)
}

func (r DetokenizeRequest) withExtra() map[string]interface{} {
	return mergeJSONFields(r, r.Extra)
}

func (r ScoreRequest) withExtra() map[string]interface{} {
	return mergeJSONFields(r, r.Extra)
}

func (r RerankRequest) withExtra() map[string]interface{} {
	return mergeJSONFields(r, r.Extra)
}

func mergeJSONFields(v interface{}, extra map[string]interface{}) map[string]interface{} {
	data, _ := json.Marshal(v)
	var fields map[string]interface{}
	_ = json.Unmarshal(data, &fields)
	for k, value := range extra {
		fields[k] = value
	}
	return fields
}
