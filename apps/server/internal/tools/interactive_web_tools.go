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

// Web/交互类工具的全局限额与常量。
const (
	defaultWebFetchChars  = 6000                                                                  // WebFetch 默认返回的可读文本字符数
	maxWebFetchChars      = 20000                                                                 // WebFetch 可读文本上限（防止撑爆模型上下文）
	maxWebFetchRawBytes   = 1_000_000                                                             // HTTP 响应原始字节读取上限（1MB，防止下载超大文件）
	defaultSearchResults  = 5                                                                     // WebSearch 默认结果条数
	maxSearchResults      = 10                                                                    // WebSearch 结果条数上限
	webRequestTimeout     = 15 * time.Second                                                      // 单次 HTTP 请求总超时
	webRequestUserAgent   = "LmyHarnessAgent/0.1 (+https://github.com/sorrymaker-01/lmy-harness)" // 自报家门的 UA，便于站点识别爬虫来源
	duckDuckGoSearchURL   = "https://duckduckgo.com/html/"                                        // DuckDuckGo 的纯 HTML 版搜索入口（无 JS、易解析、无需 API key）
	contentTypeTextPrefix = "text/"                                                               // 判定文本类响应的 Content-Type 前缀
)

// RegisterInteractiveWeb 注册“交互 + 联网”三件套：
// AskUserQuestion（向用户提澄清问题）、WebFetch（抓取网页）、WebSearch（Web 搜索）。
func RegisterInteractiveWeb(registry *runtime.Runtime) {
	registry.Register(NewAskUserQuestionTool())
	registry.Register(NewWebFetchTool())
	registry.Register(NewWebSearchTool())
}

// AskUserQuestionTool 让模型在信息不足时向用户提出澄清问题。
// 它不做任何 IO，也不真正“暂停”执行：只是把问题包装成结构化结果返回给模型，
// 并通过 instruction 字段指示模型在最终回答中把问题呈现给用户、等待用户下一条消息。
// 即前端无需为此实现专门的交互组件——交互闭环由“模型转述 + 用户回复”天然完成。
type AskUserQuestionTool struct{}

// NewAskUserQuestionTool 创建 AskUserQuestion 工具（无状态）。
func NewAskUserQuestionTool() AskUserQuestionTool {
	return AskUserQuestionTool{}
}

// Tool 返回 AskUserQuestion 的元信息：question 必填，context（提问原因）与
// choices（候选答案列表）可选，choices 可供前端/模型渲染成快捷选项。
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

// Invoke 组装“等待用户”结构化结果。关键点：
//   - status=waiting_for_user 作为状态标记，供上层/前端识别当前处于待用户输入状态；
//   - choices 最多保留 8 个（stringSliceFromInput 会裁剪并去掉空白项）；
//   - instruction 是写给模型自己看的指令：让它在最终回答中转述问题并停止继续行动，
//     从而把"工具结果"转化为一次真实的人机交互回合。
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

// WebFetchTool 抓取公开 HTTP/HTTPS URL，并把 HTML 转成模型可读的纯文本。
// 完全基于 Go 标准库（net/http + regexp）实现，不依赖 headless 浏览器或第三方抓取服务，
// 因此拿到的是原始 HTML（不执行 JS）。内置 SSRF 防护：拒绝 localhost/私网/保留 IP。
type WebFetchTool struct {
	client *http.Client // 复用的 HTTP 客户端，带超时与重定向安全校验
}

// NewWebFetchTool 创建 WebFetch 工具，HTTP 客户端在此一次性构造并复用（连接池）。
func NewWebFetchTool() WebFetchTool {
	return WebFetchTool{client: newWebHTTPClient()}
}

// Tool 返回 WebFetch 的元信息：url 必填；prompt 是可选的“提取指令”（仅原样透传回输出，
// 供模型对返回内容自行应用）；max_chars 控制可读文本长度（500~20000，默认 6000）。
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

// Invoke 抓取 URL 并返回结构化结果。流程与安全要点：
//  1. validatePublicHTTPURL 做 SSRF 校验（协议白名单 + localhost/私网 IP 黑名单 + DNS 解析检查），
//     不通过直接返回 error（属于安全性失败，不给模型“绕过”的余地）；
//  2. fetchURL 带 1MB 原始字节上限和 15s 超时；重定向链路上每一跳也会重新做 SSRF 校验；
//  3. readableContent 按 Content-Type 分流：HTML 剥标签转纯文本并提取 <title>，
//     普通文本类原样规整空白，二进制类型只返回元信息 + warning；
//  4. 按 rune 截断到 max_chars（防止把多字节字符截成半个），并用 truncated 标记告知模型。
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
	// 用 rune 数判断是否截断，与 TrimRunes 的截断口径保持一致。
	truncated := len([]rune(fullContent)) > maxChars
	content = shared.TrimRunes(fullContent, maxChars)
	output := map[string]any{
		"url":         parsed.String(),
		"finalUrl":    fetched.finalURL, // 重定向后的最终地址，便于模型识别跳转
		"statusCode":  fetched.statusCode,
		"contentType": fetched.contentType,
		"title":       title,
		"content":     content,
		"truncated":   truncated,
	}
	if strings.TrimSpace(prompt) != "" {
		// prompt 只是原样带回：提醒模型“用户希望按这个指令处理内容”，工具本身不做模型调用。
		output["prompt"] = strings.TrimSpace(prompt)
	}
	if unsupported != "" {
		output["warning"] = unsupported
	}
	return output, nil
}

// WebSearchTool 在公开 Web 上执行搜索。实现方式：抓取 DuckDuckGo 的纯 HTML 版
// 搜索页（duckduckgo.com/html/）并用正则解析结果，无需任何 API key 或付费搜索服务。
// 代价是解析依赖 DDG 的页面结构（class="result__a" 等），页面改版时需要同步调整正则。
type WebSearchTool struct {
	client *http.Client // 与 WebFetch 相同配置的安全 HTTP 客户端
}

// NewWebSearchTool 创建 WebSearch 工具。
func NewWebSearchTool() WebSearchTool {
	return WebSearchTool{client: newWebHTTPClient()}
}

// Tool 返回 WebSearch 的元信息：query 必填，max_results 可选（1~10，默认 5）。
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

// Invoke 执行搜索：拼出 DDG HTML 版查询 URL（query 做 URL 转义）→ 复用 fetchURL 抓取
// →正则解析出前 N 条结果（标题/链接/摘要）→ 返回结构化列表。
// provider 字段明确标注结果来源，便于前端展示与后续替换搜索后端。
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

// fetchedURL 是一次 HTTP 抓取的结果快照。
type fetchedURL struct {
	body        []byte // 响应体（已按 maxBytes 截断）
	finalURL    string // 经过重定向后的最终 URL
	statusCode  int
	contentType string
}

// searchResult 是单条搜索结果，字段名与前端约定的 JSON 输出格式一致。
type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// newWebHTTPClient 构造 Web 工具共用的 HTTP 客户端：
//   - 全局 15s 超时；
//   - CheckRedirect 限制最多 5 跳，且每一跳重定向目标都重新做 SSRF 校验——
//     这是防护关键：否则攻击者可用公网 URL 302 跳转到内网地址绕过首次校验。
func newWebHTTPClient() *http.Client {
	return &http.Client{
		Timeout: webRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			// 对重定向目标再次执行公网校验，封堵“跳转进内网”的 SSRF 路径。
			if _, err := validatePublicHTTPURL(req.Context(), req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

// fetchURL 执行一次受限的 GET 请求：
//   - context 超时叠加 client 超时双保险；
//   - 设置自定义 UA（对站点透明）与偏向文本内容的 Accept 头；
//   - 用 io.LimitReader(maxBytes+1) 限制读取量：多读 1 字节是为了区分“正好 maxBytes”
//     和“超出被截断”两种情况，超出部分直接丢弃；
//   - 非 2xx 状态一律视为失败返回 error。
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
		finalURL:    res.Request.URL.String(), // res.Request 是重定向链的最后一个请求
		statusCode:  res.StatusCode,
		contentType: res.Header.Get("content-type"),
	}, nil
}

// validatePublicHTTPURL 是 Web 工具的 SSRF 防线，逐层校验：
//  1. 语法：URL 可解析、scheme 仅限 http/https（排除 file://、gopher:// 等）、host 非空；
//  2. 主机名：拦截 localhost 及 *.localhost（含尾点写法 "localhost."）；
//  3. 字面 IP：直接检查是否属于被禁网段；
//  4. 域名：先做 DNS 解析（3s 超时），对解析出的**每一个** IP 都检查，
//     任意一个命中私网/保留段即拒绝——防止攻击者用一条 A 记录指向内网的域名绕过。
//
// 注意仍存在理论上的 DNS rebinding 窗口（校验与实际连接是两次解析），
// 但配合重定向逐跳复检，已覆盖绝大多数 SSRF 场景。
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
	// TrimSuffix 去掉 FQDN 尾点，防止 "localhost." 这类写法绕过判断。
	lowerHost := strings.ToLower(strings.TrimSuffix(host, "."))
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") {
		return nil, errors.New("localhost URLs are blocked")
	}
	if ip := net.ParseIP(host); ip != nil {
		// 字面 IP：无需 DNS，直接判定。
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
		// 只要有一个解析结果落在禁用网段就整体拒绝（域名可能同时解析出公网+内网地址）。
		if isBlockedIP(addr.IP) {
			return nil, errors.New("private, local, and reserved IP addresses are blocked")
		}
	}
	return parsed, nil
}

// isBlockedIP 判定 IP 是否属于禁止访问的网段：回环、RFC1918 私网、
// 链路本地（含 169.254.169.254 云元数据地址）、组播以及未指定地址（0.0.0.0/::）。
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

// readableContent 把 HTTP 响应体转成模型可读文本。返回 (正文, 标题, 不支持类型的警告)：
//   - 先剥掉 UTF-8 BOM；
//   - Content-Type 含 html、或声明缺失但内容“长得像 HTML”时，走 HTML→纯文本 + 提取标题；
//   - 其他文本类（text/*、json、xml、javascript）只做空白规整；
//   - 二进制类型不返回正文，用第三个返回值给出警告文案。
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

// isTextContentType 判断 Content-Type 是否属于可直接展示的文本类。
// 空 Content-Type 乐观地按文本处理（很多简单服务不返回该头）。
func isTextContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	return strings.HasPrefix(contentType, contentTypeTextPrefix) ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") ||
		strings.Contains(contentType, "javascript")
}

// looksLikeHTML 用内容嗅探兜底：即使服务器没有正确声明 Content-Type，
// 只要以 <!doctype html>/<html> 开头或包含 <body> 也按 HTML 处理。
func looksLikeHTML(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") || strings.Contains(lower, "<body")
}

// htmlTitle 用正则提取 <title> 内容（(?is)：忽略大小写 + . 匹配换行），并做实体解码与空白规整。
func htmlTitle(value string) string {
	match := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return cleanHTMLText(match[1])
}

// htmlToText 是轻量级的 HTML→纯文本转换（正则实现，不引入 HTML 解析器依赖）：
//  1. 整块删除 <script>/<style>/注释（内容对阅读无意义且噪音大）；
//  2. 把块级标签（br/p/div/li/h1-h6 等）的开标签替换为换行，保留文档的段落结构；
//  3. 剩余所有标签替换为空格；最后解码 HTML 实体并规整空白。
//
// 局限：不执行 JS、不处理畸形嵌套，但对“给模型读正文”这一目标足够。
func htmlToText(value string) string {
	clean := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`).ReplaceAllString(value, " ")
	clean = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`).ReplaceAllString(clean, " ")
	clean = regexp.MustCompile(`(?is)<!--.*?-->`).ReplaceAllString(clean, " ")
	clean = regexp.MustCompile(`(?i)<\s*(br|p|div|li|tr|h[1-6]|section|article|header|footer|blockquote)[^>]*>`).ReplaceAllString(clean, "\n")
	clean = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(clean, " ")
	return cleanHTMLText(clean)
}

// cleanHTMLText 解码 HTML 实体（&amp; → & 等）后做空白规整。
func cleanHTMLText(value string) string {
	value = html.UnescapeString(value)
	return normalizeWhitespace(value)
}

// normalizeWhitespace 规整空白：统一换行符；每行内部连续空白压成单个空格；
// 连续多个空行压成一个空行（保留段落分隔感但去除大片空白），首尾空白去除。
func normalizeWhitespace(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			// 只在前面已有内容且上一行不是空行时保留一个空行。
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

// parseDuckDuckGoResults 用正则从 DDG HTML 版结果页解析搜索结果。解析策略：
//   - 标题链接锚点：class 含 "result__a" 的 <a>，捕获 href 与锚文本；
//   - 摘要：class 含 "result__snippet" 的 <a> 或 <div>；
//   - 用 FindAllStringSubmatchIndex 拿到每条链接的位置，把“当前链接之后、下一条链接之前”
//     的 HTML 片段作为该结果的归属区间，在区间内查找摘要——避免摘要与标题错位配对；
//   - href 经 decodeDuckDuckGoURL 还原真实目标地址；用 seen 集合按 URL 去重；
//   - 摘要按 rune 截到 500 字符。
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
		// match[2]:match[3] 是 href 捕获组，match[4]:match[5] 是锚文本捕获组。
		href := decodeDuckDuckGoURL(page[match[2]:match[3]])
		title := htmlToText(page[match[4]:match[5]])
		if href == "" || title == "" {
			continue
		}
		if _, ok := seen[href]; ok {
			continue
		}
		seen[href] = struct{}{}
		// 当前结果的 HTML 片段：从本链接结束到下一个链接开始（最后一条到页尾）。
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

// decodeDuckDuckGoURL 把 DDG 结果页里的链接还原为真实目标 URL。
// DDG 的结果链接通常是形如 //duckduckgo.com/l/?uddg=<编码后的真实URL> 的跳转链接，
// 这里依次处理：HTML 实体解码 → 补全协议相对（//）前缀 → 补全站内相对路径的 host →
// 若是 duckduckgo.com 域名且带 uddg 参数，则 URL 解码取出真实地址；否则原样返回。
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
		// 站内相对路径（如 /l/?uddg=...）：补全为 duckduckgo.com 绝对地址以便取参。
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

// boundedInt 在 intFromInput 宽容转换的基础上，把结果钳制到 [minValue, maxValue] 区间，
// 用于对模型传入的数值参数（max_chars、max_results 等）做上下限保护。
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

// stringSliceFromInput 宽容地把模型传入的 JSON 数组转成字符串切片：
// 兼容 []string 与 []any（JSON 反序列化的实际类型）两种形态，
// 过滤空白项、非字符串项，并按 maxItems 截断（防止模型给出超长选项列表）。
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
