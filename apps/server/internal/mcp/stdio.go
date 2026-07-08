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

const protocolVersion = "2024-11-05"

type Client struct {
	server claudecode.MCPServer
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	mu     sync.Mutex
	nextID int
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolInvoker struct {
	client *Client
	tool   Tool
}

func RegisterConfiguredServers(ctx context.Context, registry *runtime.Runtime, config claudecode.MCPConfig) {
	for _, server := range config.Servers {
		client, tools, err := StartStdioServer(ctx, server)
		if err != nil {
			continue
		}
		for _, tool := range tools {
			registry.Register(toolInvoker{client: client, tool: tool})
		}
	}
}

func StartStdioServer(ctx context.Context, server claudecode.MCPServer) (*Client, []Tool, error) {
	if strings.TrimSpace(server.Command) == "" {
		return nil, nil, errors.New("stdio MCP server command is required")
	}
	if server.Type != "" && server.Type != "stdio" {
		return nil, nil, fmt.Errorf("unsupported MCP server type: %s", server.Type)
	}
	cmd := exec.Command(server.Command, server.Args...)
	cmd.Env = append(os.Environ(), envPairs(server.Env)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
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
	initCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := client.initialize(initCtx); err != nil {
		_ = cmd.Process.Kill()
		if stderr.Len() > 0 {
			return nil, nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, nil, err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, nil, err
	}
	go func() { _ = cmd.Wait() }()
	return client, tools, nil
}

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

func (i toolInvoker) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	return i.client.CallTool(ctx, i.tool.Name, input)
}

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

func (c *Client) notify(ctx context.Context, method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

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

type rpcResponse struct {
	ID     any             `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func readRPCMessage(reader *bufio.Reader) (rpcResponse, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return rpcResponse{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
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

func envPairs(values map[string]string) []string {
	pairs := make([]string, 0, len(values))
	for key, value := range values {
		if strings.TrimSpace(key) != "" {
			pairs = append(pairs, key+"="+value)
		}
	}
	return pairs
}

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

func schemaOrDefault(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return runtime.Schema(map[string]any{}, []string{})
	}
	return schema
}
