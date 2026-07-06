package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"code.byted.org/ai/lmy/apps/server/internal/agent"
	"code.byted.org/ai/lmy/apps/server/internal/claudecode"
	"code.byted.org/ai/lmy/apps/server/internal/contracts"
	statedb "code.byted.org/ai/lmy/apps/server/internal/infra/db"
	"code.byted.org/ai/lmy/apps/server/internal/knowledge"
	"code.byted.org/ai/lmy/apps/server/internal/mcp"
	"code.byted.org/ai/lmy/apps/server/internal/memory"
	"code.byted.org/ai/lmy/apps/server/internal/model"
	"code.byted.org/ai/lmy/apps/server/internal/runtime"
	"code.byted.org/ai/lmy/apps/server/internal/shared"
	"code.byted.org/ai/lmy/apps/server/internal/skills"
	"code.byted.org/ai/lmy/apps/server/internal/state"
	"code.byted.org/ai/lmy/apps/server/internal/tools"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
)

type HTTPServer struct {
	store                 memory.Store
	runtime               *runtime.Runtime
	skillRegistry         *skills.Registry
	skillConfig           *skills.ConfigStore
	knowledgeStore        *knowledge.Store
	stateDB               *sql.DB
	stateStore            *state.Store
	agent                 *agent.Agent
	startupContext        claudecode.StartupContext
	defaultConversationID string
	staticDir             string
}

type knowledgeItemResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType,omitempty"`
	ImportedAt  string `json:"importedAt"`
}

type toolConfigItemResponse struct {
	Tool           *contracts.RuntimeTool `json:"tool,omitempty"`
	ToolName       string                 `json:"toolName"`
	Enabled        bool                   `json:"enabled"`
	ApprovalPolicy string                 `json:"approvalPolicy"`
	Config         map[string]any         `json:"config"`
	UpdatedAt      string                 `json:"updatedAt,omitempty"`
	Registered     bool                   `json:"registered"`
}

type modelConfigResponse struct {
	ID             string         `json:"id"`
	Provider       string         `json:"provider"`
	BaseURL        string         `json:"baseURL"`
	Model          string         `json:"model"`
	Temperature    float64        `json:"temperature"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
	Extra          map[string]any `json:"extra"`
	UpdatedAt      string         `json:"updatedAt,omitempty"`
	APIKeySet      bool           `json:"apiKeySet"`
	APIKeyPreview  string         `json:"apiKeyPreview,omitempty"`
}

func NewHTTPServer(staticDir string) *HTTPServer {
	startupContext := claudecode.LoadStartupContext()
	stateDB, err := statedb.Open(startupContext.StateDBPath())
	if err != nil {
		stateDB = nil
	}
	stateStore := state.NewStore(stateDB)
	modelConfig := model.DefaultConfigFromEnv()
	if stateStore != nil {
		_ = stateStore.SyncMCPServers(startupContext.MCP.Servers)
		if servers, err := stateStore.EnabledMCPServers(); err == nil {
			startupContext.MCP.Servers = servers
		}
		_ = stateStore.SeedModelConfig(modelConfigRecordFromConfig("default", modelConfig))
		if record, ok, err := stateStore.ModelConfig("default"); err == nil && ok {
			modelConfig = modelConfigFromRecord(record)
		}
	}
	var store memory.Store = memory.NewInMemoryStore()
	if stateDB != nil {
		if persistentStore, err := memory.NewPersistentStoreWithDB(stateDB); err == nil {
			store = persistentStore
		}
	}
	registry := runtime.NewRuntime()
	if stateStore != nil {
		registry.SetToolConfigProvider(stateStore)
	}
	skillRegistry := skills.NewRegistry()
	_ = skillRegistry.LoadFromDirectories(skillDirs(startupContext.SkillDirectories))
	skillConfig := skills.NewConfigStore(skillRegistry)
	if stateDB != nil {
		skillConfig = skills.NewSQLiteConfigStore(skillRegistry, stateDB)
	}
	knowledgeStore, err := knowledge.NewStore(startupContext.KnowledgeDir())
	if err != nil {
		knowledgeStore, _ = knowledge.NewStore("")
	}
	tools.RegisterCoreCoder(registry)
	tools.RegisterGeneric(registry, store)
	tools.RegisterInteractiveWeb(registry)
	mcp.RegisterConfiguredServers(context.Background(), registry, startupContext.MCP)
	agent := agent.NewAgent(store, registry, model.NewOpenAICompatibleModel(modelConfig), skillRegistry, skillConfig, startupContext)
	registry.Register(tools.NewAgentTool(agent))
	defaultConversationID := ""
	if existing := store.ListConversations(); len(existing) > 0 {
		defaultConversationID = existing[0].ID
	} else {
		defaultConversation := store.CreateConversation("New conversation")
		defaultConversationID = defaultConversation.ID
	}
	return &HTTPServer{
		store:                 store,
		runtime:               registry,
		skillRegistry:         skillRegistry,
		skillConfig:           skillConfig,
		knowledgeStore:        knowledgeStore,
		stateDB:               stateDB,
		stateStore:            stateStore,
		agent:                 agent,
		startupContext:        startupContext,
		defaultConversationID: defaultConversationID,
		staticDir:             staticDir,
	}
}

func skillDirs(dirs []claudecode.SkillDirectory) []skills.Directory {
	out := make([]skills.Directory, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, skills.Directory{Path: dir.Path, Scope: dir.Scope})
	}
	return out
}

func (s *HTTPServer) Register(h *server.Hertz) {
	h.GET("/health", s.handleHealth)
	h.GET("/api/conversations", s.handleListConversations)
	h.POST("/api/conversations", s.handleCreateConversation)
	h.DELETE("/api/conversations/:conversationId", s.handleDeleteConversation)
	h.GET("/api/conversations/:conversationId/messages", s.handleMessages)
	h.GET("/api/conversations/:conversationId/traces", s.handleTraces)
	h.POST("/api/conversations/:conversationId/chat", s.handleChat)
	h.POST("/api/conversations/:conversationId/chat/stream", s.handleChatStream)
	h.GET("/api/model/config", s.handleModelConfig)
	h.PUT("/api/model/config", s.handleUpdateModelConfig)
	h.GET("/api/model/configs", s.handleModelConfigs)
	h.PUT("/api/model/configs/:configId", s.handleUpdateModelConfigByID)
	h.DELETE("/api/model/configs/:configId", s.handleDeleteModelConfig)
	h.GET("/api/tools", s.handleTools)
	h.GET("/api/tools/config", s.handleToolConfigs)
	h.PUT("/api/tools/config", s.handleUpdateToolConfig)
	h.GET("/api/mcp/servers", s.handleMCPServers)
	h.GET("/api/mcp/servers/config", s.handleMCPServerConfigs)
	h.PUT("/api/mcp/servers/config", s.handleUpdateMCPServerConfig)
	h.GET("/api/skills", s.handleSkills)
	h.PUT("/api/skills/config", s.handleUpdateSkillConfig)
	h.GET("/api/skills/:skillName", s.handleSkillDetail)
	h.DELETE("/api/skills/:skillName", s.handleDeleteSkill)
	h.GET("/api/knowledge", s.handleKnowledgeList)
	h.POST("/api/knowledge/import", s.handleKnowledgeImport)
	h.DELETE("/api/knowledge/:itemId", s.handleKnowledgeDelete)

	h.GET("/", s.serveStaticFile("index.html"))
	h.GET("/main.js", s.serveStaticFile("main.js"))
	h.GET("/main.js.map", s.serveStaticFile("main.js.map"))
	h.GET("/markdown.js", s.serveStaticFile("markdown.js"))
	h.GET("/markdown.js.map", s.serveStaticFile("markdown.js.map"))
	h.GET("/styles.css", s.serveStaticFile("styles.css"))
}

func (s *HTTPServer) handleHealth(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, utils.H{"ok": true})
}

func (s *HTTPServer) handleListConversations(ctx context.Context, c *app.RequestContext) {
	conversations := s.visibleConversations()
	defaultConversationID := s.defaultConversationID
	if len(conversations) > 0 {
		defaultConversationID = conversations[0].ID
	}
	c.JSON(consts.StatusOK, utils.H{
		"conversations":         conversations,
		"defaultConversationId": defaultConversationID,
	})
}

func (s *HTTPServer) handleCreateConversation(ctx context.Context, c *app.RequestContext) {
	var body struct {
		Title string `json:"title"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	if reusable, ok := s.reusableInitialConversation(body.Title); ok {
		c.JSON(consts.StatusOK, utils.H{"conversation": reusable})
		return
	}
	conversation := s.store.CreateConversation(body.Title)
	c.JSON(consts.StatusCreated, utils.H{"conversation": conversation})
}

func (s *HTTPServer) handleDeleteConversation(ctx context.Context, c *app.RequestContext) {
	conversationID := strings.TrimSpace(c.Param("conversationId"))
	if conversationID == "" {
		writeHertzError(c, consts.StatusBadRequest, "conversationId is required")
		return
	}
	if !s.store.DeleteConversation(conversationID) {
		writeHertzError(c, consts.StatusNotFound, "conversation not found")
		return
	}
	conversations := s.visibleConversations()
	defaultConversationID := ""
	if len(conversations) > 0 {
		defaultConversationID = conversations[0].ID
	}
	s.defaultConversationID = defaultConversationID
	c.JSON(consts.StatusOK, utils.H{"conversations": conversations, "defaultConversationId": defaultConversationID})
}

func (s *HTTPServer) handleMessages(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	c.JSON(consts.StatusOK, utils.H{"messages": s.store.Messages(conversationID)})
}

func (s *HTTPServer) handleTraces(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	c.JSON(consts.StatusOK, utils.H{"traces": s.store.Traces(conversationID)})
}

func (s *HTTPServer) handleChat(ctx context.Context, c *app.RequestContext) {
	var body struct {
		Message       string `json:"message"`
		ModelConfigID string `json:"modelConfigId"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeHertzError(c, consts.StatusBadRequest, "message is required")
		return
	}

	conversationID := c.Param("conversationId")
	modelClient, err := s.modelClientForRequest(body.ModelConfigID)
	if err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	output, err := s.agent.Run(ctx, agent.RunInput{ConversationID: conversationID, UserMessage: body.Message, Model: modelClient})
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, output)
}

func (s *HTTPServer) handleChatStream(ctx context.Context, c *app.RequestContext) {
	var body struct {
		Message       string `json:"message"`
		ModelConfigID string `json:"modelConfigId"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeHertzError(c, consts.StatusBadRequest, "message is required")
		return
	}

	writer := sse.NewWriter(c)
	c.Response.Header.Set("X-Accel-Buffering", "no")
	defer writer.Close()

	conversationID := c.Param("conversationId")
	modelClient, err := s.modelClientForRequest(body.ModelConfigID)
	if err != nil {
		_ = writeSSEEvent(writer, contracts.AgentStreamEvent{
			Type:      "error",
			Title:     "请求失败",
			Content:   err.Error(),
			CreatedAt: shared.Now(),
		})
		return
	}
	output, err := s.agent.RunStream(ctx, agent.RunInput{ConversationID: conversationID, UserMessage: body.Message, Model: modelClient}, func(event contracts.AgentStreamEvent) error {
		return writeSSEEvent(writer, event)
	})
	if err != nil {
		_ = writeSSEEvent(writer, contracts.AgentStreamEvent{
			Type:      "error",
			Title:     "请求失败",
			Content:   err.Error(),
			TraceID:   output.Trace.ID,
			CreatedAt: shared.Now(),
		})
		return
	}
	_ = writeSSEEvent(writer, contracts.AgentStreamEvent{
		Type:      "done",
		Title:     "完成",
		Content:   "stream completed",
		Message:   &output.AssistantMessage,
		TraceID:   output.Trace.ID,
		CreatedAt: shared.Now(),
	})
}

func (s *HTTPServer) handleModelConfig(ctx context.Context, c *app.RequestContext) {
	record, err := s.currentModelConfigRecord()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"config": modelConfigResponseFromRecord(record)})
}

func (s *HTTPServer) handleModelConfigs(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		c.JSON(consts.StatusOK, utils.H{"configs": []modelConfigResponse{modelConfigResponseFromRecord(modelConfigRecordFromConfig("default", model.DefaultConfigFromEnv()))}, "defaultConfigId": "default"})
		return
	}
	if _, err := s.currentModelConfigRecord(); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	records, err := s.stateStore.ListModelConfigs()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"configs": modelConfigResponses(records), "defaultConfigId": "default"})
}

func (s *HTTPServer) handleUpdateModelConfig(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "state store is unavailable")
		return
	}
	s.handleSaveModelConfig(c, "default")
}

func (s *HTTPServer) handleUpdateModelConfigByID(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "state store is unavailable")
		return
	}
	s.handleSaveModelConfig(c, c.Param("configId"))
}

func (s *HTTPServer) handleDeleteModelConfig(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "state store is unavailable")
		return
	}
	configID := strings.TrimSpace(c.Param("configId"))
	if configID == "" {
		writeHertzError(c, consts.StatusBadRequest, "model config id is required")
		return
	}
	if configID == "default" {
		writeHertzError(c, consts.StatusBadRequest, "default model config cannot be deleted")
		return
	}
	if err := s.stateStore.DeleteModelConfig(configID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeHertzError(c, consts.StatusNotFound, "model config not found")
			return
		}
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	records, err := s.stateStore.ListModelConfigs()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"configs": modelConfigResponses(records), "defaultConfigId": "default"})
}

func (s *HTTPServer) handleSaveModelConfig(c *app.RequestContext, configID string) {
	var body struct {
		ID             *string        `json:"id"`
		Provider       *string        `json:"provider"`
		APIKey         *string        `json:"apiKey"`
		BaseURL        *string        `json:"baseURL"`
		Model          *string        `json:"model"`
		Temperature    *float64       `json:"temperature"`
		TimeoutSeconds *int           `json:"timeoutSeconds"`
		Extra          map[string]any `json:"extra"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	configID = strings.TrimSpace(configID)
	if body.ID != nil && configID == "" {
		configID = *body.ID
	}
	configID = strings.TrimSpace(configID)
	if configID == "" {
		writeHertzError(c, consts.StatusBadRequest, "model config id is required")
		return
	}
	record := modelConfigRecordFromConfig(configID, model.DefaultConfigFromEnv())
	if existing, ok, err := s.stateStore.ModelConfig(configID); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	} else if ok {
		record = existing
	}
	record.ID = configID
	if body.Provider != nil {
		record.Provider = *body.Provider
	}
	if body.APIKey != nil {
		record.APIKey = *body.APIKey
	}
	if body.BaseURL != nil {
		record.BaseURL = *body.BaseURL
	}
	if body.Model != nil {
		record.Model = *body.Model
	}
	if body.Temperature != nil {
		record.Temperature = *body.Temperature
	}
	if body.TimeoutSeconds != nil {
		record.TimeoutSeconds = *body.TimeoutSeconds
	}
	if body.Extra != nil {
		record.Extra = body.Extra
	}
	if err := s.stateStore.SaveModelConfig(record); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	if saved, ok, err := s.stateStore.ModelConfig(record.ID); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	} else if ok {
		record = saved
	}
	if s.agent != nil && record.ID == "default" {
		s.agent.SetModel(model.NewOpenAICompatibleModel(modelConfigFromRecord(record)))
	}
	c.JSON(consts.StatusOK, utils.H{"config": modelConfigResponseFromRecord(record)})
}

func (s *HTTPServer) handleTools(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, utils.H{"tools": s.runtime.ListRegisteredTools()})
}

func (s *HTTPServer) handleToolConfigs(ctx context.Context, c *app.RequestContext) {
	s.writeToolConfigs(c)
}

func (s *HTTPServer) handleUpdateToolConfig(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "state store is unavailable")
		return
	}
	var body struct {
		ToolName       string         `json:"toolName"`
		Enabled        *bool          `json:"enabled"`
		ApprovalPolicy string         `json:"approvalPolicy"`
		Config         map[string]any `json:"config"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	body.ToolName = strings.TrimSpace(body.ToolName)
	if body.ToolName == "" {
		writeHertzError(c, consts.StatusBadRequest, "toolName is required")
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.Config == nil {
		body.Config = map[string]any{}
	}
	if err := s.stateStore.SaveToolConfig(state.ToolConfigRecord{
		ToolName:       body.ToolName,
		Enabled:        enabled,
		ApprovalPolicy: body.ApprovalPolicy,
		Config:         body.Config,
	}); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	s.writeToolConfigs(c)
}

func (s *HTTPServer) handleMCPServers(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, utils.H{
		"servers": s.startupContext.MCP.Servers,
	})
}

func (s *HTTPServer) handleMCPServerConfigs(ctx context.Context, c *app.RequestContext) {
	s.writeMCPServerConfigs(c, false)
}

func (s *HTTPServer) handleUpdateMCPServerConfig(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "state store is unavailable")
		return
	}
	var body struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeHertzError(c, consts.StatusBadRequest, "name is required")
		return
	}
	if body.Enabled == nil {
		writeHertzError(c, consts.StatusBadRequest, "enabled is required")
		return
	}
	if err := s.stateStore.SetMCPServerEnabled(body.Name, *body.Enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeHertzError(c, consts.StatusNotFound, "mcp server not found")
			return
		}
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	s.writeMCPServerConfigs(c, true)
}

func (s *HTTPServer) writeToolConfigs(c *app.RequestContext) {
	configs := map[string]state.ToolConfigRecord{}
	if s.stateStore != nil {
		loaded, err := s.stateStore.ListToolConfigs()
		if err != nil {
			writeHertzError(c, consts.StatusInternalServerError, err.Error())
			return
		}
		configs = loaded
	}
	items := []toolConfigItemResponse{}
	seen := map[string]struct{}{}
	for _, tool := range s.runtime.ListRegisteredTools() {
		toolCopy := tool
		record, ok := configs[tool.Name]
		if !ok {
			record = state.ToolConfigRecord{
				ToolName:       tool.Name,
				Enabled:        true,
				ApprovalPolicy: "auto",
				Config:         map[string]any{},
			}
		}
		if record.Config == nil {
			record.Config = map[string]any{}
		}
		items = append(items, toolConfigItemResponse{
			Tool:           &toolCopy,
			ToolName:       tool.Name,
			Enabled:        record.Enabled,
			ApprovalPolicy: nonEmpty(record.ApprovalPolicy, "auto"),
			Config:         record.Config,
			UpdatedAt:      record.UpdatedAt,
			Registered:     true,
		})
		seen[tool.Name] = struct{}{}
	}
	for name, record := range configs {
		if _, ok := seen[name]; ok {
			continue
		}
		if record.Config == nil {
			record.Config = map[string]any{}
		}
		items = append(items, toolConfigItemResponse{
			ToolName:       name,
			Enabled:        record.Enabled,
			ApprovalPolicy: nonEmpty(record.ApprovalPolicy, "auto"),
			Config:         record.Config,
			UpdatedAt:      record.UpdatedAt,
			Registered:     false,
		})
	}
	c.JSON(consts.StatusOK, utils.H{"tools": items})
}

func (s *HTTPServer) writeMCPServerConfigs(c *app.RequestContext, restartRequired bool) {
	if s.stateStore == nil {
		c.JSON(consts.StatusOK, utils.H{"servers": []state.MCPServerConfigRecord{}, "restartRequired": restartRequired})
		return
	}
	servers, err := s.stateStore.ListMCPServerConfigs()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"servers": servers, "restartRequired": restartRequired})
}

func (s *HTTPServer) handleSkills(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, utils.H{"skills": s.skillConfig.List(s.skillRegistry)})
}

func (s *HTTPServer) handleUpdateSkillConfig(ctx context.Context, c *app.RequestContext) {
	var body struct {
		EnabledSkills []string `json:"enabledSkills"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	updated := s.skillConfig.SetEnabled(s.skillRegistry, body.EnabledSkills)
	if err := s.skillConfig.Save(s.skillRegistry); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"skills": updated})
}

func (s *HTTPServer) handleSkillDetail(ctx context.Context, c *app.RequestContext) {
	name := c.Param("skillName")
	skill, ok := s.skillRegistry.Get(name)
	if !ok {
		writeHertzError(c, consts.StatusNotFound, "skill not found")
		return
	}
	detail := skill.Detail()
	enabled := s.skillConfig.EnabledMap()
	deleted := s.skillConfig.DeletedMap()
	if deleted[strings.ToLower(detail.Name)] {
		writeHertzError(c, consts.StatusNotFound, "skill not found")
		return
	}
	detail.Enabled = enabled[strings.ToLower(detail.Name)]
	c.JSON(consts.StatusOK, utils.H{"skill": detail})
}

func (s *HTTPServer) handleDeleteSkill(ctx context.Context, c *app.RequestContext) {
	name := c.Param("skillName")
	updated, ok := s.skillConfig.DeleteOne(s.skillRegistry, name)
	if !ok {
		writeHertzError(c, consts.StatusNotFound, "skill not found")
		return
	}
	if err := s.skillConfig.Save(s.skillRegistry); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"skills": updated})
}

func (s *HTTPServer) handleKnowledgeList(ctx context.Context, c *app.RequestContext) {
	if s.knowledgeStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "knowledge store is unavailable")
		return
	}
	items, err := s.knowledgeStore.List()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"items": knowledgeResponseItems(items)})
}

func (s *HTTPServer) handleKnowledgeImport(ctx context.Context, c *app.RequestContext) {
	if s.knowledgeStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "knowledge store is unavailable")
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		writeHertzError(c, consts.StatusBadRequest, "file is required")
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	defer file.Close()
	item, err := s.knowledgeStore.Import(fileHeader.Filename, fileHeader.Header.Get("Content-Type"), file)
	if err != nil {
		status := consts.StatusInternalServerError
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "exceeds") {
			status = consts.StatusBadRequest
		}
		writeHertzError(c, status, err.Error())
		return
	}
	items, err := s.knowledgeStore.List()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusCreated, utils.H{"item": knowledgeResponseItem(item), "items": knowledgeResponseItems(items)})
}

func (s *HTTPServer) handleKnowledgeDelete(ctx context.Context, c *app.RequestContext) {
	if s.knowledgeStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "knowledge store is unavailable")
		return
	}
	itemID := strings.TrimSpace(c.Param("itemId"))
	if itemID == "" {
		writeHertzError(c, consts.StatusBadRequest, "itemId is required")
		return
	}
	deleted, err := s.knowledgeStore.Delete(itemID)
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeHertzError(c, consts.StatusNotFound, "knowledge item not found")
		return
	}
	items, err := s.knowledgeStore.List()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"items": knowledgeResponseItems(items)})
}

func knowledgeResponseItems(items []knowledge.Item) []knowledgeItemResponse {
	out := make([]knowledgeItemResponse, 0, len(items))
	for _, item := range items {
		out = append(out, knowledgeResponseItem(item))
	}
	return out
}

func knowledgeResponseItem(item knowledge.Item) knowledgeItemResponse {
	return knowledgeItemResponse{
		ID:          item.ID,
		Name:        item.Name,
		Size:        item.Size,
		ContentType: item.ContentType,
		ImportedAt:  item.ImportedAt,
	}
}

func (s *HTTPServer) visibleConversations() []contracts.Conversation {
	items := s.store.ListConversations()
	out := make([]contracts.Conversation, 0, len(items))
	seenInitial := false
	for _, conversation := range items {
		if isInitialConversation(conversation, s.store.Messages(conversation.ID)) {
			if seenInitial {
				continue
			}
			seenInitial = true
		}
		out = append(out, conversation)
	}
	return out
}

func (s *HTTPServer) reusableInitialConversation(title string) (contracts.Conversation, bool) {
	title = strings.TrimSpace(title)
	if title != "" && !strings.EqualFold(title, "New conversation") {
		return contracts.Conversation{}, false
	}
	for _, conversation := range s.store.ListConversations() {
		if isInitialConversation(conversation, s.store.Messages(conversation.ID)) {
			return conversation, true
		}
	}
	return contracts.Conversation{}, false
}

func isInitialConversation(conversation contracts.Conversation, messages []contracts.Message) bool {
	return strings.TrimSpace(conversation.Title) == "New conversation" && len(messages) == 0
}

func (s *HTTPServer) currentModelConfigRecord() (state.ModelConfigRecord, error) {
	defaultRecord := modelConfigRecordFromConfig("default", model.DefaultConfigFromEnv())
	if s.stateStore == nil {
		return defaultRecord, nil
	}
	if err := s.stateStore.SeedModelConfig(defaultRecord); err != nil {
		return state.ModelConfigRecord{}, err
	}
	record, ok, err := s.stateStore.ModelConfig(defaultRecord.ID)
	if err != nil {
		return state.ModelConfigRecord{}, err
	}
	if ok {
		return record, nil
	}
	return defaultRecord, nil
}

func modelConfigRecordFromConfig(id string, config model.Config) state.ModelConfigRecord {
	config = model.NormalizeConfig(config)
	return state.ModelConfigRecord{
		ID:             id,
		Provider:       config.Provider,
		APIKey:         config.APIKey,
		BaseURL:        config.BaseURL,
		Model:          config.Model,
		Temperature:    config.Temperature,
		TimeoutSeconds: config.TimeoutSeconds,
		Extra:          map[string]any{},
	}
}

func modelConfigFromRecord(record state.ModelConfigRecord) model.Config {
	return model.NormalizeConfig(model.Config{
		Provider:       record.Provider,
		APIKey:         record.APIKey,
		BaseURL:        record.BaseURL,
		Model:          record.Model,
		Temperature:    record.Temperature,
		TimeoutSeconds: record.TimeoutSeconds,
	})
}

func modelConfigResponseFromRecord(record state.ModelConfigRecord) modelConfigResponse {
	return modelConfigResponse{
		ID:             record.ID,
		Provider:       record.Provider,
		BaseURL:        record.BaseURL,
		Model:          record.Model,
		Temperature:    record.Temperature,
		TimeoutSeconds: record.TimeoutSeconds,
		Extra:          record.Extra,
		UpdatedAt:      record.UpdatedAt,
		APIKeySet:      strings.TrimSpace(record.APIKey) != "",
		APIKeyPreview:  secretPreview(record.APIKey),
	}
}

func modelConfigResponses(records []state.ModelConfigRecord) []modelConfigResponse {
	out := make([]modelConfigResponse, 0, len(records))
	for _, record := range records {
		out = append(out, modelConfigResponseFromRecord(record))
	}
	return out
}

func (s *HTTPServer) modelClientForRequest(configID string) (model.Client, error) {
	configID = strings.TrimSpace(configID)
	if configID == "" {
		configID = "default"
	}
	record := modelConfigRecordFromConfig("default", model.DefaultConfigFromEnv())
	if s.stateStore != nil {
		if err := s.stateStore.SeedModelConfig(record); err != nil {
			return nil, err
		}
		loaded, ok, err := s.stateStore.ModelConfig(configID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("model config %q not found", configID)
		}
		record = loaded
	} else if configID != "default" {
		return nil, fmt.Errorf("model config %q not found", configID)
	}
	return model.NewOpenAICompatibleModel(modelConfigFromRecord(record)), nil
}

func secretPreview(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "********"
	}
	return value[:4] + "..." + value[len(value)-4:]
}

func nonEmpty(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (s *HTTPServer) serveStaticFile(name string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Response.Header.Set("Cache-Control", "no-cache")
		c.File(filepath.Join(s.staticDir, name))
	}
}

func writeHertzError(c *app.RequestContext, status int, message string) {
	c.JSON(status, utils.H{"error": message})
}

func writeSSEEvent(writer *sse.Writer, event contracts.AgentStreamEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = shared.Now()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return writer.WriteEvent("", event.Type, payload)
}
