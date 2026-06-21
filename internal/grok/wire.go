package grok

import "encoding/json"

// 以下类型与上游 OpenAI Responses 风格 JSON 字段一一对应，仅用于编解码，不暴露给 MCP 层。

// responsesRequest 为 POST /v1/responses 的请求体。
type responsesRequest struct {
	Model  string         `json:"model"`
	Input  []inputMessage `json:"input"`
	Tools  []toolDef      `json:"tools"`
	Stream bool           `json:"stream"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// toolDef 描述上游内置工具（web_search / x_search）及其可选参数。
type toolDef struct {
	Type                     string   `json:"type"`
	AllowedDomains           []string `json:"allowed_domains,omitempty"`
	ExcludedDomains          []string `json:"excluded_domains,omitempty"`
	EnableImageUnderstanding *bool    `json:"enable_image_understanding,omitempty"`
	EnableImageSearch        *bool    `json:"enable_image_search,omitempty"`
}

// responsesResponse 出现在流式 response.completed 事件中的完整响应快照。
type responsesResponse struct {
	Output    []outputItem    `json:"output"`
	Usage     json.RawMessage `json:"usage"`
	Citations json.RawMessage `json:"citations"`
}

type outputItem struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type        string       `json:"type"`
	Text        string       `json:"text"`
	Annotations []annotation `json:"annotations"`
}

type annotation struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type citationItem struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// streamEvent 为 SSE data 行解析后的单条事件。
type streamEvent struct {
	Type     string            `json:"type"`
	Item     streamOutputItem  `json:"item"`
	Response responsesResponse `json:"response"`
}

type streamOutputItem struct {
	Type   string          `json:"type"`
	Action webSearchAction `json:"action"`
}

type webSearchAction struct {
	Query string `json:"query"`
	URL   string `json:"url"`
}