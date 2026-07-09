package memory

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	statedb "github.com/sorrymaker-01/lmy-harness/apps/server/internal/infra/db"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

// PersistentStore 是 Store 接口的 SQLite 持久化实现。
//
// 设计上采用“内存缓存 + write-through 落盘”的组合模式：
//   - 内嵌 InMemoryStore：所有读操作（Messages/ListConversations/Traces 等）
//     直接命中内存，无需查库，读路径与纯内存实现完全相同；
//   - 写操作先更新内存，再同步 upsert 到 SQLite（conversations / messages /
//     conversation_memories / agent_runs 四张表）；
//   - 进程启动时通过 load() 把 SQLite 中的历史数据全量加载回内存。
//
// 工作记忆（WorkingMemory）不单独建表持久化——它是单轮临时状态，
// 其最终快照已随 trace 的 trace_json 一起写入 agent_runs，重启时从中恢复。
type PersistentStore struct {
	*InMemoryStore            // 内存缓存层：读操作直接复用其实现
	db             *sql.DB    // SQLite 连接；为 nil 时退化为纯内存模式
	writeMu        sync.Mutex // 串行化数据库写操作，避免并发写触发 SQLITE_BUSY
}

// NewPersistentStore 按目录（或 .db 文件路径）创建持久化存储：
//   - dir 为空：不落盘，退化为纯内存模式（db == nil，所有 upsert 直接跳过）；
//   - dir 是目录：在其下创建/打开 state.db；
//   - dir 以 .db 结尾：直接作为数据库文件路径使用。
//
// 打开数据库时由 infra/db 包负责执行 schema 迁移（建表）。
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

// NewPersistentStoreWithDB 用已打开的数据库连接构造持久化存储，
// 便于与 state.Store 等其他组件共享同一个 SQLite 连接（同一个 state.db）。
// 构造时立即执行 load()，把库中的会话/消息/短期记忆/trace 全量加载进内存缓存。
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

// CreateConversation 覆写内存实现：先在内存中创建，再 write-through 落盘。
// 落盘错误被有意忽略（尽力持久化），保证主流程不因磁盘问题中断。
func (s *PersistentStore) CreateConversation(title string) contracts.Conversation {
	conversation := s.InMemoryStore.CreateConversation(title)
	_ = s.upsertConversation(conversation)
	return conversation
}

// DeleteConversation 先删内存，再删数据库。数据库中 messages/chat_turns/
// conversation_memories/agent_runs 等表都对 conversations 设置了
// ON DELETE CASCADE 外键，因此只需删除 conversations 一行即可级联清理。
// 返回值取“内存删除成功 或 数据库确实删掉了行”的并集：
// 这样即使内存中不存在（如重启后仅存在于库中的脏数据）也能正确报告删除成功。
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

// AddMessage 先写内存（可能触发会话自动补建与标题/UpdatedAt 更新），
// 然后把“最新的会话元信息 + 消息本体”一并落盘，保证库中会话行与消息行同步。
func (s *PersistentStore) AddMessage(message contracts.Message) {
	s.InMemoryStore.AddMessage(message)
	if conversation, ok := s.conversation(message.ConversationID); ok {
		_ = s.upsertConversation(conversation)
	}
	_ = s.upsertMessage(message)
}

// UpdateMessage 先更新内存；内存中找不到目标消息时直接返回 false，
// 不做任何落盘（内存是权威数据源）。更新成功后同步刷新会话行与消息行。
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

// UpdateShortMemory 先按内存实现完成摘要/事实/任务的滚动更新，
// 再把结果整体 upsert 到 conversation_memories 表（每会话一行）。
func (s *PersistentStore) UpdateShortMemory(conversationID string, userMessage string, assistantAnswer string, toolResults []contracts.ToolResult) contracts.ShortMemory {
	memory := s.InMemoryStore.UpdateShortMemory(conversationID, userMessage, assistantAnswer, toolResults)
	_ = s.upsertShortMemory(memory)
	return memory
}

// CompleteWorkingMemory 不做任何额外持久化：工作记忆是单轮临时状态，
// 其最终快照会包含在 trace 中随 agent_runs.trace_json 一起落盘（见 upsertTrace），
// 因此这里仅透传给内存实现。
func (s *PersistentStore) CompleteWorkingMemory(conversationID string, turnID string, assistantAnswer string, status string) contracts.WorkingMemory {
	return s.InMemoryStore.CompleteWorkingMemory(conversationID, turnID, assistantAnswer, status)
}

// AddTrace 先写内存，再把整条 trace 序列化为 JSON upsert 到 agent_runs 表。
func (s *PersistentStore) AddTrace(trace contracts.Trace) {
	s.InMemoryStore.AddTrace(trace)
	_ = s.upsertTrace(trace)
}

// UpdateTrace 运行过程中每次 trace 变化（新增步骤、完成、报错）都会调用，
// 内存更新后整条重新 upsert，保证 agent_runs 始终保存最新完整快照。
func (s *PersistentStore) UpdateTrace(trace contracts.Trace) {
	s.InMemoryStore.UpdateTrace(trace)
	_ = s.upsertTrace(trace)
}

// conversation 在持锁状态下读取内存中的会话元信息，供落盘前取最新标题/时间。
func (s *PersistentStore) conversation(conversationID string) (contracts.Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, ok := s.conversations[conversationID]
	return conversation, ok
}

// upsertConversation 把会话元信息落盘到 conversations 表。
// 采用 INSERT ... ON CONFLICT DO UPDATE（upsert）：首次写入插入全量字段，
// 已存在时只更新 title 与 updated_at——created_at、project_root、status 等
// 字段一经创建即视为不可变，避免被后续更新覆盖。
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
		nonEmpty(conversation.Title, "新对话"), // 标题兜底，满足表的 NOT NULL 语义
		formatTime(conversation.CreatedAt),
		formatTime(conversation.UpdatedAt),
	)
	return err
}

// upsertMessage 把消息落盘到 messages 表。冲突时全字段覆盖更新，
// 支撑“流式回答先占位、完成后回填内容”的 UpdateMessage 场景。
// Metadata 序列化为 JSON 字符串存入 metadata_json 列（空 map 存 "{}"）。
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

// upsertShortMemory 把短期记忆落盘到 conversation_memories 表。
// 该表以 conversation_id 为主键（每个会话恰好一行），因此冲突键是
// conversation_id 而非 id；RecentFacts 序列化为 JSON 数组存入 facts_json。
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

// upsertTrace 把一次智能体运行的 trace 落盘到 agent_runs 表。关键做法：
//   - 整条 trace 结构体序列化成 JSON 存入 trace_json 列（文档式存储），
//     其中包含全部步骤与工作记忆快照，加载时无损还原；
//   - status 不由调用方传入，而是根据 Error/CompletedAt 推导（见 traceStatus）；
//   - completed_at 用 NULLIF(?, ”) 把空字符串转成 NULL，表示“仍在运行”。
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

// load 在进程启动时把 SQLite 中的全部历史数据加载回内存缓存。
// 加载顺序刻意为“会话 → 消息 → 短期记忆 → trace”：先建立会话骨架，
// 再挂载从属数据，避免消息导入时触发不必要的会话补建。
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

// loadConversations 加载会话元信息，并为每个会话预建空的消息/trace 切片，
// 与 CreateConversation 的内存初始化行为保持一致。
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

// loadMessages 按 (created_at, id) 升序加载全部消息，保证恢复后的
// 消息顺序与原始插入顺序一致（id 兜底解决同一时间戳的排序稳定性）。
// 每条消息通过 importMessageLocked 导入，同步修正会话的时间与标题。
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
		// 只有非空且非 "{}" 的 metadata 才反序列化，避免产生无意义的空 map 分配。
		if strings.TrimSpace(metadata) != "" && metadata != "{}" {
			_ = json.Unmarshal([]byte(metadata), &message.Metadata)
		}
		s.mu.Lock()
		s.importMessageLocked(message)
		s.mu.Unlock()
	}
	return rows.Err()
}

// loadShortMemories 加载各会话的短期记忆；facts_json 反序列化失败或为空时
// 兜底为空切片，保证内存中的 RecentFacts 永远非 nil。
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

// loadTraces 从 agent_runs 表加载 trace：只读 trace_json 一列，
// 直接反序列化即可还原完整 trace（其余列只是为 SQL 查询冗余的投影字段）。
// 解析失败的行跳过而非报错，容忍历史版本遗留的脏数据。
// trace 中若携带工作记忆快照，则一并恢复到 workingByTurn / latestWorkingByConv，
// 这也是工作记忆唯一的持久化恢复途径。
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

// importMessageLocked 是启动加载专用的消息导入逻辑（调用方必须已持有 s.mu）。
// 与运行时的 AddMessage 不同：会话的 CreatedAt/UpdatedAt 不用当前时间，
// 而是根据消息自身的时间戳回推（取最早消息为创建时间、最晚消息为更新时间），
// 从而在重启后还原真实的会话时间线；首条用户消息同样回填为会话标题。
func (s *PersistentStore) importMessageLocked(message contracts.Message) {
	if _, ok := s.conversations[message.ConversationID]; !ok {
		// 库中存在孤儿消息（缺少会话行）时兜底补建会话。
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

// importTraceLocked 按 ID 去重导入 trace（调用方必须已持有 s.mu）：
// 已存在同 ID 则覆盖，否则追加，语义与 UpdateTrace 一致。
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

// traceStatus 根据 trace 内容推导 agent_runs.status 列的取值：
// 有错误 → failed；已有完成时间 → completed；否则视为仍在运行 running。
// 由数据推导而非显式传参，避免状态与内容不一致。
func traceStatus(trace contracts.Trace) string {
	if strings.TrimSpace(trace.Error) != "" {
		return "failed"
	}
	if trace.CompletedAt != nil {
		return "completed"
	}
	return "running"
}

// mustJSON 序列化任意值为 JSON 字符串；失败时返回 "{}" 兜底，
// 保证写库参数永远是合法 JSON（列有 NOT NULL DEFAULT '{}' 语义）。
func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// formatTime 统一时间落盘格式：UTC + RFC3339Nano 字符串（SQLite 无原生时间类型，
// 文本格式可按字典序正确排序）；零值时间用当前时间兜底，避免写入空串。
func formatTime(value time.Time) string {
	if value.IsZero() {
		value = shared.Now()
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// parseTime 解析落盘的时间字符串：优先 RFC3339Nano，其次兼容历史数据的
// 毫秒级格式；都失败时返回当前时间兜底，保证加载流程不中断。
func parseTime(value string) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
		return parsed
	}
	if parsed, err := time.Parse("2006-01-02T15:04:05.999Z", strings.TrimSpace(value)); err == nil {
		return parsed
	}
	return shared.Now()
}

// nonEmpty 返回非空白的 value，否则返回 fallback，用于字段默认值兜底。
func nonEmpty(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
