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

	"code.byted.org/ai/lmy/apps/server/internal/contracts"
	"code.byted.org/ai/lmy/apps/server/internal/shared"
)

type Input struct {
	System   string
	Messages []contracts.LLMMessage
	Tools    []map[string]any
}

type Client interface {
	Chat(ctx context.Context, input Input) (contracts.ModelResponse, error)
}

type StreamDelta struct {
	Content string
}

type StreamHandler func(StreamDelta) error

type StreamingClient interface {
	Client
	ChatStream(ctx context.Context, input Input, onDelta StreamHandler) (contracts.ModelResponse, error)
}

type EmbeddingClient interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type Config struct {
	Provider       string
	APIKey         string
	BaseURL        string
	Model          string
	EmbeddingModel string
	Temperature    float64
	TimeoutSeconds int
}

type OpenAICompatibleModel struct {
	config Config
	client *http.Client
}

type OpenAIEmbeddingClient struct {
	config Config
	client *http.Client
}

func NewDefaultModel() Client {
	return NewOpenAICompatibleModel(DefaultConfigFromEnv())
}

func NewOpenAICompatibleModel(config Config) Client {
	config = NormalizeConfig(config)
	return OpenAICompatibleModel{
		config: config,
		client: &http.Client{Timeout: time.Duration(config.TimeoutSeconds) * time.Second},
	}
}

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

func DefaultConfigFromEnv() Config {
	return NormalizeConfig(Config{
		Provider:       "openai-compatible",
		APIKey:         firstEnv("OPENAI_API_KEY", "ARK_API_KEY"),
		BaseURL:        envOrDefault("OPENAI_BASE_URL", "https://ark-cn-beijing.bytedance.net/api/v3"),
		Model:          envOrDefault("OPENAI_MODEL", "ep-20260507115713-ltdzl"),
		EmbeddingModel: strings.TrimSpace(os.Getenv("OPENAI_EMBEDDING_MODEL")),
		Temperature:    envFloatOrDefault("OPENAI_TEMPERATURE", 0.2),
		TimeoutSeconds: envIntOrDefault("OPENAI_TIMEOUT_SECONDS", 60),
	})
}

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

func NormalizeConfig(config Config) Config {
	config.Provider = strings.TrimSpace(config.Provider)
	if config.Provider == "" {
		config.Provider = "openai-compatible"
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL == "" {
		config.BaseURL = "https://ark-cn-beijing.bytedance.net/api/v3"
	}
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" {
		config.Model = "ep-20260507115713-ltdzl"
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

func firstEnv(names ...string) string {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value != "" {
			return value
		}
	}
	return ""
}

func envOrDefault(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

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

func (m OpenAICompatibleModel) Chat(ctx context.Context, input Input) (contracts.ModelResponse, error) {
	config := NormalizeConfig(m.config)
	if strings.TrimSpace(config.APIKey) == "" {
		return contracts.ModelResponse{}, fmt.Errorf("model api key is required: save OPENAI_API_KEY in sqlite model_configs or set OPENAI_API_KEY/ARK_API_KEY")
	}
	payload := openAIChatPayload(config, input, false)
	body, err := json.Marshal(payload)
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+config.APIKey)
	res, err := m.client.Do(req)
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var errorBody bytes.Buffer
		_, _ = errorBody.ReadFrom(res.Body)
		return contracts.ModelResponse{}, fmt.Errorf("model request failed: %s %s", res.Status, errorBody.String())
	}
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
		return contracts.ModelResponse{}, nil
	}
	message := response.Choices[0].Message
	toolCalls := make([]contracts.ModelToolCall, 0, len(message.ToolCalls))
	for _, raw := range message.ToolCalls {
		args := map[string]any{}
		if raw.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(raw.Function.Arguments), &args)
		}
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
		return m.embedMultimodalTexts(ctx, config, endpoint, clean)
	}
	payload := map[string]any{
		"model": config.EmbeddingModel,
		"input": clean,
	}
	return m.postEmbeddingRequest(ctx, config, endpoint, payload, len(clean))
}

func (m *OpenAIEmbeddingClient) embedMultimodalTexts(ctx context.Context, config Config, endpoint string, texts []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))
	for _, text := range texts {
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

func parseEmbeddingResponse(body []byte, expected int) ([][]float32, error) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Data) == 0 {
		return nil, nil
	}
	var listData []struct {
		Index     *int      `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(envelope.Data, &listData); err == nil {
		vectors := make([][]float32, len(listData))
		for i, item := range listData {
			index := i
			if item.Index != nil {
				index = *item.Index
			}
			if index >= 0 && index < len(vectors) {
				vectors[index] = item.Embedding
			}
		}
		return compactEmbeddingVectors(vectors), nil
	}
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

func compactEmbeddingVectors(vectors [][]float32) [][]float32 {
	out := make([][]float32, 0, len(vectors))
	for _, vector := range vectors {
		if len(vector) > 0 {
			out = append(out, vector)
		}
	}
	return out
}

func isMultimodalEmbeddingEndpoint(endpoint string) bool {
	return strings.Contains(strings.ToLower(endpoint), "/embeddings/multimodal")
}

func embeddingEndpointURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	lower := strings.ToLower(baseURL)
	if strings.HasSuffix(lower, "/embeddings") || strings.Contains(lower, "/embeddings/") {
		return baseURL
	}
	return baseURL + "/embeddings"
}

func (m OpenAICompatibleModel) ChatStream(ctx context.Context, input Input, onDelta StreamHandler) (contracts.ModelResponse, error) {
	config := NormalizeConfig(m.config)
	if strings.TrimSpace(config.APIKey) == "" {
		return contracts.ModelResponse{}, fmt.Errorf("model api key is required: save OPENAI_API_KEY in sqlite model_configs or set OPENAI_API_KEY/ARK_API_KEY")
	}
	payload := openAIChatPayload(config, input, true)
	body, err := json.Marshal(payload)
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return contracts.ModelResponse{}, err
	}
	req.Header.Set("content-type", "application/json")
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

	var content strings.Builder
	type toolCallParts struct {
		id        string
		name      string
		arguments string
	}
	toolParts := map[int]*toolCallParts{}
	toolOrder := []int{}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
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
				content.WriteString(choice.Delta.Content)
				if onDelta != nil {
					if err := onDelta(StreamDelta{Content: choice.Delta.Content}); err != nil {
						return contracts.ModelResponse{}, err
					}
				}
			}
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

	toolCalls := make([]contracts.ModelToolCall, 0, len(toolOrder))
	for _, index := range toolOrder {
		part := toolParts[index]
		args := map[string]any{}
		if strings.TrimSpace(part.arguments) != "" {
			_ = json.Unmarshal([]byte(part.arguments), &args)
		}
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

func encodeOpenAIMessage(message contracts.LLMMessage) map[string]any {
	encoded := map[string]any{"role": string(message.Role)}
	switch message.Role {
	case contracts.RoleTool:
		encoded["tool_call_id"] = message.ToolCallID
		encoded["content"] = message.Content
	case contracts.RoleAssistant:
		if message.Content == "" && len(message.ToolCalls) > 0 {
			encoded["content"] = nil
		} else {
			encoded["content"] = message.Content
		}
		if len(message.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
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
