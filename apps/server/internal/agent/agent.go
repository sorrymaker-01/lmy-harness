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

// Agent 是整个服务的核心编排器（agent loop 的实现者）。
// 它把记忆（memory.Store）、工具运行时（runtime.Runtime）、大模型客户端（model.Client）、
// skill 注册表/配置、知识库（RAG）和 Claude Code 风格的启动上下文（CLAUDE.md 等）组合在一起，
// 以多轮循环的方式驱动“模型决策 -> 工具执行 -> 结果回填”，直到产出最终回答。
// 除 model 字段外，其余依赖在构造后基本不再变更；model 可通过 SetModel 在运行期热切换，
// 因此用 modelMu 读写锁保护。
type Agent struct {
	store       memory.Store              // 会话消息、短期记忆、工作记忆与 trace 的持久化存储
	runtime     *runtime.Runtime          // 本地工具运行时：负责工具注册、schema 导出与实际调用
	modelMu     sync.RWMutex              // 保护 model 字段的并发读写（支持运行期切换模型）
	model       model.Client              // 默认的大模型客户端（可被 RunInput.Model 按次覆盖）
	skillReg    *skills.Registry          // 所有已发现 skill 的注册表（提供解析/查找能力）
	skillConfig *skills.ConfigStore       // skill 的启用/禁用配置（用户可动态开关）
	knowledge   *knowledge.Store          // 知识库存储，用于 RAG 检索；可为 nil 表示未启用知识库
	startup     claudecode.StartupContext // 启动时收集的 Claude Code 上下文：CLAUDE.md、规则、自动记忆、MCP 配置等
	maxRounds   int                       // agent 主循环的最大轮数上限，防止模型无限调用工具
}

// RunInput 是一次 agent 运行（一个用户回合）的全部输入参数。
// 除 ConversationID 与 UserMessage 外，其余字段多为可选的行为开关，
// 供上层（HTTP 层、多模型对比、消息重放等场景）精细控制消息存储与事件推送行为。
type RunInput struct {
	ConversationID            string                     // 会话 ID，用于定位记忆、消息历史与 trace
	UserMessage               string                     // 用户本轮输入的原始文本
	UserMessageID             string                     // 可选：外部指定的用户消息 ID；为空则自动生成
	Model                     model.Client               // 可选：本次运行使用的模型客户端；为 nil 时使用 Agent 默认模型
	ModelConfigID             string                     // 可选：模型配置 ID（供上层记录本次使用的模型配置）
	KnowledgeBaseID           string                     // 可选：知识库 ID；非空且未提供 RetrievalResult 时触发内部 RAG 检索
	RetrievalResult           *knowledge.RetrievalResult // 可选：外部预先完成的检索结果，提供后跳过内部检索直接注入
	RecentMessages            []contracts.Message        // 可选：外部指定的近期消息窗口（配合 UseRecentMessages 使用）
	UseRecentMessages         bool                       // 为 true 时使用 RecentMessages，而不是从 store 读取最近 12 条
	AssistantMessageID        string                     // 可选：外部指定的助手消息 ID；为空则自动生成
	AssistantMetadata         map[string]any             // 可选：附加到助手消息 metadata 的额外键值（会覆盖内置同名键）
	SkipUserMessageStore      bool                       // 为 true 时不把用户消息写入 store（例如多模型对比场景已写过一次）
	SuppressUserMessageEvent  bool                       // 为 true 时不向流式回调发送 user_message 事件
	SkipAssistantMessageStore bool                       // 为 true 时不把助手消息写入 store
	SkipShortMemoryUpdate     bool                       // 为 true 时本回合结束后不更新短期记忆摘要
	SuppressKnowledgeEvent    bool                       // 为 true 时不发送 knowledge_retrieved 事件（避免对比模式重复展示）
}

// RunOutput 是一次 agent 运行的结果：落库后的用户消息、最终助手消息，
// 以及记录了完整执行过程（每轮模型输出、工具调用与结果、上下文快照）的 Trace。
type RunOutput struct {
	UserMessage      contracts.Message `json:"userMessage"`
	AssistantMessage contracts.Message `json:"assistantMessage"`
	Trace            contracts.Trace   `json:"trace"`
}

// AgentEventHandler 是流式事件回调。agent 在运行过程中把每个关键节点
// （用户输入、轮次开始、模型增量输出、工具调用/结果、skill 加载、最终回答等）
// 封装成 contracts.AgentStreamEvent 依次推送给它（通常由 HTTP SSE 层实现）。
// 回调返回非 nil 错误会中止本次运行（例如客户端断开连接）。
type AgentEventHandler func(contracts.AgentStreamEvent) error

// modelSkillRequest 表示模型在纯文本回复中主动请求加载某个 skill 的解析结果。
// 模型可用 <load_skill name="xxx">原因</load_skill> 或 "LOAD_SKILL: xxx 原因" 两种协议表达，
// 由 parseModelSkillRequest 负责识别。
type modelSkillRequest struct {
	Name   string // 规范化后的 skill 名称（小写、去掉前导 / 与 skill: 前缀）
	Reason string // 模型给出的加载理由，会回显在流式事件与注入的上下文中
}

// maxModelRequestedSkillLoads 限制单次运行中模型主动请求加载 skill 的次数，
// 防止模型陷入“不断请求 skill 而不回答”的死循环；超限后强制进入最终回答恢复流程。
const maxModelRequestedSkillLoads = 3

// NewAgent 构造一个 Agent。
// 参数依次为：消息/记忆存储、工具运行时、默认模型客户端、skill 注册表、
// skill 启用配置，以及启动时收集的 Claude Code 上下文。
// 主循环最大轮数默认 20，可通过 SetMaxRounds 调整；知识库通过 SetKnowledgeStore 另行注入。
func NewAgent(store memory.Store, runtime *runtime.Runtime, model model.Client, skillReg *skills.Registry, skillConfig *skills.ConfigStore, startup claudecode.StartupContext) *Agent {
	return &Agent{store: store, runtime: runtime, model: model, skillReg: skillReg, skillConfig: skillConfig, startup: startup, maxRounds: 20}
}

// CloneRuntimeWithout 返回一个剔除了指定名称工具的运行时副本，
// 用于需要禁用部分工具的派生场景（如子任务、受限模式），不影响原运行时。
func (a *Agent) CloneRuntimeWithout(names ...string) *runtime.Runtime {
	return a.runtime.CloneWithout(names...)
}

// Model 返回当前默认模型客户端（读锁保护，可与 SetModel 并发安全地调用）。
func (a *Agent) Model() model.Client {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.model
}

// SetModel 在运行期热切换默认模型客户端；传入 nil 时忽略。
// 写锁保护，保证并发执行中的回合读取到的是一致的客户端引用。
func (a *Agent) SetModel(model model.Client) {
	if model == nil {
		return
	}
	a.modelMu.Lock()
	a.model = model
	a.modelMu.Unlock()
}

// SetKnowledgeStore 注入知识库存储，启用 RAG 检索能力。
// 注意：该字段未加锁，预期在服务启动阶段（开始处理请求前）调用一次。
func (a *Agent) SetKnowledgeStore(store *knowledge.Store) {
	a.knowledge = store
}

// SkillRegistry 返回 skill 注册表，供 HTTP 层查询/管理 skill 使用。
func (a *Agent) SkillRegistry() *skills.Registry {
	return a.skillReg
}

// SkillConfig 返回 skill 启用配置存储，供上层动态开关 skill 使用。
func (a *Agent) SkillConfig() *skills.ConfigStore {
	return a.skillConfig
}

// StartupContext 返回启动时收集的 Claude Code 上下文（CLAUDE.md、规则、MCP 等）。
func (a *Agent) StartupContext() claudecode.StartupContext {
	return a.startup
}

// SetMaxRounds 设置 agent 主循环最大轮数；仅接受正数，非正数被忽略。
func (a *Agent) SetMaxRounds(maxRounds int) {
	if maxRounds > 0 {
		a.maxRounds = maxRounds
	}
}

// Run 以非流式方式执行一个完整的用户回合（不推送中间事件），
// 内部与 RunStream 共用同一套 run 实现。
func (a *Agent) Run(ctx context.Context, input RunInput) (RunOutput, error) {
	return a.run(ctx, input, nil)
}

// RunStream 以流式方式执行一个完整的用户回合，
// 通过 emit 回调实时推送执行过程中的各类事件（供 SSE 前端逐步渲染）。
func (a *Agent) RunStream(ctx context.Context, input RunInput, emit AgentEventHandler) (RunOutput, error) {
	return a.run(ctx, input, emit)
}

// run 是 agent 的核心执行入口，完整实现一个用户回合的 agent loop：
//
//  1. 准备阶段：读取近期消息与短期记忆，落库用户消息，初始化工作记忆与 Trace；
//  2. 上下文注入：按需执行知识库 RAG 检索并注入召回片段；
//     根据用户消息（/skill 显式调用或触发词命中）渐进加载 skill 提示包；
//  3. 主循环（最多 maxRounds 轮）：每轮先压缩历史消息，再携带 system prompt 与
//     工具 schema 调用模型；若模型返回工具调用则并行执行并把结果以 tool 消息回填，
//     进入下一轮；若模型以文本协议请求加载 skill，则注入 skill 全文后继续；
//     若模型直接给出文本回答，则作为最终答案退出循环；
//  4. 兜底恢复：循环耗尽仍无答案时，强制模型在“禁止工具/skill”约束下直接作答；
//  5. 收尾：落库助手消息、更新短期记忆与工作记忆、写入完整 Trace 与上下文快照，
//     并发送 final 事件。
//
// emit 为 nil 时表示非流式执行，所有事件推送会被静默跳过。
func (a *Agent) run(ctx context.Context, input RunInput, emit AgentEventHandler) (RunOutput, error) {
	// —— 准备阶段：确定近期对话窗口 ——
	// 默认从存储读取最近 12 条消息作为模型可见的对话历史；
	// 上层（多模型对比、重放）可通过 UseRecentMessages 显式传入统一历史，保证多次运行输入一致。
	recent := input.RecentMessages
	if !input.UseRecentMessages {
		recent = a.store.RecentMessages(input.ConversationID, 12)
	}
	// 用户消息 ID 允许外部指定（幂等/重放场景），否则自动生成。
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
	// 落库用户消息；对比/重放场景可能已写入过，通过开关跳过避免重复。
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

	// —— 初始化本回合（turn）的执行环境 ——
	turnID := shared.NewID("turn")
	// 短期记忆：跨回合累积的对话摘要与事实，作为压缩形式的历史上下文来源。
	shortMemory := a.store.GetShortMemory(input.ConversationID)
	// 工作记忆：本回合内的意图、任务状态、约束、工具结果摘要等临时状态，随执行过程持续更新。
	workingMemory := a.store.StartWorkingMemory(input.ConversationID, turnID, userMessage.ID, input.UserMessage, shortMemory, recent)
	availableTools := a.runtime.ListTools()
	// 允许调用的工具 ID 白名单：执行阶段用它拦截模型幻觉出的未注册工具。
	allowedToolIDs := toolIDSet(availableTools)
	// 已启用且允许模型调用的 skill 清单（仅元数据），用于 system prompt 中的 skill 列表。
	enabledSkills := a.enabledSkillManifests()
	// 初始模型消息序列：短期记忆摘要（若有）+ 近期对话 + 本次用户消息。
	modelMessages := buildInitialModelMessages(shortMemory, recent, input.UserMessage)
	// 模型客户端优先级：本次请求指定 > Agent 默认模型。
	modelClient := input.Model
	if modelClient == nil {
		modelClient = a.Model()
	}

	// Trace 记录本回合完整的执行轨迹（各轮模型输出、工具调用与结果、记忆快照等），
	// 先落库占位，执行过程中每轮增量更新，便于前端实时查看与事后审计。
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

	// —— 跨轮累积的状态 ——
	var allToolCalls []contracts.ToolCall     // 所有轮次的工具调用（最终写入 trace 与助手消息 metadata）
	var allToolResults []contracts.ToolResult // 所有轮次的工具结果
	var loadedSkillDetails []skills.Detail    // 已加载的 skill 完整详情（上下文压缩时需要原样保留）
	loadedSkills := map[string]struct{}{}     // 已加载 skill 名集合，用于拒绝模型重复请求同一 skill
	modelRequestedSkillLoads := 0             // 模型主动请求加载 skill 的次数（超过上限则强制收敛）
	retrievalResult := knowledge.RetrievalResult{}
	finalAnswer := ""

	// —— 知识库 RAG 上下文注入 ——
	// 两条路径：
	//  1) 上层已完成检索（input.RetrievalResult 非空）：直接复用结果注入，避免重复检索，
	//     多模型对比时也能保证各模型看到同一份召回；
	//  2) 指定了知识库 ID 且已配置知识库存储：以用户消息为 query 检索 TopK=8 的片段。
	// 召回片段会被渲染成一条 user 消息，插入到最后一条用户消息之前（见 insertKnowledgeContext）；
	// 检索失败不阻断回合，仅发送错误事件并降级为无 RAG 运行。
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

	// —— Skill 渐进加载（第一阶段：基于用户消息的预加载）——
	// 若用户显式用 /skillName 调用、或消息命中某 skill 的触发词/轻量匹配器，
	// 则在进入模型循环前就把该 skill 的完整提示包（元数据、README、指令、示例、资源）注入上下文。
	// explicit 标记区分“用户显式指定”与“启发式命中”，注入文案中会说明选择方式。
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

	// —— Agent 主循环 ——
	// 每轮 = 一次模型调用 + （可选的）一批并行工具执行。循环出口有三种：
	//  a) 模型直接给出文本回答：finalAnswer 非空后 break；
	//  b) 模型请求加载 skill：注入 skill 全文后 continue 进入下一轮（有次数与重复护栏）；
	//  c) 达到 maxRounds 上限：跳出循环后走 recoverFinalAnswer 兜底。
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
		// 每轮调用模型前先做上下文压缩：截断历史中过长的工具输出，
		// 并在消息过多时把旧消息折叠为摘要（已加载的 skill 提示包会被限额保留）。
		compressModelMessages(&modelMessages, loadedSkillDetails)
		// system prompt 每轮重建（包含启动上下文、工具清单、MCP、skill 元数据列表与协议说明），
		// 同时把可用工具的 JSON schema 传给模型以启用原生 function calling。
		resp, err := a.chatModel(ctx, modelClient, model.Input{
			System:   systemPrompt(availableTools, enabledSkills, a.startup),
			Messages: modelMessages,
			Tools:    runtime.ToolSchemasFor(availableTools),
		}, emit, trace.ID, round)
		completedAt := shared.Now()
		if err != nil {
			// 模型调用失败：把错误与当前累积状态（含上下文快照）完整写入 trace 便于事后排查，
			// 再上报错误事件并终止本回合。
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
			// 模型没有发起工具调用：要么是最终文本回答，要么是在用文本协议请求加载 skill。
			// —— Skill 渐进加载（第二阶段：模型主动请求）——
			// 模型可在回复中输出 <load_skill name="x">原因</load_skill> 或 "LOAD_SKILL: x 原因"，
			// 表示 system prompt 里的 skill 元数据不足以完成任务，需要完整 skill 包。
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
				// 防死循环护栏 1：模型请求 skill 次数超限，强制切换到直接回答（恢复流程）。
				if modelRequestedSkillLoads > maxModelRequestedSkillLoads {
					finalAnswer = a.recoverFinalAnswer(ctx, modelClient, modelMessages, input.UserMessage, emit, trace.ID, round, "模型连续请求加载 skill，已切换为直接回答。")
					break
				}
				// 防死循环护栏 2：同一 skill 已加载过仍重复请求，说明模型陷入循环，同样强制收敛。
				if _, loaded := loadedSkills[request.Name]; loaded {
					finalAnswer = a.recoverFinalAnswer(ctx, modelClient, modelMessages, input.UserMessage, emit, trace.ID, round, "Skill /"+request.Name+" 已经加载，模型仍重复请求；请直接回答用户。")
					break
				}
				// 校验 skill 是否存在、已启用且允许模型调用；不可用时也走恢复流程直接作答。
				detail, ok := a.loadSkillInstructionsForModel(request, emit, trace.ID)
				if !ok {
					finalAnswer = a.recoverFinalAnswer(ctx, modelClient, modelMessages, input.UserMessage, emit, trace.ID, round, "请求的 skill /"+request.Name+" 不可用或已禁用；请基于当前上下文直接回答用户。")
					break
				}
				// 加载成功：记录到已加载集合，把完整 skill 包作为一条 user 消息注入，
				// 然后进入下一轮，让模型带着新的 skill 上下文继续处理。
				loadedSkills[detail.Name] = struct{}{}
				loadedSkillDetails = appendUniqueSkillDetail(loadedSkillDetails, detail)
				modelMessages = appendSkillInstructionsContext(modelMessages, detail, request.Reason, input.UserMessage)
				continue
			}
			// 普通文本回答：作为最终答案结束主循环。
			// resp.Content 为空时回退到 resp.Message.Content（兼容不同模型客户端的字段填充方式）。
			modelMessages = append(modelMessages, resp.Message)
			finalAnswer = resp.Content
			if strings.TrimSpace(finalAnswer) == "" {
				finalAnswer = resp.Message.Content
			}
			break
		}

		// —— 模型发起了工具调用 ——
		// 先把带 tool_calls 的 assistant 消息追加进历史（模型协议要求 tool 结果消息必须跟在其后），
		// 再经 runtime.ResolveToolCall 把模型给出的工具名/参数解析、规范化为可执行的 ToolCall。
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
		// 工具调用先记入工作记忆，再并行执行所有工具，最后把结果摘要也写入工作记忆。
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

		// 把每个工具结果以 role=tool 的消息回填进模型历史，并按索引对应回 tool_call_id，
		// 使模型能把结果与自己发起的调用一一对应（execToolsParallel 保证结果按调用顺序写回）。
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

		// 每轮结束即增量更新 trace 并落库，保证运行中断时也能看到已完成轮次的完整轨迹。
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

	// —— 兜底恢复 ——
	// 主循环耗尽（模型一直在调工具/请求 skill）仍没有文本回答时，
	// 追加一条强约束的用户指令、换用“最终回答”system prompt 再调一次模型；
	// 若恢复调用也失败，则退化为固定的错误提示文案，保证用户总能收到回复。
	if strings.TrimSpace(finalAnswer) == "" {
		finalAnswer = a.recoverFinalAnswer(ctx, modelClient, modelMessages, input.UserMessage, emit, trace.ID, a.maxRounds+1, "已达到最大工具/skill 轮数；请停止请求工具或 skill，直接给出最终回答。")
		if strings.TrimSpace(finalAnswer) == "" {
			finalAnswer = "模型连续请求工具或 skill，未能生成最终回答。请简化问题，或切换到更稳定支持工具调用的模型后重试。"
		}
		modelMessages = append(modelMessages, contracts.LLMMessage{Role: contracts.RoleAssistant, Content: finalAnswer})
	}

	// —— 收尾阶段 ——
	// 构建完整上下文快照（system prompt 原文、各类上下文来源、工具结果等），
	// 供前端“上下文透视”功能展示模型本回合实际看到的全部信息。
	contextSnapshot := buildContextSnapshot(input.ConversationID, turnID, input.UserMessage, shortMemory, workingMemory, recent, allToolResults, availableTools, enabledSkills, retrievalResult, a.startup)
	completedAt := shared.Now()

	assistantMessageID := strings.TrimSpace(input.AssistantMessageID)
	if assistantMessageID == "" {
		assistantMessageID = shared.NewID("msg")
	}
	// 助手消息 metadata 携带本回合的工具结果、循环轮数、已加载 skill 与 RAG 召回，
	// 便于前端还原执行过程；上层传入的 AssistantMetadata 可覆盖同名键。
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
	// 用本轮问答与工具结果滚动更新短期记忆（跨回合摘要），并把工作记忆标记为收尾状态。
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

// emitAgentEvent 是事件推送的统一入口：emit 为 nil（非流式模式）时静默跳过，
// 并为未设置时间戳的事件补上当前时间。返回 emit 的错误，供调用方据此中止运行。
func emitAgentEvent(emit AgentEventHandler, event contracts.AgentStreamEvent) error {
	if emit == nil {
		return nil
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = shared.Now()
	}
	return emit(event)
}

// chatModel 封装一次模型调用，自动在流式与阻塞式之间选择：
// 若客户端实现了 model.StreamingClient 且处于流式模式（emit 非 nil），
// 则走 ChatStream，并把内容增量转发为 answer_delta 事件（前端可实时渲染打字机效果）；
// 否则退化为普通阻塞式 Chat。round/traceID 仅用于事件标注，不影响调用行为。
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

// recoverFinalAnswer 是“最终回答恢复”流程：当模型陷入工具/skill 请求循环、
// 或达到轮数上限仍未产出文本回答时被调用。做法是：
//  1. 复制现有消息历史（避免污染调用方的 modelMessages）；
//  2. 末尾追加一条带 reason 说明的强约束用户指令（禁止工具、禁止 skill、禁止协议片段），
//     并重申原始用户请求；
//  3. 换用精简的 finalAnswerSystemPrompt（不再暴露工具与 skill 清单，从源头消除诱惑）
//     再调一次模型，尽力得到一个自然语言回答。
//
// 失败或结果为空时返回空串，由调用方决定兜底文案。
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

// streamModelOutput 为 model_message 事件挑选可展示的内容：
// 优先展示模型文本；无文本时展示工具调用的紧凑 JSON 摘要（截断 2400 字符）；
// 都没有则返回占位提示，避免前端出现空白气泡。
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

// streamToolResults 把一轮工具结果渲染成人类可读的多行列表用于 tool_results 事件：
// 成功的输出紧凑 JSON 摘要，失败的输出错误信息，均截断到 1000 字符。
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

// execToolsParallel 并行执行一轮内模型发起的所有工具调用。
// 每个调用起一个 goroutine，结果按原始索引写回 results 切片，
// 保证结果顺序与调用顺序一致（回填 tool_call_id 时依赖该顺序）。
// 执行前先用 allowedToolIDs 白名单校验，拦截未启用/不存在的工具（模型可能幻觉工具名），
// 拦截时以失败结果返回而不是中断整轮。实际执行委托给 runtime.Invoke，
// 并携带会话 ID、存储与模型客户端（部分工具如摘要/子代理类需要反向调用模型）。
func (a *Agent) execToolsParallel(ctx context.Context, conversationID string, calls []contracts.ToolCall, allowedToolIDs map[string]struct{}, modelClient model.Client) []contracts.ToolResult {
	results := make([]contracts.ToolResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(index int, tc contracts.ToolCall) {
			defer wg.Done()
			// 白名单校验：模型请求了本次运行未启用的工具时直接返回失败结果。
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

// buildContextSnapshot 汇总本回合模型“看到的一切”，生成用于审计与前端展示的上下文快照：
// 短期记忆、工作记忆、近期对话、启动上下文（CLAUDE.md/规则/自动记忆/MCP）、
// RAG 召回片段与工具结果，同时保存当轮实际使用的 system prompt 原文，
// 让用户可以精确回溯“模型为什么这样回答”。
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

// startupContextSources 把启动上下文拆解为快照中的独立来源条目：
// CLAUDE.md 指令、无路径限定的全局规则（路径限定规则只在命中路径时生效，不进全局快照）、
// 自动记忆文件以及 MCP 服务配置，方便前端按来源分类展示。
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
		// 带 Paths 的规则是路径限定规则，仅对特定文件路径生效，不作为全局上下文来源。
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

// toolIDSet 把工具列表转成 ID 集合，供执行阶段以 O(1) 校验工具是否在白名单内。
func toolIDSet(tools []contracts.RuntimeTool) map[string]struct{} {
	allowed := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		allowed[tool.ID] = struct{}{}
	}
	return allowed
}

// enabledSkillManifests 返回“已启用且未禁止模型调用”的 skill 元数据清单。
// 该清单只含名称/用途/触发词等轻量元数据，进入 system prompt 的 skill 列表
// （完整正文按渐进加载协议按需注入）；skill 体系未配置时返回 nil。
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

// appendUniqueSkillDetail 按名称（忽略大小写）去重地追加 skill 详情，
// 保证上下文压缩时保留的 skill 提示包不会重复注入。
func appendUniqueSkillDetail(current []skills.Detail, next skills.Detail) []skills.Detail {
	for _, item := range current {
		if strings.EqualFold(item.Name, next.Name) {
			return current
		}
	}
	return append(current, next)
}

// maybeLoadSkill 实现 skill 渐进加载的第一阶段：进入模型循环前，
// 用 skill 注册表对用户消息做解析——命中显式的 /skillName 调用（explicit=true）
// 或触发词/轻量启发式匹配（explicit=false）。命中时返回该 skill 的完整详情，
// 并发送 skill_loaded 事件供前端展示。
// 返回值依次为：skill 详情、是否用户显式指定、是否命中、事件推送错误。
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

// loadSkillInstructionsForModel 处理主循环中模型主动发出的 skill 加载请求（渐进加载第二阶段）。
// 依次校验三个条件：skill 存在于注册表、在配置中处于启用状态、未标记 DisableModelInvocation；
// 任一不满足即返回 false，由调用方转入最终回答恢复流程。
// 校验通过时发送 skill_loaded 事件（附模型给出的请求原因）并返回完整详情。
func (a *Agent) loadSkillInstructionsForModel(request modelSkillRequest, emit AgentEventHandler, traceID string) (skills.Detail, bool) {
	if a.skillReg == nil || a.skillConfig == nil {
		return skills.Detail{}, false
	}
	skill, ok := a.skillReg.Get(request.Name)
	if !ok {
		return skills.Detail{}, false
	}
	detail := skill.Detail()
	// 配置里的键是小写、不带 / 前缀的 skill 名，这里做同样的规范化后再查启用状态。
	skillKey := strings.ToLower(strings.TrimPrefix(detail.Name, "/"))
	if !a.skillConfig.EnabledMap()[skillKey] {
		return skills.Detail{}, false
	}
	// 标记了 DisableModelInvocation 的 skill 只允许用户显式调用，模型请求一律拒绝。
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

// appendSkillContext 把预加载阶段（第一阶段）命中的 skill 完整提示包包装成一条 user 消息
// 追加到模型历史。文案中标注选择方式（slash=用户显式指定 / heuristic=启发式命中）、
// 重申原始用户请求，并明确告知模型：skill 只是提示上下文，不是可执行工具，
// 防止模型把 skill 幻觉成工具调用。
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

// appendSkillInstructionsContext 与 appendSkillContext 类似，但用于模型主动请求加载的场景
// （第二阶段）：额外记录模型给出的请求原因，并再次强调 skill 没有执行方法、不是工具。
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

// renderSkillPackage 把 skill 详情渲染成完整的 Markdown 提示包，包含：
// 元数据（名称/来源/用途/触发词/工具白名单与黑名单/模型偏好/推理强度等）、
// README 全文、描述、指令正文（SKILL.md 主体）、对话示例与附属资源。
// 这就是渐进加载时真正注入模型上下文的“skill 全文”。
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

// renderExamples 把 skill 的对话示例渲染成 Markdown 小节（用户/助手成对展示），
// 帮助模型模仿 skill 期望的问答风格；无示例时输出占位文案。
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

// renderResources 把 skill 的附属资源（脚本、参考文档等）渲染成 Markdown 小节，
// 包含名称、类型、URI 与内联内容；无资源时输出占位文案。
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

// emptyFallback 返回 value（若去除空白后非空），否则返回 fallback 占位文案。
func emptyFallback(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// renderLoadedSkill 渲染 skill_loaded 事件（预加载阶段）给用户看的摘要：
// 标注加载模式（用户显式指定 / 启发式匹配），提示包内容截断到 2200 字符，
// 与注入模型的完整版相互独立。
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

// renderLoadedSkillForModel 渲染 skill_loaded 事件（模型主动请求阶段）给用户看的摘要，
// 额外展示模型给出的请求原因；提示包内容同样截断到 2200 字符。
func renderLoadedSkillForModel(detail skills.Detail, reason string) string {
	return strings.Join([]string{
		"Skill: /" + detail.Name,
		"模式：模型请求更多 skill 操作信息",
		"原因：" + strings.TrimSpace(reason),
		"已加载的提示包：",
		shared.TrimRunes(renderSkillPackage(detail), 2200),
	}, "\n")
}

// parseModelSkillRequest 从模型的纯文本回复中解析 skill 加载请求，支持两种协议：
//  1. XML 风格：<load_skill name="xxx">原因</load_skill>（不区分大小写、允许跨行、引号可省略）；
//  2. 行前缀风格：LOAD_SKILL: xxx 原因（容忍前导空白与可选的 / 前缀）。
//
// 解析出的名称经 normalizeRequestedSkill 规范化；两种格式均不匹配时返回 false，
// 调用方据此把该回复当作普通最终回答处理。
func parseModelSkillRequest(content string) (modelSkillRequest, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return modelSkillRequest{}, false
	}
	// 优先匹配 XML 风格：捕获组 1 为 skill 名，捕获组 2 为标签体（模型给出的加载理由）。
	if match := regexp.MustCompile(`(?is)<load_skill\s+name=["']?([A-Za-z0-9_:-]+)["']?[^>]*>(.*?)</load_skill>`).FindStringSubmatch(content); len(match) >= 3 {
		name := normalizeRequestedSkill(match[1])
		if name == "" {
			return modelSkillRequest{}, false
		}
		return modelSkillRequest{Name: name, Reason: strings.TrimSpace(match[2])}, true
	}
	// 退回匹配行前缀风格：某一行以 LOAD_SKILL: 开头，冒号后为 skill 名，行内剩余部分作为理由。
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

// normalizeRequestedSkill 规范化模型给出的 skill 名称，使其与注册表/配置中的键一致：
// 去除前导空白与 "/" 前缀、去除 "skill:" 前缀，并统一转为小写。
func normalizeRequestedSkill(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "/"))
	value = strings.TrimPrefix(value, "skill:")
	return strings.ToLower(value)
}

// renderShortMemory 把短期记忆（跨回合摘要 + 当前任务 + 近期事实）渲染成多行文本，
// 用于注入模型上下文（对话摘要）与上下文快照展示。仅拼接非空字段以保持简洁。
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

// renderWorkingMemory 把工作记忆（本回合的意图、当前任务及状态、约束、
// 工具结果摘要、临时记录）渲染成多行文本，用于 turn_start/tool_results 等事件
// 与上下文快照的可读展示。同样只拼接非空字段。
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

// renderMessages 把一组会话消息渲染成 "role: content" 的多行文本，
// 用于上下文快照中“近期对话”来源的可读展示。
func renderMessages(messages []contracts.Message) string {
	lines := []string{}
	for _, message := range messages {
		lines = append(lines, string(message.Role)+": "+message.Content)
	}
	return strings.Join(lines, "\n")
}

// finalTaskStatus 根据本回合所有工具结果推断收尾时写入工作记忆的任务状态：
//   - 任一工具因“需要用户确认”被阻断 -> waiting_for_confirmation（优先级最高）；
//   - 任一工具失败且带错误信息 -> blocked；
//   - 否则视为 in_progress（本回合已推进，但不武断标记为已完成，留待后续回合判断）。
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

// buildInitialModelMessages 构造进入主循环前的初始模型消息序列，顺序为：
//  1. 若短期记忆摘要有效（非默认占位），先以一对 user/assistant 消息把它作为
//     “已压缩的历史上下文”注入——用 assistant 的确认回复形成合法对话轮次，
//     让模型把摘要当作既定背景而非当前提问；
//  2. 依次追加近期对话中的 user/assistant 消息（过滤掉 tool 等其他角色）；
//  3. 最后追加本次的用户消息。
//
// 该序列之后还会被知识库注入与 skill 注入进一步改写。
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

// hasUsefulShortMemory 判断短期记忆摘要是否携带真实信息：
// 排除空串以及新旧两版默认占位摘要，避免把“尚无历史”的占位文案当上下文注入模型。
func hasUsefulShortMemory(summary string) bool {
	trimmed := strings.TrimSpace(summary)
	return trimmed != "" && trimmed != contracts.DefaultShortMemorySummary && trimmed != contracts.LegacyDefaultShortMemorySummary
}

// insertKnowledgeContext 把 RAG 召回片段渲染成一条 user 消息，
// 插入到消息序列中“最后一条 user 消息之前”，使召回上下文紧邻当前提问、
// 又不打断已有的对话轮次顺序。
// 无召回结果时原样返回；消息为空时直接返回仅含召回上下文的序列；
// 找不到任何 user 消息时兜底追加到末尾。为避免副作用，先复制原切片再插入。
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
	// 从后往前找到最后一条 user 消息的位置，把召回上下文插入其前面。
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == contracts.RoleUser {
			out = append(out[:i], append([]contracts.LLMMessage{contextMessage}, out[i:]...)...)
			return out
		}
	}
	return append(out, contextMessage)
}

// renderKnowledgePromptContext 把召回结果渲染成注入模型上下文的完整 Markdown：
// 首部给出使用指引（相关则优先参考、不相关则忽略、不得声称来自工具执行，
// 以免模型把 RAG 内容误当作工具结果），随后逐条列出片段的标题、来源、片段 ID、
// 召回依据（向量/关键词等）、标题路径，以及截断到 2600 字符的正文。
// 这是 RAG 上下文真正进入模型的“注入版”，与展示用的 renderKnowledgeContext 不同。
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

// renderKnowledgeContext 渲染 knowledge_retrieved 事件展示给用户看的召回摘要：
// 每条含序号、标题、召回依据与截断到 500 字符的正文预览。
// 与注入模型的 renderKnowledgePromptContext 相互独立（更短、面向人阅读）。
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

// retrievalContextSources 把召回片段转换成上下文快照中的 knowledge 类来源条目，
// 除标题与正文外，还在 Meta 里保留片段/文档/知识库 ID、召回来源与各路打分、原始 URI，
// 供前端做溯源展示与调试召回质量。无召回结果时返回 nil。
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

// compressModelMessages 是每轮模型调用前执行的上下文压缩，就地改写传入的消息切片。
// 策略分两步：
//  1. snipToolOutputs：无条件截断历史中过长的 tool 输出（降低单条体积）；
//  2. 折叠旧消息：仅当消息总数超过 keepRecent+2（保留窗口 + 那对摘要引导消息）时，
//     把靠前的“旧消息”抽取为一段关键信息摘要，只保留最近 keepRecent 条“尾部消息”。
//
// split 计算需要特别处理：折叠点若正好落在 role=tool 的消息上，会把 tool 结果与它对应的
// assistant tool_call 拆散、破坏模型协议要求的配对，因此向前回退 split 直到不落在 tool 上。
// 折叠后的头部由“摘要 user + assistant 确认”这对引导消息组成，并把已加载的 skill 提示包
// （限额）重新拼回头部，确保 skill 上下文不会因压缩而丢失；最后接上尾部窗口。
func compressModelMessages(messages *[]contracts.LLMMessage, loadedSkills []skills.Detail) {
	snipToolOutputs(*messages)
	const keepRecent = 10
	if len(*messages) <= keepRecent+2 {
		return
	}
	split := len(*messages) - keepRecent
	// 避免折叠点落在 tool 消息上，从而把 tool 结果与其前置 assistant 调用拆散。
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
	// 压缩会丢弃旧消息里的 skill 全文，这里把已加载 skill 的提示包重新注入头部以免上下文断裂。
	head = append(head, retainedSkillContextMessages(loadedSkills)...)
	*messages = append(head, tail...)
}

// snipToolOutputs 就地精简历史中过长的 tool 消息，控制上下文体积：
// 仅处理 role=tool 且内容超过 1500 字符的消息；对多行输出保留头 3 行与尾 3 行、
// 中间以省略提示替代（因头尾通常含最关键的状态/结论）；对行数很少的长文本
// 则直接按字符截断到 1500。其他角色消息一律不动。
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

// extractKeyInfo 从被折叠的旧消息里抽取一段轻量“对话摘要”文本：
// 逐条保留 user/assistant 消息（各截断到 220 字符），并特别保留任何含 "error"
// 的消息（无论角色），因为错误信息对后续决策价值高、不应在压缩中丢失。
// 结果最多保留最后 12 行（更靠后的内容更贴近当前上下文），无有效内容时返回占位串。
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

// retainedSkillContextMessages 在上下文压缩后，把已加载的 skill 提示包重新拼成
// 一批 user 消息注入头部，避免 skill 全文被折叠丢失导致模型“忘记”正在使用的 skill。
// 为控制体积设了双重限额：单个 skill 最多 perSkillLimit（20000）字符，
// 所有 skill 合计最多 totalLimit（100000）字符；接近总限额时对当前 skill 再做二次截断，
// 超出总限额则停止注入后续 skill。
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

// toolResultContent 把一个工具结果转成回填给模型的 tool 消息正文：
// 失败时输出 "Error: <错误>"，成功时输出紧凑 JSON（截断到 4000 字符）的工具输出。
func toolResultContent(result contracts.ToolResult) string {
	if !result.OK {
		return "Error: " + result.Error
	}
	return shared.CompactJSON(result.Output, 4000)
}

// systemPrompt 组装主循环每轮使用的完整 system prompt，是模型行为的“宪法”。
// 它由若干区块拼接而成：
//   - 角色与工作方式说明（以 agent loop 方式循环推进直到能可靠回答）；
//   - 启动上下文（renderStartupPrompt：项目根/配置目录、CLAUDE.md 指令、规则、
//     路径限定规则、自动记忆、设置）；
//   - 工具清单（本地运行时工具的名称与描述，供模型决定是否 function calling）；
//   - MCP 服务清单（外部集成，明确与 skill 是两类不同机制）；
//   - Skill 提示包区块：只列出已启用且允许模型调用的 skill 元数据，并说明渐进加载协议
//     （用 <load_skill name="x">原因</load_skill> 请求加载完整 SKILL.md）；
//   - 规则区：强调工具用法、必须等待工具结果、多轮推进、禁止把 skill 当工具等约束。
//
// 该 prompt 每轮重建，因此工具/skill 清单变化能即时反映到模型。
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

// finalAnswerSystemPrompt 是“最终回答恢复”阶段专用的精简 system prompt。
// 与常规 systemPrompt 的关键区别是：不暴露任何工具与 skill 清单，从源头断绝
// 模型继续请求工具/skill 的诱惑，并强约束其直接用自然语言作答；
// 若上下文不足则要求说明缺什么并给出当前结论/下一步。仅附带项目根目录作为最小背景。
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

// renderStartupPrompt 把启动时收集的 Claude Code 上下文渲染成 system prompt 的“启动上下文”区块。
// 依次包含：项目根/配置目录、CLAUDE.md 指令（截断 12000）、无路径限定的全局规则（截断 8000）、
// 路径限定规则（仅列出路径与适用范围，正文不展开，因为它们只在命中路径时才应生效）、
// 自动记忆（截断 12000）与设置（紧凑 JSON，截断 6000）。各区块按来源分节，便于模型区分权重。
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
	// 规则按是否带 Paths 分流：带 Paths 的是路径限定规则（仅对特定文件路径生效，
	// 正文不进全局 prompt，只列路径提示），其余为全局规则（正文完整展开）。
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

// renderMCPPrompt 把 MCP 服务配置渲染成 system prompt 中的服务清单：
// 每行含服务名、作用域、类型与连接目标（command 优先，其次 URL，都缺省时标注已省略）。
// 无配置时输出占位提示。
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

// firstNonEmptyString 返回参数列表中第一个去除空白后非空的字符串（并返回其去空白版本），
// 全为空时返回空串。用于在多个候选字段间择优取值（如 WhenToUse 缺失时回退到 Description）。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
