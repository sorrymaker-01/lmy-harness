// Package httpapi 是整个服务端的 HTTP 接入层（基于 CloudWeGo Hertz 框架）。
//
// 该包负责：
//   - 组装并持有所有下层子系统（会话存储 memory、工具运行时 runtime、技能 skills、
//     知识库 knowledge、状态库 state、MCP 服务、Agent 编排器等）；
//   - 注册全部 REST/SSE 路由，把 HTTP 请求翻译成对 Agent 与各子系统的调用；
//   - 通过 SSE（Server-Sent Events）向前端流式推送 Agent 执行过程中的事件；
//   - 托管前端构建产物（index.html / main.js / styles.css 等静态资源）。
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/agent"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/claudecode"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	statedb "github.com/sorrymaker-01/lmy-harness/apps/server/internal/infra/db"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/knowledge"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/mcp"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/memory"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/model"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/skills"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/state"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/tools"
)

const (
	// defaultModelConfigID 是"默认对话模型"配置在 state 库中的固定主键。
	// 该配置由环境变量播种（seed），且不可删除，聊天请求未指定模型时兜底使用它。
	defaultModelConfigID = "default"
	// defaultEmbeddingModelConfig 是从环境变量播种的默认向量化（embedding）模型配置的固定主键。
	defaultEmbeddingModelConfig = "default-embedding"
	// reasoningModelType 表示可用于对话/推理的模型类型；只有该类型的配置才能参与聊天。
	reasoningModelType = "reasoning"
	// embeddingModelType 表示仅用于知识库向量化检索的 embedding 模型类型。
	embeddingModelType = "embedding"
	// initialConversationTitle 是新建会话的默认中文标题；
	// 标题等于它且没有任何消息的会话被视为"未使用的初始会话"，可被复用而不重复创建。
	initialConversationTitle = "新对话"
	// legacyInitialTitle 是历史版本使用的英文默认标题，为兼容旧数据同样按初始会话处理。
	legacyInitialTitle = "New conversation"
)

// HTTPServer 是 HTTP 接入层的核心对象，聚合了服务端全部子系统的引用。
// 它由 NewHTTPServer 一次性构建，之后通过 Register 把各 handler 挂载到 Hertz 路由上。
// 所有 handler 都是 HTTPServer 的方法，共享这里持有的依赖。
type HTTPServer struct {
	store                 memory.Store              // 会话与消息存储（内存实现或基于 SQLite 的持久化实现）
	runtime               *runtime.Runtime          // 工具运行时注册表：内置工具 + MCP 工具都注册在这里
	skillRegistry         *skills.Registry          // 技能（skill）注册表，从技能目录扫描加载
	skillConfig           *skills.ConfigStore       // 技能启用/删除状态的配置存储（可落 SQLite）
	knowledgeStore        *knowledge.Store          // 知识库存储与 RAG 检索入口
	stateDB               *sql.DB                   // 底层 SQLite 连接，被 stateStore/knowledge/memory 共用；打不开时为 nil
	stateStore            *state.Store              // 结构化状态库：模型配置、工具配置、MCP 配置、多模型轮次/回答记录
	agent                 *agent.Agent              // Agent 编排器：负责一次对话轮的完整推理-工具调用循环
	startupContext        claudecode.StartupContext // 启动上下文：数据目录、技能目录、MCP 服务器等启动期配置
	defaultConversationID string                    // 当前默认会话 ID，供前端初次进入时定位
	staticDir             string                    // 前端静态资源所在目录
}

// knowledgeItemResponse 是知识库中单个文档（导入条目）对外的 JSON 视图。
// 与内部 knowledge.Item 解耦，只暴露前端需要的字段（分块统计、导入时间、状态等）。
type knowledgeItemResponse struct {
	ID              string `json:"id"`
	KnowledgeBaseID string `json:"knowledgeBaseId,omitempty"`
	Name            string `json:"name"`
	Size            int64  `json:"size"`
	ContentType     string `json:"contentType,omitempty"`
	ImportedAt      string `json:"importedAt"`
	Status          string `json:"status,omitempty"`
	ChunkCount      int    `json:"chunkCount,omitempty"`
	ChildChunks     int    `json:"childChunkCount,omitempty"`
	ParentChunks    int    `json:"parentChunkCount,omitempty"`
}

// knowledgeBaseResponse 是知识库（Knowledge Base）对外的 JSON 视图，
// 包含文档数量与父/子分块统计，用于前端知识库管理页展示。
type knowledgeBaseResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	DocumentCount int    `json:"documentCount"`
	ChunkCount    int    `json:"chunkCount"`
	ChildChunks   int    `json:"childChunkCount"`
	ParentChunks  int    `json:"parentChunkCount"`
}

// toolConfigItemResponse 是单个工具的"注册信息 + 用户配置"合并视图。
// Registered=true 表示该工具当前已在 runtime 注册（Tool 字段非空）；
// Registered=false 表示配置库中存在残留配置但工具本次启动未注册（例如 MCP 服务被禁用）。
type toolConfigItemResponse struct {
	Tool           *contracts.RuntimeTool `json:"tool,omitempty"`
	ToolName       string                 `json:"toolName"`
	Enabled        bool                   `json:"enabled"`
	ApprovalPolicy string                 `json:"approvalPolicy"`
	Config         map[string]any         `json:"config"`
	UpdatedAt      string                 `json:"updatedAt,omitempty"`
	Registered     bool                   `json:"registered"`
}

// modelConfigResponse 是模型配置对外的 JSON 视图。
// 出于安全考虑，绝不回传完整 APIKey：只返回 APIKeySet（是否已配置）
// 和 APIKeyPreview（形如 "sk-a...f9x2" 的脱敏预览，见 secretPreview）。
type modelConfigResponse struct {
	ID             string         `json:"id"`
	ModelType      string         `json:"modelType"`
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

// NewHTTPServer 构建整个服务端的对象图（composition root）。
//
// 参数 staticDir 为前端静态资源目录；返回可直接调用 Register 挂载路由的 *HTTPServer。
//
// 关键流程（按顺序）：
//  1. 加载启动上下文（数据目录、技能目录、MCP 配置等）；
//  2. 打开 SQLite 状态库；失败时降级为 nil，所有依赖它的功能自动退化为内存/只读模式；
//  3. 把环境变量中的默认对话模型 / embedding 模型配置"播种"进状态库，
//     再优先读回库中已有配置（用户在 UI 上改过的配置优先于环境变量）；
//  4. 选择会话存储：有 SQLite 时用持久化实现，否则退回内存实现；
//  5. 初始化工具运行时并注册三类内置工具（编码类、通用类、交互式 Web 类），
//     随后连接配置的 MCP 服务器并把其暴露的工具一并注册进 runtime；
//  6. 加载技能注册表与技能启用配置（有 SQLite 时配置持久化）；
//  7. 构建知识库存储（共享同一个 SQLite 连接以支持 chunk/向量表）；
//  8. 组装 Agent（注入 store/runtime/模型客户端/技能/启动上下文），并把 Agent 自身
//     包装成一个可被递归调用的工具（NewAgentTool）注册回 runtime，支持子 Agent 场景；
//  9. 确定默认会话：复用已存在的第一个会话，否则创建一个"新对话"；
//  10. 最后根据当前 embedding 模型配置初始化知识库的向量化能力。
func NewHTTPServer(staticDir string) *HTTPServer {
	startupContext := claudecode.LoadStartupContext()
	// 打开 SQLite 状态库；失败只记日志不中断启动，后续组件对 nil DB 都有降级路径。
	stateDB, err := statedb.Open(startupContext.StateDBPath())
	if err != nil {
		log.Printf("state database unavailable at %s: %v", startupContext.StateDBPath(), err)
		stateDB = nil
	}
	stateStore := state.NewStore(stateDB)
	modelConfig := model.DefaultConfigFromEnv()
	if stateStore != nil {
		// 把启动文件里声明的 MCP 服务器同步进状态库（新增的插入、已有的保留启用状态），
		// 然后只保留"已启用"的服务器参与本次启动的连接注册。
		_ = stateStore.SyncMCPServers(startupContext.MCP.Servers)
		if servers, err := stateStore.EnabledMCPServers(); err == nil {
			startupContext.MCP.Servers = servers
		}
		// SeedModelConfig 是幂等的：仅当库中不存在该 ID 时才写入，
		// 因此环境变量只在首次启动时生效，之后以库中（UI 修改过的）配置为准。
		_ = stateStore.SeedModelConfig(modelConfigRecordFromConfig(defaultModelConfigID, modelConfig))
		if embeddingRecord, ok := embeddingModelConfigRecordFromEnv(); ok {
			_ = stateStore.SeedModelConfig(embeddingRecord)
		}
		// 读回库中的默认模型配置覆盖环境变量默认值。
		if record, ok, err := stateStore.ModelConfig(defaultModelConfigID); err == nil && ok {
			modelConfig = modelConfigFromRecord(record)
		}
	}
	// 会话/消息存储：优先使用共享 SQLite 连接的持久化实现，失败降级为纯内存。
	var store memory.Store = memory.NewInMemoryStore()
	if stateDB != nil {
		if persistentStore, err := memory.NewPersistentStoreWithDB(stateDB); err == nil {
			store = persistentStore
		} else {
			log.Printf("persistent memory store unavailable: %v", err)
		}
	}
	// 工具运行时：stateStore 同时充当工具配置提供方（启用状态 / 审批策略）。
	registry := runtime.NewRuntime()
	if stateStore != nil {
		registry.SetToolConfigProvider(stateStore)
	}
	// 技能注册表：从多个技能目录扫描加载；启用配置在有 SQLite 时持久化到库。
	skillRegistry := skills.NewRegistry()
	_ = skillRegistry.LoadFromDirectories(skillDirs(startupContext.SkillDirectories))
	skillConfig := skills.NewConfigStore(skillRegistry)
	if stateDB != nil {
		skillConfig = skills.NewSQLiteConfigStore(skillRegistry, stateDB)
	}
	// 知识库存储：复用同一个 SQLite 连接（chunk、向量索引等表）；
	// 初始化失败时退化为空目录存储，保证接口可用但无数据。
	knowledgeOptions := []knowledge.Option{}
	if stateDB != nil {
		knowledgeOptions = append(knowledgeOptions, knowledge.WithDB(stateDB))
	}
	knowledgeStore, err := knowledge.NewStoreWithOptions(startupContext.KnowledgeDir(), knowledgeOptions...)
	if err != nil {
		log.Printf("knowledge store unavailable at %s: %v", startupContext.KnowledgeDir(), err)
		knowledgeStore, _ = knowledge.NewStore("")
	}
	// 注册三类内置工具，然后连接 MCP 服务器把外部工具也注册进 runtime。
	tools.RegisterCoreCoder(registry)
	tools.RegisterGeneric(registry, store)
	tools.RegisterInteractiveWeb(registry)
	mcp.RegisterConfiguredServers(context.Background(), registry, startupContext.MCP)
	// 组装 Agent，并把 Agent 本身包装成工具注册回 runtime（支持"Agent 调 Agent"的子任务模式）。
	agent := agent.NewAgent(store, registry, model.NewOpenAICompatibleModel(modelConfig), skillRegistry, skillConfig, startupContext)
	agent.SetKnowledgeStore(knowledgeStore)
	registry.Register(tools.NewAgentTool(agent))
	// 默认会话：优先复用已有的第一个会话，避免每次启动都新建空会话。
	defaultConversationID := ""
	if existing := store.ListConversations(); len(existing) > 0 {
		defaultConversationID = existing[0].ID
	} else {
		defaultConversation := store.CreateConversation(initialConversationTitle)
		defaultConversationID = defaultConversation.ID
	}
	httpServer := &HTTPServer{
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
	// 根据当前 embedding 模型配置为知识库装配向量化与向量索引能力（无配置则自动关闭）。
	_ = httpServer.configureKnowledgeEmbedding()
	return httpServer
}

// skillDirs 把启动上下文中的技能目录描述（claudecode.SkillDirectory）
// 转换为 skills 包所需的 Directory 切片，保留路径与作用域（scope）信息。
func skillDirs(dirs []claudecode.SkillDirectory) []skills.Directory {
	out := make([]skills.Directory, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, skills.Directory{Path: dir.Path, Scope: dir.Scope})
	}
	return out
}

// Register 把全部路由挂载到 Hertz 引擎上，是 HTTP 层唯一的路由注册入口。
//
// 路由按功能分组：
//   - /health：健康检查；
//   - /api/conversations/**：会话 CRUD、消息/追踪查询、同步聊天与 SSE 流式聊天、
//     多模型轮次的 canonical 回答切换；
//   - /api/model/**：模型配置的查询与增删改（单默认配置 + 多配置管理两套接口并存）；
//   - /api/tools/**、/api/mcp/**：工具与 MCP 服务器的查询和启用配置；
//   - /api/skills/**：技能列表、详情、启用配置与删除；
//   - /api/knowledge-bases/** 与 /api/knowledge/**：知识库与知识文档的管理、导入和检索；
//   - 根路径及若干固定文件名：前端静态资源。
func (s *HTTPServer) Register(h *server.Hertz) {
	h.GET("/health", s.handleHealth)
	// —— 会话与聊天 ——
	h.GET("/api/conversations", s.handleListConversations)
	h.POST("/api/conversations", s.handleCreateConversation)
	h.DELETE("/api/conversations/:conversationId", s.handleDeleteConversation)
	h.GET("/api/conversations/:conversationId/messages", s.handleMessages)
	h.GET("/api/conversations/:conversationId/traces", s.handleTraces)
	h.POST("/api/conversations/:conversationId/chat", s.handleChat)
	h.POST("/api/conversations/:conversationId/chat/stream", s.handleChatStream)
	h.PUT("/api/conversations/:conversationId/turns/:turnId/canonical-response", s.handleSelectCanonicalResponse)
	// —— 模型配置 ——
	// /api/model/config 是操作默认配置的旧接口；/api/model/configs 支持多模型配置管理。
	h.GET("/api/model/config", s.handleModelConfig)
	h.PUT("/api/model/config", s.handleUpdateModelConfig)
	h.GET("/api/model/configs", s.handleModelConfigs)
	h.PUT("/api/model/configs/:configId", s.handleUpdateModelConfigByID)
	h.DELETE("/api/model/configs/:configId", s.handleDeleteModelConfig)
	// —— 工具与 MCP 服务器 ——
	h.GET("/api/tools", s.handleTools)
	h.GET("/api/tools/config", s.handleToolConfigs)
	h.PUT("/api/tools/config", s.handleUpdateToolConfig)
	h.GET("/api/mcp/servers", s.handleMCPServers)
	h.GET("/api/mcp/servers/config", s.handleMCPServerConfigs)
	h.PUT("/api/mcp/servers/config", s.handleUpdateMCPServerConfig)
	// —— 技能 ——
	h.GET("/api/skills", s.handleSkills)
	h.PUT("/api/skills/config", s.handleUpdateSkillConfig)
	h.GET("/api/skills/:skillName", s.handleSkillDetail)
	h.DELETE("/api/skills/:skillName", s.handleDeleteSkill)
	// —— 知识库与知识文档 ——
	h.GET("/api/knowledge-bases", s.handleKnowledgeBases)
	h.POST("/api/knowledge-bases", s.handleCreateKnowledgeBase)
	h.DELETE("/api/knowledge-bases/:knowledgeBaseId", s.handleDeleteKnowledgeBase)
	h.GET("/api/knowledge", s.handleKnowledgeList)
	h.POST("/api/knowledge/import", s.handleKnowledgeImport)
	h.POST("/api/knowledge/search", s.handleKnowledgeSearch)
	h.DELETE("/api/knowledge/:itemId", s.handleKnowledgeDelete)

	// —— 前端静态资源 ——
	// 采用"显式白名单"方式逐个注册固定文件名，而不是挂载整个目录，
	// 避免目录遍历风险，也使路由表一目了然。
	h.GET("/", s.serveStaticFile("index.html"))
	h.GET("/main.js", s.serveStaticFile("main.js"))
	h.GET("/main.js.map", s.serveStaticFile("main.js.map"))
	h.GET("/markdown.js", s.serveStaticFile("markdown.js"))
	h.GET("/markdown.js.map", s.serveStaticFile("markdown.js.map"))
	h.GET("/styles.css", s.serveStaticFile("styles.css"))
}

// handleHealth 处理 GET /health，返回 {"ok": true}，用于存活探测。
func (s *HTTPServer) handleHealth(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, utils.H{"ok": true})
}

// handleListConversations 处理 GET /api/conversations。
// 返回对前端"可见"的会话列表（多个空白初始会话只显示一个，见 visibleConversations）
// 以及 defaultConversationId（列表首个会话优先，前端据此选中默认会话）。
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

// handleCreateConversation 处理 POST /api/conversations，请求体 {"title": string}。
// 若请求的是默认标题且已存在一个"空白初始会话"，则直接复用（返回 200），
// 避免用户反复点"新对话"产生大量空会话；否则创建新会话并返回 201。
func (s *HTTPServer) handleCreateConversation(ctx context.Context, c *app.RequestContext) {
	var body struct {
		Title string `json:"title"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	// 复用已有的空白初始会话：200 表示复用，201 表示真正新建。
	if reusable, ok := s.reusableInitialConversation(body.Title); ok {
		c.JSON(consts.StatusOK, utils.H{"conversation": reusable})
		return
	}
	conversation := s.store.CreateConversation(body.Title)
	c.JSON(consts.StatusCreated, utils.H{"conversation": conversation})
}

// handleDeleteConversation 处理 DELETE /api/conversations/:conversationId。
// 删除后重新计算可见会话列表和默认会话 ID 并一并返回，方便前端一次刷新界面。
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

// handleMessages 处理 GET /api/conversations/:conversationId/messages，
// 返回该会话的全部历史消息（用户/助手消息，含多模型元数据）。
func (s *HTTPServer) handleMessages(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	c.JSON(consts.StatusOK, utils.H{"messages": s.store.Messages(conversationID)})
}

// handleTraces 处理 GET /api/conversations/:conversationId/traces，
// 返回该会话所有 Agent 执行追踪（每轮的推理步骤、工具调用及结果），用于前端"过程"面板。
func (s *HTTPServer) handleTraces(ctx context.Context, c *app.RequestContext) {
	conversationID := c.Param("conversationId")
	c.JSON(consts.StatusOK, utils.H{"traces": s.store.Traces(conversationID)})
}

// handleChat 处理 POST /api/conversations/:conversationId/chat（非流式同步聊天）。
//
// 请求体为 chatRequestBody（message 必填，可选模型选择与知识库 ID）；
// 成功时一次性返回 agent.RunOutput（助手消息 + 完整 Trace）的 JSON。
// 注意：该接口只支持单模型；多模型并行回答依赖 SSE 逐路推送，必须走 /chat/stream。
func (s *HTTPServer) handleChat(ctx context.Context, c *app.RequestContext) {
	var body chatRequestBody
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeHertzError(c, consts.StatusBadRequest, "message is required")
		return
	}
	// 归一化模型选择（去重、上限校验、确定主模型）。
	modelIDs, primaryModelID, err := s.normalizeChatModelSelection(body)
	if err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	// 多模型需要按路持续推送事件，同步 JSON 响应无法表达，直接拒绝并提示改用流式接口。
	if len(modelIDs) > 1 {
		writeHertzError(c, consts.StatusBadRequest, "multi-model chat requires /chat/stream")
		return
	}

	conversationID := c.Param("conversationId")
	modelClient, err := s.modelClientForRequest(primaryModelID)
	if err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	output, err := s.agent.Run(ctx, agent.RunInput{
		ConversationID:  conversationID,
		UserMessage:     body.Message,
		Model:           modelClient,
		ModelConfigID:   primaryModelID,
		KnowledgeBaseID: body.KnowledgeBaseID,
	})
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, output)
}

// handleChatStream 处理 POST /api/conversations/:conversationId/chat/stream（SSE 流式聊天）。
//
// 请求体与 /chat 相同（chatRequestBody）；响应是一条 SSE 事件流：
// 每个事件的 event 字段即 AgentStreamEvent.Type（如 user_message、assistant_delta、
// tool_call、tool_result、error、done 等），data 为事件的 JSON 序列化结果。
//
// 关键流程：
//  1. 参数在建立 SSE 前先校验，此阶段错误仍以普通 JSON 错误返回；
//  2. 创建 SSE writer 之后，任何错误都只能作为 "error" 事件写进流里
//     （HTTP 状态码已经发出，无法再改）；
//  3. 单模型走 agent.RunStream 并把回调事件逐条转写为 SSE；
//     多模型（modelIDs > 1）转交 runMultiModelChatStream 处理；
//  4. 正常结束时补发一条 "done" 事件，携带最终助手消息与 TraceID，作为流的收尾标记。
func (s *HTTPServer) handleChatStream(ctx context.Context, c *app.RequestContext) {
	var body chatRequestBody
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeHertzError(c, consts.StatusBadRequest, "message is required")
		return
	}

	// 从这里开始进入 SSE 模式：设置 X-Accel-Buffering: no 告知 Nginx 等反向代理
	// 关闭响应缓冲，保证事件能实时到达浏览器；defer Close 负责在函数返回时结束流。
	writer := sse.NewWriter(c)
	c.Response.Header.Set("X-Accel-Buffering", "no")
	defer writer.Close()

	conversationID := c.Param("conversationId")
	modelIDs, primaryModelID, err := s.normalizeChatModelSelection(body)
	if err != nil {
		// SSE 已建立，错误只能以 error 事件的形式下发。
		writeSSEError(writer, err.Error(), "")
		return
	}
	// 多模型并行回答：整套编排（并行执行、事件合流、canonical 选择）在 multi_model.go 中实现。
	if len(modelIDs) > 1 {
		if err := s.runMultiModelChatStream(ctx, writer, conversationID, body, modelIDs, primaryModelID); err != nil {
			writeSSEError(writer, err.Error(), "")
		}
		return
	}
	// 单模型路径：构建指定配置的模型客户端并交给 Agent 流式执行。
	modelClient, err := s.modelClientForRequest(primaryModelID)
	if err != nil {
		writeSSEError(writer, err.Error(), "")
		return
	}
	output, err := s.agent.RunStream(ctx, agent.RunInput{
		ConversationID:  conversationID,
		UserMessage:     body.Message,
		Model:           modelClient,
		ModelConfigID:   primaryModelID,
		KnowledgeBaseID: body.KnowledgeBaseID,
	}, func(event contracts.AgentStreamEvent) error {
		// 给每个事件补上模型配置 ID，前端据此把事件归属到对应模型的 UI 区块。
		event.ModelConfigID = primaryModelID
		return writeSSEEvent(writer, event)
	})
	if err != nil {
		// 失败时带上 TraceID，便于前端跳转查看这次执行的追踪详情。
		writeSSEError(writer, err.Error(), output.Trace.ID)
		return
	}
	// 收尾事件：告知前端流已完成，并附带最终助手消息，供前端落定 UI 状态。
	_ = writeSSEEvent(writer, contracts.AgentStreamEvent{
		Type:      "done",
		Title:     "完成",
		Content:   "stream completed",
		Message:   &output.AssistantMessage,
		TraceID:   output.Trace.ID,
		CreatedAt: shared.Now(),
	})
}

// handleModelConfig 处理 GET /api/model/config，返回默认对话模型配置（脱敏视图）。
func (s *HTTPServer) handleModelConfig(ctx context.Context, c *app.RequestContext) {
	record, err := s.currentModelConfigRecord()
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"config": modelConfigResponseFromRecord(record)})
}

// handleModelConfigs 处理 GET /api/model/configs，返回全部模型配置列表与默认配置 ID。
// 状态库不可用时，退化为只返回由环境变量构造的默认配置这一个条目。
func (s *HTTPServer) handleModelConfigs(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		c.JSON(consts.StatusOK, utils.H{"configs": []modelConfigResponse{modelConfigResponseFromRecord(modelConfigRecordFromConfig(defaultModelConfigID, model.DefaultConfigFromEnv()))}, "defaultConfigId": defaultModelConfigID})
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
	c.JSON(consts.StatusOK, utils.H{"configs": modelConfigResponses(records), "defaultConfigId": defaultModelConfigID})
}

// handleUpdateModelConfig 处理 PUT /api/model/config，
// 更新默认对话模型配置（固定 configID = "default"），实际逻辑委托给 handleSaveModelConfig。
func (s *HTTPServer) handleUpdateModelConfig(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "state store is unavailable")
		return
	}
	s.handleSaveModelConfig(c, defaultModelConfigID)
}

// handleUpdateModelConfigByID 处理 PUT /api/model/configs/:configId，
// 新建或更新指定 ID 的模型配置（upsert 语义），实际逻辑委托给 handleSaveModelConfig。
func (s *HTTPServer) handleUpdateModelConfigByID(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "state store is unavailable")
		return
	}
	s.handleSaveModelConfig(c, c.Param("configId"))
}

// handleDeleteModelConfig 处理 DELETE /api/model/configs/:configId。
// 默认配置（"default"）不允许删除；删除成功后返回最新配置列表。
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
	if configID == defaultModelConfigID {
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
	c.JSON(consts.StatusOK, utils.H{"configs": modelConfigResponses(records), "defaultConfigId": defaultModelConfigID})
}

// handleSaveModelConfig 是模型配置保存（新建/更新）的公共实现。
//
// 请求体所有字段均为指针（PATCH 语义）：只有请求中出现的字段才覆盖已有值，
// 未出现的字段保留库中原值；配置不存在时以环境变量默认值为基底新建。
//
// 关键规则：
//   - 默认配置强制为 reasoning 类型，防止把对话模型误改成 embedding 导致聊天不可用；
//   - 保存后若改动的是默认对话模型，则热更新 Agent 的模型客户端（无需重启进程）；
//   - 任何保存都会触发 configureKnowledgeEmbedding 重新装配知识库向量化能力，
//     因为改动的可能正是 embedding 模型配置。
func (s *HTTPServer) handleSaveModelConfig(c *app.RequestContext, configID string) {
	var body struct {
		ID             *string        `json:"id"`
		ModelType      *string        `json:"modelType"`
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
	// 基底选择：库中已有该配置则在其上做增量修改，否则以环境变量默认值起底新建。
	record := modelConfigRecordFromConfig(configID, model.DefaultConfigFromEnv())
	if existing, ok, err := s.stateStore.ModelConfig(configID); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	} else if ok {
		record = existing
	}
	record.ID = configID
	if body.ModelType != nil {
		record.ModelType = *body.ModelType
	}
	// 默认配置必须保持 reasoning 类型（聊天兜底模型），忽略请求中的其他类型。
	if configID == defaultModelConfigID {
		record.ModelType = reasoningModelType
	}
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
	// 热更新：默认对话模型被修改时，立即替换 Agent 持有的模型客户端，改动即时生效。
	if s.agent != nil && record.ID == defaultModelConfigID && record.ModelType == reasoningModelType {
		config := modelConfigFromRecord(record)
		s.agent.SetModel(model.NewOpenAICompatibleModel(config))
	}
	// 重新装配知识库的 embedding 能力（本次保存可能新增/修改/清空了 embedding 配置）。
	if err := s.configureKnowledgeEmbedding(); err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"config": modelConfigResponseFromRecord(record)})
}

// handleTools 处理 GET /api/tools，返回 runtime 中当前注册的全部工具定义
// （含内置工具与 MCP 工具）。
func (s *HTTPServer) handleTools(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, utils.H{"tools": s.runtime.ListRegisteredTools()})
}

// handleToolConfigs 处理 GET /api/tools/config，
// 返回"注册工具 ∪ 已保存配置"的合并列表（见 writeToolConfigs）。
func (s *HTTPServer) handleToolConfigs(ctx context.Context, c *app.RequestContext) {
	s.writeToolConfigs(c)
}

// handleUpdateToolConfig 处理 PUT /api/tools/config。
// 请求体：{toolName, enabled?, approvalPolicy?, config?}；enabled 缺省视为 true。
// 保存后返回最新的工具配置全量列表，方便前端整体刷新。
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

// handleMCPServers 处理 GET /api/mcp/servers，
// 返回本次启动实际连接的（已启用的）MCP 服务器列表。
func (s *HTTPServer) handleMCPServers(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, utils.H{
		"servers": s.startupContext.MCP.Servers,
	})
}

// handleMCPServerConfigs 处理 GET /api/mcp/servers/config，
// 返回状态库中全部 MCP 服务器配置（含禁用的），restartRequired 恒为 false。
func (s *HTTPServer) handleMCPServerConfigs(ctx context.Context, c *app.RequestContext) {
	s.writeMCPServerConfigs(c, false)
}

// handleUpdateMCPServerConfig 处理 PUT /api/mcp/servers/config，
// 请求体：{name, enabled}，切换指定 MCP 服务器的启用状态。
// 由于 MCP 连接只在启动时建立，改动需重启才生效，因此响应中 restartRequired=true。
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

// writeToolConfigs 组装并写出工具配置列表，是 GET/PUT /api/tools/config 的公共出口。
//
// 合并策略：
//  1. 先遍历 runtime 中已注册的工具，逐个附上库中配置（无配置的用默认值
//     enabled=true / approvalPolicy=auto 补齐），标记 Registered=true；
//  2. 再补充库中存在但本次未注册的工具配置（如被禁用的 MCP 服务遗留的配置），
//     标记 Registered=false，让前端仍能看到并管理这些"孤儿"配置。
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
	// 第一遍：已注册工具 + 对应配置（缺省配置现场补齐）。
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
	// 第二遍：库中有配置但当前未注册的工具（Registered=false）。
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

// writeMCPServerConfigs 写出状态库中的 MCP 服务器配置列表。
// restartRequired 用于提示前端"改动需要重启进程才生效"（更新配置后为 true）。
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

// handleSkills 处理 GET /api/skills，返回全部技能及其启用状态（已删除的技能被过滤）。
func (s *HTTPServer) handleSkills(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, utils.H{"skills": s.skillConfig.List(s.skillRegistry)})
}

// handleUpdateSkillConfig 处理 PUT /api/skills/config。
// 请求体 {enabledSkills: []string} 为"全量启用列表"语义：列表内的技能启用，其余全部禁用。
// 更新后持久化并返回最新技能列表。
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

// handleSkillDetail 处理 GET /api/skills/:skillName，返回单个技能的完整详情。
// 已被标记删除的技能即使仍在注册表中也按 404 处理（软删除对外不可见）。
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
	// 软删除的技能对外表现为不存在。
	if deleted[strings.ToLower(detail.Name)] {
		writeHertzError(c, consts.StatusNotFound, "skill not found")
		return
	}
	detail.Enabled = enabled[strings.ToLower(detail.Name)]
	c.JSON(consts.StatusOK, utils.H{"skill": detail})
}

// handleDeleteSkill 处理 DELETE /api/skills/:skillName。
// 采用软删除：仅在配置中标记删除（不动磁盘上的技能文件），随后持久化并返回最新技能列表。
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

// handleKnowledgeBases 处理 GET /api/knowledge-bases，返回全部知识库及统计信息。
func (s *HTTPServer) handleKnowledgeBases(ctx context.Context, c *app.RequestContext) {
	if s.knowledgeStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "knowledge store is unavailable")
		return
	}
	bases, err := s.knowledgeStore.ListKnowledgeBases(ctx)
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"knowledgeBases": knowledgeBaseResponses(bases)})
}

// handleCreateKnowledgeBase 处理 POST /api/knowledge-bases，请求体 {name, description}。
// 创建成功返回 201，响应同时携带新库、最新知识库列表和新库内文档列表（为空），
// 让前端一次拿到全部刷新所需数据。参数校验错误（"required"）映射为 400。
func (s *HTTPServer) handleCreateKnowledgeBase(ctx context.Context, c *app.RequestContext) {
	if s.knowledgeStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "knowledge store is unavailable")
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	base, err := s.knowledgeStore.CreateKnowledgeBase(ctx, body.Name, body.Description)
	if err != nil {
		status := consts.StatusInternalServerError
		if strings.Contains(err.Error(), "required") {
			status = consts.StatusBadRequest
		}
		writeHertzError(c, status, err.Error())
		return
	}
	bases, err := s.knowledgeStore.ListKnowledgeBases(ctx)
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	items, err := s.knowledgeStore.ListByKnowledgeBase(ctx, base.ID)
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusCreated, utils.H{
		"knowledgeBase":  knowledgeBaseResponseFromRecord(base),
		"knowledgeBases": knowledgeBaseResponses(bases),
		"items":          knowledgeResponseItems(items),
	})
}

// handleDeleteKnowledgeBase 处理 DELETE /api/knowledge-bases/:knowledgeBaseId。
// 不可删除的库（如默认库，错误信息含 "cannot be deleted"）映射为 400；不存在返回 404。
func (s *HTTPServer) handleDeleteKnowledgeBase(ctx context.Context, c *app.RequestContext) {
	if s.knowledgeStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "knowledge store is unavailable")
		return
	}
	id := strings.TrimSpace(c.Param("knowledgeBaseId"))
	if id == "" {
		writeHertzError(c, consts.StatusBadRequest, "knowledgeBaseId is required")
		return
	}
	deleted, err := s.knowledgeStore.DeleteKnowledgeBase(ctx, id)
	if err != nil {
		status := consts.StatusInternalServerError
		if strings.Contains(err.Error(), "cannot be deleted") {
			status = consts.StatusBadRequest
		}
		writeHertzError(c, status, err.Error())
		return
	}
	if !deleted {
		writeHertzError(c, consts.StatusNotFound, "knowledge base not found")
		return
	}
	bases, err := s.knowledgeStore.ListKnowledgeBases(ctx)
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"knowledgeBases": knowledgeBaseResponses(bases), "items": []knowledgeItemResponse{}})
}

// handleKnowledgeList 处理 GET /api/knowledge?knowledgeBaseId=xxx，
// 返回指定知识库（或全部）下的文档条目列表。
func (s *HTTPServer) handleKnowledgeList(ctx context.Context, c *app.RequestContext) {
	if s.knowledgeStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "knowledge store is unavailable")
		return
	}
	items, err := s.knowledgeStore.ListByKnowledgeBase(ctx, strings.TrimSpace(c.Query("knowledgeBaseId")))
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"items": knowledgeResponseItems(items)})
}

// handleKnowledgeImport 处理 POST /api/knowledge/import（multipart 表单上传）。
// 表单字段：file（必填，待导入文件）、knowledgeBaseId（可选，目标知识库）。
// 导入内部会完成解析、分块（父/子 chunk）及异步向量化；
// 成功返回 201，附带新条目、该库最新条目列表和知识库列表。
// "required"/"exceeds"（超限）类错误映射为 400。
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
	knowledgeBaseID := strings.TrimSpace(string(c.FormValue("knowledgeBaseId")))
	item, err := s.knowledgeStore.ImportToKnowledgeBase(ctx, knowledgeBaseID, fileHeader.Filename, fileHeader.Header.Get("Content-Type"), file)
	if err != nil {
		status := consts.StatusInternalServerError
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "exceeds") {
			status = consts.StatusBadRequest
		}
		writeHertzError(c, status, err.Error())
		return
	}
	items, err := s.knowledgeStore.ListByKnowledgeBase(ctx, item.KnowledgeBaseID)
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	bases, _ := s.knowledgeStore.ListKnowledgeBases(ctx)
	c.JSON(consts.StatusCreated, utils.H{"item": knowledgeResponseItem(item), "items": knowledgeResponseItems(items), "knowledgeBases": knowledgeBaseResponses(bases)})
}

// handleKnowledgeSearch 处理 POST /api/knowledge/search（RAG 检索调试/独立检索接口）。
// 请求体：{query 必填, conversationId?, knowledgeBaseIds?, docIds?, topK?, metadata?}，
// 直接调用 knowledge.Store.Retrieve 并返回完整检索结果（含召回与重排明细）。
func (s *HTTPServer) handleKnowledgeSearch(ctx context.Context, c *app.RequestContext) {
	if s.knowledgeStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "knowledge store is unavailable")
		return
	}
	var body struct {
		Query            string         `json:"query"`
		ConversationID   string         `json:"conversationId"`
		KnowledgeBaseIDs []string       `json:"knowledgeBaseIds"`
		DocIDs           []string       `json:"docIds"`
		TopK             int            `json:"topK"`
		Metadata         map[string]any `json:"metadata"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Query) == "" {
		writeHertzError(c, consts.StatusBadRequest, "query is required")
		return
	}
	result, err := s.knowledgeStore.Retrieve(ctx, body.Query, knowledge.RetrievalOptions{
		ConversationID: body.ConversationID,
		TopK:           body.TopK,
		Filter: knowledge.RetrievalFilter{
			KnowledgeBaseIDs: body.KnowledgeBaseIDs,
			DocIDs:           body.DocIDs,
			Metadata:         body.Metadata,
		},
	})
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(consts.StatusOK, utils.H{"result": result})
}

// handleKnowledgeDelete 处理 DELETE /api/knowledge/:itemId?knowledgeBaseId=xxx，
// 删除单个知识文档，并返回删除后的条目列表与知识库列表供前端刷新。
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
	items, err := s.knowledgeStore.ListByKnowledgeBase(ctx, strings.TrimSpace(c.Query("knowledgeBaseId")))
	if err != nil {
		writeHertzError(c, consts.StatusInternalServerError, err.Error())
		return
	}
	bases, _ := s.knowledgeStore.ListKnowledgeBases(ctx)
	c.JSON(consts.StatusOK, utils.H{"items": knowledgeResponseItems(items), "knowledgeBases": knowledgeBaseResponses(bases)})
}

// knowledgeBaseResponses 把内部知识库记录批量转换为对外 JSON 视图。
func knowledgeBaseResponses(bases []knowledge.KnowledgeBase) []knowledgeBaseResponse {
	out := make([]knowledgeBaseResponse, 0, len(bases))
	for _, base := range bases {
		out = append(out, knowledgeBaseResponseFromRecord(base))
	}
	return out
}

// knowledgeBaseResponseFromRecord 把单个内部知识库记录逐字段映射为对外视图。
func knowledgeBaseResponseFromRecord(base knowledge.KnowledgeBase) knowledgeBaseResponse {
	return knowledgeBaseResponse{
		ID:            base.ID,
		Name:          base.Name,
		Description:   base.Description,
		Status:        base.Status,
		CreatedAt:     base.CreatedAt,
		UpdatedAt:     base.UpdatedAt,
		DocumentCount: base.DocumentCount,
		ChunkCount:    base.ChunkCount,
		ChildChunks:   base.ChildChunks,
		ParentChunks:  base.ParentChunks,
	}
}

// knowledgeResponseItems 把内部知识文档条目批量转换为对外 JSON 视图。
func knowledgeResponseItems(items []knowledge.Item) []knowledgeItemResponse {
	out := make([]knowledgeItemResponse, 0, len(items))
	for _, item := range items {
		out = append(out, knowledgeResponseItem(item))
	}
	return out
}

// knowledgeResponseItem 把单个内部知识文档条目逐字段映射为对外视图。
func knowledgeResponseItem(item knowledge.Item) knowledgeItemResponse {
	return knowledgeItemResponse{
		ID:              item.ID,
		KnowledgeBaseID: item.KnowledgeBaseID,
		Name:            item.Name,
		Size:            item.Size,
		ContentType:     item.ContentType,
		ImportedAt:      item.ImportedAt,
		Status:          item.Status,
		ChunkCount:      item.ChunkCount,
		ChildChunks:     item.ChildChunks,
		ParentChunks:    item.ParentChunks,
	}
}

// visibleConversations 返回对前端可见的会话列表。
// 规则：多个"空白初始会话"（默认标题且无消息）只保留遇到的第一个，
// 其余隐藏，防止历史遗留的空会话在侧边栏堆积。
func (s *HTTPServer) visibleConversations() []contracts.Conversation {
	items := s.store.ListConversations()
	out := make([]contracts.Conversation, 0, len(items))
	seenInitial := false
	for _, conversation := range items {
		if isInitialConversation(conversation, s.store.Messages(conversation.ID)) {
			// 只展示第一个空白初始会话，后续的全部跳过。
			if seenInitial {
				continue
			}
			seenInitial = true
		}
		out = append(out, conversation)
	}
	return out
}

// reusableInitialConversation 判断"新建会话"请求能否复用一个已存在的空白初始会话。
//
// 只有当请求标题为空、等于中文默认标题或旧英文默认标题时才允许复用（否则用户是想建带标题的新会话）；
// 命中时返回第一个"空白初始会话"及 true，供 handleCreateConversation 直接返回它而不新建。
func (s *HTTPServer) reusableInitialConversation(title string) (contracts.Conversation, bool) {
	title = strings.TrimSpace(title)
	if title != "" && title != initialConversationTitle && !strings.EqualFold(title, legacyInitialTitle) {
		return contracts.Conversation{}, false
	}
	for _, conversation := range s.store.ListConversations() {
		if isInitialConversation(conversation, s.store.Messages(conversation.ID)) {
			return conversation, true
		}
	}
	return contracts.Conversation{}, false
}

// isInitialConversation 判断一个会话是否为"空白初始会话"：
// 标题等于默认（中文或旧英文）标题且没有任何消息。
// 这类会话可被复用、且在侧边栏中最多只显示一个。
func isInitialConversation(conversation contracts.Conversation, messages []contracts.Message) bool {
	title := strings.TrimSpace(conversation.Title)
	return (title == initialConversationTitle || strings.EqualFold(title, legacyInitialTitle)) && len(messages) == 0
}

// currentModelConfigRecord 读取（并在必要时按环境变量播种）当前默认对话模型配置记录。
//
// 状态库不可用时直接返回由环境变量构造的默认记录；库可用时先幂等 seed，
// 再读回库中记录（用户改过的配置优先），读不到则回退到默认记录。
func (s *HTTPServer) currentModelConfigRecord() (state.ModelConfigRecord, error) {
	defaultRecord := modelConfigRecordFromConfig(defaultModelConfigID, model.DefaultConfigFromEnv())
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

// configureKnowledgeEmbedding 根据当前 embedding 模型配置为知识库装配（或关闭）向量化能力。
//
// 该方法在启动时以及每次保存模型配置后调用，用于让 embedding 配置的增删改即时生效：
//   - 知识库或状态库/DB 缺失时，清空 embedder 与向量索引，知识库退化为纯文本检索；
//   - 有 embedding 配置时，构建 OpenAI 兼容的 embedding 客户端并挂上 SQLite 向量索引，
//     随后异步回填历史文档缺失的向量（BackfillMissingVectorsAsync），避免阻塞请求；
//   - 无任何 embedding 配置时同样关闭向量能力。
func (s *HTTPServer) configureKnowledgeEmbedding() error {
	if s == nil || s.knowledgeStore == nil {
		return nil
	}
	if s.stateStore == nil || s.stateDB == nil {
		s.knowledgeStore.SetEmbedder(nil)
		s.knowledgeStore.SetVectorIndex(nil)
		return nil
	}
	if embeddingRecord, ok := embeddingModelConfigRecordFromEnv(); ok {
		if err := s.stateStore.SeedModelConfig(embeddingRecord); err != nil {
			return err
		}
	}
	record, ok, err := s.stateStore.FirstModelConfigByType(embeddingModelType)
	if err != nil {
		return err
	}
	if !ok {
		s.knowledgeStore.SetEmbedder(nil)
		s.knowledgeStore.SetVectorIndex(nil)
		return nil
	}
	embedder := model.NewOpenAIEmbeddingClient(embeddingConfigFromRecord(record))
	s.knowledgeStore.SetEmbedder(embedder)
	if embedder == nil {
		s.knowledgeStore.SetVectorIndex(nil)
		return nil
	}
	s.knowledgeStore.SetVectorIndex(knowledge.NewSQLiteVecIndex(s.stateDB))
	s.knowledgeStore.BackfillMissingVectorsAsync()
	return nil
}

// modelConfigRecordFromConfig 把运行时 model.Config 转换为可落库的状态记录。
// 转换前先 NormalizeConfig 规整字段；生成的记录固定标记为 reasoning 类型（对话模型）。
func modelConfigRecordFromConfig(id string, config model.Config) state.ModelConfigRecord {
	config = model.NormalizeConfig(config)
	return state.ModelConfigRecord{
		ID:             id,
		ModelType:      reasoningModelType,
		Provider:       config.Provider,
		APIKey:         config.APIKey,
		BaseURL:        config.BaseURL,
		Model:          config.Model,
		Temperature:    config.Temperature,
		TimeoutSeconds: config.TimeoutSeconds,
		Extra:          map[string]any{},
	}
}

// embeddingModelConfigRecordFromEnv 从环境变量构造默认 embedding 模型配置记录。
// 当环境既未提供 embedding 模型名也未提供 API Key 时返回 (零值, false)，表示"无 embedding 配置"，
// 调用方据此跳过播种、关闭知识库向量化能力。
func embeddingModelConfigRecordFromEnv() (state.ModelConfigRecord, bool) {
	config := model.DefaultEmbeddingConfigFromEnv()
	if strings.TrimSpace(config.EmbeddingModel) == "" && strings.TrimSpace(config.APIKey) == "" {
		return state.ModelConfigRecord{}, false
	}
	return state.ModelConfigRecord{
		ID:             defaultEmbeddingModelConfig,
		ModelType:      embeddingModelType,
		Provider:       config.Provider,
		APIKey:         config.APIKey,
		BaseURL:        config.BaseURL,
		Model:          config.EmbeddingModel,
		Temperature:    config.Temperature,
		TimeoutSeconds: config.TimeoutSeconds,
		Extra:          map[string]any{},
	}, true
}

// modelConfigFromRecord 把状态库中的对话模型记录还原为运行时 model.Config（经 NormalizeConfig 规整）。
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

// embeddingConfigFromRecord 把 embedding 模型记录还原为运行时 model.Config。
// 注意 record.Model 同时写入 Model 与 EmbeddingModel 两个字段，以适配 embedding 客户端的取值方式。
func embeddingConfigFromRecord(record state.ModelConfigRecord) model.Config {
	return model.NormalizeConfig(model.Config{
		Provider:       record.Provider,
		APIKey:         record.APIKey,
		BaseURL:        record.BaseURL,
		Model:          record.Model,
		EmbeddingModel: record.Model,
		Temperature:    record.Temperature,
		TimeoutSeconds: record.TimeoutSeconds,
	})
}

// modelConfigResponseFromRecord 把模型配置记录转换为对外脱敏视图：
// 绝不回传明文 APIKey，只暴露 APIKeySet（是否已配置）与 APIKeyPreview（脱敏预览）。
func modelConfigResponseFromRecord(record state.ModelConfigRecord) modelConfigResponse {
	return modelConfigResponse{
		ID:             record.ID,
		ModelType:      record.ModelType,
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

// modelConfigResponses 把模型配置记录列表批量转换为对外脱敏视图列表。
func modelConfigResponses(records []state.ModelConfigRecord) []modelConfigResponse {
	out := make([]modelConfigResponse, 0, len(records))
	for _, record := range records {
		out = append(out, modelConfigResponseFromRecord(record))
	}
	return out
}

// modelClientForRequest 根据模型配置 ID 构建一个可用于聊天的模型客户端。
//
// 处理流程与校验：
//   - configID 为空时回退到默认配置；
//   - 有状态库时先幂等 seed 默认配置，再按 ID 读取，找不到即报错；
//     无状态库时只允许使用 default，其余 ID 一律报"未找到"；
//   - 拒绝 embedding 类型的配置用于聊天；
//   - 拒绝"provider 声明为 openai-compatible 却指向 Anthropic base_url"的错误组合
//     （因为本客户端发送 OpenAI 风格的 /chat/completions，见 isOpenAICompatibleAnthropicBaseURL）；
//   - 校验通过后返回 OpenAI 兼容模型客户端。
func (s *HTTPServer) modelClientForRequest(configID string) (model.Client, error) {
	configID = strings.TrimSpace(configID)
	if configID == "" {
		configID = defaultModelConfigID
	}
	record := modelConfigRecordFromConfig(defaultModelConfigID, model.DefaultConfigFromEnv())
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
	} else if configID != defaultModelConfigID {
		return nil, fmt.Errorf("model config %q not found", configID)
	}
	if record.ModelType != reasoningModelType {
		return nil, fmt.Errorf("model config %q is an embedding model and cannot be used for chat", configID)
	}
	if isOpenAICompatibleAnthropicBaseURL(record) {
		return nil, fmt.Errorf("model config %q uses Anthropic base_url %q, but provider %q sends OpenAI-compatible /chat/completions requests; use https://api.deepseek.com for DeepSeek OpenAI-compatible chat", configID, record.BaseURL, record.Provider)
	}
	return model.NewOpenAICompatibleModel(modelConfigFromRecord(record)), nil
}

// isOpenAICompatibleAnthropicBaseURL 检测一种常见的错误配置：
// provider 标为 "openai-compatible" 但 base_url 指向 Anthropic 的 /anthropic 端点。
// 这种组合会导致以 OpenAI 协议请求 Anthropic 端点而失败，需在建客户端前拦截并给出明确提示。
func isOpenAICompatibleAnthropicBaseURL(record state.ModelConfigRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Provider), "openai-compatible") &&
		strings.Contains(strings.ToLower(strings.TrimSpace(record.BaseURL)), "/anthropic")
}

// secretPreview 生成密钥的脱敏预览：空串返回空；长度不超过 8 返回全掩码；
// 否则保留首 4 位与末 4 位、中间用 "..." 连接（形如 "sk-a...f9x2"）。
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

// nonEmpty 返回去空白后非空的 value，否则返回 fallback，用于给缺省字段兜底。
func nonEmpty(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// serveStaticFile 返回一个只服务 staticDir 下指定文件名的 Hertz handler。
// 文件名在注册路由时写死（白名单），这里直接拼接目录并回传文件；
// 设置 Cache-Control: no-cache 以便前端产物更新后浏览器能及时拉到最新版本。
func (s *HTTPServer) serveStaticFile(name string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Response.Header.Set("Cache-Control", "no-cache")
		c.File(filepath.Join(s.staticDir, name))
	}
}

// writeHertzError 以统一的 {"error": message} JSON 结构和给定状态码写出普通（非 SSE）错误响应。
func writeHertzError(c *app.RequestContext, status int, message string) {
	c.JSON(status, utils.H{"error": message})
}

// writeSSEError 向 SSE 流写入一条 "error" 事件。
// 用于 SSE 已建立、HTTP 状态码无法再更改的场景，把错误信息（可选 traceID）作为事件推给前端。
func writeSSEError(writer *sse.Writer, message string, traceID string) {
	_ = writeSSEEvent(writer, contracts.AgentStreamEvent{
		Type:    "error",
		Title:   "请求失败",
		Content: message,
		TraceID: traceID,
	})
}

// writeSSEEvent 把一个 AgentStreamEvent 序列化为 JSON 并作为一条 SSE 事件写出。
//
// 约定：SSE 的 event 名取自 event.Type，data 为事件的完整 JSON 序列化结果；
// 未显式设置 CreatedAt 时补当前时间，保证前端始终能拿到事件时间戳。
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
