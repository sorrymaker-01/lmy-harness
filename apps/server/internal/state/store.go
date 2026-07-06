package state

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"code.byted.org/ai/lmy/apps/server/internal/claudecode"
	"code.byted.org/ai/lmy/apps/server/internal/runtime"
)

type Store struct {
	db *sql.DB
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
		`INSERT OR IGNORE INTO model_configs(id, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
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
		`SELECT id, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at
		 FROM model_configs
		 WHERE id = ?`,
		id,
	).Scan(
		&record.ID,
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
		`SELECT id, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at
		 FROM model_configs
		 ORDER BY CASE WHEN id = 'default' THEN 0 ELSE 1 END, id`,
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
		`INSERT INTO model_configs(id, provider, api_key, base_url, model, temperature, timeout_seconds, extra_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			provider = excluded.provider,
			api_key = excluded.api_key,
			base_url = excluded.base_url,
			model = excluded.model,
			temperature = excluded.temperature,
			timeout_seconds = excluded.timeout_seconds,
			extra_json = excluded.extra_json,
			updated_at = excluded.updated_at`,
		record.ID,
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
	if record.Model == "" {
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
	return record
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
