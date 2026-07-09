package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

// Input 是一次模型调用的输入集合：
//   - System：本轮拼装好的 system prompt（记忆、技能、知识库片段等都已注入）；
//   - Messages：对话历史（用户/助手/工具消息），使用 contracts.LLMMessage 线协议；
//   - Tools：暴露给模型的工具定义列表，元素已是 OpenAI tools 数组要求的
//     {"type":"function","function":{...}} 结构，可直接嵌入请求体。
type Input struct {
	System   string
	Messages []contracts.LLMMessage
	Tools    []map[string]any
}

// Client 是聊天模型的最小抽象：一次非流式补全调用。
// Agent 循环只依赖该接口，从而可替换任意 OpenAI 兼容的后端实现。
type Client interface {
	Chat(ctx context.Context, input Input) (contracts.ModelResponse, error)
}

// StreamDelta 表示流式响应中的一个内容增量片段（仅文本部分；
// 工具调用增量在内部累积，不逐段回调）。
type StreamDelta struct {
	Content string
}

// StreamHandler 是流式回调函数：每收到一段内容增量就被调用一次。
// 返回非 nil 错误会立即中断整个流式请求（例如客户端 SSE 已断开）。
type StreamHandler func(StreamDelta) error

// StreamingClient 在 Client 基础上扩展了流式接口。调用方（HTTP 层）
// 通过类型断言判断底层实现是否支持流式，不支持则回退到 Chat。
type StreamingClient interface {
	Client
	ChatStream(ctx context.Context, input Input, onDelta StreamHandler) (contracts.ModelResponse, error)
}

// EmbeddingClient 是文本向量化的抽象接口，knowledge（RAG）模块
// 依赖它把文档分块与查询转成向量做相似度检索。
type EmbeddingClient interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Config 描述一个 OpenAI 兼容模型端点的完整配置。
// 配置来源有两处：环境变量（DefaultConfigFromEnv）与 SQLite 中的
// model_configs 表（用户在前端保存的模型配置），后者优先生效。
type Config struct {
	Provider       string  // 提供方标识，目前固定为 "openai-compatible"
	APIKey         string  // Bearer 鉴权用的 API Key
	BaseURL        string  // API 基地址（如 https://api.openai.com/v1 或 Ark 端点）
	Model          string  // 聊天模型名
	EmbeddingModel string  // 向量模型名（为空表示未启用 embedding）
	Temperature    float64 // 采样温度，超出 [0,2] 会被归一化为 0.2
	TimeoutSeconds int     // 单次 HTTP 请求超时秒数
}

// OpenAICompatibleModel 是基于 OpenAI Chat Completions 协议的聊天模型
// 客户端实现，兼容 OpenAI、火山方舟（Ark）等任何遵循同协议的服务。
// 值类型 + 无内部可变状态，天然并发安全。
type OpenAICompatibleModel struct {
	config Config
	client *http.Client
}

// OpenAIEmbeddingClient 是基于 OpenAI /embeddings 协议的向量化客户端，
// 额外兼容 Ark 的多模态 embedding 端点（/embeddings/multimodal）。
type OpenAIEmbeddingClient struct {
	config Config
	client *http.Client
}

// NewDefaultModel 用环境变量中的默认配置构造聊天模型客户端。
func NewDefaultModel() Client {
	return NewOpenAICompatibleModel(DefaultConfigFromEnv())
}

// NewOpenAICompatibleModel 用给定配置构造聊天模型客户端。
// 配置先经 NormalizeConfig 兜底（补默认值、裁剪空白），
// 并根据 TimeoutSeconds 设置 http.Client 的整体超时。
func NewOpenAICompatibleModel(config Config) Client {
	config = NormalizeConfig(config)
	return OpenAICompatibleModel{
		config: config,
		client: &http.Client{Timeout: time.Duration(config.TimeoutSeconds) * time.Second},
	}
}

// NewOpenAIEmbeddingClient 用给定配置构造向量化客户端。
// 若未配置 EmbeddingModel 则返回 nil，表示 embedding 能力未启用
// （调用方以 nil 判断是否可用；Embed 对 nil 接收者也做了容错）。
func NewOpenAIEmbeddingClient(config Config) *OpenAIEmbeddingClient {
	config = NormalizeConfig(config)
	if strings.TrimSpace(config.EmbeddingModel) == "" {
		return nil
	}
	return &OpenAIEmbeddingClient{
		config: config,
		client: &http.Client{Timeout: time.Duration(config.TimeoutSeconds) * time.Second},
	}
}

// DefaultConfigFromEnv 从环境变量读取聊天模型的默认配置：
//   - OPENAI_API_KEY / ARK_API_KEY：API Key（前者优先，兼容火山方舟命名）；
//   - OPENAI_BASE_URL：基地址，默认官方 https://api.openai.com/v1；
//   - OPENAI_MODEL：模型名，默认 gpt-4o-mini；
//   - OPENAI_EMBEDDING_MODEL：向量模型名（可选）；
//   - OPENAI_TEMPERATURE：温度，默认 0.2；
//   - OPENAI_TIMEOUT_SECONDS：请求超时秒数，默认 60。
func DefaultConfigFromEnv() Config {
	return NormalizeConfig(Config{
		Provider:       "openai-compatible",
		APIKey:         firstEnv("OPENAI_API_KEY", "ARK_API_KEY"),
		BaseURL:        envOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		Model:          envOrDefault("OPENAI_MODEL", "gpt-4o-mini"),
		EmbeddingModel: strings.TrimSpace(os.Getenv("OPENAI_EMBEDDING_MODEL")),
		Temperature:    envFloatOrDefault("OPENAI_TEMPERATURE", 0.2),
		TimeoutSeconds: envIntOrDefault("OPENAI_TIMEOUT_SECONDS", 60),
	})
}

// DefaultEmbeddingConfigFromEnv 从环境变量读取 embedding 专用配置。
// 之所以与聊天模型配置分开，是因为向量服务往往部署在不同端点、
// 使用不同 Key：OPENAI_EMBEDDING_API_KEY / ARK_EMBEDDING_API_KEY、
// OPENAI_EMBEDDING_BASE_URL、OPENAI_EMBEDDING_MODEL、
// OPENAI_EMBEDDING_TIMEOUT_SECONDS（缺省时回退到 OPENAI_TIMEOUT_SECONDS）。
func DefaultEmbeddingConfigFromEnv() Config {
	embeddingModel := strings.TrimSpace(os.Getenv("OPENAI_EMBEDDING_MODEL"))
	return NormalizeConfig(Config{
		Provider:       "openai-compatible",
		APIKey:         firstEnv("OPENAI_EMBEDDING_API_KEY", "ARK_EMBEDDING_API_KEY"),
		BaseURL:        strings.TrimSpace(os.Getenv("OPENAI_EMBEDDING_BASE_URL")),
		Model:          embeddingModel,
		EmbeddingModel: embeddingModel,
		Temperature:    0.2,
		TimeoutSeconds: envIntOrDefault("OPENAI_EMBEDDING_TIMEOUT_SECONDS", envIntOrDefault("OPENAI_TIMEOUT_SECONDS", 60)),
	})
}

// NormalizeConfig 对配置做统一的归一化与兜底：去除首尾空白、
// BaseURL 去掉尾部斜杠（便于后续安全拼接路径）、缺省值填充
// （Provider/BaseURL/Model/Temperature/TimeoutSeconds）。
// 该函数在"构造客户端"与"每次请求前"都会调用，确保即使配置
// 来自数据库热更新也始终合法。
func NormalizeConfig(config Config) Config {
	config.Provider = strings.TrimSpace(config.Provider)
	if config.Provider == "" {
		config.Provider = "openai-compatible"
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL == "" {
		config.BaseURL = "https://api.openai.com/v1"
	}
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" {
		config.Model = "gpt-4o-mini"
	}
	config.EmbeddingModel = strings.TrimSpace(config.EmbeddingModel)
	if config.Temperature < 0 || config.Temperature > 2 {
		config.Temperature = 0.2
	}
	if config.TimeoutSeconds <= 0 {
		config.TimeoutSeconds = 60
	}
	return config
}

// firstEnv 按顺序返回第一个非空的环境变量值，用于实现
// "OPENAI_* 优先、ARK_* 兜底"的多命名兼容策略。
func firstEnv(names ...string) string {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value != "" {
			return value
		}
	}
	return ""
}

// envOrDefault 读取字符串环境变量，为空时返回默认值。
func envOrDefault(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

// envFloatOrDefault 读取浮点型环境变量，为空或解析失败时返回默认值。
func envFloatOrDefault(name string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// envIntOrDefault 读取整型环境变量，为空或解析失败时返回默认值。
func envIntOrDefault(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// Chat 发起一次非流式的 OpenAI 兼容 /chat/completions 请求：
// 构造请求体 -> POST -> 解析首个 choice 的文本与 tool_calls ->
// 转换为系统内部的 contracts.ModelResponse。
// 上下文取消/超时通过 ctx 与 http.Client 的 Timeout 双重保障。
func (m OpenAICompatibleModel) Chat(ctx context.Context, input Input) (contracts.ModelResponse, error) {
	// 每次请求前重新归一化配置，防御空字段（配置可能来自数据库热更新）。
	config := NormalizeConfig(m.config)
	if strings.TrimSpace(config.APIKey) == "" {
		// 缺少 API Key 时快速失败，并在错误信息里提示两种配置途径
		// （SQLite model_configs 或环境变量），方便用户自助排查。
		return contracts.ModelResponse{}, fmt.Errorf("model api key is required: save OPENAI_API_KEY in sqlite model_configs or set OPENAI_API_KEY/ARK_API_KEY")
	}
	// stream=false：构造非流式请求体（model/messages/temperature/tools）。
	payload := openAIChatPayload(config, input, false)
	body, err := json.Marshal(payload)
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	// chatEndpointURL 会智能拼接 /chat/completions 后缀（若 BaseURL 未包含）。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatEndpointURL(config.BaseURL), bytes.NewReader(body))
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	// 标准 OpenAI 鉴权方式：Bearer Token。
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+config.APIKey)
	res, err := m.client.Do(req)
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// 非 2xx 时把响应体全文带进错误信息（通常包含服务端的
		// JSON 错误详情，如限流、参数错误），便于定位问题。
		var errorBody bytes.Buffer
		_, _ = errorBody.ReadFrom(res.Body)
		return contracts.ModelResponse{}, fmt.Errorf("model request failed: %s %s", res.Status, errorBody.String())
	}
	// 用匿名结构体按需解码响应：只取 choices[].message 的 content、
	// tool_calls 以及 usage 的 token 统计，忽略其余字段。
	var response struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return contracts.ModelResponse{}, err
	}
	if len(response.Choices) == 0 {
		// 服务端未返回任何 choice 时视为空回复而非错误，
		// 交由上层 Agent 循环决定如何处理。
		return contracts.ModelResponse{}, nil
	}
	// 只消费第一个 choice（请求未设置 n，服务端默认也只返回一个）。
	message := response.Choices[0].Message
	// 把 OpenAI 原始 tool_calls 转换为内部 ModelToolCall：
	// arguments 是 JSON 字符串，需要反序列化成 map 供运行时直接使用；
	// 解析失败时静默容忍（保留空 map），避免模型输出畸形 JSON 导致整轮失败。
	toolCalls := make([]contracts.ModelToolCall, 0, len(message.ToolCalls))
	for _, raw := range message.ToolCalls {
		args := map[string]any{}
		if raw.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(raw.Function.Arguments), &args)
		}
		// 部分兼容端点不返回 tool call id，此处补一个本地生成的 id，
		// 保证后续 RoleTool 消息的 tool_call_id 能正确关联。
		id := raw.ID
		if id == "" {
			id = shared.NewID("call")
		}
		toolCalls = append(toolCalls, contracts.ModelToolCall{
			ID:        id,
			Name:      raw.Function.Name,
			Arguments: args,
		})
	}
	// 组装可直接追加回对话历史的 assistant 消息（含 tool_calls），
	// Agent 下一轮循环会原样带上它，满足 OpenAI 协议对消息序列的要求。
	modelMessage := contracts.LLMMessage{
		Role:      contracts.RoleAssistant,
		Content:   message.Content,
		ToolCalls: toolCalls,
	}
	return contracts.ModelResponse{
		Content:          message.Content,
		Message:          modelMessage,
		ToolCalls:        toolCalls,
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
	}, nil
}

// Embed 将一批文本转换为向量，走 OpenAI 兼容的 /embeddings 协议。
// 处理流程：
//  1. nil 接收者直接返回 (nil, nil)——embedding 未启用时调用方无需判空；
//  2. 校验 API Key 与 EmbeddingModel，缺失时报出带配置指引的错误；
//  3. 清洗输入：去除空白文本（空串会被部分服务端拒绝）；
//  4. 若端点是 Ark 多模态 embedding（路径含 /embeddings/multimodal），
//     切换到逐条请求的多模态输入格式；否则一次批量请求全部文本。
func (m *OpenAIEmbeddingClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if m == nil {
		return nil, nil
	}
	config := NormalizeConfig(m.config)
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("embedding api key is required: save an embedding model API key in sqlite model_configs or set OPENAI_EMBEDDING_API_KEY/ARK_EMBEDDING_API_KEY")
	}
	if strings.TrimSpace(config.EmbeddingModel) == "" {
		return nil, fmt.Errorf("embedding model is required: save a model_type=embedding config in sqlite model_configs or set OPENAI_EMBEDDING_MODEL")
	}
	// 过滤空文本，避免服务端因空输入报错，同时减少无效计费。
	clean := make([]string, 0, len(texts))
	for _, text := range texts {
		text = strings.TrimSpace(text)
		if text != "" {
			clean = append(clean, text)
		}
	}
	if len(clean) == 0 {
		return nil, nil
	}
	endpoint := embeddingEndpointURL(config.BaseURL)
	if isMultimodalEmbeddingEndpoint(endpoint) {
		// 多模态端点的 input 格式与标准端点不同且通常不支持批量文本，
		// 需要走逐条请求的专用路径。
		return m.embedMultimodalTexts(ctx, config, endpoint, clean)
	}
	// 标准 OpenAI embeddings：input 直接传字符串数组即可批量向量化。
	payload := map[string]any{
		"model": config.EmbeddingModel,
		"input": clean,
	}
	return m.postEmbeddingRequest(ctx, config, endpoint, payload, len(clean))
}

// embedMultimodalTexts 针对 Ark 多模态 embedding 端点逐条向量化文本。
// 多模态协议的 input 是内容分片数组（[{"type":"text","text":...}]），
// 一次请求只产出一个向量，因此必须循环调用并逐条校验返回数量。
func (m *OpenAIEmbeddingClient) embedMultimodalTexts(ctx context.Context, config Config, endpoint string, texts []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))
	for _, text := range texts {
		// 多模态输入格式：把纯文本包装成 type=text 的内容分片。
		payload := map[string]any{
			"model": config.EmbeddingModel,
			"input": []map[string]any{
				{
					"type": "text",
					"text": text,
				},
			},
		}
		result, err := m.postEmbeddingRequest(ctx, config, endpoint, payload, 1)
		if err != nil {
			return nil, err
		}
		if len(result) != 1 {
			return nil, fmt.Errorf("embedding count mismatch: got %d, want 1", len(result))
		}
		vectors = append(vectors, result[0])
	}
	return vectors, nil
}

// postEmbeddingRequest 是 embedding 请求的公共发送逻辑：
// 序列化 payload -> POST（Bearer 鉴权）-> 非 2xx 时带响应体报错 ->
// 交给 parseEmbeddingResponse 解析向量。expected 为期望的向量数量，
// 用于校验"单向量响应 vs 多输入请求"的不一致情况。
func (m *OpenAIEmbeddingClient) postEmbeddingRequest(ctx context.Context, config Config, endpoint string, payload map[string]any, expected int) ([][]float32, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+config.APIKey)
	res, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var errorBody bytes.Buffer
		_, _ = errorBody.ReadFrom(res.Body)
		return nil, fmt.Errorf("embedding request failed: %s %s", res.Status, errorBody.String())
	}
	var responseBody bytes.Buffer
	_, _ = responseBody.ReadFrom(res.Body)
	return parseEmbeddingResponse(responseBody.Bytes(), expected)
}

// parseEmbeddingResponse 解析 embedding 响应体，同时兼容两种 data 形态：
//  1. 标准 OpenAI 格式：data 为数组 [{"index":0,"embedding":[...]}, ...]，
//     按 index 字段还原顺序（服务端可能乱序返回），index 缺失时按位置兜底；
//  2. 某些兼容服务（如部分多模态端点）：data 为单个对象 {"embedding":[...]}。
//
// expected 用于一致性校验：若单对象响应却请求了多条输入，说明服务端
// 不支持批量，直接报错而不是静默丢数据。
func parseEmbeddingResponse(body []byte, expected int) ([][]float32, error) {
	// 先只取出 data 字段的原始 JSON，再尝试两种形态的解码。
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Data) == 0 {
		return nil, nil
	}
	// 形态一：数组格式（标准 OpenAI）。
	var listData []struct {
		Index     *int      `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(envelope.Data, &listData); err == nil {
		vectors := make([][]float32, len(listData))
		for i, item := range listData {
			// 优先使用服务端返回的 index 对齐输入顺序，
			// 这是 OpenAI 协议保证"输入-向量"对应关系的方式。
			index := i
			if item.Index != nil {
				index = *item.Index
			}
			if index >= 0 && index < len(vectors) {
				vectors[index] = item.Embedding
			}
		}
		// 压缩掉空槽位（index 越界或缺失导致的空向量）。
		return compactEmbeddingVectors(vectors), nil
	}
	// 形态二：单对象格式（部分兼容服务）。
	var singleData struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(envelope.Data, &singleData); err != nil {
		return nil, err
	}
	if len(singleData.Embedding) == 0 {
		return nil, nil
	}
	if expected > 1 {
		return nil, fmt.Errorf("embedding response returned one vector for %d requested inputs", expected)
	}
	return [][]float32{singleData.Embedding}, nil
}

// compactEmbeddingVectors 过滤掉空向量，只保留有效结果。
func compactEmbeddingVectors(vectors [][]float32) [][]float32 {
	out := make([][]float32, 0, len(vectors))
	for _, vector := range vectors {
		if len(vector) > 0 {
			out = append(out, vector)
		}
	}
	return out
}

// isMultimodalEmbeddingEndpoint 判断端点是否为 Ark 风格的多模态
// embedding 接口（路径包含 /embeddings/multimodal），该接口的
// 请求/响应格式与标准 OpenAI embeddings 不同，需要特殊处理。
func isMultimodalEmbeddingEndpoint(endpoint string) bool {
	return strings.Contains(strings.ToLower(endpoint), "/embeddings/multimodal")
}

// embeddingEndpointURL 由 BaseURL 推导 embeddings 端点：
// 若用户配置的 BaseURL 已经是完整的 embeddings 地址（以 /embeddings
// 结尾或路径中已包含 /embeddings/，如多模态端点），则原样使用；
// 否则自动追加 /embeddings。这样既支持"只填基地址"也支持"填完整 URL"。
func embeddingEndpointURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	lower := strings.ToLower(baseURL)
	if strings.HasSuffix(lower, "/embeddings") || strings.Contains(lower, "/embeddings/") {
		return baseURL
	}
	return baseURL + "/embeddings"
}

// chatEndpointURL 由 BaseURL 推导 chat completions 端点，
// 逻辑与 embeddingEndpointURL 相同：已含 /chat/completions 则原样使用，
// 否则自动追加，兼容用户填写"基地址"或"完整地址"两种习惯。
func chatEndpointURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	lower := strings.ToLower(baseURL)
	if strings.HasSuffix(lower, "/chat/completions") || strings.Contains(lower, "/chat/completions/") {
		return baseURL
	}
	return baseURL + "/chat/completions"
}

// ChatStream 发起一次流式（SSE）的 /chat/completions 请求。
// 与 Chat 的区别与要点：
//   - 请求体带 "stream": true，并声明 Accept: text/event-stream；
//   - 逐行扫描响应体，解析 "data: {...}" 事件，遇到 "data: [DONE]" 结束；
//   - 文本增量（delta.content）实时通过 onDelta 回调透传给上层
//     （HTTP 层再经 SSE 转发到前端，实现打字机效果）；
//   - 工具调用是"分片"下发的（同一个 tool call 的 id/name/arguments
//     可能分散在多个 chunk 中，按 index 归组），需要在本地累积拼接，
//     待流结束后统一解析成完整的 ModelToolCall；
//   - 最终返回与 Chat 相同结构的 ModelResponse，使上层对流式/非流式
//     可以统一处理（注意：此处不回填 usage，很多服务端流式不返回）。
func (m OpenAICompatibleModel) ChatStream(ctx context.Context, input Input, onDelta StreamHandler) (contracts.ModelResponse, error) {
	config := NormalizeConfig(m.config)
	if strings.TrimSpace(config.APIKey) == "" {
		return contracts.ModelResponse{}, fmt.Errorf("model api key is required: save OPENAI_API_KEY in sqlite model_configs or set OPENAI_API_KEY/ARK_API_KEY")
	}
	// stream=true：让服务端以 SSE 分片返回。
	payload := openAIChatPayload(config, input, true)
	body, err := json.Marshal(payload)
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatEndpointURL(config.BaseURL), bytes.NewReader(body))
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	req.Header.Set("content-type", "application/json")
	// 显式声明期望 SSE 响应；部分网关依赖该头才走流式路径。
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("authorization", "Bearer "+config.APIKey)
	res, err := m.client.Do(req)
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var errorBody bytes.Buffer
		_, _ = errorBody.ReadFrom(res.Body)
		return contracts.ModelResponse{}, fmt.Errorf("model stream request failed: %s %s", res.Status, errorBody.String())
	}

	// content 累积全部文本增量；toolParts 按 chunk 中的 index
	// 归组累积工具调用分片（id 通常只在首个分片出现，name/arguments
	// 则可能被拆成多段字符串逐步下发）；toolOrder 记录 index 首次
	// 出现的顺序，保证最终工具调用列表与模型输出顺序一致
	//（map 遍历无序，不能直接依赖 map）。
	var content strings.Builder
	type toolCallParts struct {
		id        string
		name      string
		arguments string
	}
	toolParts := map[int]*toolCallParts{}
	toolOrder := []int{}
	scanner := bufio.NewScanner(res.Body)
	// 扩大扫描缓冲到 4MB：单个 SSE 事件行可能包含很长的
	// arguments JSON（如大段代码），默认 64KB 上限会导致扫描失败。
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过 SSE 的空行（事件分隔符）与 ":" 开头的注释/心跳行。
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		// 只处理 data 字段，忽略 event/id/retry 等其他 SSE 字段。
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		// OpenAI 协议约定的流结束哨兵。
		if data == "[DONE]" {
			break
		}
		// 每个 data 载荷是一个 chat.completion.chunk JSON，
		// 按需解码 delta.content 与 delta.tool_calls。
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return contracts.ModelResponse{}, err
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				// 文本增量：既累积到完整内容，也立即回调给上层，
				// 让前端能实时看到生成过程；回调报错（如连接断开）
				// 时立刻终止流，避免继续消耗 token。
				content.WriteString(choice.Delta.Content)
				if onDelta != nil {
					if err := onDelta(StreamDelta{Content: choice.Delta.Content}); err != nil {
						return contracts.ModelResponse{}, err
					}
				}
			}
			// 工具调用分片：按 index 找到（或新建）对应的累积槽。
			// id 是"整体赋值"（只在某个分片出现一次），
			// name/arguments 是"字符串拼接"（可能被拆成任意多段）。
			for _, rawCall := range choice.Delta.ToolCalls {
				part := toolParts[rawCall.Index]
				if part == nil {
					part = &toolCallParts{}
					toolParts[rawCall.Index] = part
					toolOrder = append(toolOrder, rawCall.Index)
				}
				if rawCall.ID != "" {
					part.id = rawCall.ID
				}
				if rawCall.Function.Name != "" {
					part.name += rawCall.Function.Name
				}
				if rawCall.Function.Arguments != "" {
					part.arguments += rawCall.Function.Arguments
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return contracts.ModelResponse{}, err
	}

	// 流结束后，把累积完成的工具调用分片解析为完整的 ModelToolCall：
	// arguments 此时才是完整 JSON，可以安全反序列化（失败则容忍为空 map）；
	// 按 toolOrder 迭代以保持模型输出顺序。
	toolCalls := make([]contracts.ModelToolCall, 0, len(toolOrder))
	for _, index := range toolOrder {
		part := toolParts[index]
		args := map[string]any{}
		if strings.TrimSpace(part.arguments) != "" {
			_ = json.Unmarshal([]byte(part.arguments), &args)
		}
		// 与非流式路径一致：缺失 id 时本地补全，保证 tool 消息可关联。
		id := part.id
		if id == "" {
			id = shared.NewID("call")
		}
		toolCalls = append(toolCalls, contracts.ModelToolCall{
			ID:        id,
			Name:      part.name,
			Arguments: args,
		})
	}
	messageContent := content.String()
	// 与 Chat 相同：组装可回填对话历史的 assistant 消息。
	modelMessage := contracts.LLMMessage{
		Role:      contracts.RoleAssistant,
		Content:   messageContent,
		ToolCalls: toolCalls,
	}
	return contracts.ModelResponse{
		Content:   messageContent,
		Message:   modelMessage,
		ToolCalls: toolCalls,
	}, nil
}

// openAIChatPayload 构造 /chat/completions 的请求体：
//   - system prompt 固定作为 messages 数组的第一条 system 消息；
//   - 其后依次编码全部历史消息（用户/助手/工具）；
//   - 仅在流式时附加 "stream": true，仅在有工具时附加 "tools"
//     （空的 tools 数组会被部分服务端判为参数错误）。
func openAIChatPayload(config Config, input Input, stream bool) map[string]any {
	messages := []map[string]any{{"role": "system", "content": input.System}}
	for _, message := range input.Messages {
		messages = append(messages, encodeOpenAIMessage(message))
	}
	payload := map[string]any{
		"model":       config.Model,
		"messages":    messages,
		"temperature": config.Temperature,
	}
	if stream {
		payload["stream"] = true
	}
	if len(input.Tools) > 0 {
		payload["tools"] = input.Tools
	}
	return payload
}

// encodeOpenAIMessage 把内部 LLMMessage 编码为 OpenAI 协议的消息对象，
// 按角色区分编码规则：
//   - tool 消息：必须携带 tool_call_id 指明回应的是哪次工具调用；
//   - assistant 消息：若只有工具调用而无文本，content 需显式置为 null
//     （OpenAI 协议要求；空字符串在部分实现下会报错），tool_calls 中的
//     arguments 需重新序列化为 JSON 字符串（协议要求字符串而非对象）；
//   - 其他角色（user 等）：直接输出 content 文本。
func encodeOpenAIMessage(message contracts.LLMMessage) map[string]any {
	encoded := map[string]any{"role": string(message.Role)}
	switch message.Role {
	case contracts.RoleTool:
		encoded["tool_call_id"] = message.ToolCallID
		encoded["content"] = message.Content
	case contracts.RoleAssistant:
		// 纯工具调用消息：content 置 null 而非空串，符合协议语义。
		if message.Content == "" && len(message.ToolCalls) > 0 {
			encoded["content"] = nil
		} else {
			encoded["content"] = message.Content
		}
		if len(message.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				// 内部以 map 保存参数，回传给模型时按协议要求
				// 转回 JSON 字符串形式。
				args, _ := json.Marshal(call.Arguments)
				calls = append(calls, map[string]any{
					"id":   call.ID,
					"type": "function",
					"function": map[string]any{
						"name":      call.Name,
						"arguments": string(args),
					},
				})
			}
			encoded["tool_calls"] = calls
		}
	default:
		encoded["content"] = message.Content
	}
	return encoded
}
