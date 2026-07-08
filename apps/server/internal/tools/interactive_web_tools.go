package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

const (
	defaultWebFetchChars  = 6000
	maxWebFetchChars      = 20000
	maxWebFetchRawBytes   = 1_000_000
	defaultSearchResults  = 5
	maxSearchResults      = 10
	webRequestTimeout     = 15 * time.Second
	webRequestUserAgent   = "LmyHarnessAgent/0.1 (+https://github.com/sorrymaker-01/lmy-harness)"
	duckDuckGoSearchURL   = "https://duckduckgo.com/html/"
	contentTypeTextPrefix = "text/"
)

func RegisterInteractiveWeb(registry *runtime.Runtime) {
	registry.Register(NewAskUserQuestionTool())
	registry.Register(NewWebFetchTool())
	registry.Register(NewWebSearchTool())
}

type AskUserQuestionTool struct{}

func NewAskUserQuestionTool() AskUserQuestionTool {
	return AskUserQuestionTool{}
}

func (AskUserQuestionTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:AskUserQuestion",
		Source:      "tool",
		Name:        "AskUserQuestion",
		Description: "当缺少信息会导致任务无法安全或正确推进时，向用户提出一个简洁的澄清问题。返回结构化的等待用户结果，供最终回答展示。",
		InputSchema: runtime.Schema(map[string]any{
			"question": map[string]any{"type": "string", "description": "要询问用户的准确问题"},
			"context":  map[string]any{"type": "string", "description": "需要这个问题的简短原因"},
			"choices": map[string]any{
				"type":        "array",
				"description": "可选的简短答案选项",
				"items":       map[string]any{"type": "string"},
			},
		}, []string{"question"}),
		Risk: contracts.RiskLow,
	}
}

func (AskUserQuestionTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	question, _ := input["question"].(string)
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, errors.New("question is required")
	}
	contextText, _ := input["context"].(string)
	return map[string]any{
		"status":      "waiting_for_user",
		"question":    question,
		"context":     strings.TrimSpace(contextText),
		"choices":     stringSliceFromInput(input["choices"], 8),
		"instruction": "请在最终回答中向用户展示这个问题，并等待用户下一条消息。",
	}, nil
}

type WebFetchTool struct {
	client *http.Client
}

func NewWebFetchTool() WebFetchTool {
	return WebFetchTool{client: newWebHTTPClient()}
}

func (WebFetchTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:WebFetch",
		Source:      "tool",
		Name:        "WebFetch",
		Description: "抓取公开 HTTP/HTTPS URL，并返回可读文本、标题、最终 URL、状态码和内容类型。会阻止私有网络和 localhost 地址。",
		InputSchema: runtime.Schema(map[string]any{
			"url":       map[string]any{"type": "string", "description": "要抓取的公开 HTTP/HTTPS URL"},
			"prompt":    map[string]any{"type": "string", "description": "可选的提取指令，供模型应用到返回内容上"},
			"max_chars": map[string]any{"type": "integer", "description": "返回可读文本的最大字符数。默认 6000，最大 20000。"},
		}, []string{"url"}),
		Risk: contracts.RiskLow,
	}
}

func (t WebFetchTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	rawURL, _ := input["url"].(string)
	parsed, err := validatePublicHTTPURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	maxChars := boundedInt(input["max_chars"], defaultWebFetchChars, 500, maxWebFetchChars)
	prompt, _ := input["prompt"].(string)

	fetched, err := fetchURL(ctx, t.client, parsed.String(), maxWebFetchRawBytes)
	if err != nil {
		return nil, err
	}
	content, title, unsupported := readableContent(fetched.body, fetched.contentType)
	fullContent := strings.TrimSpace(content)
	truncated := len([]rune(fullContent)) > maxChars
	content = shared.TrimRunes(fullContent, maxChars)
	output := map[string]any{
		"url":         parsed.String(),
		"finalUrl":    fetched.finalURL,
		"statusCode":  fetched.statusCode,
		"contentType": fetched.contentType,
		"title":       title,
		"content":     content,
		"truncated":   truncated,
	}
	if strings.TrimSpace(prompt) != "" {
		output["prompt"] = strings.TrimSpace(prompt)
	}
	if unsupported != "" {
		output["warning"] = unsupported
	}
	return output, nil
}

type WebSearchTool struct {
	client *http.Client
}

func NewWebSearchTool() WebSearchTool {
	return WebSearchTool{client: newWebHTTPClient()}
}

func (WebSearchTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:WebSearch",
		Source:      "tool",
		Name:        "WebSearch",
		Description: "在公开 Web 上搜索查询词，并返回包含标题、URL 和摘要的简洁结果列表。本地搜索提供方使用 DuckDuckGo HTML。",
		InputSchema: runtime.Schema(map[string]any{
			"query":       map[string]any{"type": "string", "description": "搜索查询词"},
			"max_results": map[string]any{"type": "integer", "description": "返回结果数量。默认 5，最大 10。"},
		}, []string{"query"}),
		Risk: contracts.RiskLow,
	}
}

func (t WebSearchTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	query, _ := input["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	maxResults := boundedInt(input["max_results"], defaultSearchResults, 1, maxSearchResults)
	searchURL := duckDuckGoSearchURL + "?q=" + url.QueryEscape(query)
	fetched, err := fetchURL(ctx, t.client, searchURL, maxWebFetchRawBytes)
	if err != nil {
		return nil, err
	}
	results := parseDuckDuckGoResults(string(fetched.body), maxResults)
	return map[string]any{
		"query":       query,
		"provider":    "duckduckgo-html",
		"results":     results,
		"resultCount": len(results),
		"statusCode":  fetched.statusCode,
	}, nil
}

type fetchedURL struct {
	body        []byte
	finalURL    string
	statusCode  int
	contentType string
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

func newWebHTTPClient() *http.Client {
	return &http.Client{
		Timeout: webRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if _, err := validatePublicHTTPURL(req.Context(), req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

func fetchURL(ctx context.Context, client *http.Client, rawURL string, maxBytes int64) (fetchedURL, error) {
	reqCtx, cancel := context.WithTimeout(ctx, webRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchedURL{}, err
	}
	req.Header.Set("user-agent", webRequestUserAgent)
	req.Header.Set("accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.8,text/plain;q=0.8,*/*;q=0.5")
	res, err := client.Do(req)
	if err != nil {
		return fetchedURL{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return fetchedURL{}, err
	}
	if int64(len(body)) > maxBytes {
		body = body[:maxBytes]
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fetchedURL{}, fmt.Errorf("request failed: %s", res.Status)
	}
	return fetchedURL{
		body:        body,
		finalURL:    res.Request.URL.String(),
		statusCode:  res.StatusCode,
		contentType: res.Header.Get("content-type"),
	}, nil
}

func validatePublicHTTPURL(ctx context.Context, rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("only http and https URLs are supported")
	}
	if parsed.Host == "" {
		return nil, errors.New("url host is required")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, errors.New("url host is required")
	}
	lowerHost := strings.ToLower(strings.TrimSuffix(host, "."))
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") {
		return nil, errors.New("localhost URLs are blocked")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return nil, errors.New("private, local, and reserved IP addresses are blocked")
		}
		return parsed, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("dns lookup returned no addresses")
	}
	for _, addr := range addrs {
		if isBlockedIP(addr.IP) {
			return nil, errors.New("private, local, and reserved IP addresses are blocked")
		}
	}
	return parsed, nil
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

func readableContent(body []byte, contentType string) (string, string, string) {
	contentType = strings.ToLower(contentType)
	text := string(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	if strings.Contains(contentType, "html") || looksLikeHTML(text) {
		return htmlToText(text), htmlTitle(text), ""
	}
	if isTextContentType(contentType) {
		return normalizeWhitespace(text), "", ""
	}
	return "", "", "unsupported non-text content type; only response metadata was returned"
}

func isTextContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	return strings.HasPrefix(contentType, contentTypeTextPrefix) ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") ||
		strings.Contains(contentType, "javascript")
}

func looksLikeHTML(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") || strings.Contains(lower, "<body")
}

func htmlTitle(value string) string {
	match := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return cleanHTMLText(match[1])
}

func htmlToText(value string) string {
	clean := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`).ReplaceAllString(value, " ")
	clean = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`).ReplaceAllString(clean, " ")
	clean = regexp.MustCompile(`(?is)<!--.*?-->`).ReplaceAllString(clean, " ")
	clean = regexp.MustCompile(`(?i)<\s*(br|p|div|li|tr|h[1-6]|section|article|header|footer|blockquote)[^>]*>`).ReplaceAllString(clean, "\n")
	clean = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(clean, " ")
	return cleanHTMLText(clean)
}

func cleanHTMLText(value string) string {
	value = html.UnescapeString(value)
	return normalizeWhitespace(value)
}

func normalizeWhitespace(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func parseDuckDuckGoResults(page string, maxResults int) []searchResult {
	linkRe := regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	snippetRe := regexp.MustCompile(`(?is)<(?:a|div)[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</(?:a|div)>`)
	linkMatches := linkRe.FindAllStringSubmatchIndex(page, -1)
	results := make([]searchResult, 0, maxResults)
	seen := map[string]struct{}{}
	for i, match := range linkMatches {
		if len(results) >= maxResults {
			break
		}
		href := decodeDuckDuckGoURL(page[match[2]:match[3]])
		title := htmlToText(page[match[4]:match[5]])
		if href == "" || title == "" {
			continue
		}
		if _, ok := seen[href]; ok {
			continue
		}
		seen[href] = struct{}{}
		segmentEnd := len(page)
		if i+1 < len(linkMatches) {
			segmentEnd = linkMatches[i+1][0]
		}
		segment := page[match[1]:segmentEnd]
		snippet := ""
		if snippetMatch := snippetRe.FindStringSubmatch(segment); len(snippetMatch) >= 2 {
			snippet = htmlToText(snippetMatch[1])
		}
		results = append(results, searchResult{
			Title:   title,
			URL:     href,
			Snippet: shared.TrimRunes(snippet, 500),
		})
	}
	return results
}

func decodeDuckDuckGoURL(raw string) string {
	raw = html.UnescapeString(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.Host == "" && strings.HasPrefix(parsed.Path, "/") {
		parsed.Scheme = "https"
		parsed.Host = "duckduckgo.com"
	}
	if parsed.Host != "" && strings.Contains(parsed.Host, "duckduckgo.com") {
		if uddg := parsed.Query().Get("uddg"); uddg != "" {
			if decoded, err := url.QueryUnescape(uddg); err == nil {
				return decoded
			}
			return uddg
		}
	}
	return raw
}

func boundedInt(value any, fallback int, minValue int, maxValue int) int {
	result := intFromInput(value, fallback)
	if result < minValue {
		return minValue
	}
	if result > maxValue {
		return maxValue
	}
	return result
}

func stringSliceFromInput(value any, maxItems int) []string {
	out := []string{}
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
	}
	if len(out) > maxItems {
		return out[:maxItems]
	}
	return out
}
