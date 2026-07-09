package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/claudecode"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
)

// protocolVersion 是本客户端声明支持的 MCP（Model Context Protocol）
// 协议版本，在 initialize 握手时发送给服务端协商。
const protocolVersion = "2024-11-05"

// Client 是一个 stdio 传输的 MCP 客户端：管理一个 MCP server 子进程，
// 通过其 stdin/stdout 以 "Content-Length 头 + JSON-RPC 2.0 消息体"
// 的帧格式（LSP 风格）进行请求/响应通信。
// mu 串行化所有读写：一次只允许一个未完成的请求在途，
// 因而无需维护 id -> 等待者 的并发映射，实现简单且足够可靠。
type Client struct {
	server claudecode.MCPServer // 该子进程对应的配置（名称、命令、环境变量等）
	cmd    *exec.Cmd            // MCP server 子进程句柄
	stdin  io.WriteCloser       // 子进程标准输入：写出 JSON-RPC 请求
	reader *bufio.Reader        // 子进程标准输出：读取 JSON-RPC 响应
	mu     sync.Mutex           // 串行化请求，保证请求-响应一一配对
	nextID int                  // JSON-RPC 请求 id 自增计数器
}

// Tool 是 MCP server 通过 tools/list 声明的一个工具定义，
// 字段与 MCP 协议的 tool 对象一致（inputSchema 为 JSON Schema）。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolInvoker 是把"某个 MCP client 上的某个工具"适配成
// runtime.Runtime 可注册工具的适配器（实现 runtime 的工具接口：
// Tool() 提供元数据，Invoke() 执行调用）。
type toolInvoker struct {
	client *Client
	tool   Tool
}

// RegisterConfiguredServers 按配置批量启动 MCP server 并把它们的
// 工具注册进运行时。config 来自 claudecode.LoadStartupContext 对
// .mcp.json / .claude.json 的发现结果（可再经 SQLite 启停过滤）。
// 设计要点：单个 server 启动失败只是跳过（best-effort），
// 不会影响其他 server 或阻塞整个服务启动。
func RegisterConfiguredServers(ctx context.Context, registry *runtime.Runtime, config claudecode.MCPConfig) {
	for _, server := range config.Servers {
		client, tools, err := StartStdioServer(ctx, server)
		if err != nil {
			continue
		}
		// 同一 server 的所有工具共享同一个 client（同一个子进程连接）。
		for _, tool := range tools {
			registry.Register(toolInvoker{client: client, tool: tool})
		}
	}
}

// StartStdioServer 启动一个 stdio 类型的 MCP server 子进程并完成
// 完整的接入流程：
//  1. 校验配置：必须有启动命令，且类型只支持 stdio（HTTP/SSE 未实现）；
//  2. 启动子进程：继承宿主环境变量并叠加配置中的 Env；
//     stdin/stdout 接管为 JSON-RPC 通道，stderr 缓存下来用于失败诊断；
//  3. initialize 握手（8 秒超时，防止行为异常的 server 卡死整个启动流程）；
//  4. tools/list 拉取该 server 声明的全部工具；
//  5. 起一个 goroutine 执行 cmd.Wait 回收子进程，避免其退出后变成僵尸进程。
//
// 任一步失败都会 Kill 子进程并返回错误；握手失败时会把 stderr 内容
// 附加到错误信息中（server 通常把启动错误打到 stderr）。
func StartStdioServer(ctx context.Context, server claudecode.MCPServer) (*Client, []Tool, error) {
	if strings.TrimSpace(server.Command) == "" {
		return nil, nil, errors.New("stdio MCP server command is required")
	}
	if server.Type != "" && server.Type != "stdio" {
		return nil, nil, fmt.Errorf("unsupported MCP server type: %s", server.Type)
	}
	cmd := exec.Command(server.Command, server.Args...)
	// 子进程环境 = 宿主全部环境变量 + 配置文件中声明的 Env（后者可覆盖）。
	cmd.Env = append(os.Environ(), envPairs(server.Env)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	// stderr 不参与协议通信，缓存起来仅用于失败时的错误报告。
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	client := &Client{
		server: server,
		cmd:    cmd,
		stdin:  stdin,
		reader: bufio.NewReader(stdout),
	}
	// 握手设置独立的 8 秒超时：MCP server 启动慢或不响应时快速放弃，
	// 不拖累其余 server 的注册和整个服务的启动。
	initCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := client.initialize(initCtx); err != nil {
		_ = cmd.Process.Kill()
		if stderr.Len() > 0 {
			// 把子进程的 stderr 输出拼进错误，方便定位启动失败原因。
			return nil, nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, nil, err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, nil, err
	}
	// 异步等待子进程退出，回收其资源；client 生命周期与服务进程相同，
	// 没有显式的关闭流程（进程退出时管道断开、子进程随之结束）。
	go func() { _ = cmd.Wait() }()
	return client, tools, nil
}

// Tool 返回该 MCP 工具在运行时中的元数据描述：
//   - ID 采用 "mcp:<server>:<tool>" 命名空间，避免与内置工具冲突；
//   - Name 经 mcpToolName 清洗为 "mcp__<server>__<tool>" 形式，
//     以满足 OpenAI function calling 对函数名字符集的限制；
//   - InputSchema 缺失时用空对象 schema 兜底（模型端要求必须有 schema）；
//   - 风险等级统一标为低（当前未从 MCP annotations 推断风险）。
func (i toolInvoker) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "mcp:" + i.client.server.Name + ":" + i.tool.Name,
		Source:      "mcp:" + i.client.server.Name,
		Name:        mcpToolName(i.client.server.Name, i.tool.Name),
		Description: i.tool.Description,
		InputSchema: schemaOrDefault(i.tool.InputSchema),
		Risk:        contracts.RiskLow,
	}
}

// Invoke 执行该 MCP 工具：直接把模型给出的参数透传给 tools/call。
// 注意传给 server 的是原始工具名（i.tool.Name），而非清洗后的对外名。
func (i toolInvoker) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	return i.client.CallTool(ctx, i.tool.Name, input)
}

// initialize 执行 MCP 协议规定的两步握手：
//  1. 发送 initialize 请求，声明协议版本、客户端能力（当前为空）
//     与客户端标识（clientInfo），等待服务端返回其能力信息；
//  2. 发送 notifications/initialized 通知（无 id 的 JSON-RPC 通知，
//     不等待响应），告知服务端握手完成、可以开始正常处理请求。
//
// 只有完成这两步之后才允许调用 tools/list、tools/call 等方法。
func (c *Client) initialize(ctx context.Context) error {
	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "lmy-harness-agent",
			"version": "0.1.0",
		},
	}); err != nil {
		return err
	}
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

// ListTools 调用 MCP 的 tools/list 方法，获取服务端声明的工具清单。
// 会过滤掉名称为空的非法条目，并对名称/描述做空白裁剪。
// 注意：当前实现只拉取一次，不处理分页 cursor，也不监听
// tools/list_changed 通知（工具集在启动后视为静态）。
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	result, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, err
	}
	tools := make([]Tool, 0, len(decoded.Tools))
	for _, tool := range decoded.Tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		tools = append(tools, Tool{
			Name:        name,
			Description: strings.TrimSpace(tool.Description),
			InputSchema: tool.InputSchema,
		})
	}
	return tools, nil
}

// CallTool 调用 MCP 的 tools/call 方法执行指定工具。
// 结果原样解码为 map 返回（含 content 等字段），不做内容抽取，
// 交由上层序列化后回传给模型。MCP 协议中工具级失败不走 JSON-RPC
// error，而是通过结果里的 isError=true 表示，因此这里要单独检查：
// 此时同时返回结果体与错误，让调用方既能标记失败也能看到错误详情。
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	result, err := c.request(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, err
	}
	if isError, _ := decoded["isError"].(bool); isError {
		return decoded, fmt.Errorf("mcp tool %s returned an error", name)
	}
	return decoded, nil
}

// request 发送一个 JSON-RPC 2.0 请求并同步等待对应响应。
// 关键实现细节：
//   - 全程持有 c.mu：同一时刻只有一个在途请求，请求与响应天然配对，
//     无需实现异步多路复用（以吞吐换取实现的简单与正确）；
//   - id 使用自增整数，读循环中跳过 id 不匹配的消息——这样可以
//     自然忽略服务端主动推送的通知（无 id）或历史遗留响应；
//   - JSON-RPC 层的 error 对象在此转换为 Go error 返回。
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.writeMessage(payload); err != nil {
		return nil, err
	}
	// 循环读取直到拿到 id 匹配的响应；期间收到的其他消息
	// （如服务端通知）直接丢弃。
	for {
		response, err := c.readMessage(ctx)
		if err != nil {
			return nil, err
		}
		if intID(response.ID) != id {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("mcp %s failed: %s", method, response.Error.Message)
		}
		return response.Result, nil
	}
}

// notify 发送一个 JSON-RPC 通知（notification）：与 request 的区别是
// 不携带 id、也不等待任何响应，用于 notifications/initialized 这类
// 单向告知消息。
func (c *Client) notify(ctx context.Context, method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// writeMessage 按 LSP 风格的帧格式向子进程 stdin 写出一条消息：
// 先写 "Content-Length: <字节数>\r\n\r\n" 头部，再写 JSON 消息体。
// 长度前缀让接收方能在字节流上精确切分消息边界。
func (c *Client) writeMessage(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

// readMessage 从子进程 stdout 读取一条完整消息，并支持 ctx 取消/超时。
// 由于对管道的阻塞读本身无法被 context 打断，这里把真正的读取放进
// goroutine，通过 select 在"读到结果"与"ctx 结束"之间竞争；
// ch 带 1 个缓冲，保证超时放弃后读取 goroutine 仍能写入并退出，不会泄漏。
// 注意：超时返回后该 goroutine 读到的消息会被丢弃，但因为握手失败时
// 子进程会被 Kill，不会造成后续消息错位。
func (c *Client) readMessage(ctx context.Context) (rpcResponse, error) {
	type result struct {
		response rpcResponse
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		response, err := readRPCMessage(c.reader)
		ch <- result{response: response, err: err}
	}()
	select {
	case <-ctx.Done():
		return rpcResponse{}, ctx.Err()
	case item := <-ch:
		return item.response, item.err
	}
}

// rpcResponse 是 JSON-RPC 2.0 响应消息的最小解码结构：
// ID 用 any 承载（不同实现可能返回数字或字符串），
// Result 保留原始 JSON 交由各方法自行按需解码，Error 为协议级错误。
type rpcResponse struct {
	ID     any             `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// rpcError 是 JSON-RPC 2.0 的 error 对象（错误码 + 人类可读消息）。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// readRPCMessage 从字节流中解析一帧 MCP 消息：
//  1. 逐行读取头部直到遇到空行（头/体分隔符），头部键名大小写不敏感，
//     行尾同时兼容 \r\n 与 \n（不同 server 实现的换行风格不一）；
//  2. 从 Content-Length 头得到消息体字节数，缺失则视为协议错误；
//  3. 用 io.ReadFull 精确读取该长度的消息体并反序列化为 rpcResponse。
func readRPCMessage(reader *bufio.Reader) (rpcResponse, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return rpcResponse{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// 空行标志头部结束，其后紧跟消息体。
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return rpcResponse{}, err
			}
			contentLength = parsed
		}
	}
	if contentLength <= 0 {
		return rpcResponse{}, errors.New("missing MCP Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return rpcResponse{}, err
	}
	var response rpcResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return rpcResponse{}, err
	}
	return response, nil
}

// intID 把 JSON-RPC 响应中类型不定的 id 归一化为 int 用于配对比较：
// 标准 JSON 解码下数字是 float64，也兼容 int 与 json.Number；
// 其他类型（字符串 id、null）返回 0，即视为与本客户端的请求不匹配。
func intID(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

// envPairs 把配置中的环境变量 map 转换为 exec.Cmd.Env 需要的
// "KEY=VALUE" 切片形式，跳过键名为空白的非法条目。
func envPairs(values map[string]string) []string {
	pairs := make([]string, 0, len(values))
	for key, value := range values {
		if strings.TrimSpace(key) != "" {
			pairs = append(pairs, key+"="+value)
		}
	}
	return pairs
}

// mcpToolName 生成暴露给模型的工具函数名："mcp__<server>__<tool>"。
// 之所以要清洗：OpenAI function calling 只允许函数名包含
// 字母/数字/下划线/连字符，而 MCP server 名与工具名可能含
// 点号、斜杠、空格等任意字符。清洗规则：
//   - 保留字母/数字/下划线，其余字符折叠为单个下划线；
//   - 去掉首尾下划线；清洗后为空则用 "unnamed" 兜底。
//
// 使用双下划线分隔可在名字中反解出 server 与 tool 两部分，
// 也与 Claude Code 的 MCP 工具命名习惯保持一致。
func mcpToolName(server string, tool string) string {
	clean := func(value string) string {
		value = strings.TrimSpace(value)
		var builder strings.Builder
		previousUnderscore := false
		for _, r := range value {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				builder.WriteRune(r)
				previousUnderscore = false
				continue
			}
			// 非法字符折叠为单个 '_'，避免出现连续下划线干扰可读性。
			if !previousUnderscore {
				builder.WriteRune('_')
				previousUnderscore = true
			}
		}
		cleaned := strings.Trim(builder.String(), "_")
		if cleaned == "" {
			return "unnamed"
		}
		return cleaned
	}
	return "mcp__" + clean(server) + "__" + clean(tool)
}

// schemaOrDefault 在 MCP server 未声明 inputSchema 时兜底一个
// 空对象 JSON Schema（type=object、无必填项）：模型端的 function
// 定义要求 parameters 字段必须存在且为合法 schema。
func schemaOrDefault(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return runtime.Schema(map[string]any{}, []string{})
	}
	return schema
}
