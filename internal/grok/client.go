package grok

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/logx"
)

const upstreamIdleConnectionPoolSize = 100

// Client 通过 HTTP 访问上游 CPA 网关的 /v1/responses 端点（SSE 流式）。
type Client struct {
	mu           sync.RWMutex
	baseURL      string
	apiKey       string
	protocol     config.UpstreamProtocol
	defaultModel string
	httpClient   *http.Client
	debugState   *logx.DebugState
	log          *logx.Logger
}

type clientSnapshot struct {
	baseURL      string
	apiKey       string
	protocol     config.UpstreamProtocol
	defaultModel string
	httpClient   *http.Client
	log          *logx.Logger
}

// NewClientWithServerSettings constructs a client from validated runtime
// settings. It returns configuration errors instead of silently falling back.
func NewClientWithServerSettings(settings config.ServerSettings, debugState *logx.DebugState) (*Client, error) {
	if debugState == nil {
		debugState = logx.NewDebugState(settings.Debug)
	}
	client := &Client{
		debugState: debugState,
		log:        logx.NewWithDebugState("grok", debugState),
	}
	if err := client.ApplyServerSettings(settings); err != nil {
		return nil, err
	}
	return client, nil
}

// ApplyServerSettings atomically swaps the upstream connection settings used by
// subsequent search requests. In-flight searches keep their connection snapshot,
// while the shared debug switch takes effect immediately for every logger.
func (c *Client) ApplyServerSettings(settings config.ServerSettings) error {
	protocol, err := config.NormalizeUpstreamProtocol(settings.UpstreamProtocol)
	if err != nil {
		return err
	}
	timeout := time.Duration(settings.TimeoutSeconds) * time.Second
	httpClient, err := newHTTPClientWithProxy(timeout, settings.ProxyURL, settings.ProxyEnabled)
	if err != nil {
		return err
	}

	c.mu.Lock()
	previousHTTPClient := c.httpClient
	c.baseURL = settings.CPABaseURL
	c.apiKey = settings.CPAAPIKey
	c.protocol = protocol
	c.defaultModel = settings.Model
	c.httpClient = httpClient
	c.debugState.SetEnabled(settings.Debug)
	c.mu.Unlock()

	// Existing requests retain their client snapshot. Closing only idle
	// connections releases resources from the replaced transport without
	// interrupting those in-flight requests.
	if previousHTTPClient != nil {
		previousHTTPClient.CloseIdleConnections()
	}
	return nil
}

func (c *Client) snapshot() clientSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return clientSnapshot{
		baseURL:      c.baseURL,
		apiKey:       c.apiKey,
		protocol:     c.protocol,
		defaultModel: c.defaultModel,
		httpClient:   c.httpClient,
		log:          c.log,
	}
}

func newHTTPClientWithProxy(timeout time.Duration, proxyURL string, proxyEnabled bool) (*http.Client, error) {
	if timeout <= 0 {
		timeout = defaultTimeoutFallback()
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = upstreamIdleConnectionPoolSize
	transport.MaxIdleConnsPerHost = upstreamIdleConnectionPoolSize
	explicitProxyURL := strings.TrimSpace(proxyURL)
	if proxyEnabled {
		if explicitProxyURL == "" {
			return nil, fmt.Errorf("proxy URL is required when proxy is enabled")
		}
		parsedProxyURL, err := url.Parse(explicitProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(parsedProxyURL)
	} else {
		// No explicit proxy is configured, so honor standard HTTP_PROXY,
		// HTTPS_PROXY, and NO_PROXY environment variables for outbound requests.
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func defaultTimeoutFallback() time.Duration {
	return 120 * time.Second
}

// SearchStream 发起流式搜索；每完成一轮 web_search_call 或 x_search_call 会调用 onRound（可为 nil），
// 最终在 response.completed 事件到达后返回聚合后的 SearchResult。
func (c *Client) SearchStream(ctx context.Context, req SearchRequest, onRound func(SearchRound)) (*SearchResult, error) {
	snapshot := c.snapshot()
	validatedRequest, err := validateSearchRequest(req)
	if err != nil {
		return nil, err
	}
	req = validatedRequest

	var result *SearchResult
	switch snapshot.protocol {
	case config.UpstreamProtocolResponses:
		result, err = snapshot.searchResponses(ctx, req, onRound)
	case config.UpstreamProtocolChatCompletions:
		result, err = snapshot.searchChatCompletions(ctx, req, onRound)
	case config.UpstreamProtocolAnthropicMessages:
		result, err = snapshot.searchAnthropicMessages(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported upstream protocol: %q", snapshot.protocol)
	}
	if err != nil {
		return nil, err
	}
	if result.Usage != nil {
		snapshot.log.Debugf("SearchStream done tokens=%d", result.Usage.TotalTokens)
	}
	return result, nil
}

func (s clientSnapshot) searchResponses(ctx context.Context, req SearchRequest, onRound func(SearchRound)) (*SearchResult, error) {
	model, body, err := buildSearchRequestBody(req, s.defaultModel)
	if err != nil {
		return nil, err
	}
	s.log.Debugf("SearchStream start protocol=%s model=%s tool=%s query=%q", s.protocol, model, req.ToolType, logx.Truncate(req.Query, 80))

	response, err := s.postJSON(ctx, "/v1/responses", body, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, s.httpError(response)
	}
	return parseSearchStream(response.Body, onRound, s.log)
}

// post 向上游发送 JSON 请求，Accept 为 text/event-stream 以接收 SSE。
func (s clientSnapshot) postJSON(ctx context.Context, endpoint string, body []byte, anthropicHeaders bool) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if anthropicHeaders {
		// CPA protects every /v1 route with its Bearer key while Anthropic-compatible
		// clients conventionally send x-api-key. Supplying both keeps the request
		// compatible with CPA and stricter Messages implementations.
		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
		httpReq.Header.Set("x-api-key", s.apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

// httpError 在非 2xx 响应时返回分类错误，仅包含状态码，不透传响应体（可能含上游敏感信息）。
func (s clientSnapshot) httpError(resp *http.Response) error {
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		s.log.Debugf("upstream HTTP %d read body error: %v", resp.StatusCode, readErr)
		return fmt.Errorf("upstream returned HTTP %d: read body: %w", resp.StatusCode, readErr)
	}
	s.log.Debugf("upstream HTTP %d: %s", resp.StatusCode, logx.Truncate(string(respBody), 256))
	// 错误信息只暴露状态码，body 仅写日志，避免向 MCP 客户端泄露 CPA 内部细节。
	return fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
}
