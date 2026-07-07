package grok

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

// allowedModels 为可透传至 CPA 的模型白名单；用户指定的 model 与默认模型均须命中。
var allowedModels = map[string]struct{}{
	"grok-4.3": {},
}

func validateModel(model string) error {
	if _, ok := allowedModels[model]; !ok {
		return fmt.Errorf("unsupported model: %q", model)
	}
	return nil
}

// validateSearchRequest 校验查询、工具类型，以及 web_search 域名过滤参数的互斥与数量上限。
func validateSearchRequest(req SearchRequest) (SearchRequest, error) {
	if strings.TrimSpace(req.Query) == "" {
		return req, fmt.Errorf("query must not be empty")
	}
	if req.ToolType != ToolTypeWebSearch && req.ToolType != ToolTypeXSearch {
		return req, fmt.Errorf("unsupported tool type: %q", req.ToolType)
	}
	if req.ToolType != ToolTypeWebSearch {
		return req, nil
	}
	if len(req.AllowedDomains) > 0 && len(req.ExcludedDomains) > 0 {
		return req, fmt.Errorf("allowed_domains and excluded_domains cannot be used together")
	}
	if len(req.AllowedDomains) > 5 {
		return req, fmt.Errorf("allowed_domains supports at most 5 entries")
	}
	if len(req.ExcludedDomains) > 5 {
		return req, fmt.Errorf("excluded_domains supports at most 5 entries")
	}

	allowedDomains, err := normalizeDomainFilters("allowed_domains", req.AllowedDomains)
	if err != nil {
		return req, err
	}
	excludedDomains, err := normalizeDomainFilters("excluded_domains", req.ExcludedDomains)
	if err != nil {
		return req, err
	}
	req.AllowedDomains = allowedDomains
	req.ExcludedDomains = excludedDomains
	return req, nil
}

func normalizeDomainFilters(fieldName string, rawDomains []string) ([]string, error) {
	if len(rawDomains) == 0 {
		return nil, nil
	}
	normalizedDomains := make([]string, 0, len(rawDomains))
	for _, rawDomain := range rawDomains {
		normalizedDomain, err := normalizeDomainFilter(rawDomain)
		if err != nil {
			return nil, fmt.Errorf("%s entry %q is invalid: %w", fieldName, rawDomain, err)
		}
		normalizedDomains = append(normalizedDomains, normalizedDomain)
	}
	return normalizedDomains, nil
}

func normalizeDomainFilter(rawDomain string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(rawDomain))
	if domain == "" {
		return "", fmt.Errorf("domain must not be empty")
	}
	if len(domain) > 253 {
		return "", fmt.Errorf("domain is too long")
	}
	if strings.Contains(domain, "://") {
		return "", fmt.Errorf("scheme is not allowed")
	}
	if strings.ContainsAny(domain, "/\\?#@:") {
		return "", fmt.Errorf("path, query, port, and userinfo are not allowed")
	}
	if strings.Contains(domain, "*") {
		return "", fmt.Errorf("wildcards are not allowed")
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return "", fmt.Errorf("domain labels must be non-empty")
	}
	if domain == "localhost" || strings.HasSuffix(domain, ".localhost") || strings.HasSuffix(domain, ".local") {
		return "", fmt.Errorf("local hostnames are not allowed")
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return "", fmt.Errorf("IP literals are not allowed")
	}

	for _, label := range strings.Split(domain, ".") {
		if len(label) > 63 {
			return "", fmt.Errorf("domain label is too long")
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("domain labels must not start or end with hyphen")
		}
		for _, character := range label {
			isLowerLetter := character >= 'a' && character <= 'z'
			isDigit := character >= '0' && character <= '9'
			if !isLowerLetter && !isDigit && character != '-' {
				return "", fmt.Errorf("domain contains unsupported characters")
			}
		}
	}
	return domain, nil
}

// buildToolDef 将业务侧 SearchRequest 映射为上游 tools[] 中的单条工具定义。
// 域名与图片相关选项仅对 web_search 生效。
func (c *Client) buildToolDef(req SearchRequest) toolDef {
	tool := toolDef{Type: string(req.ToolType)}

	if req.ToolType == ToolTypeWebSearch {
		tool.AllowedDomains = req.AllowedDomains
		tool.ExcludedDomains = req.ExcludedDomains
		tool.EnableImageUnderstanding = req.EnableImageUnderstanding
		tool.EnableImageSearch = req.EnableImageSearch
	}

	return tool
}

// buildSearchRequestBody 组装 /v1/responses 请求体；未指定 model 时使用客户端默认模型。
func (c *Client) buildSearchRequestBody(req SearchRequest) (string, []byte, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.defaultModel
	}
	if err := validateModel(model); err != nil {
		return "", nil, err
	}

	upstreamReq := responsesRequest{
		Model:  model,
		Input:  []inputMessage{{Role: "user", Content: req.Query}},
		Tools:  []toolDef{c.buildToolDef(req)},
		Stream: true,
	}

	body, err := json.Marshal(upstreamReq)
	if err != nil {
		return "", nil, fmt.Errorf("marshal request: %w", err)
	}
	return model, body, nil
}
