// Package grok 封装对 CPA 兼容 Grok /v1/responses 接口的调用：
// 构建 web_search / x_search 工具请求、解析 SSE 流、汇总答案与引用。
package grok

import "encoding/json"

// ToolType 对应上游内置工具 type 字段。
type ToolType string

const (
	ToolTypeWebSearch ToolType = "web_search"
	ToolTypeXSearch   ToolType = "x_search"
)

// SearchRequest 描述一次搜索调用的业务参数（由 MCP 层映射而来）。
type SearchRequest struct {
	Query                    string
	Model                    string
	ToolType                 ToolType
	AllowedDomains           []string
	ExcludedDomains          []string
	EnableImageUnderstanding *bool
	EnableImageSearch        *bool
}

// Usage 为上游返回的 token 用量统计。
type Usage struct {
	InputTokens     int `json:"input_tokens,omitempty"`
	OutputTokens    int `json:"output_tokens,omitempty"`
	TotalTokens     int `json:"total_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Source 表示一条被引用的来源，通常来自 content 块中的 url_citation 注解。
type Source struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// SearchRound 表示流式响应中一轮 web_search_call 动作（查询或抓取 URL）。
type SearchRound struct {
	Round int
	Query string
	URL   string
}

// SearchResult 为解析完成后的结构化搜索结果。
type SearchResult struct {
	Answer      string
	Citations   []string
	Sources     []Source
	Usage       *Usage
	RawResponse json.RawMessage
}