package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/claudecode"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/knowledge"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/memory"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/model"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/skills"
)

type Agent struct {
	store       memory.Store
	runtime     *runtime.Runtime
	modelMu     sync.RWMutex
	model       model.Client
	skillReg    *skills.Registry
	skillConfig *skills.ConfigStore
	knowledge   *knowledge.Store
	startup     claudecode.StartupContext
	maxRounds   int
}

type RunInput struct {
	ConversationID            string
	UserMessage               string
	UserMessageID             string
	Model                     model.Client
	ModelConfigID             string
	KnowledgeBaseID           string
	RetrievalResult           *knowledge.RetrievalResult
	RecentMessages            []contracts.Message
	UseRecentMessages         bool
	AssistantMessageID        string
	AssistantMetadata         map[string]any
	SkipUserMessageStore      bool
	SuppressUserMessageEvent  bool
	SkipAssistantMessageStore bool
	SkipShortMemoryUpdate     bool
	SuppressKnowledgeEvent    bool
}

type RunOutput struct {
	UserMessage      contracts.Message `json:"userMessage"`
	AssistantMessage contracts.Message `json:"assistantMessage"`
	Trace            contracts.Trace   `json:"trace"`
}

type AgentEventHandler func(contracts.AgentStreamEvent) error

type modelSkillRequest struct {
	Name   string
	Reason string
}

const maxModelRequestedSkillLoads = 3

func NewAgent(store memory.Store, runtime *runtime.Runtime, model model.Client, skillReg *skills.Registry, skillConfig *skills.ConfigStore, startup claudecode.StartupContext) *Agent {
	return &Agent{store: store, runtime: runtime, model: model, skillReg: skillReg, skillConfig: skillConfig, startup: startup, maxRounds: 20}
}

func (a *Agent) CloneRuntimeWithout(names ...string) *runtime.Runtime {
	return a.runtime.CloneWithout(names...)
}

func (a *Agent) Model() model.Client {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.model
}

func (a *Agent) SetModel(model model.Client) {
	if model == nil {
		return
	}
	a.modelMu.Lock()
	a.model = model
	a.modelMu.Unlock()
}

func (a *Agent) SetKnowledgeStore(store *knowledge.Store) {
	a.knowledge = store
}

func (a *Agent) SkillRegistry() *skills.Registry {
	return a.skillReg
}

func (a *Agent) SkillConfig() *skills.ConfigStore {
	return a.skillConfig
}

func (a *Agent) StartupContext() claudecode.StartupContext {
	return a.startup
}

func (a *Agent) SetMaxRounds(maxRounds int) {
	if maxRounds > 0 {
		a.maxRounds = maxRounds
	}
}

func (a *Agent) Run(ctx context.Context, input RunInput) (RunOutput, error) {
	return a.run(ctx, input, nil)
}

func (a *Agent) RunStream(ctx context.Context, input RunInput, emit AgentEventHandler) (RunOutput, error) {
	return a.run(ctx, input, emit)
}

func (a *Agent) run(ctx context.Context, input RunInput, emit AgentEventHandler) (RunOutput, error) {
	recent := input.RecentMessages
	if !input.UseRecentMessages {
		recent = a.store.RecentMessages(input.ConversationID, 12)
	}
	userMessageID := strings.TrimSpace(input.UserMessageID)
	if userMessageID == "" {
		userMessageID = shared.NewID("msg")
	}
	userMessage := contracts.Message{
		ID:             userMessageID,
		ConversationID: input.ConversationID,
		Role:           contracts.RoleUser,
		Content:        input.UserMessage,
		CreatedAt:      shared.Now(),
	}
	if !input.SkipUserMessageStore {
		a.store.AddMessage(userMessage)
	}
	if !input.SuppressUserMessageEvent {
		if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:    "user_message",
			Title:   "用户输入",
			Content: input.UserMessage,
			Message: &userMessage,
		}); err != nil {
			return RunOutput{}, err
		}
	}

	turnID := shared.NewID("turn")
	shortMemory := a.store.GetShortMemory(input.ConversationID)
	workingMemory := a.store.StartWorkingMemory(input.ConversationID, turnID, userMessage.ID, input.UserMessage, shortMemory, recent)
	availableTools := a.runtime.ListTools()
	allowedToolIDs := toolIDSet(availableTools)
	enabledSkills := a.enabledSkillManifests()
	modelMessages := buildInitialModelMessages(shortMemory, recent, input.UserMessage)
	modelClient := input.Model
	if modelClient == nil {
		modelClient = a.Model()
	}

	trace := contracts.Trace{
		ID:                    shared.NewID("trace"),
		ConversationID:        input.ConversationID,
		TurnID:                turnID,
		UserMessageID:         userMessage.ID,
		StartedAt:             shared.Now(),
		MemorySnapshot:        shortMemory,
		WorkingMemorySnapshot: workingMemory,
		AvailableTools:        availableTools,
		LoopSteps:             []contracts.AgentLoopStep{},
		ToolCalls:             []contracts.ToolCall{},
		ToolResults:           []contracts.ToolResult{},
	}
	a.store.AddTrace(trace)
	initialWorkingMemory := workingMemory
	if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
		Type:          "turn_start",
		Title:         "开始处理",
		Content:       renderWorkingMemory(workingMemory),
		WorkingMemory: &initialWorkingMemory,
		TraceID:       trace.ID,
	}); err != nil {
		return RunOutput{}, err
	}

	var allToolCalls []contracts.ToolCall
	var allToolResults []contracts.ToolResult
	var loadedSkillDetails []skills.Detail
	loadedSkills := map[string]struct{}{}
	modelRequestedSkillLoads := 0
	retrievalResult := knowledge.RetrievalResult{}
	finalAnswer := ""

	knowledgeBaseID := strings.TrimSpace(input.KnowledgeBaseID)
	if input.RetrievalResult != nil && len(input.RetrievalResult.RerankedResults) > 0 {
		retrievalResult = *input.RetrievalResult
		modelMessages = insertKnowledgeContext(modelMessages, retrievalResult)
		if !input.SuppressKnowledgeEvent {
			if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
				Type:    "knowledge_retrieved",
				Title:   "知识库召回",
				Content: renderKnowledgeContext(retrievalResult),
				TraceID: trace.ID,
			}); err != nil {
				return RunOutput{}, err
			}
		}
	} else if a.knowledge != nil && knowledgeBaseID != "" {
		result, err := a.knowledge.Retrieve(ctx, input.UserMessage, knowledge.RetrievalOptions{
			ConversationID: input.ConversationID,
			TopK:           8,
			Filter: knowledge.RetrievalFilter{
				KnowledgeBaseIDs: []string{knowledgeBaseID},
			},
		})
		if err == nil && len(result.RerankedResults) > 0 {
			retrievalResult = result
			modelMessages = insertKnowledgeContext(modelMessages, result)
			if !input.SuppressKnowledgeEvent {
				if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
					Type:    "knowledge_retrieved",
					Title:   "知识库召回",
					Content: renderKnowledgeContext(result),
					TraceID: trace.ID,
				}); err != nil {
					return RunOutput{}, err
				}
			}
		} else if err != nil {
			_ = emitAgentEvent(emit, contracts.AgentStreamEvent{
				Type:    "knowledge_retrieval_error",
				Title:   "知识库召回失败",
				Content: err.Error(),
				TraceID: trace.ID,
			})
		}
	}

	if detail, explicit, ok, err := a.maybeLoadSkill(input.UserMessage, emit, trace.ID); err != nil {
		trace.Error = err.Error()
		completedAt := shared.Now()
		trace.CompletedAt = &completedAt
		trace.WorkingMemorySnapshot = workingMemory
		a.store.UpdateTrace(trace)
		return RunOutput{}, err
	} else if ok {
		loadedSkillDetails = append(loadedSkillDetails, detail)
		loadedSkills[detail.Name] = struct{}{}
		modelMessages = appendSkillContext(modelMessages, input.UserMessage, detail, explicit)
	}

	for round := 1; round <= a.maxRounds; round++ {
		if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:    "round_start",
			Round:   round,
			Title:   fmt.Sprintf("第 %d 轮：模型决策", round),
			Content: "模型正在基于历史对话、工作记忆和工具 schema 生成下一步。",
			TraceID: trace.ID,
		}); err != nil {
			return RunOutput{}, err
		}
		compressModelMessages(&modelMessages, loadedSkillDetails)
		resp, err := a.chatModel(ctx, modelClient, model.Input{
			System:   systemPrompt(availableTools, enabledSkills, a.startup),
			Messages: modelMessages,
			Tools:    runtime.ToolSchemasFor(availableTools),
		}, emit, trace.ID, round)
		completedAt := shared.Now()
		if err != nil {
			trace.Error = err.Error()
			trace.CompletedAt = &completedAt
			trace.ToolCalls = allToolCalls
			trace.ToolResults = allToolResults
			trace.WorkingMemorySnapshot = workingMemory
			contextSnapshot := buildContextSnapshot(input.ConversationID, turnID, input.UserMessage, shortMemory, workingMemory, recent, allToolResults, availableTools, enabledSkills, retrievalResult, a.startup)
			trace.ContextSnapshot = &contextSnapshot
			a.store.UpdateTrace(trace)
			_ = emitAgentEvent(emit, contracts.AgentStreamEvent{
				Type:    "error",
				Round:   round,
				Title:   "模型调用失败",
				Content: err.Error(),
				TraceID: trace.ID,
			})
			return RunOutput{}, err
		}

		if len(resp.ToolCalls) == 0 {
			if request, ok := parseModelSkillRequest(resp.Content); ok {
				modelMessages = append(modelMessages, resp.Message)
				modelRequestedSkillLoads++
				assistantMessage := resp.Message
				if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
					Type:      "model_message",
					Round:     round,
					Content:   streamModelOutput(resp, nil),
					Assistant: &assistantMessage,
					TraceID:   trace.ID,
				}); err != nil {
					return RunOutput{}, err
				}
				if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
					Type:    "answer_reset",
					Round:   round,
					TraceID: trace.ID,
				}); err != nil {
					return RunOutput{}, err
				}
				if modelRequestedSkillLoads > maxModelRequestedSkillLoads {
					finalAnswer = a.recoverFinalAnswer(ctx, modelClient, modelMessages, input.UserMessage, emit, trace.ID, round, "模型连续请求加载 skill，已切换为直接回答。")
					break
				}
				if _, loaded := loadedSkills[request.Name]; loaded {
					finalAnswer = a.recoverFinalAnswer(ctx, modelClient, modelMessages, input.UserMessage, emit, trace.ID, round, "Skill /"+request.Name+" 已经加载，模型仍重复请求；请直接回答用户。")
					break
				}
				detail, ok := a.loadSkillInstructionsForModel(request, emit, trace.ID)
				if !ok {
					finalAnswer = a.recoverFinalAnswer(ctx, modelClient, modelMessages, input.UserMessage, emit, trace.ID, round, "请求的 skill /"+request.Name+" 不可用或已禁用；请基于当前上下文直接回答用户。")
					break
				}
				loadedSkills[detail.Name] = struct{}{}
				loadedSkillDetails = appendUniqueSkillDetail(loadedSkillDetails, detail)
				modelMessages = appendSkillInstructionsContext(modelMessages, detail, request.Reason, input.UserMessage)
				continue
			}
			modelMessages = append(modelMessages, resp.Message)
			finalAnswer = resp.Content
			if strings.TrimSpace(finalAnswer) == "" {
				finalAnswer = resp.Message.Content
			}
			break
		}

		modelMessages = append(modelMessages, resp.Message)
		roundToolCalls := make([]contracts.ToolCall, 0, len(resp.ToolCalls))
		for _, modelCall := range resp.ToolCalls {
			roundToolCalls = append(roundToolCalls, a.runtime.ResolveToolCall(modelCall))
		}
		assistantMessage := resp.Message
		if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:      "model_message",
			Round:     round,
			Content:   streamModelOutput(resp, roundToolCalls),
			Assistant: &assistantMessage,
			ToolCalls: roundToolCalls,
			TraceID:   trace.ID,
		}); err != nil {
			return RunOutput{}, err
		}
		if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:    "answer_reset",
			Round:   round,
			TraceID: trace.ID,
		}); err != nil {
			return RunOutput{}, err
		}
		if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:      "tool_calls",
			Round:     round,
			Title:     fmt.Sprintf("第 %d 轮：工具调用", round),
			Content:   shared.CompactJSON(roundToolCalls, 2400),
			ToolCalls: roundToolCalls,
			TraceID:   trace.ID,
		}); err != nil {
			return RunOutput{}, err
		}
		workingMemory = a.store.RecordToolCalls(input.ConversationID, turnID, roundToolCalls)
		roundResults := a.execToolsParallel(ctx, input.ConversationID, roundToolCalls, allowedToolIDs, modelClient)
		workingMemory = a.store.RecordToolResults(input.ConversationID, turnID, roundResults)
		roundWorkingMemory := workingMemory
		if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:          "tool_results",
			Round:         round,
			Title:         fmt.Sprintf("第 %d 轮：工具结果", round),
			Content:       streamToolResults(roundResults),
			ToolResults:   roundResults,
			WorkingMemory: &roundWorkingMemory,
			TraceID:       trace.ID,
		}); err != nil {
			return RunOutput{}, err
		}

		for i, result := range roundResults {
			toolCallID := ""
			if i < len(roundToolCalls) {
				toolCallID = roundToolCalls[i].ID
			}
			modelMessages = append(modelMessages, contracts.LLMMessage{
				Role:       contracts.RoleTool,
				ToolCallID: toolCallID,
				Content:    toolResultContent(result),
			})
		}

		allToolCalls = append(allToolCalls, roundToolCalls...)
		allToolResults = append(allToolResults, roundResults...)
		trace.LoopSteps = append(trace.LoopSteps, contracts.AgentLoopStep{
			Round:       round,
			Assistant:   resp.Message,
			ToolCalls:   roundToolCalls,
			ToolResults: roundResults,
		})
		trace.ToolCalls = allToolCalls
		trace.ToolResults = allToolResults
		trace.WorkingMemorySnapshot = workingMemory
		a.store.UpdateTrace(trace)
		if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:    "round_complete",
			Round:   round,
			Title:   fmt.Sprintf("第 %d 轮完成", round),
			Content: fmt.Sprintf("本轮完成 %d 个工具调用，继续交给模型判断是否需要下一轮。", len(roundToolCalls)),
			TraceID: trace.ID,
		}); err != nil {
			return RunOutput{}, err
		}
	}

	if strings.TrimSpace(finalAnswer) == "" {
		finalAnswer = a.recoverFinalAnswer(ctx, modelClient, modelMessages, input.UserMessage, emit, trace.ID, a.maxRounds+1, "已达到最大工具/skill 轮数；请停止请求工具或 skill，直接给出最终回答。")
		if strings.TrimSpace(finalAnswer) == "" {
			finalAnswer = "模型连续请求工具或 skill，未能生成最终回答。请简化问题，或切换到更稳定支持工具调用的模型后重试。"
		}
		modelMessages = append(modelMessages, contracts.LLMMessage{Role: contracts.RoleAssistant, Content: finalAnswer})
	}

	contextSnapshot := buildContextSnapshot(input.ConversationID, turnID, input.UserMessage, shortMemory, workingMemory, recent, allToolResults, availableTools, enabledSkills, retrievalResult, a.startup)
	completedAt := shared.Now()

	assistantMessageID := strings.TrimSpace(input.AssistantMessageID)
	if assistantMessageID == "" {
		assistantMessageID = shared.NewID("msg")
	}
	assistantMetadata := map[string]any{
		"toolResults": allToolResults,
		"loopRounds":  len(trace.LoopSteps) + 1,
		"skills":      loadedSkillDetails,
		"retrieval":   retrievalResult.RerankedResults,
	}
	for key, value := range input.AssistantMetadata {
		assistantMetadata[key] = value
	}
	assistantMessage := contracts.Message{
		ID:             assistantMessageID,
		ConversationID: input.ConversationID,
		Role:           contracts.RoleAssistant,
		Content:        finalAnswer,
		CreatedAt:      shared.Now(),
		Metadata:       assistantMetadata,
	}
	if !input.SkipAssistantMessageStore {
		a.store.AddMessage(assistantMessage)
	}
	if !input.SkipShortMemoryUpdate {
		a.store.UpdateShortMemory(input.ConversationID, input.UserMessage, finalAnswer, allToolResults)
	}
	workingMemory = a.store.CompleteWorkingMemory(input.ConversationID, turnID, finalAnswer, finalTaskStatus(allToolResults))

	trace.CompletedAt = &completedAt
	trace.ToolCalls = allToolCalls
	trace.ToolResults = allToolResults
	trace.WorkingMemorySnapshot = workingMemory
	trace.ContextSnapshot = &contextSnapshot
	trace.FinalAnswer = finalAnswer
	a.store.UpdateTrace(trace)
	if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
		Type:    "final",
		Title:   "最终回答",
		Content: finalAnswer,
		Message: &assistantMessage,
		TraceID: trace.ID,
	}); err != nil {
		return RunOutput{}, err
	}
	return RunOutput{UserMessage: userMessage, AssistantMessage: assistantMessage, Trace: trace}, nil
}

func emitAgentEvent(emit AgentEventHandler, event contracts.AgentStreamEvent) error {
	if emit == nil {
		return nil
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = shared.Now()
	}
	return emit(event)
}

func (a *Agent) chatModel(ctx context.Context, client model.Client, input model.Input, emit AgentEventHandler, traceID string, round int) (contracts.ModelResponse, error) {
	if emit == nil {
		return client.Chat(ctx, input)
	}
	streamer, ok := client.(model.StreamingClient)
	if !ok {
		return client.Chat(ctx, input)
	}
	return streamer.ChatStream(ctx, input, func(delta model.StreamDelta) error {
		if delta.Content == "" {
			return nil
		}
		return emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:    "answer_delta",
			Round:   round,
			Content: delta.Content,
			TraceID: traceID,
		})
	})
}

func (a *Agent) recoverFinalAnswer(ctx context.Context, client model.Client, messages []contracts.LLMMessage, originalUserMessage string, emit AgentEventHandler, traceID string, round int, reason string) string {
	recoveryMessages := append([]contracts.LLMMessage(nil), messages...)
	recoveryMessages = append(recoveryMessages, contracts.LLMMessage{
		Role: contracts.RoleUser,
		Content: strings.Join([]string{
			"[请直接给出最终回答]",
			reason,
			"不要再请求加载 skill，不要再调用工具，也不要输出 XML/JSON 协议片段。",
			"请基于已有上下文，用自然语言回答原始用户请求。",
			"原始用户请求：" + originalUserMessage,
		}, "\n"),
	})
	resp, err := a.chatModel(ctx, client, model.Input{
		System:   finalAnswerSystemPrompt(a.startup),
		Messages: recoveryMessages,
	}, emit, traceID, round)
	if err != nil {
		_ = emitAgentEvent(emit, contracts.AgentStreamEvent{
			Type:    "error",
			Round:   round,
			Title:   "最终回答恢复失败",
			Content: err.Error(),
			TraceID: traceID,
		})
		return ""
	}
	if strings.TrimSpace(resp.Content) != "" {
		return resp.Content
	}
	if strings.TrimSpace(resp.Message.Content) != "" {
		return resp.Message.Content
	}
	return ""
}

func streamModelOutput(resp contracts.ModelResponse, calls []contracts.ToolCall) string {
	if strings.TrimSpace(resp.Content) != "" {
		return resp.Content
	}
	if len(resp.ToolCalls) > 0 {
		return shared.CompactJSON(resp.ToolCalls, 2400)
	}
	if len(calls) > 0 {
		return shared.CompactJSON(calls, 2400)
	}
	return "(empty model output)"
}

func streamToolResults(results []contracts.ToolResult) string {
	lines := make([]string, 0, len(results))
	for _, result := range results {
		name := result.ToolID
		if result.OK {
			lines = append(lines, "- "+name+": "+shared.TrimRunes(shared.CompactJSON(result.Output, 1000), 1000))
			continue
		}
		lines = append(lines, "- "+name+" failed: "+shared.TrimRunes(result.Error, 1000))
	}
	if len(lines) == 0 {
		return "本轮没有工具结果。"
	}
	return strings.Join(lines, "\n")
}

func (a *Agent) execToolsParallel(ctx context.Context, conversationID string, calls []contracts.ToolCall, allowedToolIDs map[string]struct{}, modelClient model.Client) []contracts.ToolResult {
	results := make([]contracts.ToolResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(index int, tc contracts.ToolCall) {
			defer wg.Done()
			if tc.ToolID != "" {
				if _, ok := allowedToolIDs[tc.ToolID]; !ok {
					results[index] = contracts.ToolResult{ToolID: tc.ToolID, OK: false, Error: "tool is not enabled for this request"}
					return
				}
			}
			results[index] = a.runtime.Invoke(ctx, tc, runtime.InvocationContext{ConversationID: conversationID, Store: a.store, Model: modelClient})
		}(i, call)
	}
	wg.Wait()
	return results
}

func buildContextSnapshot(conversationID string, turnID string, userMessage string, shortMemory contracts.ShortMemory, workingMemory contracts.WorkingMemory, recent []contracts.Message, toolResults []contracts.ToolResult, tools []contracts.RuntimeTool, skillManifests []skills.Manifest, retrieval knowledge.RetrievalResult, startup claudecode.StartupContext) contracts.ContextSnapshot {
	sources := []contracts.ContextSource{
		{
			Type:    "short_memory",
			Title:   "短期记忆",
			Content: renderShortMemory(shortMemory),
		},
		{
			Type:    "working_memory",
			Title:   "工作记忆",
			Content: renderWorkingMemory(workingMemory),
		},
		{
			Type:    "conversation",
			Title:   "近期对话",
			Content: renderMessages(recent),
		},
	}
	sources = append(sources, startupContextSources(startup)...)
	sources = append(sources, retrievalContextSources(retrieval)...)
	if len(toolResults) > 0 {
		sources = append(sources, contracts.ContextSource{
			Type:    "tool",
			Title:   "工具结果",
			Content: shared.CompactJSON(toolResults, 2000),
		})
	}
	return contracts.ContextSnapshot{
		ID:             shared.NewID("ctx"),
		ConversationID: conversationID,
		TurnID:         turnID,
		CreatedAt:      shared.Now(),
		System:         systemPrompt(tools, skillManifests, startup),
		UserMessage:    userMessage,
		ShortMemory:    shortMemory,
		WorkingMemory:  workingMemory,
		RecentMessages: recent,
		ToolResults:    toolResults,
		Sources:        sources,
	}
}

func startupContextSources(startup claudecode.StartupContext) []contracts.ContextSource {
	sources := []contracts.ContextSource{}
	for _, file := range startup.Instructions {
		sources = append(sources, contracts.ContextSource{
			Type:    "instruction",
			Title:   file.Scope + " " + file.Path,
			Content: file.Content,
		})
	}
	for _, file := range startup.Rules {
		if len(file.Paths) > 0 {
			continue
		}
		sources = append(sources, contracts.ContextSource{
			Type:    "rule",
			Title:   file.Scope + " " + file.Path,
			Content: file.Content,
		})
	}
	if startup.AutoMemory != nil {
		sources = append(sources, contracts.ContextSource{
			Type:    "auto_memory",
			Title:   startup.AutoMemory.Path,
			Content: startup.AutoMemory.Content,
		})
	}
	if len(startup.MCP.Servers) > 0 {
		sources = append(sources, contracts.ContextSource{
			Type:    "mcp",
			Title:   "MCP 服务",
			Content: shared.CompactJSON(startup.MCP.Servers, 4000),
		})
	}
	return sources
}

func toolIDSet(tools []contracts.RuntimeTool) map[string]struct{} {
	allowed := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		allowed[tool.ID] = struct{}{}
	}
	return allowed
}

func (a *Agent) enabledSkillManifests() []skills.Manifest {
	if a.skillReg == nil || a.skillConfig == nil {
		return nil
	}
	all := a.skillConfig.List(a.skillReg)
	enabled := make([]skills.Manifest, 0, len(all))
	for _, manifest := range all {
		if manifest.Enabled && !manifest.DisableModelInvocation {
			enabled = append(enabled, manifest)
		}
	}
	return enabled
}

func appendUniqueSkillDetail(current []skills.Detail, next skills.Detail) []skills.Detail {
	for _, item := range current {
		if strings.EqualFold(item.Name, next.Name) {
			return current
		}
	}
	return append(current, next)
}

func (a *Agent) maybeLoadSkill(userMessage string, emit AgentEventHandler, traceID string) (skills.Detail, bool, bool, error) {
	if a.skillReg == nil || a.skillConfig == nil {
		return skills.Detail{}, false, false, nil
	}
	skill, _, explicit, ok := a.skillReg.Resolve(userMessage, a.skillConfig.EnabledMap())
	if !ok {
		return skills.Detail{}, false, false, nil
	}
	detail := skill.Detail()
	if err := emitAgentEvent(emit, contracts.AgentStreamEvent{
		Type:    "skill_loaded",
		Title:   "渐进加载 Skill",
		Content: renderLoadedSkill(detail, explicit),
		TraceID: traceID,
	}); err != nil {
		return skills.Detail{}, explicit, true, err
	}
	return detail, explicit, true, nil
}

func (a *Agent) loadSkillInstructionsForModel(request modelSkillRequest, emit AgentEventHandler, traceID string) (skills.Detail, bool) {
	if a.skillReg == nil || a.skillConfig == nil {
		return skills.Detail{}, false
	}
	skill, ok := a.skillReg.Get(request.Name)
	if !ok {
		return skills.Detail{}, false
	}
	detail := skill.Detail()
	skillKey := strings.ToLower(strings.TrimPrefix(detail.Name, "/"))
	if !a.skillConfig.EnabledMap()[skillKey] {
		return skills.Detail{}, false
	}
	if detail.DisableModelInvocation {
		return skills.Detail{}, false
	}
	_ = emitAgentEvent(emit, contracts.AgentStreamEvent{
		Type:    "skill_loaded",
		Title:   "模型请求 Skill 信息",
		Content: renderLoadedSkillForModel(detail, request.Reason),
		TraceID: traceID,
	})
	return detail, true
}

func appendSkillContext(messages []contracts.LLMMessage, originalUserMessage string, detail skills.Detail, explicit bool) []contracts.LLMMessage {
	mode := "heuristic"
	if explicit {
		mode = "slash"
	}
	content := strings.Join([]string{
		"[已渐进加载 Skill 上下文]",
		"选择方式：" + mode,
		renderSkillPackage(detail),
		"",
		"原始用户请求：",
		originalUserMessage,
		"",
		"请把上面的 skill 包作为提示上下文来回答原始用户请求。不要把 skill 描述成可执行动作或工具调用。",
	}, "\n")
	return append(messages, contracts.LLMMessage{Role: contracts.RoleUser, Content: content})
}

func appendSkillInstructionsContext(messages []contracts.LLMMessage, detail skills.Detail, reason string, originalUserMessage string) []contracts.LLMMessage {
	content := strings.Join([]string{
		"[已渐进加载 Skill 详细说明]",
		"模型请求原因：" + reason,
		"",
		renderSkillPackage(detail),
		"",
		"原始用户请求：",
		originalUserMessage,
		"",
		"请把这个 skill 包作为提示上下文继续处理。Skill 没有执行方法，也不是工具；不要虚构 skill 工具调用。",
	}, "\n")
	return append(messages, contracts.LLMMessage{Role: contracts.RoleUser, Content: content})
}

func renderSkillPackage(detail skills.Detail) string {
	return strings.Join([]string{
		"## Skill 元数据",
		"名称：/" + detail.Name,
		"ID: " + detail.ID,
		"来源：" + emptyFallback(detail.Source, "未知"),
		"路径：" + detail.Path,
		"用途：" + detail.Purpose,
		"适用场景：" + emptyFallback(detail.WhenToUse, detail.Description),
		"触发词：" + strings.Join(detail.Triggers, ", "),
		"允许用户调用：" + fmt.Sprintf("%t", detail.UserInvocable),
		"禁止模型调用：" + fmt.Sprintf("%t", detail.DisableModelInvocation),
		"允许工具：" + strings.Join(detail.AllowedTools, ", "),
		"禁止工具：" + strings.Join(detail.DisallowedTools, ", "),
		"模型：" + detail.Model,
		"推理强度：" + detail.Effort,
		"上下文：" + detail.Context,
		"子代理：" + detail.Agent,
		"Shell：" + detail.Shell,
		"",
		"## README.md",
		emptyFallback(detail.Readme, "没有 README.md 内容。"),
		"",
		"## 描述",
		emptyFallback(detail.Description, "没有描述。"),
		"",
		"## 指令",
		emptyFallback(detail.Instructions, "没有指令。"),
		"",
		"## 示例",
		renderExamples(detail.Examples),
		"",
		"## 资源",
		renderResources(detail.Resources),
	}, "\n")
}

func renderExamples(examples []skills.Example) string {
	if len(examples) == 0 {
		return "没有示例。"
	}
	blocks := make([]string, 0, len(examples))
	for _, example := range examples {
		title := strings.TrimSpace(example.Name)
		if title == "" {
			title = "示例"
		}
		blocks = append(blocks, strings.Join([]string{
			"### " + title,
			"用户：" + example.User,
			"助手：" + example.Assistant,
		}, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

func renderResources(resources []skills.Resource) string {
	if len(resources) == 0 {
		return "没有资源。"
	}
	blocks := make([]string, 0, len(resources))
	for _, resource := range resources {
		lines := []string{"### " + resource.Name, "类型：" + resource.Type}
		if strings.TrimSpace(resource.URI) != "" {
			lines = append(lines, "URI: "+resource.URI)
		}
		if strings.TrimSpace(resource.Content) != "" {
			lines = append(lines, "内容："+resource.Content)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

func emptyFallback(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func renderLoadedSkill(detail skills.Detail, explicit bool) string {
	mode := "启发式匹配"
	if explicit {
		mode = "用户通过 / 指定"
	}
	return strings.Join([]string{
		"Skill: /" + detail.Name,
		"模式：" + mode,
		"已加载的提示包：",
		shared.TrimRunes(renderSkillPackage(detail), 2200),
	}, "\n")
}

func renderLoadedSkillForModel(detail skills.Detail, reason string) string {
	return strings.Join([]string{
		"Skill: /" + detail.Name,
		"模式：模型请求更多 skill 操作信息",
		"原因：" + strings.TrimSpace(reason),
		"已加载的提示包：",
		shared.TrimRunes(renderSkillPackage(detail), 2200),
	}, "\n")
}

func parseModelSkillRequest(content string) (modelSkillRequest, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return modelSkillRequest{}, false
	}
	if match := regexp.MustCompile(`(?is)<load_skill\s+name=["']?([A-Za-z0-9_:-]+)["']?[^>]*>(.*?)</load_skill>`).FindStringSubmatch(content); len(match) >= 3 {
		name := normalizeRequestedSkill(match[1])
		if name == "" {
			return modelSkillRequest{}, false
		}
		return modelSkillRequest{Name: name, Reason: strings.TrimSpace(match[2])}, true
	}
	if match := regexp.MustCompile(`(?im)^\s*LOAD_SKILL\s*:\s*(/?[A-Za-z0-9_:-]+)\s*(.*)$`).FindStringSubmatch(content); len(match) >= 2 {
		name := normalizeRequestedSkill(match[1])
		if name == "" {
			return modelSkillRequest{}, false
		}
		reason := ""
		if len(match) >= 3 {
			reason = strings.TrimSpace(match[2])
		}
		return modelSkillRequest{Name: name, Reason: reason}, true
	}
	return modelSkillRequest{}, false
}

func normalizeRequestedSkill(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "/"))
	value = strings.TrimPrefix(value, "skill:")
	return strings.ToLower(value)
}

func renderShortMemory(memory contracts.ShortMemory) string {
	parts := []string{"摘要：" + memory.Summary}
	if memory.ActiveTask != "" {
		parts = append(parts, "当前任务："+memory.ActiveTask)
	}
	if len(memory.RecentFacts) > 0 {
		parts = append(parts, "近期事实：\n- "+strings.Join(memory.RecentFacts, "\n- "))
	}
	return strings.Join(parts, "\n")
}

func renderWorkingMemory(memory contracts.WorkingMemory) string {
	parts := []string{"意图：" + memory.Intent}
	if memory.ActiveTask != nil {
		parts = append(parts, "当前任务："+memory.ActiveTask.Goal+" ("+memory.ActiveTask.Status+")")
	}
	if len(memory.Constraints) > 0 {
		parts = append(parts, "约束：\n- "+strings.Join(memory.Constraints, "\n- "))
	}
	if len(memory.ToolResultSummaries) > 0 {
		parts = append(parts, "工具结果摘要：\n- "+strings.Join(memory.ToolResultSummaries, "\n- "))
	}
	if len(memory.Scratchpad) > 0 {
		parts = append(parts, "临时记录：\n- "+strings.Join(memory.Scratchpad, "\n- "))
	}
	return strings.Join(parts, "\n")
}

func renderMessages(messages []contracts.Message) string {
	lines := []string{}
	for _, message := range messages {
		lines = append(lines, string(message.Role)+": "+message.Content)
	}
	return strings.Join(lines, "\n")
}

func finalTaskStatus(results []contracts.ToolResult) string {
	for _, result := range results {
		if strings.HasPrefix(result.Error, "confirmation required") {
			return "waiting_for_confirmation"
		}
		if !result.OK && result.Error != "" {
			return "blocked"
		}
	}
	return "in_progress"
}

func buildInitialModelMessages(shortMemory contracts.ShortMemory, recent []contracts.Message, userMessage string) []contracts.LLMMessage {
	messages := []contracts.LLMMessage{}
	if hasUsefulShortMemory(shortMemory.Summary) {
		messages = append(messages,
			contracts.LLMMessage{Role: contracts.RoleUser, Content: "[上下文已压缩 - 对话摘要]\n" + renderShortMemory(shortMemory)},
			contracts.LLMMessage{Role: contracts.RoleAssistant, Content: "收到，我已经理解此前对话的上下文。"},
		)
	}
	for _, message := range recent {
		if message.Role == contracts.RoleUser || message.Role == contracts.RoleAssistant {
			messages = append(messages, contracts.LLMMessage{Role: message.Role, Content: message.Content})
		}
	}
	messages = append(messages, contracts.LLMMessage{Role: contracts.RoleUser, Content: userMessage})
	return messages
}

func hasUsefulShortMemory(summary string) bool {
	trimmed := strings.TrimSpace(summary)
	return trimmed != "" && trimmed != contracts.DefaultShortMemorySummary && trimmed != contracts.LegacyDefaultShortMemorySummary
}

func insertKnowledgeContext(messages []contracts.LLMMessage, retrieval knowledge.RetrievalResult) []contracts.LLMMessage {
	if len(retrieval.RerankedResults) == 0 {
		return messages
	}
	contextMessage := contracts.LLMMessage{
		Role:    contracts.RoleUser,
		Content: renderKnowledgePromptContext(retrieval),
	}
	if len(messages) == 0 {
		return []contracts.LLMMessage{contextMessage}
	}
	out := append([]contracts.LLMMessage(nil), messages...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == contracts.RoleUser {
			out = append(out[:i], append([]contracts.LLMMessage{contextMessage}, out[i:]...)...)
			return out
		}
	}
	return append(out, contextMessage)
}

func renderKnowledgePromptContext(retrieval knowledge.RetrievalResult) string {
	sections := []string{
		"[召回的知识库上下文]",
		"当这些知识片段与用户请求相关时，请优先参考它们；如果不相关，请忽略。不要声称这些内容来自工具执行。",
	}
	for i, chunk := range retrieval.RerankedResults {
		source := strings.TrimSpace(chunk.SourceURI)
		if source == "" {
			source = chunk.DocID
		}
		title := strings.TrimSpace(chunk.Title)
		if title == "" {
			title = "未命名"
		}
		header := fmt.Sprintf("## 结果 %d：%s", i+1, title)
		meta := []string{
			"来源：" + source,
			"片段：" + chunk.ID,
			"召回依据：" + strings.Join(chunk.Sources, ", "),
		}
		if strings.TrimSpace(chunk.HeadingPath) != "" {
			meta = append(meta, "标题路径："+chunk.HeadingPath)
		}
		sections = append(sections, strings.Join([]string{
			header,
			strings.Join(meta, "\n"),
			shared.TrimRunes(chunk.Content, 2600),
		}, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

func renderKnowledgeContext(retrieval knowledge.RetrievalResult) string {
	if len(retrieval.RerankedResults) == 0 {
		return "未找到相关知识片段。"
	}
	lines := []string{}
	for i, chunk := range retrieval.RerankedResults {
		title := strings.TrimSpace(chunk.Title)
		if title == "" {
			title = chunk.DocID
		}
		lines = append(lines, fmt.Sprintf("%d. %s [%s]\n%s", i+1, title, strings.Join(chunk.Sources, ", "), shared.TrimRunes(chunk.Content, 500)))
	}
	return strings.Join(lines, "\n\n")
}

func retrievalContextSources(retrieval knowledge.RetrievalResult) []contracts.ContextSource {
	if len(retrieval.RerankedResults) == 0 {
		return nil
	}
	sources := make([]contracts.ContextSource, 0, len(retrieval.RerankedResults))
	for _, chunk := range retrieval.RerankedResults {
		title := strings.TrimSpace(chunk.Title)
		if title == "" {
			title = chunk.DocID
		}
		sources = append(sources, contracts.ContextSource{
			Type:    "knowledge",
			Title:   title,
			Content: chunk.Content,
			Meta: map[string]any{
				"chunkId":         chunk.ID,
				"docId":           chunk.DocID,
				"knowledgeBaseId": chunk.KnowledgeBaseID,
				"sources":         chunk.Sources,
				"scores":          chunk.Scores,
				"sourceUri":       chunk.SourceURI,
			},
		})
	}
	return sources
}

func compressModelMessages(messages *[]contracts.LLMMessage, loadedSkills []skills.Detail) {
	snipToolOutputs(*messages)
	const keepRecent = 10
	if len(*messages) <= keepRecent+2 {
		return
	}
	split := len(*messages) - keepRecent
	for split > 0 && (*messages)[split].Role == contracts.RoleTool {
		split--
	}
	old := append([]contracts.LLMMessage(nil), (*messages)[:split]...)
	tail := append([]contracts.LLMMessage(nil), (*messages)[split:]...)
	summary := extractKeyInfo(old)
	head := []contracts.LLMMessage{
		{Role: contracts.RoleUser, Content: "[上下文已压缩 - 对话摘要]\n" + summary},
		{Role: contracts.RoleAssistant, Content: "收到，我已经理解此前对话的上下文。"},
	}
	head = append(head, retainedSkillContextMessages(loadedSkills)...)
	*messages = append(head, tail...)
}

func snipToolOutputs(messages []contracts.LLMMessage) {
	for i := range messages {
		if messages[i].Role != contracts.RoleTool || len(messages[i].Content) <= 1500 {
			continue
		}
		lines := strings.Split(messages[i].Content, "\n")
		if len(lines) <= 6 {
			messages[i].Content = shared.TrimRunes(messages[i].Content, 1500)
			continue
		}
		messages[i].Content = strings.Join(lines[:3], "\n") +
			"\n...（为节省上下文，已截断工具输出）...\n" +
			strings.Join(lines[len(lines)-3:], "\n")
	}
}

func extractKeyInfo(messages []contracts.LLMMessage) string {
	lines := []string{}
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		prefix := string(message.Role)
		if strings.Contains(strings.ToLower(message.Content), "error") {
			lines = append(lines, prefix+": "+shared.TrimRunes(message.Content, 220))
			continue
		}
		if message.Role == contracts.RoleUser || message.Role == contracts.RoleAssistant {
			lines = append(lines, prefix+": "+shared.TrimRunes(message.Content, 220))
		}
	}
	if len(lines) == 0 {
		return "(previous context compressed)"
	}
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}

func retainedSkillContextMessages(loadedSkills []skills.Detail) []contracts.LLMMessage {
	if len(loadedSkills) == 0 {
		return nil
	}
	const perSkillLimit = 20000
	const totalLimit = 100000
	out := []contracts.LLMMessage{}
	used := 0
	for _, detail := range loadedSkills {
		if used >= totalLimit {
			break
		}
		content := shared.TrimRunes(renderSkillPackage(detail), perSkillLimit)
		if remaining := totalLimit - used; len([]rune(content)) > remaining {
			content = shared.TrimRunes(content, remaining)
		}
		used += len([]rune(content))
		out = append(out, contracts.LLMMessage{
			Role:    contracts.RoleUser,
			Content: "[Skill context retained after compaction]\n" + content,
		})
	}
	return out
}

func toolResultContent(result contracts.ToolResult) string {
	if !result.OK {
		return "Error: " + result.Error
	}
	return shared.CompactJSON(result.Output, 4000)
}

func systemPrompt(tools []contracts.RuntimeTool, skillManifests []skills.Manifest, startup claudecode.StartupContext) string {
	toolLines := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolLines = append(toolLines, "- **"+tool.Name+"**: "+tool.Description)
	}
	if len(toolLines) == 0 {
		toolLines = append(toolLines, "- 当前没有注册本地运行时工具。")
	}
	skillLines := make([]string, 0, len(skillManifests))
	for _, skill := range skillManifests {
		line := "- /" + skill.Name + ": " + skill.Purpose
		whenToUse := firstNonEmptyString(skill.WhenToUse, skill.Description)
		if strings.TrimSpace(whenToUse) != "" {
			line += " 适用场景：" + whenToUse
		}
		if len(skill.Triggers) > 0 {
			line += " 触发词：" + strings.Join(skill.Triggers, ", ")
		}
		if strings.TrimSpace(skill.Source) != "" {
			line += " 来源：" + skill.Source
		}
		skillLines = append(skillLines, line)
	}
	if len(skillLines) == 0 {
		skillLines = append(skillLines, "- 当前没有启用 skill。")
	}
	return strings.Join([]string{
		"你是一个能够实现工作任务并进行深度排疑解惑的智能助手，运行在本地 Web 对话界面背后。",
		"你需要以循环方式工作：收集上下文，在有价值时采取行动，观察结果，验证进展，并持续推进，直到能够可靠地回答用户。",
		"",
		renderStartupPrompt(startup),
		"",
		"# 工具",
		"工具是本地运行时或外部集成暴露的可执行能力。只有当工具能实质性帮助完成任务时才使用。",
		strings.Join(toolLines, "\n"),
		"",
		"# MCP",
		"MCP 服务是从配置中加载的外部工具或数据集成。MCP 与 skill 是两类不同机制。",
		renderMCPPrompt(startup.MCP),
		"",
		"# Skill 提示包",
		"Skill 不是工具，而是渐进加载的提示词/上下文包。",
		"这里仅列出已启用且允许模型调用的 skill 元数据：名称、用途、适用说明、触发词和来源。完整的 SKILL.md 正文、支持资源和示例只会在用户通过 /skillName 选择、轻量匹配器命中，或你明确请求加载时进入下一轮模型上下文。",
		"如果某个已列出的 skill 看起来相关，但元数据不足以完成任务，请只回复 <load_skill name=\"skill_name\">说明为什么需要完整 skill 包</load_skill>。系统会在下一轮加载 README.md、描述、指令、示例和资源。这只是提示上下文加载，不是工具执行。",
		strings.Join(skillLines, "\n"),
		"",
		"# 规则",
		"1. 当工具能实质性帮助回答用户时，主动使用工具。",
		"2. 如果调用了工具，必须等待工具结果后再给出最终回答。",
		"3. 当结果显示还需要更多工作时，继续进行多轮处理。",
		"4. 不要把 skill 当成工具调用，不要虚构 skill 工具调用，也不要把 skill 描述为可执行能力。",
		"5. 区分 skill 和 MCP：skill 加载提示上下文；MCP 暴露外部工具或资源。",
		"6. 对工具失败或策略阻断给出清晰解释。",
	}, "\n")
}

func finalAnswerSystemPrompt(startup claudecode.StartupContext) string {
	parts := []string{
		"你是一个能够实现工作任务并进行深度排疑解惑的智能助手。",
		"当前处于最终回答恢复阶段。你必须直接回答用户，不要请求工具，不要请求加载 skill，不要输出 XML/JSON 协议片段。",
		"如果已有上下文不足以完成任务，请说明缺少哪些信息，并给出当前能确定的结论或下一步建议。",
	}
	if strings.TrimSpace(startup.ProjectRoot) != "" {
		parts = append(parts, "项目根目录："+startup.ProjectRoot)
	}
	return strings.Join(parts, "\n")
}

func renderStartupPrompt(startup claudecode.StartupContext) string {
	sections := []string{
		"# 启动上下文",
		"项目根目录：" + startup.ProjectRoot,
		"配置目录：" + startup.ConfigDir,
	}
	if len(startup.Instructions) > 0 {
		sections = append(sections, "## CLAUDE.md 指令")
		for _, file := range startup.Instructions {
			sections = append(sections, "### "+file.Scope+" "+file.Path+"\n"+shared.TrimRunes(file.Content, 12000))
		}
	}
	unscopedRules := []claudecode.ContextFile{}
	pathScopedRules := []claudecode.ContextFile{}
	for _, rule := range startup.Rules {
		if len(rule.Paths) > 0 {
			pathScopedRules = append(pathScopedRules, rule)
			continue
		}
		unscopedRules = append(unscopedRules, rule)
	}
	if len(unscopedRules) > 0 {
		sections = append(sections, "## 规则")
		for _, rule := range unscopedRules {
			sections = append(sections, "### "+rule.Scope+" "+rule.Path+"\n"+shared.TrimRunes(rule.Content, 8000))
		}
	}
	if len(pathScopedRules) > 0 {
		lines := []string{}
		for _, rule := range pathScopedRules {
			lines = append(lines, "- "+rule.Path+" 适用路径："+strings.Join(rule.Paths, ", "))
		}
		sections = append(sections, "## 路径限定规则\n"+strings.Join(lines, "\n"))
	}
	if startup.AutoMemory != nil && strings.TrimSpace(startup.AutoMemory.Content) != "" {
		sections = append(sections, "## 自动记忆\n"+shared.TrimRunes(startup.AutoMemory.Content, 12000))
	}
	if len(startup.Settings) > 0 {
		sections = append(sections, "## 设置\n"+shared.CompactJSON(startup.Settings, 6000))
	}
	return strings.Join(sections, "\n\n")
}

func renderMCPPrompt(config claudecode.MCPConfig) string {
	if len(config.Servers) == 0 {
		return "- 没有从 .mcp.json 或配置中发现 MCP 服务。"
	}
	lines := make([]string, 0, len(config.Servers))
	for _, server := range config.Servers {
		target := server.Command
		if target == "" {
			target = server.URL
		}
		if target == "" {
			target = "（已省略配置目标）"
		}
		line := "- " + server.Name + " [" + server.Scope + "]"
		if server.Type != "" {
			line += " 类型=" + server.Type
		}
		line += " 目标=" + target
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
