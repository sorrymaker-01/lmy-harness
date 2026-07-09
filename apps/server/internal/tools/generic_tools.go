package tools

import (
	"context"
	"errors"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/memory"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
)

// RegisterGeneric 注册通用辅助工具：memory_snapshot（读取会话记忆）与 echo（回显/联调用）。
// store 参数是默认的记忆存储，作为调用时 InvocationContext.Store 缺失时的兜底。
func RegisterGeneric(registry *runtime.Runtime, store memory.Store) {
	registry.Register(NewMemorySnapshotTool(store))
	registry.Register(NewEchoTool())
}

// EchoTool 原样回显输入文本。主要价值是作为“最小工具”用于验证
// 工具注册 → schema 导出 → 模型调用 → 结果回传整条链路是否打通（联调/冒烟测试）。
type EchoTool struct{}

// NewEchoTool 创建 echo 工具（无状态）。
func NewEchoTool() EchoTool {
	return EchoTool{}
}

// Tool 返回 echo 的元信息：仅 text 一个必填参数。
func (EchoTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:echo",
		Source:      "tool",
		Name:        "echo",
		Description: "通过通用工具运行时回显输入内容。",
		InputSchema: runtime.Schema(map[string]any{
			"text": map[string]any{"type": "string"},
		}, []string{"text"}),
		Risk: contracts.RiskLow,
	}
}

// Invoke 校验 text 非空后原样包在 {"echo": ...} 里返回。
func (EchoTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	text, _ := input["text"].(string)
	if text == "" {
		return nil, errors.New("text is required")
	}
	return map[string]any{"echo": text}, nil
}

// MemorySnapshotTool 让模型主动查看当前会话的“短期记忆摘要 + 最近消息”，
// 用于模型自查上下文状态（例如确认之前记了什么、最近聊了什么）。
type MemorySnapshotTool struct {
	store memory.Store // 注册时注入的默认存储；调用时优先使用 InvocationContext 里的存储
}

// NewMemorySnapshotTool 创建 memory_snapshot 工具，持有默认记忆存储作兜底。
func NewMemorySnapshotTool(store memory.Store) MemorySnapshotTool {
	return MemorySnapshotTool{store: store}
}

// Tool 返回 memory_snapshot 的元信息：无参数（要看哪个会话由 InvocationContext 决定，
// 模型无法也不应指定其他会话，避免跨会话信息泄露）。
func (MemorySnapshotTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:memory_snapshot",
		Source:      "tool",
		Name:        "memory_snapshot",
		Description: "返回当前会话的短期记忆和近期消息。",
		InputSchema: runtime.Schema(map[string]any{}, []string{}),
		Risk:        contracts.RiskLow,
	}
}

// Invoke 返回当前会话的记忆快照。存储选择顺序：优先用调用上下文注入的 Store
// （子 Agent 等场景下与全局 store 不同），否则回退到注册时的默认 store。
// 输出包含短期记忆摘要和最近 6 条消息——条数固定较小，防止把长历史整段灌回上下文。
func (t MemorySnapshotTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	store := t.store
	if invokeCtx.Store != nil {
		store = invokeCtx.Store
	}
	if store == nil {
		return nil, errors.New("memory store is unavailable")
	}
	return map[string]any{
		"shortMemory":    store.GetShortMemory(invokeCtx.ConversationID),
		"recentMessages": store.RecentMessages(invokeCtx.ConversationID, 6),
	}, nil
}
