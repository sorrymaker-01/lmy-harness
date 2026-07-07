package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"code.byted.org/ai/lmy/apps/server/internal/claudecode"
	"code.byted.org/ai/lmy/apps/server/internal/runtime"
)

type Store struct {
	db *sql.DB
}

var errNotLatestTurn = errors.New("chat turn is not the latest turn")

func IsNotLatestTurn(err error) bool {
	return errors.Is(err, errNotLatestTurn)
}

type ToolConfigRecord struct {
	ToolName       string         `json:"toolName"`
	Enabled        bool           `json:"enabled"`
	ApprovalPolicy string         `json:"approvalPolicy"`
	Config         map[string]any `json:"config"`
	UpdatedAt      string         `json:"updatedAt"`
}

type ModelConfigRecord struct {
	ID             string         `json:"id"`
	ModelType      string         `json:"modelType"`
	Provider       string         `json:"provider"`
	APIKey         string         `json:"apiKey,omitempty"`
	BaseURL        string         `json:"baseURL"`
	Model          string         `json:"model"`
	Temperature    float64        `json:"temperature"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
	Extra          map[string]any `json:"extra"`
	UpdatedAt      string         `json:"updatedAt"`
}

type MCPServerConfigRecord struct {
	Name      string            `json:"name"`
	Scope     string            `json:"scope"`
	Type      string            `json:"type"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	URL       string            `json:"url,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
	UpdatedAt string            `json:"updatedAt"`
}

type ChatTurnRecord struct {
	ID                   string         `json:"id"`
	ConversationID       string         `json:"conversationId"`
	UserMessageID        string         `json:"userMessageId"`
	AssistantMessageID   string         `json:"assistantMessageId"`
	Mode                 string         `json:"mode"`
	PrimaryModelConfigID string         `json:"primaryModelConfigId"`
	CanonicalResponseID  string         `json:"canonicalResponseId"`
	Status               string         `json:"status"`
	Metadata             map[string]any `json:"metadata"`
	CreatedAt            string         `json:"createdAt"`
	UpdatedAt            string         `json:"updatedAt"`
}

type ModelResponseRecord struct {
	ID              string         `json:"id"`
	TurnID          string         `json:"turnId"`
	ConversationID  string         `json:"conversationId"`
	ModelConfigID   string         `json:"modelConfigId"`
	TraceID         string         `json:"traceId"`
	Content         string         `json:"content"`
	Status          string         `json:"status"`
	Error           string         `json:"error,omitempty"`
	PrimaryResponse bool           `json:"primaryResponse"`
	Metadata        map[string]any `json:"metadata"`
	CreatedAt       string         `json:"createdAt"`
	CompletedAt     string         `json:"completedAt,omitempty"`
	UpdatedAt       string         `json:"updatedAt"`
}

func NewStore(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

func (s *Store) ToolConfigFor(toolName string) (runtime.ToolConfig, bool) {
	if s == nil || s.db == nil || strings.TrimSpace(toolName) == "" {
		return runtime.ToolConfig{}, false
	}
	var enabled int
	var approvalPolicy string
	err := s.db.QueryRow(
		`SELECT enabled, approval_policy FROM tool_configs WHERE tool_name = ?`,
		toolName,
	).Scan(&enabled, &approvalPolicy)
	if err != nil {
		return runtime.ToolConfig{}, false
	}
	approvalPolicy = strings.TrimSpace(approvalPolicy)
	if approvalPolicy == "" {
		approvalPolicy = "auto"
	}
	return runtime.ToolConfig{Enabled: enabled != 0, ApprovalPolicy: approvalPolicy}, true
}

func (s *Store) ListToolConfigs() (map[string]ToolConfigRecord, error) {
	if s == nil || s.db == nil {
		return map[string]ToolConfigRecord{}, nil
	}
	rows, err := s.db.Query(`SELECT tool_name, enabled, approval_policy, config_json, updated_at FROM tool_configs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configs := map[string]ToolConfigRecord{}
	for rows.Next() {
		var record ToolConfigRecord
		var enabled int
		var configJSON string
		if err := rows.Scan(&record.ToolName, &enabled, &record.ApprovalPolicy, &configJSON, &record.UpdatedAt); err != nil {
			return nil, err
		}
		record.Enabled = enabled != 0
		record.Config = map[string]any{}
		_ = json.Unmarshal([]byte(configJSON), &record.Config)
		configs[record.ToolName] = record
	}
	return configs, rows.Err()
}

func (s *Store) SaveToolConfig(record ToolConfigRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	record.ToolName = strings.TrimSpace(record.ToolName)
	if record.ToolName == "" {
		return nil
	}
	record.ApprovalPolicy = normalizeApprovalPolicy(record.ApprovalPolicy)
	if record.Config == nil {
		record.Config = map[string]any{}
	}
	configJSON, err := json.Marshal(record.Config)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO tool_configs(tool_name, enabled, approval_policy, config_json, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(tool_name) DO UPDATE SET
			enabled = excluded.enabled,
			approval_policy = excluded.approval_policy,
			config_json = excluded.config_json,
			updated_at = excluded.updated_at`,
		record.ToolName,
		boolInt(record.Enabled),
		record.ApprovalPolicy,
		string(configJSON),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) SeedModelConfig(record ModelConfigRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	record = normalizeModelConfigRecord(record)
	extraJSON, err := json.Marshal(record.Extra)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR IGNORE INTO model_configs(id, model_type, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.ModelType,
		record.Provider,
		record.APIKey,
		record.BaseURL,
		record.Model,
		record.Temperature,
		record.TimeoutSeconds,
		string(extraJSON),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ModelConfig(id string) (ModelConfigRecord, bool, error) {
	if s == nil || s.db == nil {
		return ModelConfigRecord{}, false, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		id = "default"
	}
	var record ModelConfigRecord
	var extraJSON string
	err := s.db.QueryRow(
		`SELECT id, model_type, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at
		 FROM model_configs
		 WHERE id = ?`,
		id,
	).Scan(
		&record.ID,
		&record.ModelType,
		&record.Provider,
		&record.APIKey,
		&record.BaseURL,
		&record.Model,
		&record.Temperature,
		&record.TimeoutSeconds,
		&extraJSON,
		&record.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return ModelConfigRecord{}, false, nil
	}
	if err != nil {
		return ModelConfigRecord{}, false, err
	}
	record.Extra = map[string]any{}
	_ = json.Unmarshal([]byte(extraJSON), &record.Extra)
	record = normalizeModelConfigRecord(record)
	return record, true, nil
}

func (s *Store) ListModelConfigs() ([]ModelConfigRecord, error) {
	if s == nil || s.db == nil {
		return []ModelConfigRecord{}, nil
	}
	rows, err := s.db.Query(
		`SELECT id, model_type, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at
		 FROM model_configs
		 ORDER BY CASE WHEN id = 'default' THEN 0 WHEN model_type = 'embedding' THEN 2 ELSE 1 END, id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []ModelConfigRecord{}
	for rows.Next() {
		var record ModelConfigRecord
		var extraJSON string
		if err := rows.Scan(
			&record.ID,
			&record.ModelType,
			&record.Provider,
			&record.APIKey,
			&record.BaseURL,
			&record.Model,
			&record.Temperature,
			&record.TimeoutSeconds,
			&extraJSON,
			&record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		record.Extra = map[string]any{}
		_ = json.Unmarshal([]byte(extraJSON), &record.Extra)
		records = append(records, normalizeModelConfigRecord(record))
	}
	return records, rows.Err()
}

func (s *Store) FirstModelConfigByType(modelType string) (ModelConfigRecord, bool, error) {
	if s == nil || s.db == nil {
		return ModelConfigRecord{}, false, nil
	}
	modelType = normalizeModelType(modelType)
	var record ModelConfigRecord
	var extraJSON string
	err := s.db.QueryRow(
		`SELECT id, model_type, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at
		 FROM model_configs
		 WHERE model_type = ?
		   AND (? != 'embedding' OR (trim(api_key) != '' AND trim(model) != ''))
		 ORDER BY CASE
			WHEN model_type = 'reasoning' AND id = 'default' THEN 0
			WHEN model_type = 'embedding' AND id = 'default-embedding' THEN 0
			ELSE 1
		 END, id
		 LIMIT 1`,
		modelType,
		modelType,
	).Scan(
		&record.ID,
		&record.ModelType,
		&record.Provider,
		&record.APIKey,
		&record.BaseURL,
		&record.Model,
		&record.Temperature,
		&record.TimeoutSeconds,
		&extraJSON,
		&record.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return ModelConfigRecord{}, false, nil
	}
	if err != nil {
		return ModelConfigRecord{}, false, err
	}
	record.Extra = map[string]any{}
	_ = json.Unmarshal([]byte(extraJSON), &record.Extra)
	return normalizeModelConfigRecord(record), true, nil
}

func (s *Store) SaveModelConfig(record ModelConfigRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	record = normalizeModelConfigRecord(record)
	extraJSON, err := json.Marshal(record.Extra)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO model_configs(id, model_type, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			model_type = excluded.model_type,
			provider = excluded.provider,
			api_key = excluded.api_key,
			base_url = excluded.base_url,
			model = excluded.model,
			temperature = excluded.temperature,
			timeout_seconds = excluded.timeout_seconds,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at`,
		record.ID,
		record.ModelType,
		record.Provider,
		record.APIKey,
		record.BaseURL,
		record.Model,
		record.Temperature,
		record.TimeoutSeconds,
		string(extraJSON),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) DeleteModelConfig(id string) error {
	if s == nil || s.db == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" || id == "default" {
		return sql.ErrNoRows
	}
	result, err := s.db.Exec(`DELETE FROM model_configs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SyncMCPServers(servers []claudecode.MCPServer) error {
	if s == nil || s.db == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, server := range servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			continue
		}
		args, err := json.Marshal(server.Args)
		if err != nil {
			return err
		}
		env, err := json.Marshal(server.Env)
		if err != nil {
			return err
		}
		headers, err := json.Marshal(server.Headers)
		if err != nil {
			return err
		}
		_, err = s.db.Exec(
			`INSERT INTO mcp_server_configs(name, scope, type, command, args_json, url, env_json, headers_json, enabled, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
			 ON CONFLICT(name) DO UPDATE SET
				scope = excluded.scope,
				type = excluded.type,
				command = excluded.command,
				args_json = excluded.args_json,
				url = excluded.url,
				env_json = excluded.env_json,
				headers_json = excluded.headers_json,
				updated_at = excluded.updated_at`,
			name,
			server.Scope,
			firstNonEmpty(server.Type, "stdio"),
			server.Command,
			string(args),
			server.URL,
			string(env),
			string(headers),
			now,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EnabledMCPServers() ([]claudecode.MCPServer, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT name, scope, type, command, args_json, url, env_json, headers_json
		 FROM mcp_server_configs
		 WHERE enabled != 0
		 ORDER BY scope, name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	servers := []claudecode.MCPServer{}
	for rows.Next() {
		var server claudecode.MCPServer
		var argsJSON string
		var envJSON string
		var headersJSON string
		if err := rows.Scan(&server.Name, &server.Scope, &server.Type, &server.Command, &argsJSON, &server.URL, &envJSON, &headersJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(argsJSON), &server.Args)
		_ = json.Unmarshal([]byte(envJSON), &server.Env)
		_ = json.Unmarshal([]byte(headersJSON), &server.Headers)
		servers = append(servers, server)
	}
	return servers, rows.Err()
}

func (s *Store) ListMCPServerConfigs() ([]MCPServerConfigRecord, error) {
	if s == nil || s.db == nil {
		return []MCPServerConfigRecord{}, nil
	}
	rows, err := s.db.Query(
		`SELECT name, scope, type, command, args_json, url, env_json, headers_json, enabled, updated_at
		 FROM mcp_server_configs
		 ORDER BY scope, name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	servers := []MCPServerConfigRecord{}
	for rows.Next() {
		var record MCPServerConfigRecord
		var argsJSON string
		var envJSON string
		var headersJSON string
		var enabled int
		if err := rows.Scan(&record.Name, &record.Scope, &record.Type, &record.Command, &argsJSON, &record.URL, &envJSON, &headersJSON, &enabled, &record.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(argsJSON), &record.Args)
		_ = json.Unmarshal([]byte(envJSON), &record.Env)
		_ = json.Unmarshal([]byte(headersJSON), &record.Headers)
		record.Enabled = enabled != 0
		servers = append(servers, record)
	}
	return servers, rows.Err()
}

func (s *Store) SetMCPServerEnabled(name string, enabled bool) error {
	if s == nil || s.db == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	result, err := s.db.Exec(
		`UPDATE mcp_server_configs
		 SET enabled = ?, updated_at = ?
		 WHERE name = ?`,
		boolInt(enabled),
		time.Now().UTC().Format(time.RFC3339Nano),
		name,
	)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SaveChatTurn(record ChatTurnRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	record = normalizeChatTurnRecord(record)
	if record.ID == "" || record.ConversationID == "" || record.UserMessageID == "" {
		return nil
	}
	metadataJSON, err := json.Marshal(record.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO chat_turns(id, conversation_id, user_message_id, assistant_message_id, mode, primary_model_config_id, canonical_response_id, status, metadata_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			conversation_id = excluded.conversation_id,
			user_message_id = excluded.user_message_id,
			assistant_message_id = excluded.assistant_message_id,
			mode = excluded.mode,
			primary_model_config_id = excluded.primary_model_config_id,
			canonical_response_id = excluded.canonical_response_id,
			status = excluded.status,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at`,
		record.ID,
		record.ConversationID,
		record.UserMessageID,
		record.AssistantMessageID,
		record.Mode,
		record.PrimaryModelConfigID,
		record.CanonicalResponseID,
		record.Status,
		string(metadataJSON),
		record.CreatedAt,
		record.UpdatedAt,
	)
	return err
}

func (s *Store) ChatTurn(id string) (ChatTurnRecord, bool, error) {
	if s == nil || s.db == nil {
		return ChatTurnRecord{}, false, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ChatTurnRecord{}, false, nil
	}
	row := s.db.QueryRow(
		`SELECT id, conversation_id, user_message_id, assistant_message_id, mode, primary_model_config_id, canonical_response_id, status, metadata_json, created_at, updated_at
		 FROM chat_turns
		 WHERE id = ?`,
		id,
	)
	record, err := scanChatTurn(row)
	if err == sql.ErrNoRows {
		return ChatTurnRecord{}, false, nil
	}
	if err != nil {
		return ChatTurnRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) LatestChatTurn(conversationID string) (ChatTurnRecord, bool, error) {
	if s == nil || s.db == nil {
		return ChatTurnRecord{}, false, nil
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ChatTurnRecord{}, false, nil
	}
	row := s.db.QueryRow(
		`SELECT id, conversation_id, user_message_id, assistant_message_id, mode, primary_model_config_id, canonical_response_id, status, metadata_json, created_at, updated_at
		 FROM chat_turns
		 WHERE conversation_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`,
		conversationID,
	)
	record, err := scanChatTurn(row)
	if err == sql.ErrNoRows {
		return ChatTurnRecord{}, false, nil
	}
	if err != nil {
		return ChatTurnRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) SaveModelResponse(record ModelResponseRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	record = normalizeModelResponseRecord(record)
	if record.ID == "" || record.TurnID == "" || record.ConversationID == "" {
		return nil
	}
	metadataJSON, err := json.Marshal(record.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO model_responses(id, turn_id, conversation_id, model_config_id, trace_id, content, status, error, primary_response, metadata_json, created_at, completed_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)
		 ON CONFLICT(id) DO UPDATE SET
			turn_id = excluded.turn_id,
			conversation_id = excluded.conversation_id,
			model_config_id = excluded.model_config_id,
			trace_id = excluded.trace_id,
			content = excluded.content,
			status = excluded.status,
			error = excluded.error,
			primary_response = excluded.primary_response,
			metadata_json = excluded.metadata_json,
			completed_at = excluded.completed_at,
			updated_at = excluded.updated_at`,
		record.ID,
		record.TurnID,
		record.ConversationID,
		record.ModelConfigID,
		record.TraceID,
		record.Content,
		record.Status,
		record.Error,
		boolInt(record.PrimaryResponse),
		string(metadataJSON),
		record.CreatedAt,
		record.CompletedAt,
		record.UpdatedAt,
	)
	return err
}

func (s *Store) ModelResponse(id string) (ModelResponseRecord, bool, error) {
	if s == nil || s.db == nil {
		return ModelResponseRecord{}, false, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ModelResponseRecord{}, false, nil
	}
	row := s.db.QueryRow(
		`SELECT id, turn_id, conversation_id, model_config_id, trace_id, content, status, error, primary_response, metadata_json, created_at, COALESCE(completed_at, ''), updated_at
		 FROM model_responses
		 WHERE id = ?`,
		id,
	)
	record, err := scanModelResponse(row)
	if err == sql.ErrNoRows {
		return ModelResponseRecord{}, false, nil
	}
	if err != nil {
		return ModelResponseRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) ModelResponsesByTurn(turnID string) ([]ModelResponseRecord, error) {
	if s == nil || s.db == nil {
		return []ModelResponseRecord{}, nil
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return []ModelResponseRecord{}, nil
	}
	rows, err := s.db.Query(
		`SELECT id, turn_id, conversation_id, model_config_id, trace_id, content, status, error, primary_response, metadata_json, created_at, COALESCE(completed_at, ''), updated_at
		 FROM model_responses
		 WHERE turn_id = ?
		 ORDER BY primary_response DESC, created_at, id`,
		turnID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []ModelResponseRecord{}
	for rows.Next() {
		record, err := scanModelResponse(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) SelectCanonicalResponse(conversationID string, turnID string, responseID string) (ChatTurnRecord, ModelResponseRecord, []ModelResponseRecord, error) {
	if s == nil || s.db == nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, sql.ErrNoRows
	}
	turn, ok, err := s.ChatTurn(turnID)
	if err != nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, err
	}
	if !ok || turn.ConversationID != strings.TrimSpace(conversationID) {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, sql.ErrNoRows
	}
	latest, ok, err := s.LatestChatTurn(conversationID)
	if err != nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, err
	}
	if !ok || latest.ID != turn.ID {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, errNotLatestTurn
	}
	var latestMessageID string
	if err := s.db.QueryRow(
		`SELECT id
		 FROM messages
		 WHERE conversation_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`,
		conversationID,
	).Scan(&latestMessageID); err != nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, err
	}
	if strings.TrimSpace(turn.AssistantMessageID) == "" || latestMessageID != turn.AssistantMessageID {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, errNotLatestTurn
	}
	response, ok, err := s.ModelResponse(responseID)
	if err != nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, err
	}
	if !ok || response.TurnID != turn.ID || response.ConversationID != turn.ConversationID || response.Status != "completed" {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, sql.ErrNoRows
	}
	turn.CanonicalResponseID = response.ID
	turn.Status = "completed"
	turn.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.SaveChatTurn(turn); err != nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, err
	}
	responses, err := s.ModelResponsesByTurn(turn.ID)
	if err != nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, err
	}
	return turn, response, responses, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeModelConfigRecord(record ModelConfigRecord) ModelConfigRecord {
	record.ID = strings.TrimSpace(record.ID)
	if record.ID == "" {
		record.ID = "default"
	}
	record.ModelType = normalizeModelType(record.ModelType)
	record.Provider = strings.TrimSpace(record.Provider)
	if record.Provider == "" {
		record.Provider = "openai-compatible"
	}
	record.APIKey = strings.TrimSpace(record.APIKey)
	record.BaseURL = strings.TrimRight(strings.TrimSpace(record.BaseURL), "/")
	if record.BaseURL == "" {
		record.BaseURL = "https://ark-cn-beijing.bytedance.net/api/v3"
	}
	record.Model = strings.TrimSpace(record.Model)
	if record.Model == "" && record.ModelType == "reasoning" {
		record.Model = "ep-20260507115713-ltdzl"
	}
	if record.Temperature < 0 || record.Temperature > 2 {
		record.Temperature = 0.2
	}
	if record.TimeoutSeconds <= 0 {
		record.TimeoutSeconds = 60
	}
	if record.Extra == nil {
		record.Extra = map[string]any{}
	}
	delete(record.Extra, "embeddingModel")
	return record
}

func normalizeModelType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "embedding", "vector", "向量模型":
		return "embedding"
	default:
		return "reasoning"
	}
}

type sqlScanner interface {
	Scan(dest ...any) error
}

func scanChatTurn(scanner sqlScanner) (ChatTurnRecord, error) {
	var record ChatTurnRecord
	var metadataJSON string
	if err := scanner.Scan(
		&record.ID,
		&record.ConversationID,
		&record.UserMessageID,
		&record.AssistantMessageID,
		&record.Mode,
		&record.PrimaryModelConfigID,
		&record.CanonicalResponseID,
		&record.Status,
		&metadataJSON,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return ChatTurnRecord{}, err
	}
	record.Metadata = map[string]any{}
	_ = json.Unmarshal([]byte(metadataJSON), &record.Metadata)
	return normalizeChatTurnRecord(record), nil
}

func scanModelResponse(scanner sqlScanner) (ModelResponseRecord, error) {
	var record ModelResponseRecord
	var primary int
	var metadataJSON string
	if err := scanner.Scan(
		&record.ID,
		&record.TurnID,
		&record.ConversationID,
		&record.ModelConfigID,
		&record.TraceID,
		&record.Content,
		&record.Status,
		&record.Error,
		&primary,
		&metadataJSON,
		&record.CreatedAt,
		&record.CompletedAt,
		&record.UpdatedAt,
	); err != nil {
		return ModelResponseRecord{}, err
	}
	record.PrimaryResponse = primary != 0
	record.Metadata = map[string]any{}
	_ = json.Unmarshal([]byte(metadataJSON), &record.Metadata)
	return normalizeModelResponseRecord(record), nil
}

func normalizeChatTurnRecord(record ChatTurnRecord) ChatTurnRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.ConversationID = strings.TrimSpace(record.ConversationID)
	record.UserMessageID = strings.TrimSpace(record.UserMessageID)
	record.AssistantMessageID = strings.TrimSpace(record.AssistantMessageID)
	switch strings.ToLower(strings.TrimSpace(record.Mode)) {
	case "multi":
		record.Mode = "multi"
	default:
		record.Mode = "single"
	}
	record.PrimaryModelConfigID = strings.TrimSpace(record.PrimaryModelConfigID)
	record.CanonicalResponseID = strings.TrimSpace(record.CanonicalResponseID)
	record.Status = normalizeRunStatus(record.Status)
	if record.Metadata == nil {
		record.Metadata = map[string]any{}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(record.CreatedAt) == "" {
		record.CreatedAt = now
	}
	if strings.TrimSpace(record.UpdatedAt) == "" {
		record.UpdatedAt = now
	}
	return record
}

func normalizeModelResponseRecord(record ModelResponseRecord) ModelResponseRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.TurnID = strings.TrimSpace(record.TurnID)
	record.ConversationID = strings.TrimSpace(record.ConversationID)
	record.ModelConfigID = strings.TrimSpace(record.ModelConfigID)
	record.TraceID = strings.TrimSpace(record.TraceID)
	record.Status = normalizeRunStatus(record.Status)
	if record.Metadata == nil {
		record.Metadata = map[string]any{}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(record.CreatedAt) == "" {
		record.CreatedAt = now
	}
	if strings.TrimSpace(record.UpdatedAt) == "" {
		record.UpdatedAt = now
	}
	if record.Status == "completed" && strings.TrimSpace(record.CompletedAt) == "" {
		record.CompletedAt = now
	}
	return record
}

func normalizeRunStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed", "failed", "running":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "running"
	}
}

func normalizeApprovalPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ask", "deny":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "auto"
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
