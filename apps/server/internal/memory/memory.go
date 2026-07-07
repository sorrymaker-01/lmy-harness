package memory

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"code.byted.org/ai/lmy/apps/server/internal/contracts"
	"code.byted.org/ai/lmy/apps/server/internal/shared"
)

type Store interface {
	CreateConversation(title string) contracts.Conversation
	DeleteConversation(conversationID string) bool
	ListConversations() []contracts.Conversation
	AddMessage(message contracts.Message)
	UpdateMessage(message contracts.Message) bool
	Messages(conversationID string) []contracts.Message
	RecentMessages(conversationID string, limit int) []contracts.Message
	GetShortMemory(conversationID string) contracts.ShortMemory
	UpdateShortMemory(conversationID string, userMessage string, assistantAnswer string, toolResults []contracts.ToolResult) contracts.ShortMemory
	StartWorkingMemory(conversationID string, turnID string, userMessageID string, userMessage string, shortMemory contracts.ShortMemory, recent []contracts.Message) contracts.WorkingMemory
	RecordToolCalls(conversationID string, turnID string, calls []contracts.ToolCall) contracts.WorkingMemory
	RecordToolResults(conversationID string, turnID string, results []contracts.ToolResult) contracts.WorkingMemory
	CompleteWorkingMemory(conversationID string, turnID string, assistantAnswer string, status string) contracts.WorkingMemory
	AddTrace(trace contracts.Trace)
	UpdateTrace(trace contracts.Trace)
	Traces(conversationID string) []contracts.Trace
}

type InMemoryStore struct {
	mu                  sync.Mutex
	conversations       map[string]contracts.Conversation
	messages            map[string][]contracts.Message
	shortMemory         map[string]contracts.ShortMemory
	latestWorkingByConv map[string]contracts.WorkingMemory
	workingByTurn       map[string]contracts.WorkingMemory
	traces              map[string][]contracts.Trace
}

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
	for turnID, memory := range s.workingByTurn {
		if memory.ConversationID == conversationID {
			delete(s.workingByTurn, turnID)
		}
	}
	return true
}

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
	if len(previousMessages) == 0 && message.Role == contracts.RoleUser {
		conversation.Title = shared.TrimRunes(message.Content, 48)
	}
	s.conversations[message.ConversationID] = conversation
}

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
		conversation := s.conversations[message.ConversationID]
		conversation.UpdatedAt = shared.Now()
		s.conversations[message.ConversationID] = conversation
		return true
	}
	return false
}

func (s *InMemoryStore) Messages(conversationID string) []contracts.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.Message{}, s.messages[conversationID]...)
}

func (s *InMemoryStore) RecentMessages(conversationID string, limit int) []contracts.Message {
	all := s.Messages(conversationID)
	if limit <= 0 || len(all) <= limit {
		return all
	}
	return all[len(all)-limit:]
}

func (s *InMemoryStore) GetShortMemory(conversationID string) contracts.ShortMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	if memory, ok := s.shortMemory[conversationID]; ok {
		return memory
	}
	memory := contracts.ShortMemory{
		ID:             shared.NewID("mem"),
		ConversationID: conversationID,
		Summary:        "No prior short-term memory.",
		RecentFacts:    []string{},
		UpdatedAt:      shared.Now(),
	}
	s.shortMemory[conversationID] = memory
	return memory
}

func (s *InMemoryStore) UpdateShortMemory(conversationID string, userMessage string, assistantAnswer string, toolResults []contracts.ToolResult) contracts.ShortMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.shortMemory[conversationID]
	if !ok {
		previous = contracts.ShortMemory{
			ID:             shared.NewID("mem"),
			ConversationID: conversationID,
			Summary:        "No prior short-term memory.",
			RecentFacts:    []string{},
		}
	}
	facts := append([]string{}, previous.RecentFacts...)
	facts = append(facts, "User asked: "+shared.TrimRunes(userMessage, 160))
	facts = append(facts, "Assistant answered: "+shared.TrimRunes(assistantAnswer, 180))
	for _, result := range toolResults {
		if result.OK {
			facts = append(facts, fmt.Sprintf("Tool %s returned %s.", result.ToolID, shared.CompactJSON(result.Output, 180)))
		}
	}
	if len(facts) > 8 {
		facts = facts[len(facts)-8:]
	}

	summaryParts := []string{}
	if previous.Summary != "" && previous.Summary != "No prior short-term memory." {
		summaryParts = append(summaryParts, previous.Summary)
	}
	summaryParts = append(summaryParts, fmt.Sprintf("Latest turn: user asked %q; assistant replied %q.", shared.TrimRunes(userMessage, 120), shared.TrimRunes(assistantAnswer, 140)))
	activeTask := previous.ActiveTask
	if startsTask(userMessage) {
		activeTask = shared.TrimRunes(userMessage, 180)
	}
	updated := contracts.ShortMemory{
		ID:             previous.ID,
		ConversationID: conversationID,
		Summary:        shared.TrimRunes(strings.Join(summaryParts, "\n"), 1400),
		RecentFacts:    facts,
		ActiveTask:     activeTask,
		UpdatedAt:      shared.Now(),
	}
	s.shortMemory[conversationID] = updated
	return updated
}

func (s *InMemoryStore) StartWorkingMemory(conversationID string, turnID string, userMessageID string, userMessage string, shortMemory contracts.ShortMemory, recent []contracts.Message) contracts.WorkingMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, hasPrevious := s.latestWorkingByConv[conversationID]
	intent := inferIntent(userMessage)
	activeTask := inferActiveTaskForWorking(userMessage, shortMemory.ActiveTask, previous, hasPrevious)
	constraints := []string{}
	if hasPrevious {
		constraints = append(constraints, previous.Constraints...)
	}
	constraints = mergeUnique(append(constraints, extractConstraints(userMessage)...))
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

func (s *InMemoryStore) RecordToolResults(conversationID string, turnID string, results []contracts.ToolResult) contracts.WorkingMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	memory := s.workingByTurn[turnID]
	memory.PendingToolCalls = []contracts.ToolCall{}
	summaries := make([]string, 0, len(results))
	for _, result := range results {
		if result.OK {
			summaries = append(summaries, fmt.Sprintf("Tool %s returned %s.", result.ToolID, shared.CompactJSON(result.Output, 180)))
		} else {
			summaries = append(summaries, fmt.Sprintf("Tool %s failed: %s", result.ToolID, shared.TrimRunes(result.Error, 180)))
		}
	}
	memory.ToolResultSummaries = summaries
	memory.Scratchpad = appendBoundedStrings(memory.Scratchpad, summaries, 12)
	memory.UpdatedAt = shared.Now()
	s.saveWorking(memory)
	return memory
}

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

func (s *InMemoryStore) AddTrace(trace contracts.Trace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces[trace.ConversationID] = append(s.traces[trace.ConversationID], trace)
}

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
	s.traces[trace.ConversationID] = append(items, trace)
}

func (s *InMemoryStore) Traces(conversationID string) []contracts.Trace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.Trace{}, s.traces[conversationID]...)
}

func (s *InMemoryStore) saveWorking(memory contracts.WorkingMemory) {
	s.workingByTurn[memory.TurnID] = memory
	s.latestWorkingByConv[memory.ConversationID] = memory
}

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

func isFollowUp(message string) bool {
	return strings.Contains(message, "刚才") ||
		strings.Contains(message, "上面") ||
		strings.Contains(message, "继续") ||
		strings.Contains(message, "这个") ||
		strings.Contains(message, "是什么") ||
		strings.Contains(message, "?") ||
		strings.Contains(message, "？")
}

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

func appendBoundedString(current []string, next string, limit int) []string {
	return appendBoundedStrings(current, []string{next}, limit)
}

func appendBoundedStrings(current []string, next []string, limit int) []string {
	current = append(current, next...)
	if len(current) > limit {
		return current[len(current)-limit:]
	}
	return current
}
