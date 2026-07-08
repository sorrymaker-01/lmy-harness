package tools

import (
	"context"
	"errors"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/memory"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
)

func RegisterGeneric(registry *runtime.Runtime, store memory.Store) {
	registry.Register(NewMemorySnapshotTool(store))
	registry.Register(NewEchoTool())
}

type EchoTool struct{}

func NewEchoTool() EchoTool {
	return EchoTool{}
}

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

func (EchoTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	text, _ := input["text"].(string)
	if text == "" {
		return nil, errors.New("text is required")
	}
	return map[string]any{"echo": text}, nil
}

type MemorySnapshotTool struct {
	store memory.Store
}

func NewMemorySnapshotTool(store memory.Store) MemorySnapshotTool {
	return MemorySnapshotTool{store: store}
}

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
