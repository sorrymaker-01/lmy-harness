package memory

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"code.byted.org/ai/lmy/apps/server/internal/contracts"
	statedb "code.byted.org/ai/lmy/apps/server/internal/infra/db"
	"code.byted.org/ai/lmy/apps/server/internal/shared"
)

type PersistentStore struct {
	*InMemoryStore
	db      *sql.DB
	writeMu sync.Mutex
}

func NewPersistentStore(dir string) (*PersistentStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return &PersistentStore{InMemoryStore: NewInMemoryStore()}, nil
	}
	path := dir
	if filepath.Ext(path) != ".db" {
		path = filepath.Join(dir, "state.db")
	}
	database, err := statedb.Open(path)
	if err != nil {
		return nil, err
	}
	return NewPersistentStoreWithDB(database)
}

func NewPersistentStoreWithDB(database *sql.DB) (*PersistentStore, error) {
	store := &PersistentStore{
		InMemoryStore: NewInMemoryStore(),
		db:            database,
	}
	if database == nil {
		return store, nil
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *PersistentStore) CreateConversation(title string) contracts.Conversation {
	conversation := s.InMemoryStore.CreateConversation(title)
	_ = s.upsertConversation(conversation)
	return conversation
}

func (s *PersistentStore) DeleteConversation(conversationID string) bool {
	deleted := s.InMemoryStore.DeleteConversation(conversationID)
	if s.db == nil || strings.TrimSpace(conversationID) == "" {
		return deleted
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.Exec(`DELETE FROM conversations WHERE id = ?`, conversationID)
	if err != nil {
		return deleted
	}
	if rows, err := result.RowsAffected(); err == nil && rows > 0 {
		return true
	}
	return deleted
}

func (s *PersistentStore) AddMessage(message contracts.Message) {
	s.InMemoryStore.AddMessage(message)
	if conversation, ok := s.conversation(message.ConversationID); ok {
		_ = s.upsertConversation(conversation)
	}
	_ = s.upsertMessage(message)
}

func (s *PersistentStore) UpdateMessage(message contracts.Message) bool {
	updated := s.InMemoryStore.UpdateMessage(message)
	if !updated {
		return false
	}
	if conversation, ok := s.conversation(message.ConversationID); ok {
		_ = s.upsertConversation(conversation)
	}
	_ = s.upsertMessage(message)
	return true
}

func (s *PersistentStore) UpdateShortMemory(conversationID string, userMessage string, assistantAnswer string, toolResults []contracts.ToolResult) contracts.ShortMemory {
	memory := s.InMemoryStore.UpdateShortMemory(conversationID, userMessage, assistantAnswer, toolResults)
	_ = s.upsertShortMemory(memory)
	return memory
}

func (s *PersistentStore) CompleteWorkingMemory(conversationID string, turnID string, assistantAnswer string, status string) contracts.WorkingMemory {
	return s.InMemoryStore.CompleteWorkingMemory(conversationID, turnID, assistantAnswer, status)
}

func (s *PersistentStore) AddTrace(trace contracts.Trace) {
	s.InMemoryStore.AddTrace(trace)
	_ = s.upsertTrace(trace)
}

func (s *PersistentStore) UpdateTrace(trace contracts.Trace) {
	s.InMemoryStore.UpdateTrace(trace)
	_ = s.upsertTrace(trace)
}

func (s *PersistentStore) conversation(conversationID string) (contracts.Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, ok := s.conversations[conversationID]
	return conversation, ok
}

func (s *PersistentStore) upsertConversation(conversation contracts.Conversation) error {
	if s.db == nil || strings.TrimSpace(conversation.ID) == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO conversations(id, title, project_root, status, created_at, updated_at, metadata_json)
		 VALUES (?, ?, '', 'active', ?, ?, '{}')
		 ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			updated_at = excluded.updated_at`,
		conversation.ID,
		nonEmpty(conversation.Title, "New conversation"),
		formatTime(conversation.CreatedAt),
		formatTime(conversation.UpdatedAt),
	)
	return err
}

func (s *PersistentStore) upsertMessage(message contracts.Message) error {
	if s.db == nil || strings.TrimSpace(message.ID) == "" {
		return nil
	}
	metadata := "{}"
	if len(message.Metadata) > 0 {
		metadata = mustJSON(message.Metadata)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO messages(id, conversation_id, role, content, metadata_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			conversation_id = excluded.conversation_id,
			role = excluded.role,
			content = excluded.content,
			metadata_json = excluded.metadata_json,
			created_at = excluded.created_at`,
		message.ID,
		message.ConversationID,
		string(message.Role),
		message.Content,
		metadata,
		formatTime(message.CreatedAt),
	)
	return err
}

func (s *PersistentStore) upsertShortMemory(memory contracts.ShortMemory) error {
	if s.db == nil || strings.TrimSpace(memory.ConversationID) == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO conversation_memories(conversation_id, id, summary, facts_json, active_task, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(conversation_id) DO UPDATE SET
			id = excluded.id,
			summary = excluded.summary,
			facts_json = excluded.facts_json,
			active_task = excluded.active_task,
			updated_at = excluded.updated_at`,
		memory.ConversationID,
		memory.ID,
		memory.Summary,
		mustJSON(memory.RecentFacts),
		memory.ActiveTask,
		formatTime(memory.UpdatedAt),
	)
	return err
}

func (s *PersistentStore) upsertTrace(trace contracts.Trace) error {
	if s.db == nil || strings.TrimSpace(trace.ID) == "" {
		return nil
	}
	completedAt := ""
	if trace.CompletedAt != nil {
		completedAt = formatTime(*trace.CompletedAt)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO agent_runs(id, conversation_id, user_message_id, status, model, trace_json, final_answer, error, started_at, completed_at)
		 VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, NULLIF(?, ''))
		 ON CONFLICT(id) DO UPDATE SET
			conversation_id = excluded.conversation_id,
			user_message_id = excluded.user_message_id,
			status = excluded.status,
			trace_json = excluded.trace_json,
			final_answer = excluded.final_answer,
			error = excluded.error,
			completed_at = excluded.completed_at`,
		trace.ID,
		trace.ConversationID,
		trace.UserMessageID,
		traceStatus(trace),
		mustJSON(trace),
		trace.FinalAnswer,
		trace.Error,
		formatTime(trace.StartedAt),
		completedAt,
	)
	return err
}

func (s *PersistentStore) load() error {
	if s.db == nil {
		return nil
	}
	if err := s.loadConversations(); err != nil {
		return err
	}
	if err := s.loadMessages(); err != nil {
		return err
	}
	if err := s.loadShortMemories(); err != nil {
		return err
	}
	if err := s.loadTraces(); err != nil {
		return err
	}
	return nil
}

func (s *PersistentStore) loadConversations() error {
	rows, err := s.db.Query(`SELECT id, title, created_at, updated_at FROM conversations`)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var conversation contracts.Conversation
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&conversation.ID, &conversation.Title, &createdAt, &updatedAt); err != nil {
			return err
		}
		conversation.CreatedAt = parseTime(createdAt)
		conversation.UpdatedAt = parseTime(updatedAt)
		s.conversations[conversation.ID] = conversation
		if _, ok := s.messages[conversation.ID]; !ok {
			s.messages[conversation.ID] = []contracts.Message{}
		}
		if _, ok := s.traces[conversation.ID]; !ok {
			s.traces[conversation.ID] = []contracts.Trace{}
		}
	}
	return rows.Err()
}

func (s *PersistentStore) loadMessages() error {
	rows, err := s.db.Query(`SELECT id, conversation_id, role, content, metadata_json, created_at FROM messages ORDER BY created_at, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var message contracts.Message
		var role string
		var metadata string
		var createdAt string
		if err := rows.Scan(&message.ID, &message.ConversationID, &role, &message.Content, &metadata, &createdAt); err != nil {
			return err
		}
		message.Role = contracts.Role(role)
		message.CreatedAt = parseTime(createdAt)
		if strings.TrimSpace(metadata) != "" && metadata != "{}" {
			_ = json.Unmarshal([]byte(metadata), &message.Metadata)
		}
		s.mu.Lock()
		s.importMessageLocked(message)
		s.mu.Unlock()
	}
	return rows.Err()
}

func (s *PersistentStore) loadShortMemories() error {
	rows, err := s.db.Query(`SELECT conversation_id, id, summary, facts_json, active_task, updated_at FROM conversation_memories`)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var memory contracts.ShortMemory
		var facts string
		var updatedAt string
		if err := rows.Scan(&memory.ConversationID, &memory.ID, &memory.Summary, &facts, &memory.ActiveTask, &updatedAt); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(facts), &memory.RecentFacts)
		if memory.RecentFacts == nil {
			memory.RecentFacts = []string{}
		}
		memory.UpdatedAt = parseTime(updatedAt)
		s.shortMemory[memory.ConversationID] = memory
	}
	return rows.Err()
}

func (s *PersistentStore) loadTraces() error {
	rows, err := s.db.Query(`SELECT trace_json FROM agent_runs ORDER BY started_at, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		var trace contracts.Trace
		if err := json.Unmarshal([]byte(payload), &trace); err != nil {
			continue
		}
		s.importTraceLocked(trace)
		if trace.WorkingMemorySnapshot.ID != "" {
			s.saveWorking(trace.WorkingMemorySnapshot)
		}
	}
	return rows.Err()
}

func (s *PersistentStore) importMessageLocked(message contracts.Message) {
	if _, ok := s.conversations[message.ConversationID]; !ok {
		s.conversations[message.ConversationID] = contracts.Conversation{
			ID:        message.ConversationID,
			Title:     "Imported conversation",
			CreatedAt: message.CreatedAt,
			UpdatedAt: message.CreatedAt,
		}
	}
	previous := s.messages[message.ConversationID]
	s.messages[message.ConversationID] = append(previous, message)
	conversation := s.conversations[message.ConversationID]
	if conversation.CreatedAt.IsZero() || message.CreatedAt.Before(conversation.CreatedAt) {
		conversation.CreatedAt = message.CreatedAt
	}
	if message.CreatedAt.After(conversation.UpdatedAt) {
		conversation.UpdatedAt = message.CreatedAt
	}
	if len(previous) == 0 && message.Role == contracts.RoleUser {
		conversation.Title = shared.TrimRunes(message.Content, 48)
	}
	s.conversations[message.ConversationID] = conversation
}

func (s *PersistentStore) importTraceLocked(trace contracts.Trace) {
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

func traceStatus(trace contracts.Trace) string {
	if strings.TrimSpace(trace.Error) != "" {
		return "failed"
	}
	if trace.CompletedAt != nil {
		return "completed"
	}
	return "running"
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		value = shared.Now()
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
		return parsed
	}
	if parsed, err := time.Parse("2006-01-02T15:04:05.999Z", strings.TrimSpace(value)); err == nil {
		return parsed
	}
	return shared.Now()
}

func nonEmpty(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
