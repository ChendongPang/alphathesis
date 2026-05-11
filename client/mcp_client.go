package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMCPClientName    = "alphathesis-mcp-client"
	defaultMCPClientVersion = "v0.1.0"
)

// MCPClient wraps the official MCP Go SDK client/session behind a small
// project-facing API.
type MCPClient struct {
	client  *mcp.Client
	session *mcp.ClientSession
}

// MCPClientOption customizes an MCP client before it connects.
type MCPClientOption func(*mcpClientConfig)

type mcpClientConfig struct {
	name             string
	title            string
	version          string
	httpClient       *http.Client
	headers          http.Header
	keepAlive        time.Duration
	commandTerminate time.Duration
	streamMaxRetries int
	disableHTTPRetry bool
	toolChanged      func(context.Context, *mcp.ToolListChangedRequest)
	promptChanged    func(context.Context, *mcp.PromptListChangedRequest)
	resourceChanged  func(context.Context, *mcp.ResourceListChangedRequest)
	resourceUpdated  func(context.Context, *mcp.ResourceUpdatedNotificationRequest)
	loggingMessage   func(context.Context, *mcp.LoggingMessageRequest)
	progressNotify   func(context.Context, *mcp.ProgressNotificationClientRequest)
	createMessage    func(context.Context, *mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error)
	elicit           func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)
}

func defaultMCPClientConfig() mcpClientConfig {
	return mcpClientConfig{
		name:    defaultMCPClientName,
		version: defaultMCPClientVersion,
		headers: make(http.Header),
	}
}

// WithMCPImplementation sets the MCP implementation identity advertised during
// initialize.
func WithMCPImplementation(name, version string) MCPClientOption {
	return func(c *mcpClientConfig) {
		if strings.TrimSpace(name) != "" {
			c.name = name
		}
		if strings.TrimSpace(version) != "" {
			c.version = version
		}
	}
}

// WithMCPTitle sets the human-readable client title advertised during initialize.
func WithMCPTitle(title string) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.title = title
	}
}

// WithMCPHTTPClient sets the HTTP client used by streamable HTTP transport.
func WithMCPHTTPClient(httpClient *http.Client) MCPClientOption {
	return func(c *mcpClientConfig) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithMCPHeader adds a header to streamable HTTP transport requests.
func WithMCPHeader(key, value string) MCPClientOption {
	return func(c *mcpClientConfig) {
		if strings.TrimSpace(key) == "" {
			return
		}
		c.headers.Add(key, value)
	}
}

// WithMCPBearerToken adds an Authorization bearer token to streamable HTTP
// transport requests.
func WithMCPBearerToken(token string) MCPClientOption {
	return func(c *mcpClientConfig) {
		if token != "" {
			c.headers.Set("Authorization", "Bearer "+token)
		}
	}
}

// WithMCPKeepAlive enables SDK keepalive pings for connected sessions.
func WithMCPKeepAlive(interval time.Duration) MCPClientOption {
	return func(c *mcpClientConfig) {
		if interval > 0 {
			c.keepAlive = interval
		}
	}
}

// WithMCPCommandTerminateDuration controls how long stdio transport waits for
// a server process to exit before terminating it.
func WithMCPCommandTerminateDuration(duration time.Duration) MCPClientOption {
	return func(c *mcpClientConfig) {
		if duration > 0 {
			c.commandTerminate = duration
		}
	}
}

// WithMCPStreamMaxRetries sets streamable HTTP reconnect attempts.
func WithMCPStreamMaxRetries(maxRetries int) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.streamMaxRetries = maxRetries
	}
}

// WithoutMCPStreamRetry disables streamable HTTP reconnect attempts.
func WithoutMCPStreamRetry() MCPClientOption {
	return func(c *mcpClientConfig) {
		c.disableHTTPRetry = true
	}
}

// WithMCPToolListChangedHandler handles tools/list_changed notifications.
func WithMCPToolListChangedHandler(handler func(context.Context, *mcp.ToolListChangedRequest)) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.toolChanged = handler
	}
}

// WithMCPPromptListChangedHandler handles prompts/list_changed notifications.
func WithMCPPromptListChangedHandler(handler func(context.Context, *mcp.PromptListChangedRequest)) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.promptChanged = handler
	}
}

// WithMCPResourceListChangedHandler handles resources/list_changed notifications.
func WithMCPResourceListChangedHandler(handler func(context.Context, *mcp.ResourceListChangedRequest)) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.resourceChanged = handler
	}
}

// WithMCPResourceUpdatedHandler handles resources/updated notifications.
func WithMCPResourceUpdatedHandler(handler func(context.Context, *mcp.ResourceUpdatedNotificationRequest)) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.resourceUpdated = handler
	}
}

// WithMCPLoggingMessageHandler handles server logging notifications.
func WithMCPLoggingMessageHandler(handler func(context.Context, *mcp.LoggingMessageRequest)) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.loggingMessage = handler
	}
}

// WithMCPProgressNotificationHandler handles server progress notifications.
func WithMCPProgressNotificationHandler(handler func(context.Context, *mcp.ProgressNotificationClientRequest)) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.progressNotify = handler
	}
}

// WithMCPSamplingHandler enables and handles sampling/createMessage requests
// from MCP servers.
func WithMCPSamplingHandler(handler func(context.Context, *mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error)) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.createMessage = handler
	}
}

// WithMCPElicitationHandler enables and handles elicitation/create requests
// from MCP servers.
func WithMCPElicitationHandler(handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) MCPClientOption {
	return func(c *mcpClientConfig) {
		c.elicit = handler
	}
}

// NewMCPStdioClient starts an MCP server command and connects over stdio.
func NewMCPStdioClient(ctx context.Context, command string, args []string, opts ...MCPClientOption) (*MCPClient, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("mcp command is required")
	}

	cfg := buildMCPClientConfig(opts...)
	transport := &mcp.CommandTransport{
		Command:           exec.CommandContext(ctx, command, args...),
		TerminateDuration: cfg.commandTerminate,
	}
	return NewMCPClientWithTransport(ctx, transport, opts...)
}

// NewMCPStreamableHTTPClient connects to an MCP streamable HTTP endpoint.
func NewMCPStreamableHTTPClient(ctx context.Context, endpoint string, opts ...MCPClientOption) (*MCPClient, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("mcp endpoint is required")
	}

	cfg := buildMCPClientConfig(opts...)
	httpClient := cfg.httpClient
	if len(cfg.headers) > 0 {
		httpClient = cloneHTTPClient(httpClient)
		httpClient.Transport = headerRoundTripper{
			base:    httpClient.Transport,
			headers: cfg.headers.Clone(),
		}
	}

	maxRetries := cfg.streamMaxRetries
	if cfg.disableHTTPRetry {
		maxRetries = -1
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: httpClient,
		MaxRetries: maxRetries,
	}
	return NewMCPClientWithTransport(ctx, transport, opts...)
}

// NewMCPClientWithTransport connects using a caller-provided MCP transport.
func NewMCPClientWithTransport(ctx context.Context, transport mcp.Transport, opts ...MCPClientOption) (*MCPClient, error) {
	if transport == nil {
		return nil, errors.New("mcp transport is required")
	}

	cfg := buildMCPClientConfig(opts...)
	sdkClient := mcp.NewClient(&mcp.Implementation{
		Name:    cfg.name,
		Title:   cfg.title,
		Version: cfg.version,
	}, &mcp.ClientOptions{
		CreateMessageHandler:        cfg.createMessage,
		ElicitationHandler:          cfg.elicit,
		ToolListChangedHandler:      cfg.toolChanged,
		PromptListChangedHandler:    cfg.promptChanged,
		ResourceListChangedHandler:  cfg.resourceChanged,
		ResourceUpdatedHandler:      cfg.resourceUpdated,
		LoggingMessageHandler:       cfg.loggingMessage,
		ProgressNotificationHandler: cfg.progressNotify,
		KeepAlive:                   cfg.keepAlive,
	})

	session, err := sdkClient.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp server: %w", err)
	}
	return &MCPClient{client: sdkClient, session: session}, nil
}

// RawClient returns the underlying official SDK client for advanced features.
func (c *MCPClient) RawClient() *mcp.Client {
	if c == nil {
		return nil
	}
	return c.client
}

// RawSession returns the underlying official SDK session for advanced features.
func (c *MCPClient) RawSession() *mcp.ClientSession {
	if c == nil {
		return nil
	}
	return c.session
}

// InitializeResult returns the negotiated MCP initialize result.
func (c *MCPClient) InitializeResult() (*mcp.InitializeResult, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}
	return c.session.InitializeResult(), nil
}

// SessionID returns the transport session ID when the transport has one.
func (c *MCPClient) SessionID() (string, error) {
	if err := c.ensureSession(); err != nil {
		return "", err
	}
	return c.session.ID(), nil
}

// Close closes the MCP session.
func (c *MCPClient) Close() error {
	if err := c.ensureSession(); err != nil {
		return err
	}
	return c.session.Close()
}

// Wait waits for the MCP session to close from the server side.
func (c *MCPClient) Wait() error {
	if err := c.ensureSession(); err != nil {
		return err
	}
	return c.session.Wait()
}

// Ping sends an MCP ping request.
func (c *MCPClient) Ping(ctx context.Context) error {
	if err := c.ensureSession(); err != nil {
		return err
	}
	return c.session.Ping(ctx, nil)
}

// ListTools returns all server tools, following pagination.
func (c *MCPClient) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}

	var tools []*mcp.Tool
	cursor := ""
	for {
		res, err := c.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list mcp tools: %w", err)
		}
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			return tools, nil
		}
		cursor = res.NextCursor
	}
}

// CallTool calls a server tool by name.
func (c *MCPClient) CallTool(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("mcp tool name is required")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}

	res, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("call mcp tool %q: %w", name, err)
	}
	return res, nil
}

// CallToolText calls a server tool and concatenates text content blocks.
func (c *MCPClient) CallToolText(ctx context.Context, name string, arguments map[string]any) (string, error) {
	res, err := c.CallTool(ctx, name, arguments)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", &MCPToolError{Name: name, Content: MCPTextContent(res.Content)}
	}
	return MCPTextContent(res.Content), nil
}

// ListResources returns all server resources, following pagination.
func (c *MCPClient) ListResources(ctx context.Context) ([]*mcp.Resource, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}

	var resources []*mcp.Resource
	cursor := ""
	for {
		res, err := c.session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list mcp resources: %w", err)
		}
		resources = append(resources, res.Resources...)
		if res.NextCursor == "" {
			return resources, nil
		}
		cursor = res.NextCursor
	}
}

// ListResourceTemplates returns all server resource templates, following pagination.
func (c *MCPClient) ListResourceTemplates(ctx context.Context) ([]*mcp.ResourceTemplate, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}

	var templates []*mcp.ResourceTemplate
	cursor := ""
	for {
		res, err := c.session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list mcp resource templates: %w", err)
		}
		templates = append(templates, res.ResourceTemplates...)
		if res.NextCursor == "" {
			return templates, nil
		}
		cursor = res.NextCursor
	}
}

// ReadResource reads a server resource by URI.
func (c *MCPClient) ReadResource(ctx context.Context, uri string) ([]*mcp.ResourceContents, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(uri) == "" {
		return nil, errors.New("mcp resource uri is required")
	}

	res, err := c.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("read mcp resource %q: %w", uri, err)
	}
	return res.Contents, nil
}

// SubscribeResource subscribes to updates for a server resource.
func (c *MCPClient) SubscribeResource(ctx context.Context, uri string) error {
	if err := c.ensureSession(); err != nil {
		return err
	}
	if strings.TrimSpace(uri) == "" {
		return errors.New("mcp resource uri is required")
	}
	return c.session.Subscribe(ctx, &mcp.SubscribeParams{URI: uri})
}

// UnsubscribeResource unsubscribes from updates for a server resource.
func (c *MCPClient) UnsubscribeResource(ctx context.Context, uri string) error {
	if err := c.ensureSession(); err != nil {
		return err
	}
	if strings.TrimSpace(uri) == "" {
		return errors.New("mcp resource uri is required")
	}
	return c.session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: uri})
}

// ListPrompts returns all server prompts, following pagination.
func (c *MCPClient) ListPrompts(ctx context.Context) ([]*mcp.Prompt, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}

	var prompts []*mcp.Prompt
	cursor := ""
	for {
		res, err := c.session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list mcp prompts: %w", err)
		}
		prompts = append(prompts, res.Prompts...)
		if res.NextCursor == "" {
			return prompts, nil
		}
		cursor = res.NextCursor
	}
}

// GetPrompt renders a server prompt by name.
func (c *MCPClient) GetPrompt(ctx context.Context, name string, arguments map[string]string) (*mcp.GetPromptResult, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("mcp prompt name is required")
	}

	res, err := c.session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("get mcp prompt %q: %w", name, err)
	}
	return res, nil
}

// SetLoggingLevel asks the server to send logging notifications at level or higher.
func (c *MCPClient) SetLoggingLevel(ctx context.Context, level mcp.LoggingLevel) error {
	if err := c.ensureSession(); err != nil {
		return err
	}
	return c.session.SetLoggingLevel(ctx, &mcp.SetLoggingLevelParams{Level: level})
}

// MCPTextContent extracts and joins text content blocks.
func MCPTextContent(contents []mcp.Content) string {
	var parts []string
	for _, content := range contents {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "")
}

// MCPToolError reports a tool-level error returned inside a successful MCP response.
type MCPToolError struct {
	Name    string
	Content string
}

func (e *MCPToolError) Error() string {
	if strings.TrimSpace(e.Content) == "" {
		return fmt.Sprintf("mcp tool %q returned an error", e.Name)
	}
	return fmt.Sprintf("mcp tool %q returned an error: %s", e.Name, e.Content)
}

func (c *MCPClient) ensureSession() error {
	if c == nil || c.session == nil {
		return errors.New("mcp client is not connected")
	}
	return nil
}

func buildMCPClientConfig(opts ...MCPClientOption) mcpClientConfig {
	cfg := defaultMCPClientConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

func cloneHTTPClient(httpClient *http.Client) *http.Client {
	if httpClient == nil {
		return &http.Client{}
	}
	clone := *httpClient
	return &clone
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers http.Header
}

func (t headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	for key, values := range t.headers {
		for _, value := range values {
			cloned.Header.Add(key, value)
		}
	}

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}
