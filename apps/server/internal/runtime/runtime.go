package runtime

import (
	"context"
	"fmt"
	"sort"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/memory"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/model"
)

// InvocationContext 是每次工具调用时由调用方（agent 主循环）注入的“会话级上下文”。
// 工具本身是无状态注册的，但很多工具（如 memory_snapshot、子 Agent）需要感知当前会话，
// 因此把会话 ID、记忆存储和模型客户端在调用时透传进来，而不是在注册时绑定，
// 这样同一个 Runtime 实例可以被多个会话安全复用。
type InvocationContext struct {
	// ConversationID 当前会话 ID，工具用它来隔离各会话的状态（例如 bash 工具按会话记录 cwd）。
	ConversationID string
	// Store 当前会话使用的记忆存储；子 Agent 场景下可能与全局 store 不同。
	Store memory.Store
	// Model 当前使用的模型客户端；子 Agent 工具会复用它，保证子 Agent 与父 Agent 用同一个模型。
	Model model.Client
}

// Invoker 是所有工具的统一抽象：本地内置工具（tools 包）和 MCP 远程工具（mcp 包）
// 都实现这个接口，从而在 Runtime 层面被完全统一地注册、列举和调用。
// Tool() 返回工具的静态元信息（ID/名称/描述/输入 schema/风险等级），
// Invoke() 执行具体逻辑；input 是模型给出的、已解析为 map 的 JSON 参数。
type Invoker interface {
	// Tool 返回工具的元信息描述，用于 schema 导出、UI 展示和风险策略判定。
	Tool() contracts.RuntimeTool
	// Invoke 执行工具。约定：返回 error 表示“调用层面失败”（会被封装为 ToolResult.Error）；
	// 很多工具选择把业务性错误（如文件不存在）作为字符串输出返回而不是 error，
	// 这样模型能读到错误详情并自行纠正，而不是让整轮调用失败。
	Invoke(ctx context.Context, input map[string]any, invokeCtx InvocationContext) (any, error)
}

// Runtime 是工具注册表 + 调用分发器。它维护两个索引：
//   - invokers：按工具 ID（如 "tool:bash"、"mcp:server:tool"）索引，是权威主键；
//   - byName：按工具名（模型在 tool_call 中使用的 name，如 "bash"）索引，
//     因为模型侧只知道 name，需要用它反查回工具 ID。
//
// 另外可挂接一个 ToolConfigProvider，实现用户级别的工具开关/审批策略（动态配置）。
type Runtime struct {
	invokers           map[string]Invoker // key: RuntimeTool.ID
	byName             map[string]Invoker // key: RuntimeTool.Name（模型可见的函数名）
	toolConfigProvider ToolConfigProvider // 可选：外部工具配置（启用状态 / 审批策略）
}

// NewRuntime 创建一个空的工具注册表。
func NewRuntime() *Runtime {
	return &Runtime{invokers: map[string]Invoker{}, byName: map[string]Invoker{}}
}

// ToolConfig 描述单个工具的用户级配置：是否启用、以及审批策略。
// ApprovalPolicy 取值约定："allow"/空 表示放行，"ask" 表示需要用户确认，"deny" 表示直接拒绝。
type ToolConfig struct {
	Enabled        bool
	ApprovalPolicy string
}

// ToolConfigProvider 由外部（如 state.Store，持久化在 SQLite 中的用户配置）实现，
// 按工具名返回配置。返回 (config, true) 表示该工具有显式配置；(zero, false) 表示未配置，
// 未配置的工具走默认策略（启用 + 按风险等级判定）。
type ToolConfigProvider interface {
	ToolConfigFor(toolName string) (ToolConfig, bool)
}

// SetToolConfigProvider 挂接工具配置提供方。在 http server 启动时由 state store 注入，
// 使用户可以在前端动态开关工具或调整审批策略，而无需重启服务。
func (r *Runtime) SetToolConfigProvider(provider ToolConfigProvider) {
	r.toolConfigProvider = provider
}

// Register 注册一个工具，同时写入 ID 索引与 Name 索引。
// 注意：同名/同 ID 的工具后注册者覆盖先注册者（无冲突检测），
// 因此注册顺序（本地工具先、MCP 工具后）隐含了覆盖优先级。
func (r *Runtime) Register(invoker Invoker) {
	r.invokers[invoker.Tool().ID] = invoker
	r.byName[invoker.Tool().Name] = invoker
}

// CloneWithout 复制出一个新的 Runtime，但排除指定名称的工具。
// 主要用途：子 Agent 工具在派生子 Agent 时调用 CloneWithout("agent")，
// 把 "agent" 工具本身从子 Agent 的工具集中剔除，防止子 Agent 再递归派生子 Agent 造成无限嵌套。
// 克隆是浅拷贝：Invoker 实例共享（因此 bash 的 cwd 状态等也共享），配置提供方也一并带过去。
func (r *Runtime) CloneWithout(names ...string) *Runtime {
	skip := map[string]struct{}{}
	for _, name := range names {
		skip[name] = struct{}{}
	}
	clone := &Runtime{invokers: map[string]Invoker{}, byName: map[string]Invoker{}, toolConfigProvider: r.toolConfigProvider}
	for _, invoker := range r.invokers {
		tool := invoker.Tool()
		if _, ok := skip[tool.Name]; ok {
			continue
		}
		clone.Register(invoker)
	}
	return clone
}

// ListTools 返回“当前对模型可见”的工具列表：会按 ToolConfigProvider 过滤掉被用户禁用的工具。
// agent 主循环每轮用它生成发送给模型的 tools schema。按 ID 排序保证输出稳定（利于测试与 prompt 缓存）。
func (r *Runtime) ListTools() []contracts.RuntimeTool {
	tools := make([]contracts.RuntimeTool, 0, len(r.invokers))
	for _, invoker := range r.invokers {
		// 被配置禁用的工具不暴露给模型，从源头避免模型尝试调用。
		if !r.toolEnabled(invoker.Tool()) {
			continue
		}
		tools = append(tools, invoker.Tool())
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].ID < tools[j].ID
	})
	return tools
}

// ListRegisteredTools 返回全部已注册工具（不做启用状态过滤）。
// 与 ListTools 的区别：本方法面向管理界面/配置页展示“所有工具及其开关状态”，
// 而 ListTools 面向模型调用。
func (r *Runtime) ListRegisteredTools() []contracts.RuntimeTool {
	tools := make([]contracts.RuntimeTool, 0, len(r.invokers))
	for _, invoker := range r.invokers {
		tools = append(tools, invoker.Tool())
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].ID < tools[j].ID
	})
	return tools
}

// Invoke 是工具调用的统一分发入口。分发与安全校验流程：
//  1. 先按 ToolID 精确查找；查不到再按 Name 兜底查找（模型返回的 tool_call 只有 name，
//     ResolveToolCall 解析失败时 ToolID 可能为空）；都找不到则返回 "tool not found" 错误结果；
//  2. 先检查用户级配置策略（configPolicy）：禁用→拒绝，deny→拒绝，ask→要求确认；
//  3. 再检查默认风险策略（decideToolPolicy）：按工具声明的 Risk 等级判定；
//  4. 全部通过后才真正执行 Invoker.Invoke。
//
// 注意本方法从不返回 Go error，而是把一切失败都封装成 ToolResult（OK=false + Error 文本），
// 这样 agent 循环可以把失败原因原样喂回给模型，让模型有机会换一种方式重试。
func (r *Runtime) Invoke(ctx context.Context, call contracts.ToolCall, invokeCtx InvocationContext) contracts.ToolResult {
	invoker, ok := r.invokers[call.ToolID]
	if !ok && call.Name != "" {
		// 按名称兜底：模型侧只知道函数名，ToolID 可能缺失或过期。
		invoker, ok = r.byName[call.Name]
		if ok {
			call.ToolID = invoker.Tool().ID
		}
	}
	if !ok {
		toolID := call.ToolID
		if toolID == "" {
			toolID = call.Name
		}
		return contracts.ToolResult{ToolID: toolID, OK: false, Output: nil, Error: "tool not found"}
	}
	if call.ToolID == "" {
		call.ToolID = invoker.Tool().ID
	}
	// 第一道闸门：用户显式配置（禁用 / deny / ask），优先级高于默认风险策略。
	if policy := r.configPolicy(invoker.Tool()); policy != "" {
		return contracts.ToolResult{ToolID: call.ToolID, OK: false, Output: nil, Error: policy}
	}
	// 第二道闸门：按工具声明的风险等级执行默认策略（medium 需确认、high 直接拒绝）。
	policy := decideToolPolicy(invoker.Tool())
	if policy != "" {
		return contracts.ToolResult{ToolID: call.ToolID, OK: false, Output: nil, Error: policy}
	}
	output, err := invoker.Invoke(ctx, call.Input, invokeCtx)
	if err != nil {
		return contracts.ToolResult{ToolID: call.ToolID, OK: false, Output: nil, Error: err.Error()}
	}
	return contracts.ToolResult{ToolID: call.ToolID, OK: true, Output: output}
}

// toolEnabled 判断工具在当前配置下是否启用。
// 无配置提供方或该工具没有显式配置时默认启用（宽松默认，保证开箱即用）。
func (r *Runtime) toolEnabled(tool contracts.RuntimeTool) bool {
	if r.toolConfigProvider == nil {
		return true
	}
	config, ok := r.toolConfigProvider.ToolConfigFor(tool.Name)
	return !ok || config.Enabled
}

// configPolicy 计算用户级配置对该工具的策略结论。
// 返回空字符串表示放行；否则返回给模型看的拒绝/待确认原因（英文文案，模型可理解并转述给用户）。
// 判定顺序：先看 Enabled（禁用即拒绝），再看 ApprovalPolicy（deny 拒绝 / ask 需确认 / 其他放行）。
func (r *Runtime) configPolicy(tool contracts.RuntimeTool) string {
	if r.toolConfigProvider == nil {
		return ""
	}
	config, ok := r.toolConfigProvider.ToolConfigFor(tool.Name)
	if !ok {
		// 未显式配置的工具不在这里拦截，交给默认风险策略处理。
		return ""
	}
	if !config.Enabled {
		return fmt.Sprintf("denied: %s is disabled by tool configuration", tool.Name)
	}
	switch config.ApprovalPolicy {
	case "deny":
		return fmt.Sprintf("denied: %s is denied by tool configuration", tool.Name)
	case "ask":
		// 当前实现里 "ask" 并不会真正弹出确认流程，而是把“需要确认”作为错误结果返回，
		// 由模型转述给用户，用户在下一条消息里授权后模型再重试。
		return fmt.Sprintf("confirmation required: %s requires approval by tool configuration", tool.Name)
	default:
		return ""
	}
}

// ResolveToolCall 把模型返回的 tool_call（只有 name + 已解析的 arguments）
// 解析成内部的 contracts.ToolCall：按 name 反查工具 ID。
// 即使查不到（ToolID 留空）也照样返回，让 Invoke 阶段统一报 "tool not found"，
// 这样错误信息会作为工具结果回传给模型而不是中断整轮对话。
func (r *Runtime) ResolveToolCall(call contracts.ModelToolCall) contracts.ToolCall {
	invoker, ok := r.byName[call.Name]
	toolID := ""
	if ok {
		toolID = invoker.Tool().ID
	}
	return contracts.ToolCall{
		ID:     call.ID,
		ToolID: toolID,
		Name:   call.Name,
		Input:  call.Arguments,
	}
}

// ToolSchemasFor 把内部的 RuntimeTool 列表转换为 OpenAI Chat Completions 风格的
// function-calling schema（{"type":"function","function":{name,description,parameters}}）。
// agent 每轮请求模型时用它生成 tools 字段；InputSchema 直接透传，
// 因此各工具在 Tool() 中声明的 JSON Schema 就是模型看到的参数约束。
func ToolSchemasFor(tools []contracts.RuntimeTool) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.InputSchema,
			},
		})
	}
	return result
}

// decideToolPolicy 是默认的风险分级策略（在用户配置未拦截时兜底）：
//   - RiskLow：直接放行（当前所有内置工具都声明为 low，包括 bash——
//     bash 的安全性改由 checkDangerousCommand 的命令黑名单保证）；
//   - RiskMedium：返回“需要确认”，实际效果同 configPolicy 的 "ask"；
//   - RiskHigh（及其他未知等级）：直接拒绝。
//
// 返回空字符串表示放行，否则为给模型的拒绝原因。
func decideToolPolicy(tool contracts.RuntimeTool) string {
	if tool.Risk == contracts.RiskLow {
		return ""
	}
	if tool.Risk == contracts.RiskMedium {
		return fmt.Sprintf("confirmation required: %s is a medium-risk tool", tool.Name)
	}
	return fmt.Sprintf("denied: %s is high-risk and blocked by the current permission policy", tool.Name)
}

// Schema 是构造 JSON Schema object 的便捷函数，供各工具声明 InputSchema 时使用，
// 统一生成 {"type":"object","properties":...,"required":...} 结构，避免样板代码。
func Schema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}
