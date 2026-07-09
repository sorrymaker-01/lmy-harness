// Package state 实现服务端“配置与运行状态”的 SQLite 存取层，
// 与 memory 包（会话记忆）分工互补：memory 负责对话内容类数据
// （会话/消息/短期记忆/trace），state 负责系统配置与多模型问答的结构化状态，包括：
//   - tool_configs：工具开关与审批策略；
//   - model_configs：模型配置（推理模型/向量模型，含密钥、地址、超参）；
//   - mcp_server_configs：MCP 服务器配置（来自 Claude Code 配置文件的同步副本）；
//   - chat_turns / model_responses：多模型问答场景下“一轮对话”与
//     “每个模型各自回答”的记录，以及选择哪个回答作为正式答案（canonical）的裁决。
//
// Store 直接持有 *sql.DB 做同步读写，不做内存缓存（配置读写频率低），
// 且所有方法都对 nil 接收者/nil db 容错（未启用持久化时安全降级为 no-op）。
package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/claudecode"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
)

// Store 是配置/运行状态的 SQLite 存取器。db 通常与 memory.PersistentStore
// 共享同一个 state.db 连接（schema 由 infra/db 包统一迁移创建）。
type Store struct {
	db *sql.DB
}

// errNotLatestTurn 是哨兵错误：当尝试对“不是会话最新一轮”的 turn 选择正式回答时返回。
// 因为历史轮次的回答已经成为后续上下文的一部分，改选会破坏对话一致性。
var errNotLatestTurn = errors.New("chat turn is not the latest turn")

// IsNotLatestTurn 判断错误是否为“非最新一轮”哨兵错误，供 HTTP 层映射为特定错误码。
func IsNotLatestTurn(err error) bool {
	return errors.Is(err, errNotLatestTurn)
}

// ToolConfigRecord 对应 tool_configs 表的一行：某个内置工具的开关状态、
// 审批策略（auto 自动执行 / ask 需人工确认 / deny 拒绝）以及自由格式的扩展配置。
type ToolConfigRecord struct {
	ToolName       string         `json:"toolName"`       // 工具名（主键）
	Enabled        bool           `json:"enabled"`        // 是否启用该工具
	ApprovalPolicy string         `json:"approvalPolicy"` // 审批策略：auto/ask/deny
	Config         map[string]any `json:"config"`         // 工具级扩展配置（存 config_json）
	UpdatedAt      string         `json:"updatedAt"`      // 最近更新时间（RFC3339Nano）
}

// ModelConfigRecord 对应 model_configs 表的一行：一个可调用的模型端点配置。
// modelType 区分 reasoning（推理/对话模型）与 embedding（向量模型，供知识库检索用）。
// id 为 "default" 的记录是默认推理模型，"default-embedding" 是默认向量模型。
type ModelConfigRecord struct {
	ID             string         `json:"id"`               // 配置 ID（主键），如 default
	ModelType      string         `json:"modelType"`        // reasoning / embedding
	Provider       string         `json:"provider"`         // 提供方，默认 openai-compatible
	APIKey         string         `json:"apiKey,omitempty"` // API 密钥（明文存库，序列化时可省略）
	BaseURL        string         `json:"baseURL"`          // API 基地址（尾部斜杠会被规范化去掉）
	Model          string         `json:"model"`            // 模型名，如 gpt-4o-mini
	Temperature    float64        `json:"temperature"`      // 采样温度，越界时回落为 0.2
	TimeoutSeconds int            `json:"timeoutSeconds"`   // 请求超时秒数，<=0 时回落为 60
	Extra          map[string]any `json:"extra"`            // 其他扩展参数（存 extra_json）
	UpdatedAt      string         `json:"updatedAt"`        // 最近更新时间
}

// MCPServerConfigRecord 对应 mcp_server_configs 表的一行：一个 MCP 服务器的
// 启动/连接配置。配置本体从 Claude Code 的配置文件同步而来（SyncMCPServers），
// 数据库只额外维护 enabled 开关，允许用户在 UI 上单独启停某个服务器。
type MCPServerConfigRecord struct {
	Name      string            `json:"name"`              // 服务器名（主键）
	Scope     string            `json:"scope"`             // 配置来源作用域（project/user 等）
	Type      string            `json:"type"`              // 连接类型：stdio / http(sse)
	Command   string            `json:"command"`           // stdio 类型的启动命令
	Args      []string          `json:"args"`              // 启动参数（存 args_json）
	URL       string            `json:"url,omitempty"`     // 远程类型的服务地址
	Env       map[string]string `json:"env,omitempty"`     // 启动环境变量（存 env_json）
	Headers   map[string]string `json:"headers,omitempty"` // 远程请求头（存 headers_json）
	Enabled   bool              `json:"enabled"`           // 是否启用（本地开关，不随同步覆盖）
	UpdatedAt string            `json:"updatedAt"`         // 最近更新时间
}

// ChatTurnRecord 对应 chat_turns 表的一行：一轮对话（一次用户提问）。
// 多模型模式（mode=multi）下，一轮会产生多条 ModelResponseRecord，
// 其中被用户/系统选定的那条 ID 记入 CanonicalResponseID，作为写回消息流的正式答案。
type ChatTurnRecord struct {
	ID                   string         `json:"id"`                   // 轮次 ID（主键）
	ConversationID       string         `json:"conversationId"`       // 所属会话
	UserMessageID        string         `json:"userMessageId"`        // 触发本轮的用户消息 ID
	AssistantMessageID   string         `json:"assistantMessageId"`   // 本轮助手消息 ID（回答占位/最终消息）
	Mode                 string         `json:"mode"`                 // single 单模型 / multi 多模型对比
	PrimaryModelConfigID string         `json:"primaryModelConfigId"` // 主模型配置 ID
	CanonicalResponseID  string         `json:"canonicalResponseId"`  // 被选定为正式答案的回答 ID
	Status               string         `json:"status"`               // running/completed/failed
	Metadata             map[string]any `json:"metadata"`             // 扩展元数据（存 metadata_json）
	CreatedAt            string         `json:"createdAt"`            // 创建时间
	UpdatedAt            string         `json:"updatedAt"`            // 更新时间
}

// ModelResponseRecord 对应 model_responses 表的一行：某个模型在某一轮中的一次回答。
// 多模型模式下同一 turn 会有多行；PrimaryResponse 标记主模型的回答
// （默认展示、默认作为正式答案的候选）。
type ModelResponseRecord struct {
	ID              string         `json:"id"`                    // 回答 ID（主键）
	TurnID          string         `json:"turnId"`                // 所属轮次
	ConversationID  string         `json:"conversationId"`        // 所属会话（冗余，便于校验/查询）
	ModelConfigID   string         `json:"modelConfigId"`         // 产生该回答的模型配置 ID
	TraceID         string         `json:"traceId"`               // 关联的运行 trace ID（可回放过程）
	Content         string         `json:"content"`               // 回答正文
	Status          string         `json:"status"`                // running/completed/failed
	Error           string         `json:"error,omitempty"`       // 失败原因
	PrimaryResponse bool           `json:"primaryResponse"`       // 是否为主模型回答
	Metadata        map[string]any `json:"metadata"`              // 扩展元数据（存 metadata_json）
	CreatedAt       string         `json:"createdAt"`             // 创建时间
	CompletedAt     string         `json:"completedAt,omitempty"` // 完成时间（未完成为空）
	UpdatedAt       string         `json:"updatedAt"`             // 更新时间
}

// NewStore 用已打开的数据库连接构造 Store；db 为 nil 时返回 nil Store，
// 所有方法对 nil 接收者均安全（返回零值/no-op），调用方无需判空。
func NewStore(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

// ToolConfigFor 查询单个工具的运行时配置（启用状态 + 审批策略），
// 供工具执行前做准入检查。查不到或出错时返回 (零值, false)，
// 由调用方决定默认行为；空的审批策略兜底为 auto。
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
	// SQLite 无布尔类型，enabled 以 INTEGER 存储，非 0 即为启用。
	return runtime.ToolConfig{Enabled: enabled != 0, ApprovalPolicy: approvalPolicy}, true
}

// ListToolConfigs 返回全部工具配置，以工具名为键的 map 形式返回，
// 便于上层与内置工具清单做合并展示（库中没有记录的工具用默认配置）。
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

// SaveToolConfig 以 upsert 方式保存工具配置：写前先规范化
// （工具名去空白、审批策略归一到 auto/ask/deny、Config 判空补 {}），
// updated_at 由服务端统一取当前 UTC 时间，避免依赖调用方时钟。
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

// SeedModelConfig 用于服务启动时播种默认模型配置（如 "default" 推理模型、
// "default-embedding" 向量模型）。与 SaveModelConfig 的关键区别是使用
// INSERT OR IGNORE：仅在记录不存在时插入，绝不覆盖用户已经修改过的配置。
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

// ModelConfig 按 ID 读取单个模型配置；id 为空时默认查 "default"。
// 返回 (记录, 是否存在, 错误) 三元组：ErrNoRows 被转换为 (零值, false, nil)，
// 让“不存在”成为正常分支而非错误。读出的 Extra JSON 会反序列化并做规范化兜底。
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

// ListModelConfigs 列出全部模型配置。排序规则针对 UI 展示：
// "default" 永远排第一，其余推理模型其次，向量模型（embedding）排最后，
// 同组内按 id 字典序稳定排序。
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

// FirstModelConfigByType 按模型类型（reasoning/embedding）挑选一条“可用的首选”配置：
//   - 对 embedding 类型额外要求 api_key 与 model 均非空——向量模型缺少任一项都
//     无法调用，宁可返回“不存在”让上层禁用向量检索；
//   - 优先返回该类型的默认记录（default / default-embedding），否则按 id 顺序取第一条。
//
// 用于知识库检索等场景中“不指定具体配置 ID，只要该类型下可用的模型”的查找。
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

// SaveModelConfig 以 upsert 方式保存模型配置（用户在设置页新增/修改模型时调用）。
// 与 SeedModelConfig 不同，冲突时会全字段覆盖更新；写前统一做规范化
// （补默认 provider/base_url、校正温度与超时等），保证库中数据始终合法可用。
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

// DeleteModelConfig 删除指定模型配置。两条保护规则：
//   - "default" 是系统兜底配置，禁止删除（直接返回 ErrNoRows）；
//   - 删除影响行数为 0（记录本就不存在）也返回 ErrNoRows，让上层统一按 404 处理。
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

// SyncMCPServers 把从 Claude Code 配置文件解析出的 MCP 服务器清单同步进
// mcp_server_configs 表。设计要点：
//   - 新记录插入时 enabled 默认为 1（新发现的服务器默认启用）；
//   - 已存在的记录只更新配置本体（scope/type/command/args/url/env/headers），
//     刻意不更新 enabled 列——用户在 UI 上的启停选择在重新同步后仍然保留。
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

// EnabledMCPServers 返回全部启用状态的 MCP 服务器（enabled != 0），
// 转换回 claudecode.MCPServer 供运行时实际拉起/连接这些服务器。
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

// ListMCPServerConfigs 返回全部 MCP 服务器配置（含禁用的），
// 按 (scope, name) 排序，供设置页展示与启停管理。
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

// SetMCPServerEnabled 单独切换某个 MCP 服务器的启用开关（UI 上的开关按钮）。
// 目标不存在时（影响行数为 0）返回 sql.ErrNoRows，供上层映射为 404。
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

// SaveChatTurn 以 upsert 方式保存一轮对话记录。写前规范化（模式归一为
// single/multi、状态归一、补时间戳），并要求 ID/会话 ID/用户消息 ID 三者齐全
// （外键指向 conversations 与 messages，缺失会破坏引用完整性，直接跳过写入）。
// 冲突更新时不覆盖 created_at，保持轮次创建时间不变。
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

// ChatTurn 按 ID 读取单轮记录，"不存在"以 (零值, false, nil) 表达而非错误。
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

// LatestChatTurn 返回会话内最新的一轮（按 created_at 降序、id 降序兜底取第一条），
// 用于 SelectCanonicalResponse 的“只允许改选最新一轮”校验。
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

// SaveModelResponse 以 upsert 方式保存某个模型的回答记录。
// completed_at 用 NULLIF(?, ”) 把空字符串写成 NULL（表示尚未完成）；
// 同一条记录会随流式生成/完成/失败被多次覆盖更新。
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

// ModelResponse 按 ID 读取单条回答记录；completed_at 为 NULL 时用
// COALESCE 转为空串，方便 Scan 进 string 字段。
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

// ModelResponsesByTurn 返回某一轮的全部模型回答：主模型回答
// （primary_response=1）排最前，其余按创建时间与 id 排序，用于多模型对比展示。
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

// SelectCanonicalResponse 在多模型模式下把某条模型回答“选为正式答案”，
// 是本文件中约束最严格的写操作。校验链依次为：
//  1. 目标 turn 必须存在且归属指定会话（防止跨会话越权改写）；
//  2. 目标 turn 必须是该会话的最新一轮——历史轮次的回答已成为后续对话的上下文，
//     改选会造成上下文与展示不一致，返回 errNotLatestTurn；
//  3. 会话的最新一条消息必须正是该 turn 的助手消息——若用户已发出新消息
//     （新一轮已开始但 turn 尚未落库），同样禁止改选；
//  4. 目标回答必须存在、归属该 turn/会话且状态为 completed（不能选中失败或未完成的回答）。
//
// 校验通过后：把回答 ID 写入 turn.CanonicalResponseID、将 turn 置为 completed，
// 并返回更新后的 turn、被选中的回答以及该轮全部回答（供前端整体刷新）。
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
	// 校验 2：只允许操作会话最新一轮。
	latest, ok, err := s.LatestChatTurn(conversationID)
	if err != nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, err
	}
	if !ok || latest.ID != turn.ID {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, errNotLatestTurn
	}
	// 校验 3：会话最新消息必须是本轮的助手消息，确保没有更新的对话内容产生。
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
	// 校验 4：候选回答必须归属本轮且已成功完成。
	response, ok, err := s.ModelResponse(responseID)
	if err != nil {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, err
	}
	if !ok || response.TurnID != turn.ID || response.ConversationID != turn.ConversationID || response.Status != "completed" {
		return ChatTurnRecord{}, ModelResponseRecord{}, nil, sql.ErrNoRows
	}
	// 全部校验通过：落盘裁决结果。
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

// firstNonEmpty 返回第一个非空白字符串（去除首尾空白后返回），全为空时返回空串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// normalizeModelConfigRecord 对模型配置做统一规范化，读路径与写路径共用，
// 保证无论数据来自请求还是历史库表，出参永远合法可用：
//   - ID 为空时回落到 "default"；modelType 归一为 reasoning/embedding；
//   - Provider 默认 openai-compatible，BaseURL 去尾斜杠并默认 OpenAI 官方地址；
//   - 推理模型缺省 Model 兜底为 gpt-4o-mini（向量模型不兜底：宁缺勿错）；
//   - 温度限定在 [0,2] 否则回落 0.2；超时 <=0 回落 60 秒；
//   - 移除 Extra 中的历史遗留字段 embeddingModel（向量模型现已拆分为独立配置）。
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
		record.BaseURL = "https://api.openai.com/v1"
	}
	record.Model = strings.TrimSpace(record.Model)
	if record.Model == "" && record.ModelType == "reasoning" {
		record.Model = "gpt-4o-mini"
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
	// 历史版本把向量模型名放在 Extra.embeddingModel，现已废弃，写入前清除。
	delete(record.Extra, "embeddingModel")
	return record
}

// normalizeModelType 把各种别名（embedding/vector/向量模型）归一为 "embedding"，
// 其余一律归为 "reasoning"，避免库中出现第三种类型值。
func normalizeModelType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "embedding", "vector", "向量模型":
		return "embedding"
	default:
		return "reasoning"
	}
}

// sqlScanner 抽象 *sql.Row 与 *sql.Rows 共同的 Scan 方法，
// 让 scanChatTurn / scanModelResponse 可同时服务单行与多行查询。
type sqlScanner interface {
	Scan(dest ...any) error
}

// scanChatTurn 从一行查询结果扫描出 ChatTurnRecord：
// metadata_json 反序列化为 map，最后统一走规范化兜底默认值。
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

// scanModelResponse 从一行查询结果扫描出 ModelResponseRecord：
// primary_response 的 INTEGER 转 bool，metadata_json 反序列化，再做规范化。
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

// normalizeChatTurnRecord 规范化轮次记录：各 ID 去空白、模式归一为
// single/multi（默认 single）、状态归一（默认 running）、Metadata 判空补 {}，
// 创建/更新时间为空时补当前 UTC 时间。
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

// normalizeModelResponseRecord 规范化回答记录，规则同上；额外地，
// 状态为 completed 但缺少完成时间时自动补当前时间，保证两者一致。
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

// normalizeRunStatus 把运行状态收敛到 completed/failed/running 三个合法值，
// 非法输入一律按 running 处理（最保守的默认状态）。
func normalizeRunStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed", "failed", "running":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "running"
	}
}

// normalizeApprovalPolicy 把审批策略收敛到 auto/ask/deny 三个合法值，
// 非法输入默认 auto（自动执行）。
func normalizeApprovalPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ask", "deny":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "auto"
	}
}

// boolInt 把 Go bool 转成 SQLite 的 INTEGER 布尔表示（1/0）。
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
