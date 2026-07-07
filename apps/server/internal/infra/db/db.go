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
	sqlite_vec.Auto()
}

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

func sqliteDSN(path string) string {
	if path == ":memory:" {
		return "file::memory:?cache=shared&_foreign_keys=on&_busy_timeout=5000"
	}
	if strings.Contains(path, "?") {
		return path + "&_foreign_keys=on&_busy_timeout=5000"
	}
	return path + "?_foreign_keys=on&_busy_timeout=5000"
}

func migrate(database *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			project_root TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			archived_at TEXT,
			metadata_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
			content TEXT NOT NULL,
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_conversation_created
			ON messages(conversation_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS chat_turns (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			user_message_id TEXT NOT NULL,
			assistant_message_id TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT 'single',
			primary_model_config_id TEXT NOT NULL DEFAULT '',
			canonical_response_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
			FOREIGN KEY (user_message_id) REFERENCES messages(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chat_turns_conversation_created
			ON chat_turns(conversation_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS model_responses (
			id TEXT PRIMARY KEY,
			turn_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			model_config_id TEXT NOT NULL,
			trace_id TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			error TEXT NOT NULL DEFAULT '',
			primary_response INTEGER NOT NULL DEFAULT 0,
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			completed_at TEXT,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (turn_id) REFERENCES chat_turns(id) ON DELETE CASCADE,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_model_responses_turn
			ON model_responses(turn_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS conversation_memories (
			conversation_id TEXT PRIMARY KEY,
			id TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			facts_json TEXT NOT NULL DEFAULT '[]',
			active_task TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS agent_runs (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			user_message_id TEXT NOT NULL,
			status TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			trace_json TEXT NOT NULL DEFAULT '{}',
			final_answer TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL,
			completed_at TEXT,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
			FOREIGN KEY (user_message_id) REFERENCES messages(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_runs_conversation_started
			ON agent_runs(conversation_id, started_at)`,
		`CREATE TABLE IF NOT EXISTS tool_configs (
			tool_name TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 1,
			approval_policy TEXT NOT NULL DEFAULT 'auto',
			config_json TEXT NOT NULL DEFAULT '{}',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS model_configs (
			id TEXT PRIMARY KEY,
			model_type TEXT NOT NULL DEFAULT 'reasoning',
			provider TEXT NOT NULL DEFAULT 'openai-compatible',
			api_key TEXT NOT NULL DEFAULT '',
			base_url TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			temperature REAL NOT NULL DEFAULT 0.2,
			timeout_seconds INTEGER NOT NULL DEFAULT 60,
			extra_json TEXT NOT NULL DEFAULT '{}',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS skill_configs (
			skill_name TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 1,
			deleted INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_server_configs (
			name TEXT PRIMARY KEY,
			scope TEXT NOT NULL DEFAULT 'project',
			type TEXT NOT NULL DEFAULT 'stdio',
			command TEXT NOT NULL DEFAULT '',
			args_json TEXT NOT NULL DEFAULT '[]',
			url TEXT NOT NULL DEFAULT '',
			env_json TEXT NOT NULL DEFAULT '{}',
			headers_json TEXT NOT NULL DEFAULT '{}',
			enabled INTEGER NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge_bases (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
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
