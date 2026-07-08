package runtime

import (
	"context"
	"fmt"
	"sort"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/memory"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/model"
)

type InvocationContext struct {
	ConversationID string
	Store          memory.Store
	Model          model.Client
}

type Invoker interface {
	Tool() contracts.RuntimeTool
	Invoke(ctx context.Context, input map[string]any, invokeCtx InvocationContext) (any, error)
}

type Runtime struct {
	invokers           map[string]Invoker
	byName             map[string]Invoker
	toolConfigProvider ToolConfigProvider
}

func NewRuntime() *Runtime {
	return &Runtime{invokers: map[string]Invoker{}, byName: map[string]Invoker{}}
}

type ToolConfig struct {
	Enabled        bool
	ApprovalPolicy string
}

type ToolConfigProvider interface {
	ToolConfigFor(toolName string) (ToolConfig, bool)
}

func (r *Runtime) SetToolConfigProvider(provider ToolConfigProvider) {
	r.toolConfigProvider = provider
}

func (r *Runtime) Register(invoker Invoker) {
	r.invokers[invoker.Tool().ID] = invoker
	r.byName[invoker.Tool().Name] = invoker
}

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

func (r *Runtime) ListTools() []contracts.RuntimeTool {
	tools := make([]contracts.RuntimeTool, 0, len(r.invokers))
	for _, invoker := range r.invokers {
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

func (r *Runtime) Invoke(ctx context.Context, call contracts.ToolCall, invokeCtx InvocationContext) contracts.ToolResult {
	invoker, ok := r.invokers[call.ToolID]
	if !ok && call.Name != "" {
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
	if policy := r.configPolicy(invoker.Tool()); policy != "" {
		return contracts.ToolResult{ToolID: call.ToolID, OK: false, Output: nil, Error: policy}
	}
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

func (r *Runtime) toolEnabled(tool contracts.RuntimeTool) bool {
	if r.toolConfigProvider == nil {
		return true
	}
	config, ok := r.toolConfigProvider.ToolConfigFor(tool.Name)
	return !ok || config.Enabled
}

func (r *Runtime) configPolicy(tool contracts.RuntimeTool) string {
	if r.toolConfigProvider == nil {
		return ""
	}
	config, ok := r.toolConfigProvider.ToolConfigFor(tool.Name)
	if !ok {
		return ""
	}
	if !config.Enabled {
		return fmt.Sprintf("denied: %s is disabled by tool configuration", tool.Name)
	}
	switch config.ApprovalPolicy {
	case "deny":
		return fmt.Sprintf("denied: %s is denied by tool configuration", tool.Name)
	case "ask":
		return fmt.Sprintf("confirmation required: %s requires approval by tool configuration", tool.Name)
	default:
		return ""
	}
}

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

func decideToolPolicy(tool contracts.RuntimeTool) string {
	if tool.Risk == contracts.RiskLow {
		return ""
	}
	if tool.Risk == contracts.RiskMedium {
		return fmt.Sprintf("confirmation required: %s is a medium-risk tool", tool.Name)
	}
	return fmt.Sprintf("denied: %s is high-risk and blocked by the current permission policy", tool.Name)
}

func Schema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}
