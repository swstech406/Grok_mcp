package grok

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/grok-mcp/internal/config"
	"github.com/grok-mcp/internal/logx"
)

// Client 通过 HTTP 访问上游 CPA 网关的 /v1/responses 端点（SSE 流式）。
type Client struct {
	baseURL      string
	apiKey       string
	defaultModel string
	httpClient   *http.Client
	log          *logx.Logger
}

// NewClient 根据全局配置构造上游客户端。
func NewClient(cfg *config.Config) *Client {
	return &Client{
		baseURL:      cfg.CPABaseURL,
		apiKey:       cfg.CPAAPIKey,
		defaultModel: cfg.Model,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		log: logx.New("grok", cfg.Debug),
	}
}

// SearchStream 发起流式搜索；每完成一轮 web_search_call 会调用 onRound（可为 nil），
// 最终在 response.completed 事件到达后返回聚合后的 SearchResult。
func (c *Client) SearchStream(ctx context.Context, req SearchRequest, onRound func(SearchRound)) (*SearchResult, error) {
	validatedRequest, err := validateSearchRequest(req)
	if err != nil {
		return nil, err
	}
	req = validatedRequest

	model, body, err := c.buildSearchRequestBody(req)
	if err != nil {
		return nil, err
	}
	c.log.Debugf("SearchStream start model=%s tool=%s query=%q", model, req.ToolType, logx.Truncate(req.Query, 80))

	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.httpError(resp)
	}

	result, err := parseSearchStream(resp.Body, onRound, c.log)
	if err != nil {
		return nil, err
	}
	if result.Usage != nil {
		c.log.Debugf("SearchStream done tokens=%d", result.Usage.TotalTokens)
	}
	return result, nil
}

// post 向上游发送 JSON 请求，Accept 为 text/event-stream 以接收 SSE。
func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

// httpError 在非 2xx 响应时返回分类错误，仅包含状态码，不透传响应体（可能含上游敏感信息）。
func (c *Client) httpError(resp *http.Response) error {
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		c.log.Debugf("upstream HTTP %d read body error: %v", resp.StatusCode, readErr)
		return fmt.Errorf("upstream returned HTTP %d: read body: %w", resp.StatusCode, readErr)
	}
	c.log.Debugf("upstream HTTP %d: %s", resp.StatusCode, logx.Truncate(string(respBody), 256))
	// 错误信息只暴露状态码，body 仅写日志，避免向 MCP 客户端泄露 CPA 内部细节。
	return fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
}
