// Package memory 实现智能体的“会话记忆”子系统，统一管理：
//   - 会话（Conversation）与消息（Message）：对话的持久上下文；
//   - 短期记忆（ShortMemory）：跨轮次的摘要 + 近期事实，随每轮问答滚动更新；
//   - 工作记忆（WorkingMemory）：单轮（turn）内的临时推理状态（意图、进行中任务、
//     约束、计划调用的工具、工具结果摘要、scratchpad）；
//   - 执行轨迹（Trace）：一次智能体运行的完整过程记录，供前端回放与调试。
//
// 包内提供两种实现：
//   - InMemoryStore：纯内存实现，进程重启后数据丢失，适合测试或未配置持久化目录的场景；
//   - PersistentStore（见 persistent.go）：内嵌 InMemoryStore 作为读缓存，
//     写操作同步落盘到 SQLite（write-through），启动时从 SQLite 全量加载回内存。
package memory

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

// Store 是记忆子系统的统一接口抽象。上层（agent/http 层）只依赖该接口，
// 从而可以在 InMemoryStore（纯内存）与 PersistentStore（SQLite 持久化）之间自由切换。
// 接口方法按对象分组：会话管理、消息管理、短期记忆、工作记忆、trace 记录。
type Store interface {
	// CreateConversation 创建一个新会话，title 为空时使用默认标题“新对话”。
	CreateConversation(title string) contracts.Conversation
	// DeleteConversation 删除会话及其关联的消息、短期记忆、工作记忆与 trace；
	// 返回是否真正删除了数据。
	DeleteConversation(conversationID string) bool
	// ListConversations 返回全部会话，按更新时间倒序（最近活跃的排前面）。
	ListConversations() []contracts.Conversation
	// AddMessage 追加一条消息；若会话不存在会自动补建（用于导入场景）。
	AddMessage(message contracts.Message)
	// UpdateMessage 按 ID 原地更新已有消息（例如流式回答完成后回填内容），
	// 消息不存在时返回 false。
	UpdateMessage(message contracts.Message) bool
	// Messages 返回会话内全部消息（按插入顺序）。
	Messages(conversationID string) []contracts.Message
	// RecentMessages 返回会话内最近 limit 条消息，limit<=0 时返回全部。
	RecentMessages(conversationID string, limit int) []contracts.Message
	// GetShortMemory 获取会话的短期记忆；不存在时惰性初始化一份默认记忆。
	GetShortMemory(conversationID string) contracts.ShortMemory
	// UpdateShortMemory 在一轮问答结束后滚动更新短期记忆：
	// 追加“用户提问/助手回答/工具返回”事实、刷新摘要、识别进行中任务。
	UpdateShortMemory(conversationID string, userMessage string, assistantAnswer string, toolResults []contracts.ToolResult) contracts.ShortMemory
	// StartWorkingMemory 在一轮对话开始时创建该 turn 的工作记忆：
	// 推断用户意图、继承/识别进行中任务、合并约束、初始化 scratchpad。
	StartWorkingMemory(conversationID string, turnID string, userMessageID string, userMessage string, shortMemory contracts.ShortMemory, recent []contracts.Message) contracts.WorkingMemory
	// RecordToolCalls 记录模型本轮计划调用的工具（写入 PendingToolCalls 与 scratchpad）。
	RecordToolCalls(conversationID string, turnID string, calls []contracts.ToolCall) contracts.WorkingMemory
	// RecordToolResults 记录工具执行结果：清空待执行列表，生成结果摘要写入 scratchpad。
	RecordToolResults(conversationID string, turnID string, results []contracts.ToolResult) contracts.WorkingMemory
	// CompleteWorkingMemory 在一轮结束时收尾：更新任务状态、把最终回答写入 scratchpad。
	CompleteWorkingMemory(conversationID string, turnID string, assistantAnswer string, status string) contracts.WorkingMemory
	// AddTrace 追加一条智能体运行轨迹（一次 run 的过程记录）。
	AddTrace(trace contracts.Trace)
	// UpdateTrace 按 ID 更新已有 trace（运行过程中不断补充步骤），不存在则追加。
	UpdateTrace(trace contracts.Trace)
	// Traces 返回会话内全部 trace（按写入顺序）。
	Traces(conversationID string) []contracts.Trace
}

// InMemoryStore 是 Store 的纯内存实现。所有数据都保存在以会话 ID / turn ID
// 为键的 map 中，用一把互斥锁保证并发安全（读写量级小，粗粒度锁足够）。
type InMemoryStore struct {
	mu sync.Mutex // 保护下面所有 map 的全局互斥锁
	// conversations：会话 ID -> 会话元信息（标题、创建/更新时间）。
	conversations map[string]contracts.Conversation
	// messages：会话 ID -> 消息列表（按插入顺序，即时间顺序）。
	messages map[string][]contracts.Message
	// shortMemory：会话 ID -> 短期记忆（每个会话仅一份，滚动覆盖）。
	shortMemory map[string]contracts.ShortMemory
	// latestWorkingByConv：会话 ID -> 该会话最近一轮的工作记忆，
	// 用于新一轮开始时继承上一轮未完成的任务与约束。
	latestWorkingByConv map[string]contracts.WorkingMemory
	// workingByTurn：turn ID -> 工作记忆，供同一轮内多次读改写（记录工具调用/结果）。
	workingByTurn map[string]contracts.WorkingMemory
	// traces：会话 ID -> trace 列表（一次智能体运行对应一条 trace）。
	traces map[string][]contracts.Trace
}

// NewInMemoryStore 构造一个空的内存存储，所有 map 均预先初始化以避免 nil map 写入。
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		conversations:       map[string]contracts.Conversation{},
		messages:            map[string][]contracts.Message{},
		shortMemory:         map[string]contracts.ShortMemory{},
		latestWorkingByConv: map[string]contracts.WorkingMemory{},
		workingByTurn:       map[string]contracts.WorkingMemory{},
		traces:              map[string][]contracts.Trace{},
	}
}

// CreateConversation 创建新会话：生成 conv 前缀的唯一 ID，并同时为消息列表、
// trace 列表预建空切片，保证后续读取不会遇到 nil。title 为空时兜底为“新对话”。
func (s *InMemoryStore) CreateConversation(title string) contracts.Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(title) == "" {
		title = "新对话"
	}
	conversation := contracts.Conversation{
		ID:        shared.NewID("conv"),
		Title:     title,
		CreatedAt: shared.Now(),
		UpdatedAt: shared.Now(),
	}
	s.conversations[conversation.ID] = conversation
	s.messages[conversation.ID] = []contracts.Message{}
	s.traces[conversation.ID] = []contracts.Trace{}
	return conversation
}

// DeleteConversation 级联删除会话的全部关联数据（消息、短期记忆、最新工作记忆、trace）。
// workingByTurn 以 turnID 为键，无法直接按会话删除，因此需要遍历筛选归属该会话的条目。
// 会话不存在时返回 false。
func (s *InMemoryStore) DeleteConversation(conversationID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conversations[conversationID]; !ok {
		return false
	}
	delete(s.conversations, conversationID)
	delete(s.messages, conversationID)
	delete(s.shortMemory, conversationID)
	delete(s.latestWorkingByConv, conversationID)
	delete(s.traces, conversationID)
	// 逐个清理属于该会话的按 turn 存储的工作记忆，避免内存泄漏。
	for turnID, memory := range s.workingByTurn {
		if memory.ConversationID == conversationID {
			delete(s.workingByTurn, turnID)
		}
	}
	return true
}

// ListConversations 返回全部会话的快照，按 UpdatedAt 倒序排列，
// 使前端会话列表中最近活跃的会话排在最前。
func (s *InMemoryStore) ListConversations() []contracts.Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]contracts.Conversation, 0, len(s.conversations))
	for _, conversation := range s.conversations {
		items = append(items, conversation)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items
}

// AddMessage 追加一条消息。为什么要自动补建会话：导入历史数据或外部写入时
// 可能先有消息后有会话，这里兜底创建标题为 "Imported conversation" 的会话，
// 保证数据一致性。同时刷新会话 UpdatedAt；若这是会话的第一条用户消息，
// 则截取前 48 个字符作为会话标题（自动命名）。
func (s *InMemoryStore) AddMessage(message contracts.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conversations[message.ConversationID]; !ok {
		created := contracts.Conversation{
			ID:        message.ConversationID,
			Title:     "Imported conversation",
			CreatedAt: shared.Now(),
			UpdatedAt: shared.Now(),
		}
		s.conversations[created.ID] = created
	}
	previousMessages := s.messages[message.ConversationID]
	s.messages[message.ConversationID] = append(previousMessages, message)
	conversation := s.conversations[message.ConversationID]
	conversation.UpdatedAt = shared.Now()
	// 首条用户消息自动成为会话标题，方便前端列表展示。
	if len(previousMessages) == 0 && message.Role == contracts.RoleUser {
		conversation.Title = shared.TrimRunes(message.Content, 48)
	}
	s.conversations[message.ConversationID] = conversation
}

// UpdateMessage 按消息 ID 原地替换消息内容，典型场景是流式回答：
// 先插入一条占位的 assistant 消息，回答完成后再回填最终内容。
// 为保持数据完整性：CreatedAt 为零值时沿用旧值（更新不应改变消息创建时间），
// Metadata 为 nil 时初始化为空 map。找不到目标消息时返回 false。
func (s *InMemoryStore) UpdateMessage(message contracts.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.messages[message.ConversationID]
	for i := range items {
		if items[i].ID != message.ID {
			continue
		}
		if message.CreatedAt.IsZero() {
			message.CreatedAt = items[i].CreatedAt
		}
		if message.Metadata == nil {
			message.Metadata = map[string]any{}
		}
		items[i] = message
		s.messages[message.ConversationID] = items
		// 消息内容变化视为会话活跃，同步刷新会话更新时间。
		conversation := s.conversations[message.ConversationID]
		conversation.UpdatedAt = shared.Now()
		s.conversations[message.ConversationID] = conversation
		return true
	}
	return false
}

// Messages 返回会话内全部消息的副本切片；复制是为了防止调用方
// 在锁外修改内部切片导致数据竞争。
func (s *InMemoryStore) Messages(conversationID string) []contracts.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.Message{}, s.messages[conversationID]...)
}

// RecentMessages 返回最近 limit 条消息（取切片尾部），用于构造模型上下文窗口；
// limit<=0 或消息总数不足时返回全部。
func (s *InMemoryStore) RecentMessages(conversationID string, limit int) []contracts.Message {
	all := s.Messages(conversationID)
	if limit <= 0 || len(all) <= limit {
		return all
	}
	return all[len(all)-limit:]
}

// GetShortMemory 获取会话的短期记忆。采用惰性初始化：首次访问时创建一份
// 带默认摘要（DefaultShortMemorySummary）的空记忆并写回缓存，
// 保证调用方永远能拿到可用的结构体而无需判空。
func (s *InMemoryStore) GetShortMemory(conversationID string) contracts.ShortMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	if memory, ok := s.shortMemory[conversationID]; ok {
		return memory
	}
	memory := contracts.ShortMemory{
		ID:             shared.NewID("mem"),
		ConversationID: conversationID,
		Summary:        contracts.DefaultShortMemorySummary,
		RecentFacts:    []string{},
		UpdatedAt:      shared.Now(),
	}
	s.shortMemory[conversationID] = memory
	return memory
}

// UpdateShortMemory 在一轮问答结束后滚动更新短期记忆，是短期记忆的核心写入口。
// 更新策略（全部基于启发式规则，不调用模型，保证低成本与确定性）：
//  1. RecentFacts：追加“用户提问 / 助手回答 / 成功的工具返回”三类事实，
//     每条截断到固定长度，整体只保留最近 8 条（滑动窗口，防止无限增长）；
//  2. Summary：在旧摘要（若非默认占位文案）后追加“最新一轮”概述，
//     整体截断到 1400 字符，形成逐轮累积但有上限的会话摘要；
//  3. ActiveTask：若本轮用户消息看起来是发起新任务（startsTask 命中动词关键词），
//     则更新进行中任务为该消息，否则沿用之前的任务。
func (s *InMemoryStore) UpdateShortMemory(conversationID string, userMessage string, assistantAnswer string, toolResults []contracts.ToolResult) contracts.ShortMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.shortMemory[conversationID]
	if !ok {
		// 首轮对话可能还没有短期记忆，这里构造一份默认值作为基线。
		previous = contracts.ShortMemory{
			ID:             shared.NewID("mem"),
			ConversationID: conversationID,
			Summary:        contracts.DefaultShortMemorySummary,
			RecentFacts:    []string{},
		}
	}
	// 复制旧事实列表（避免共享底层数组），再追加本轮产生的新事实。
	facts := append([]string{}, previous.RecentFacts...)
	facts = append(facts, "用户提问："+shared.TrimRunes(userMessage, 160))
	facts = append(facts, "助手回答："+shared.TrimRunes(assistantAnswer, 180))
	for _, result := range toolResults {
		// 只记录成功的工具结果；失败信息由 trace/工作记忆负责，短期记忆聚焦有效事实。
		if result.OK {
			facts = append(facts, fmt.Sprintf("工具 %s 返回：%s。", result.ToolID, shared.CompactJSON(result.Output, 180)))
		}
	}
	// 滑动窗口：只保留最近 8 条事实，控制注入提示词的体积。
	if len(facts) > 8 {
		facts = facts[len(facts)-8:]
	}

	summaryParts := []string{}
	// 旧摘要若还是默认/历史版本的占位文案则丢弃，避免无意义文本累积。
	if previous.Summary != "" && previous.Summary != contracts.DefaultShortMemorySummary && previous.Summary != contracts.LegacyDefaultShortMemorySummary {
		summaryParts = append(summaryParts, previous.Summary)
	}
	summaryParts = append(summaryParts, fmt.Sprintf("最新一轮：用户问 %q；助手回答 %q。", shared.TrimRunes(userMessage, 120), shared.TrimRunes(assistantAnswer, 140)))
	activeTask := previous.ActiveTask
	// 用户消息命中任务性关键词（帮我/生成/创建…）时，视为开启了新任务。
	if startsTask(userMessage) {
		activeTask = shared.TrimRunes(userMessage, 180)
	}
	updated := contracts.ShortMemory{
		ID:             previous.ID, // 记忆 ID 稳定不变，只更新内容
		ConversationID: conversationID,
		Summary:        shared.TrimRunes(strings.Join(summaryParts, "\n"), 1400),
		RecentFacts:    facts,
		ActiveTask:     activeTask,
		UpdatedAt:      shared.Now(),
	}
	s.shortMemory[conversationID] = updated
	return updated
}

// StartWorkingMemory 在一轮对话（turn）开始时构建该轮的工作记忆，
// 是智能体“思考前准备上下文”的步骤：
//   - Intent：用关键词启发式推断本轮意图（use_tool / task / chat）；
//   - ActiveTask：结合短期记忆里的任务与上一轮工作记忆里未完成的任务做继承判断；
//   - Constraints：继承上一轮约束并合并本轮消息中新提取的约束（去重、最多 8 条）；
//   - Scratchpad：初始化为意图、可用历史轮数、当前任务三行，后续步骤不断追加。
func (s *InMemoryStore) StartWorkingMemory(conversationID string, turnID string, userMessageID string, userMessage string, shortMemory contracts.ShortMemory, recent []contracts.Message) contracts.WorkingMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 取该会话上一轮的工作记忆，用于跨轮继承任务与约束。
	previous, hasPrevious := s.latestWorkingByConv[conversationID]
	intent := inferIntent(userMessage)
	activeTask := inferActiveTaskForWorking(userMessage, shortMemory.ActiveTask, previous, hasPrevious)
	constraints := []string{}
	if hasPrevious {
		// 约束（如“必须用中文”“不要修改代码”）通常跨轮有效，因此从上一轮继承。
		constraints = append(constraints, previous.Constraints...)
	}
	constraints = mergeUnique(append(constraints, extractConstraints(userMessage)...))
	// 与事实列表相同的滑动窗口策略：只保留最近 8 条约束。
	if len(constraints) > 8 {
		constraints = constraints[len(constraints)-8:]
	}
	scratchpad := []string{
		"Intent: " + intent,
		fmt.Sprintf("Recent turns available: %d", len(recent)),
		"Active task: none",
	}
	if activeTask != nil {
		scratchpad[2] = "Active task: " + activeTask.Goal
	}
	memory := contracts.WorkingMemory{
		ID:             shared.NewID("wm"),
		ConversationID: conversationID,
		TurnID:         turnID,
		UserMessageID:  userMessageID,
		Intent:         intent,
		ActiveTask:     activeTask,
		Constraints:    constraints,
		Scratchpad:     scratchpad,
		UpdatedAt:      shared.Now(),
	}
	s.saveWorking(memory)
	return memory
}

// RecordToolCalls 记录模型在本轮计划调用的工具列表：写入 PendingToolCalls
// （表示“已计划、未执行”），并把工具 ID 列表追加到 scratchpad 供回放观察。
// scratchpad 使用 appendBoundedString 限制在 12 行以内。
func (s *InMemoryStore) RecordToolCalls(conversationID string, turnID string, calls []contracts.ToolCall) contracts.WorkingMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	memory := s.workingByTurn[turnID]
	memory.PendingToolCalls = calls
	if len(calls) == 0 {
		memory.Scratchpad = appendBoundedString(memory.Scratchpad, "Planned tools: none", 12)
	} else {
		ids := make([]string, 0, len(calls))
		for _, call := range calls {
			ids = append(ids, call.ToolID)
		}
		memory.Scratchpad = appendBoundedString(memory.Scratchpad, "Planned tools: "+strings.Join(ids, ", "), 12)
	}
	memory.UpdatedAt = shared.Now()
	s.saveWorking(memory)
	return memory
}

// RecordToolResults 记录工具执行结果：清空 PendingToolCalls（计划的调用已完成），
// 为每个结果生成一行摘要（成功记录压缩后的输出 JSON，失败记录错误信息），
// 同时写入 ToolResultSummaries 与 scratchpad，供模型在下一步推理时引用。
func (s *InMemoryStore) RecordToolResults(conversationID string, turnID string, results []contracts.ToolResult) contracts.WorkingMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	memory := s.workingByTurn[turnID]
	memory.PendingToolCalls = []contracts.ToolCall{}
	summaries := make([]string, 0, len(results))
	for _, result := range results {
		if result.OK {
			// 成功：把输出压缩成不超过 180 字符的紧凑 JSON，避免撑爆上下文。
			summaries = append(summaries, fmt.Sprintf("Tool %s returned %s.", result.ToolID, shared.CompactJSON(result.Output, 180)))
		} else {
			// 失败：保留截断后的错误消息，便于模型决定是否重试或换策略。
			summaries = append(summaries, fmt.Sprintf("Tool %s failed: %s", result.ToolID, shared.TrimRunes(result.Error, 180)))
		}
	}
	memory.ToolResultSummaries = summaries
	memory.Scratchpad = appendBoundedStrings(memory.Scratchpad, summaries, 12)
	memory.UpdatedAt = shared.Now()
	s.saveWorking(memory)
	return memory
}

// CompleteWorkingMemory 在一轮结束时收尾工作记忆：把任务状态更新为调用方
// 给定的 status（如 completed / in_progress），并把最终回答（截断到 220 字符）
// 追加到 scratchpad。任务状态会影响下一轮 StartWorkingMemory 的任务继承判断。
func (s *InMemoryStore) CompleteWorkingMemory(conversationID string, turnID string, assistantAnswer string, status string) contracts.WorkingMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	memory := s.workingByTurn[turnID]
	if memory.ActiveTask != nil {
		memory.ActiveTask.Status = status
	}
	memory.Scratchpad = appendBoundedString(memory.Scratchpad, "Assistant answer: "+shared.TrimRunes(assistantAnswer, 220), 12)
	memory.UpdatedAt = shared.Now()
	s.saveWorking(memory)
	return memory
}

// AddTrace 追加一条 trace。一次智能体运行（run）在开始时创建 trace，
// 之后运行过程中通过 UpdateTrace 不断补充步骤。
func (s *InMemoryStore) AddTrace(trace contracts.Trace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces[trace.ConversationID] = append(s.traces[trace.ConversationID], trace)
}

// UpdateTrace 按 trace ID 就地更新；若找不到（例如进程重启后只剩持久化数据、
// 或调用顺序异常），退化为追加，保证 trace 不丢失。
func (s *InMemoryStore) UpdateTrace(trace contracts.Trace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.traces[trace.ConversationID]
	for i := range items {
		if items[i].ID == trace.ID {
			items[i] = trace
			s.traces[trace.ConversationID] = items
			return
		}
	}
	// 未找到同 ID trace：兜底追加，而不是静默丢弃。
	s.traces[trace.ConversationID] = append(items, trace)
}

// Traces 返回会话内全部 trace 的副本切片（复制原因同 Messages：防止外部修改内部状态）。
func (s *InMemoryStore) Traces(conversationID string) []contracts.Trace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.Trace{}, s.traces[conversationID]...)
}

// saveWorking 把工作记忆同时写入两个索引：按 turn 的索引（同轮内读改写）
// 与按会话的“最新一轮”索引（下一轮继承任务/约束时使用）。调用方必须已持有 s.mu。
func (s *InMemoryStore) saveWorking(memory contracts.WorkingMemory) {
	s.workingByTurn[memory.TurnID] = memory
	s.latestWorkingByConv[memory.ConversationID] = memory
}

// inferIntent 用关键词启发式推断用户消息的意图：
// 提到“工具/tool/skill”判为 use_tool，命中任务动词判为 task，否则视为普通聊天 chat。
// 采用规则而非模型判断，是为了零成本、零延迟且结果可预期。
func inferIntent(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(message, "工具") || strings.Contains(lower, "tool") || strings.Contains(lower, "skill") {
		return "use_tool"
	}
	if startsTask(message) {
		return "task"
	}
	return "chat"
}

// inferActiveTaskForWorking 决定本轮工作记忆中的“进行中任务”，优先级为：
//  1. 继承来源：上一轮工作记忆里未完成（status != completed）的任务优先于短期记忆里的任务；
//  2. 若消息是追问（isFollowUp）且存在可继承任务 → 延续该任务；
//  3. 若消息本身发起新任务（startsTask）→ 以本条消息为新任务目标；
//  4. 否则若仍有可继承任务 → 继续延续；都没有则返回 nil（本轮无任务）。
func inferActiveTaskForWorking(message string, shortTask string, previous contracts.WorkingMemory, hasPrevious bool) *contracts.ActiveTask {
	inherited := shortTask
	if hasPrevious && previous.ActiveTask != nil && previous.ActiveTask.Status != "completed" {
		inherited = previous.ActiveTask.Goal
	}
	if inherited != "" && isFollowUp(message) {
		return &contracts.ActiveTask{Goal: inherited, Status: "in_progress"}
	}
	if startsTask(message) {
		return &contracts.ActiveTask{Goal: shared.TrimRunes(message, 180), Status: "in_progress"}
	}
	if inherited != "" {
		return &contracts.ActiveTask{Goal: inherited, Status: "in_progress"}
	}
	return nil
}

// startsTask 判断消息是否在发起一个任务：命中中英文常见“任务动词”
// （帮我/请/生成/创建/实现/分析/build/create）即认为是任务性请求。
func startsTask(message string) bool {
	return strings.Contains(message, "帮我") ||
		strings.Contains(message, "请") ||
		strings.Contains(message, "生成") ||
		strings.Contains(message, "创建") ||
		strings.Contains(message, "实现") ||
		strings.Contains(message, "分析") ||
		strings.Contains(strings.ToLower(message), "build") ||
		strings.Contains(strings.ToLower(message), "create")
}

// isFollowUp 判断消息是否是对上文的追问/延续：命中指代词（刚才/上面/这个）、
// 延续词（继续）或疑问标记（是什么/?/？）即视为追问，用于任务继承判断。
func isFollowUp(message string) bool {
	return strings.Contains(message, "刚才") ||
		strings.Contains(message, "上面") ||
		strings.Contains(message, "继续") ||
		strings.Contains(message, "这个") ||
		strings.Contains(message, "是什么") ||
		strings.Contains(message, "?") ||
		strings.Contains(message, "？")
}

// extractConstraints 从用户消息中提取约束：只要出现约束性措辞
// （必须/需要/不要/禁止/只），就把整条消息（截断 160 字符）作为一条约束记录，
// 供后续每一轮推理时提示模型遵守。
func extractConstraints(message string) []string {
	if strings.Contains(message, "必须") ||
		strings.Contains(message, "需要") ||
		strings.Contains(message, "不要") ||
		strings.Contains(message, "禁止") ||
		strings.Contains(message, "只") {
		return []string{shared.TrimRunes(message, 160)}
	}
	return nil
}

// mergeUnique 去重合并字符串列表：跳过空白项，保留首次出现的顺序。
// 用于约束合并，避免同一约束跨轮反复累积。
func mergeUnique(items []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

// appendBoundedString 是 appendBoundedStrings 的单元素便捷封装。
func appendBoundedString(current []string, next string, limit int) []string {
	return appendBoundedStrings(current, []string{next}, limit)
}

// appendBoundedStrings 追加若干条目并保持“最多 limit 条”的滑动窗口
// （超限时丢弃最旧的条目）。scratchpad、事实列表等都靠它防止无限增长。
func appendBoundedStrings(current []string, next []string, limit int) []string {
	current = append(current, next...)
	if len(current) > limit {
		return current[len(current)-limit:]
	}
	return current
}
