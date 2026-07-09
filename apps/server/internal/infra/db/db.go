// Package db 负责打开 SQLite 数据库并维护全量 schema（建表迁移）。
// memory.PersistentStore（会话记忆）、state.Store（配置/多模型状态）与
// knowledge 包（知识库/RAG）共享由本包打开的同一个 state.db 连接。
// 通过 sqlite-vec 扩展提供向量检索能力，通过 FTS5 提供全文检索能力。
package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

func init() {
	// 注册 sqlite-vec 扩展的自动加载，使后续打开的每个连接都具备
	// 向量表（vec0）与向量距离计算能力，供知识库向量检索使用。
	sqlite_vec.Auto()
}

// Open 打开（必要时创建）指定路径的 SQLite 数据库并执行 schema 迁移。
// 关键设置：
//   - path 为空时落到系统临时目录的 lmy-state.db；
//   - SetMaxOpenConns(1)：SQLite 单文件写并发能力弱，限制单连接
//     从源头避免 SQLITE_BUSY / 数据库锁竞争；
//   - DSN 上开启外键约束与 5 秒 busy_timeout；
//   - 迁移失败时关闭连接并返回错误，保证不会拿到半初始化的库。
func Open(path string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = filepath.Join(os.TempDir(), "lmy-state.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite3", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := migrate(database); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// sqliteDSN 为数据库路径拼接连接参数：启用外键约束（_foreign_keys=on，
// 支撑各表的 ON DELETE CASCADE 级联删除）与 5 秒忙等待（_busy_timeout）。
// 支持 :memory: 内存库（测试用，cache=shared 让多连接共享同一份内存数据）。
func sqliteDSN(path string) string {
	if path == ":memory:" {
		return "file::memory:?cache=shared&_foreign_keys=on&_busy_timeout=5000"
	}
	if strings.Contains(path, "?") {
		return path + "&_foreign_keys=on&_busy_timeout=5000"
	}
	return path + "?_foreign_keys=on&_busy_timeout=5000"
}

// migrate 顺序执行全部建表/建索引/触发器语句，完成 schema 迁移。
// 采用 CREATE TABLE IF NOT EXISTS 幂等风格：每次启动都能安全重放，
// 新增列则通过末尾的 ensureColumn 单独做增量迁移。
func migrate(database *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		// schema_migrations：记录已应用的迁移版本号，作为 schema 版本标记。
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,       -- 迁移版本号
			applied_at TEXT NOT NULL           -- 应用时间
		)`,
		// conversations：会话主表（memory 模块）。存储会话元信息，
		// 是 messages/chat_turns/conversation_memories/agent_runs 等表的父表，
		// 删除会话时通过外键 ON DELETE CASCADE 级联清理全部从属数据。
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,                       -- 会话 ID（conv 前缀）
			title TEXT NOT NULL,                       -- 会话标题（默认取首条用户消息）
			project_root TEXT NOT NULL DEFAULT '',     -- 关联的项目根目录（预留）
			status TEXT NOT NULL DEFAULT 'active',     -- 会话状态：active/archived 等
			created_at TEXT NOT NULL,                  -- 创建时间（RFC3339Nano）
			updated_at TEXT NOT NULL,                  -- 最近活跃时间，用于列表排序
			archived_at TEXT,                          -- 归档时间（可空）
			metadata_json TEXT NOT NULL DEFAULT '{}'   -- 扩展元数据
		)`,
		// messages：消息表（memory 模块）。会话内的用户/助手/工具消息，
		// role 用 CHECK 约束限定为 user/assistant/tool 三种角色。
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,                                              -- 消息 ID
			conversation_id TEXT NOT NULL,                                   -- 所属会话
			role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),-- 角色枚举
			content TEXT NOT NULL,                                           -- 消息正文
			metadata_json TEXT NOT NULL DEFAULT '{}',                        -- 扩展元数据
			created_at TEXT NOT NULL,                                        -- 创建时间（用于排序）
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		// 按 (会话, 创建时间) 建索引，加速按会话拉取有序消息。
		`CREATE INDEX IF NOT EXISTS idx_messages_conversation_created
			ON messages(conversation_id, created_at)`,
		// chat_turns：一轮对话表（state 模块）。记录一次用户提问对应的轮次，
		// mode 区分单模型/多模型，canonical_response_id 指向被选为正式答案的回答。
		`CREATE TABLE IF NOT EXISTS chat_turns (
			id TEXT PRIMARY KEY,                                -- 轮次 ID
			conversation_id TEXT NOT NULL,                     -- 所属会话
			user_message_id TEXT NOT NULL,                     -- 触发本轮的用户消息
			assistant_message_id TEXT NOT NULL DEFAULT '',     -- 本轮助手消息
			mode TEXT NOT NULL DEFAULT 'single',               -- single 单模型 / multi 多模型
			primary_model_config_id TEXT NOT NULL DEFAULT '',  -- 主模型配置 ID
			canonical_response_id TEXT NOT NULL DEFAULT '',    -- 被选为正式答案的回答 ID
			status TEXT NOT NULL DEFAULT 'running',            -- running/completed/failed
			metadata_json TEXT NOT NULL DEFAULT '{}',          -- 扩展元数据
			created_at TEXT NOT NULL,                          -- 创建时间
			updated_at TEXT NOT NULL,                          -- 更新时间
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
			FOREIGN KEY (user_message_id) REFERENCES messages(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chat_turns_conversation_created
			ON chat_turns(conversation_id, created_at)`,
		// model_responses：模型回答表（state 模块）。多模型模式下同一 turn 会有多行，
		// primary_response 标记主模型回答，trace_id 关联对应运行轨迹以便回放。
		`CREATE TABLE IF NOT EXISTS model_responses (
			id TEXT PRIMARY KEY,                            -- 回答 ID
			turn_id TEXT NOT NULL,                          -- 所属轮次
			conversation_id TEXT NOT NULL,                 -- 所属会话（冗余，便于查询/校验）
			model_config_id TEXT NOT NULL,                 -- 产生该回答的模型配置
			trace_id TEXT NOT NULL DEFAULT '',             -- 关联的运行 trace ID
			content TEXT NOT NULL DEFAULT '',              -- 回答正文
			status TEXT NOT NULL DEFAULT 'running',        -- running/completed/failed
			error TEXT NOT NULL DEFAULT '',                -- 失败原因
			primary_response INTEGER NOT NULL DEFAULT 0,   -- 是否主模型回答（1/0）
			metadata_json TEXT NOT NULL DEFAULT '{}',      -- 扩展元数据
			created_at TEXT NOT NULL,                      -- 创建时间
			completed_at TEXT,                             -- 完成时间（未完成为 NULL）
			updated_at TEXT NOT NULL,                      -- 更新时间
			FOREIGN KEY (turn_id) REFERENCES chat_turns(id) ON DELETE CASCADE,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_model_responses_turn
			ON model_responses(turn_id, created_at)`,
		// conversation_memories：短期记忆表（memory 模块）。每个会话恰好一行
		// （conversation_id 为主键），summary 是滚动摘要，facts_json 是近期事实数组。
		`CREATE TABLE IF NOT EXISTS conversation_memories (
			conversation_id TEXT PRIMARY KEY,          -- 会话 ID（一会话一行）
			id TEXT NOT NULL,                          -- 记忆 ID（mem 前缀，稳定不变）
			summary TEXT NOT NULL DEFAULT '',          -- 逐轮累积的会话摘要
			facts_json TEXT NOT NULL DEFAULT '[]',     -- 近期事实数组（滑动窗口）
			active_task TEXT NOT NULL DEFAULT '',      -- 当前进行中的任务
			updated_at TEXT NOT NULL,                  -- 更新时间
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		// agent_runs：运行轨迹表（memory 模块）。一次智能体运行一行，
		// trace_json 以文档式 JSON 存储完整 trace（含全部步骤与工作记忆快照），
		// 加载时直接反序列化即可无损还原；其余列是为 SQL 查询冗余的投影字段。
		`CREATE TABLE IF NOT EXISTS agent_runs (
			id TEXT PRIMARY KEY,                       -- 运行 ID
			conversation_id TEXT NOT NULL,             -- 所属会话
			user_message_id TEXT NOT NULL,             -- 触发运行的用户消息
			status TEXT NOT NULL,                      -- running/completed/failed（由 trace 推导）
			model TEXT NOT NULL DEFAULT '',            -- 使用的模型（冗余）
			trace_json TEXT NOT NULL DEFAULT '{}',     -- 完整 trace 的 JSON 快照
			final_answer TEXT NOT NULL DEFAULT '',     -- 最终回答（冗余）
			error TEXT NOT NULL DEFAULT '',            -- 错误信息
			started_at TEXT NOT NULL,                  -- 开始时间
			completed_at TEXT,                         -- 完成时间（运行中为 NULL）
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
			FOREIGN KEY (user_message_id) REFERENCES messages(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_runs_conversation_started
			ON agent_runs(conversation_id, started_at)`,
		// tool_configs：工具配置表（state 模块）。以工具名为主键，
		// 存储启用开关与审批策略（auto/ask/deny），config_json 存工具级扩展配置。
		`CREATE TABLE IF NOT EXISTS tool_configs (
			tool_name TEXT PRIMARY KEY,                    -- 工具名
			enabled INTEGER NOT NULL DEFAULT 1,            -- 是否启用（1/0）
			approval_policy TEXT NOT NULL DEFAULT 'auto',  -- 审批策略 auto/ask/deny
			config_json TEXT NOT NULL DEFAULT '{}',        -- 工具级扩展配置
			updated_at TEXT NOT NULL                       -- 更新时间
		)`,
		// model_configs：模型配置表（state 模块）。以配置 ID 为主键，
		// model_type 区分 reasoning（推理/对话）与 embedding（向量）模型，
		// default / default-embedding 为默认配置。
		`CREATE TABLE IF NOT EXISTS model_configs (
			id TEXT PRIMARY KEY,                                       -- 配置 ID（如 default）
			model_type TEXT NOT NULL DEFAULT 'reasoning',             -- reasoning / embedding
			provider TEXT NOT NULL DEFAULT 'openai-compatible',       -- 提供方
			api_key TEXT NOT NULL DEFAULT '',                         -- API 密钥（明文）
			base_url TEXT NOT NULL DEFAULT '',                        -- API 基地址
			model TEXT NOT NULL DEFAULT '',                           -- 模型名
			temperature REAL NOT NULL DEFAULT 0.2,                    -- 采样温度
			timeout_seconds INTEGER NOT NULL DEFAULT 60,              -- 请求超时（秒）
			extra_json TEXT NOT NULL DEFAULT '{}',                    -- 其他扩展参数
			updated_at TEXT NOT NULL                                  -- 更新时间
		)`,
		// skill_configs：技能配置表。以技能名为主键，记录启用与逻辑删除标记。
		`CREATE TABLE IF NOT EXISTS skill_configs (
			skill_name TEXT PRIMARY KEY,               -- 技能名
			enabled INTEGER NOT NULL DEFAULT 1,        -- 是否启用（1/0）
			deleted INTEGER NOT NULL DEFAULT 0,        -- 是否逻辑删除（1/0）
			updated_at TEXT NOT NULL                   -- 更新时间
		)`,
		// mcp_server_configs：MCP 服务器配置表（state 模块）。配置本体从
		// Claude Code 配置文件同步而来，enabled 是本地维护的启停开关
		// （重新同步时刻意不覆盖，保留用户 UI 上的选择）。
		`CREATE TABLE IF NOT EXISTS mcp_server_configs (
			name TEXT PRIMARY KEY,                     -- 服务器名
			scope TEXT NOT NULL DEFAULT 'project',     -- 来源作用域 project/user
			type TEXT NOT NULL DEFAULT 'stdio',        -- 连接类型 stdio / http
			command TEXT NOT NULL DEFAULT '',          -- stdio 启动命令
			args_json TEXT NOT NULL DEFAULT '[]',      -- 启动参数数组
			url TEXT NOT NULL DEFAULT '',              -- 远程服务地址
			env_json TEXT NOT NULL DEFAULT '{}',       -- 启动环境变量
			headers_json TEXT NOT NULL DEFAULT '{}',   -- 远程请求头
			enabled INTEGER NOT NULL DEFAULT 1,        -- 是否启用（本地开关，1/0）
			updated_at TEXT NOT NULL                   -- 更新时间
		)`,
		// knowledge_bases：知识库元数据表（knowledge 模块）。一条记录代表一个
		// 知识库，是 documents 的父表；deleted_at 支持软删除（保留历史数据）。
		`CREATE TABLE IF NOT EXISTS knowledge_bases (
			id TEXT PRIMARY KEY,                       -- 知识库 ID
			name TEXT NOT NULL,                        -- 知识库名称
			description TEXT NOT NULL DEFAULT '',      -- 描述
			status TEXT NOT NULL DEFAULT 'active',     -- 状态 active 等
			created_at TEXT NOT NULL,                  -- 创建时间
			updated_at TEXT NOT NULL,                  -- 更新时间
			deleted_at TEXT                            -- 软删除时间（可空）
		)`,
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			knowledge_base_id TEXT NOT NULL,
			title TEXT NOT NULL,
			source_type TEXT NOT NULL DEFAULT 'upload',
			source_uri TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			active_version INTEGER NOT NULL DEFAULT 1,
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (knowledge_base_id) REFERENCES knowledge_bases(id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_documents_kb_status_updated
			ON documents(knowledge_base_id, status, updated_at)`,
		`CREATE TABLE IF NOT EXISTS document_versions (
			id TEXT PRIMARY KEY,
			doc_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			content_hash TEXT NOT NULL DEFAULT '',
			raw_path TEXT NOT NULL DEFAULT '',
			parsed_path TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'indexing',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			activated_at TEXT,
			UNIQUE(doc_id, version),
			FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_versions_doc_status
			ON document_versions(doc_id, status)`,
		`CREATE TABLE IF NOT EXISTS document_chunks (
			id TEXT PRIMARY KEY,
			doc_id TEXT NOT NULL,
			doc_version INTEGER NOT NULL,
			knowledge_base_id TEXT NOT NULL,
			parent_chunk_id TEXT NOT NULL DEFAULT '',
			chunk_type TEXT NOT NULL DEFAULT 'child',
			chunk_index INTEGER NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			content_hash TEXT NOT NULL DEFAULT '',
			token_count INTEGER NOT NULL DEFAULT 0,
			heading_path TEXT NOT NULL DEFAULT '',
			start_offset INTEGER NOT NULL DEFAULT 0,
			end_offset INTEGER NOT NULL DEFAULT 0,
			prev_chunk_id TEXT NOT NULL DEFAULT '',
			next_chunk_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE,
			FOREIGN KEY (knowledge_base_id) REFERENCES knowledge_bases(id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunks_doc_version
			ON document_chunks(doc_id, doc_version, chunk_type, status)`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunks_kb_status
			ON document_chunks(knowledge_base_id, status, chunk_type)`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunks_content_hash
			ON document_chunks(content_hash)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS document_chunks_fts USING fts5(
			title,
			content,
			heading_path,
			content='document_chunks',
			content_rowid='rowid'
		)`,
		`CREATE TRIGGER IF NOT EXISTS document_chunks_ai AFTER INSERT ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(rowid, title, content, heading_path)
			VALUES (new.rowid, new.title, new.content, new.heading_path);
		END`,
		`CREATE TRIGGER IF NOT EXISTS document_chunks_ad AFTER DELETE ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(document_chunks_fts, rowid, title, content, heading_path)
			VALUES('delete', old.rowid, old.title, old.content, old.heading_path);
		END`,
		`CREATE TRIGGER IF NOT EXISTS document_chunks_au AFTER UPDATE OF title, content, heading_path ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(document_chunks_fts, rowid, title, content, heading_path)
			VALUES('delete', old.rowid, old.title, old.content, old.heading_path);
			INSERT INTO document_chunks_fts(rowid, title, content, heading_path)
			VALUES (new.rowid, new.title, new.content, new.heading_path);
		END`,
		`CREATE TABLE IF NOT EXISTS ingestion_jobs (
			id TEXT PRIMARY KEY,
			doc_id TEXT NOT NULL,
			doc_version INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			stage TEXT NOT NULL DEFAULT 'upload',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			started_at TEXT,
			finished_at TEXT,
			FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ingestion_jobs_doc_created
			ON ingestion_jobs(doc_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS index_outbox (
			id TEXT PRIMARY KEY,
			event_type TEXT NOT NULL,
			doc_id TEXT NOT NULL DEFAULT '',
			doc_version INTEGER NOT NULL DEFAULT 0,
			chunk_id TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL DEFAULT 'pending',
			retry_count INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_index_outbox_status_created
			ON index_outbox(status, created_at)`,
		`CREATE TABLE IF NOT EXISTS vector_index_state (
			name TEXT PRIMARY KEY,
			backend TEXT NOT NULL DEFAULT 'sqlite-vec',
			vector_table TEXT NOT NULL,
			dimension INTEGER NOT NULL DEFAULT 0,
			distance_metric TEXT NOT NULL DEFAULT 'cosine',
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS document_chunk_vector_rows (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chunk_id TEXT NOT NULL UNIQUE,
			doc_id TEXT NOT NULL,
			doc_version INTEGER NOT NULL,
			knowledge_base_id TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			source_type TEXT NOT NULL DEFAULT '',
			heading_path TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			vector_dimension INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunk_vector_rows_chunk
			ON document_chunk_vector_rows(chunk_id)`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunk_vector_rows_doc
			ON document_chunk_vector_rows(doc_id, doc_version, status)`,
		`CREATE INDEX IF NOT EXISTS idx_document_chunk_vector_rows_kb
			ON document_chunk_vector_rows(knowledge_base_id, status)`,
		`CREATE TABLE IF NOT EXISTS retrieval_logs (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL DEFAULT '',
			query TEXT NOT NULL,
			metadata_filter_json TEXT NOT NULL DEFAULT '{}',
			keyword_results_json TEXT NOT NULL DEFAULT '[]',
			vector_results_json TEXT NOT NULL DEFAULT '[]',
			merged_results_json TEXT NOT NULL DEFAULT '[]',
			reranked_results_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_retrieval_logs_conversation_created
			ON retrieval_logs(conversation_id, created_at)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at)
			VALUES (2, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			return err
		}
	}
	if err := ensureColumn(database, "skill_configs", "deleted", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(database, "model_configs", "model_type", "TEXT NOT NULL DEFAULT 'reasoning'"); err != nil {
		return err
	}
	return nil
}

// ensureColumn 做增量列迁移：用 PRAGMA table_info 检查目标列是否已存在，
// 不存在时才 ALTER TABLE ADD COLUMN。用于给旧库补充新版本引入的列
// （SQLite 的 ADD COLUMN 不支持 IF NOT EXISTS，需自行判存在）。
func ensureColumn(database *sql.DB, table string, column string, definition string) error {
	rows, err := database.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = database.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}
