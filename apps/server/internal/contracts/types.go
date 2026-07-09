package contracts

import (
	"time"
)

// Role 表示一条消息的发送方角色，取值与 OpenAI Chat Completions
// 协议中的 role 字段对齐，因此可以在系统内部消息与模型 API 消息之间
// 直接转换而无需映射表。
type Role string

const (
	// RoleUser 表示终端用户发出的消息。
	RoleUser Role = "user"
	// RoleAssistant 表示模型（Agent）生成的消息，可能携带 tool_calls。
	RoleAssistant Role = "assistant"
	// RoleTool 表示工具执行结果消息，必须携带 ToolCallID 以回应
	// 对应的 assistant tool call（OpenAI 协议要求）。
	RoleTool Role = "tool"
)

const (
	// DefaultShortMemorySummary 是短期记忆为空时的默认占位摘要（中文）。
	DefaultShortMemorySummary = "没有历史短期记忆。"
	// LegacyDefaultShortMemorySummary 是历史版本使用的英文占位摘要，
	// 保留用于兼容旧数据（判断记忆是否为"空"时两者都要识别）。
	LegacyDefaultShortMemorySummary = "No prior short-term memory."
)

// RiskLevel 表示一个运行时工具的风险等级，用于前端展示与
// 潜在的调用审批策略（例如高风险工具需要用户确认）。
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"    // 低风险：只读或无副作用操作
	RiskMedium RiskLevel = "medium" // 中风险：会修改本地状态
	RiskHigh   RiskLevel = "high"   // 高风险：可能产生不可逆或外部副作用
)

// Conversation 表示一个会话（对话线程），是消息、短期记忆、trace
// 等数据的顶层聚合单位。由 memory.Store 持久化（内存或 SQLite）。
type Conversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Message 表示会话中持久化的一条消息（用户输入或最终的助手回复），
// 是前端消息列表渲染与历史回放的数据来源。Metadata 用于携带
// 附加信息（如多模型回复的 responseId、modelConfigId 等）。
// 注意与 LLMMessage 区分：Message 是"存储/展示"模型，
// LLMMessage 是发给大模型 API 的"线协议"模型。
type Message struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversationId"`
	Role           Role           `json:"role"`
	Content        string         `json:"content"`
	CreatedAt      time.Time      `json:"createdAt"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// RuntimeTool 是注册进工具运行时（runtime.Runtime）的工具元数据描述，
// 也是暴露给大模型的 function calling 工具定义的来源：
//   - ID：全局唯一标识，MCP 工具形如 "mcp:<server>:<tool>"；
//   - Source：工具来源（内置模块名或 "mcp:<server>"），用于分组展示与启停管理；
//   - Name：暴露给模型的函数名（需满足 OpenAI 对函数名的字符限制）；
//   - InputSchema：JSON Schema 形式的参数定义，直接嵌入 chat completions
//     请求的 tools[].function.parameters 字段；
//   - Risk：风险等级，见 RiskLevel。
type RuntimeTool struct {
	ID          string         `json:"id"`
	Source      string         `json:"source"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Risk        RiskLevel      `json:"risk"`
}

// ToolCall 表示 Agent 循环中一次"已解析并定位到具体工具"的调用请求：
// ID 沿用模型返回的 tool call id（用于回填 RoleTool 消息的 tool_call_id），
// ToolID/Name 指向 RuntimeTool，Input 是已反序列化的参数对象。
// 与 ModelToolCall 的区别：ModelToolCall 是模型原始输出，
// ToolCall 是经运行时路由后的执行单元，会出现在事件流与 trace 中。
type ToolCall struct {
	ID     string         `json:"id"`
	ToolID string         `json:"toolId"`
	Name   string         `json:"name,omitempty"`
	Input  map[string]any `json:"input"`
}

// ToolResult 表示一次工具调用的执行结果，OK 标记成功与否，
// Output 为任意可 JSON 化的返回值，Error 为失败原因。
// 结果会被序列化后作为 RoleTool 消息回传给模型，并记录到 trace。
type ToolResult struct {
	ToolID string `json:"toolId"`
	OK     bool   `json:"ok"`
	Output any    `json:"output"`
	Error  string `json:"error,omitempty"`
}

// ActiveTask 表示工作记忆中当前正在推进的任务目标及其状态，
// 帮助模型在多轮循环中保持对长任务的专注。
type ActiveTask struct {
	Goal   string `json:"goal"`
	Status string `json:"status"`
}

// ShortMemory 是会话级别的"短期记忆"：跨轮次持续维护的对话摘要
// （Summary）、近期事实列表（RecentFacts）与当前活跃任务（ActiveTask）。
// 每轮结束后由 Agent 更新并持久化，下一轮组装上下文时注入 system prompt，
// 用于在不携带全部历史消息的情况下保留对话连续性。
type ShortMemory struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	Summary        string    `json:"summary"`
	RecentFacts    []string  `json:"recentFacts"`
	ActiveTask     string    `json:"activeTask,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// WorkingMemory 是"单轮（turn）级别"的工作记忆，随 Agent 循环逐步演化：
// 记录本轮用户意图（Intent）、约束（Constraints）、待执行的工具调用
// （PendingToolCalls）、工具结果摘要（ToolResultSummaries）与草稿区
// （Scratchpad）。它既通过事件流实时推给前端做过程可视化，
// 也随 trace 持久化用于事后回放调试。
type WorkingMemory struct {
	ID                  string      `json:"id"`
	ConversationID      string      `json:"conversationId"`
	TurnID              string      `json:"turnId"`
	UserMessageID       string      `json:"userMessageId,omitempty"`
	Intent              string      `json:"intent"`
	ActiveTask          *ActiveTask `json:"activeTask,omitempty"`
	Constraints         []string    `json:"constraints"`
	PendingToolCalls    []ToolCall  `json:"pendingToolCalls"`
	ToolResultSummaries []string    `json:"toolResultSummaries"`
	Scratchpad          []string    `json:"scratchpad"`
	UpdatedAt           time.Time   `json:"updatedAt"`
}

// ContextSource 表示注入上下文的一份外部素材来源，例如 RAG 知识库
// 检索片段、CLAUDE.md 项目说明、技能（skill）正文等。
// Type 标识来源类别，Content 为实际注入的文本。
type ContextSource struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Content string         `json:"content"`
	Meta    map[string]any `json:"metadata,omitempty"`
}

// ContextSnapshot 是某一轮对话开始时"完整上下文"的快照：
// 包含最终拼装的 system prompt、用户消息、短期/工作记忆、最近消息、
// 工具结果与全部素材来源。它挂在 Trace 上持久化，
// 使得可以精确复现"模型当时到底看到了什么"，是可观测性的核心数据。
type ContextSnapshot struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversationId"`
	TurnID         string          `json:"turnId"`
	CreatedAt      time.Time       `json:"createdAt"`
	System         string          `json:"system"`
	UserMessage    string          `json:"userMessage"`
	ShortMemory    ShortMemory     `json:"shortMemory"`
	WorkingMemory  WorkingMemory   `json:"workingMemory"`
	RecentMessages []Message       `json:"recentMessages"`
	ToolResults    []ToolResult    `json:"toolResults"`
	Sources        []ContextSource `json:"sources"`
}

// LLMMessage 是发送给大模型 API 的消息线协议（wire format），
// 字段命名与 OpenAI Chat Completions 保持一致（tool_call_id、tool_calls）：
//   - RoleTool 消息通过 ToolCallID 关联它所回应的 assistant tool call；
//   - RoleAssistant 消息可携带 ToolCalls（模型请求调用的工具列表）。
//
// model 包的 encodeOpenAIMessage 负责把它编码为 OpenAI 请求体格式。
type LLMMessage struct {
	Role       Role            `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []ModelToolCall `json:"tool_calls,omitempty"`
}

// ModelToolCall 是模型输出中的一次工具调用请求（function call）。
// 与 OpenAI 原始格式不同的是：Arguments 已从 JSON 字符串解析为
// map[string]any，方便运行时直接消费；ID 若模型未提供则由系统补全。
type ModelToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ModelResponse 是 model.Client.Chat/ChatStream 的统一返回值：
//   - Content：本次回复的纯文本内容；
//   - Message：可直接追加回对话历史的 assistant 消息（含 tool_calls），
//     供 Agent 循环下一轮请求复用；
//   - ToolCalls：解析后的工具调用列表（为空表示模型给出最终答案）；
//   - PromptTokens/CompletionTokens：token 用量统计（流式响应下
//     部分服务端不返回 usage，可能为 0）。
type ModelResponse struct {
	Content          string          `json:"content"`
	Message          LLMMessage      `json:"message"`
	ToolCalls        []ModelToolCall `json:"toolCalls"`
	PromptTokens     int             `json:"promptTokens"`
	CompletionTokens int             `json:"completionTokens"`
}

// AgentStreamEvent 是 Agent 执行过程通过 SSE 推送给前端的事件信封，
// 是后端与前端之间最核心的实时数据契约。Type 决定事件语义
// （如轮次开始、内容增量 delta、工具调用/结果、记忆更新、最终消息等），
// 其余字段按事件类型选择性填充：
//   - Round：当前 Agent 循环轮次；
//   - ResponseID/ModelConfigID/Primary/Canonical：多模型并行回复场景下
//     标识事件属于哪个模型的回复流、哪个是主回复；
//   - Message/Assistant/ToolCall/ToolResult/WorkingMemory：对应事件的载荷；
//   - TraceID：关联本轮 trace，便于前端跳转到执行详情。
type AgentStreamEvent struct {
	Type          string         `json:"type"`
	Round         int            `json:"round,omitempty"`
	Title         string         `json:"title,omitempty"`
	Content       string         `json:"content,omitempty"`
	TurnID        string         `json:"turnId,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
	ModelConfigID string         `json:"modelConfigId,omitempty"`
	Primary       bool           `json:"primary,omitempty"`
	Canonical     bool           `json:"canonical,omitempty"`
	Message       *Message       `json:"message,omitempty"`
	Assistant     *LLMMessage    `json:"assistant,omitempty"`
	ToolCall      *ToolCall      `json:"toolCall,omitempty"`
	ToolResult    *ToolResult    `json:"toolResult,omitempty"`
	ToolCalls     []ToolCall     `json:"toolCalls,omitempty"`
	ToolResults   []ToolResult   `json:"toolResults,omitempty"`
	WorkingMemory *WorkingMemory `json:"workingMemory,omitempty"`
	TraceID       string         `json:"traceId,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
}

// AgentLoopStep 记录 Agent 循环中一轮完整的"模型输出 -> 工具调用 ->
// 工具结果"步骤，按轮次收集进 Trace，用于回放每一轮的决策过程。
type AgentLoopStep struct {
	Round       int          `json:"round"`
	Assistant   LLMMessage   `json:"assistant"`
	ToolCalls   []ToolCall   `json:"toolCalls"`
	ToolResults []ToolResult `json:"toolResults"`
}

// Trace 是一轮用户请求（turn）的完整执行追踪记录：起止时间、
// 记忆快照、上下文快照、当时可用的工具列表、每轮循环步骤、
// 全部工具调用与结果、最终答案或错误。它是系统可观测性与
// 调试回放能力的载体，由 memory.Store 持久化并通过 API 暴露给前端。
type Trace struct {
	ID                    string           `json:"id"`
	ConversationID        string           `json:"conversationId"`
	TurnID                string           `json:"turnId"`
	UserMessageID         string           `json:"userMessageId"`
	StartedAt             time.Time        `json:"startedAt"`
	CompletedAt           *time.Time       `json:"completedAt,omitempty"`
	MemorySnapshot        ShortMemory      `json:"memorySnapshot"`
	WorkingMemorySnapshot WorkingMemory    `json:"workingMemorySnapshot"`
	ContextSnapshot       *ContextSnapshot `json:"contextSnapshot,omitempty"`
	AvailableTools        []RuntimeTool    `json:"availableTools"`
	LoopSteps             []AgentLoopStep  `json:"loopSteps"`
	ToolCalls             []ToolCall       `json:"toolCalls"`
	ToolResults           []ToolResult     `json:"toolResults"`
	FinalAnswer           string           `json:"finalAnswer,omitempty"`
	Error                 string           `json:"error,omitempty"`
}
