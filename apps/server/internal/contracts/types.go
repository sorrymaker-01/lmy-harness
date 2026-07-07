package contracts

import (
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

const (
	DefaultShortMemorySummary       = "没有历史短期记忆。"
	LegacyDefaultShortMemorySummary = "No prior short-term memory."
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

type Conversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Message struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversationId"`
	Role           Role           `json:"role"`
	Content        string         `json:"content"`
	CreatedAt      time.Time      `json:"createdAt"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type RuntimeTool struct {
	ID          string         `json:"id"`
	Source      string         `json:"source"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Risk        RiskLevel      `json:"risk"`
}

type ToolCall struct {
	ID     string         `json:"id"`
	ToolID string         `json:"toolId"`
	Name   string         `json:"name,omitempty"`
	Input  map[string]any `json:"input"`
}

type ToolResult struct {
	ToolID string `json:"toolId"`
	OK     bool   `json:"ok"`
	Output any    `json:"output"`
	Error  string `json:"error,omitempty"`
}

type ActiveTask struct {
	Goal   string `json:"goal"`
	Status string `json:"status"`
}

type ShortMemory struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	Summary        string    `json:"summary"`
	RecentFacts    []string  `json:"recentFacts"`
	ActiveTask     string    `json:"activeTask,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

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

type ContextSource struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Content string         `json:"content"`
	Meta    map[string]any `json:"metadata,omitempty"`
}

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

type LLMMessage struct {
	Role       Role            `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []ModelToolCall `json:"tool_calls,omitempty"`
}

type ModelToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ModelResponse struct {
	Content          string          `json:"content"`
	Message          LLMMessage      `json:"message"`
	ToolCalls        []ModelToolCall `json:"toolCalls"`
	PromptTokens     int             `json:"promptTokens"`
	CompletionTokens int             `json:"completionTokens"`
}

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

type AgentLoopStep struct {
	Round       int          `json:"round"`
	Assistant   LLMMessage   `json:"assistant"`
	ToolCalls   []ToolCall   `json:"toolCalls"`
	ToolResults []ToolResult `json:"toolResults"`
}

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
