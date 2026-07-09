/**
 * main.ts —— 前端单页应用（SPA）的唯一入口，不依赖任何框架。
 *
 * 整体架构：
 * - 原生 TypeScript + 原生 DOM API（document.createElement / innerHTML），无 React/Vue；
 * - 状态管理：模块级顶层变量（conversations / availableSkills / modelConfigs 等）
 *   充当全局 store，配合一组 renderXxx() 函数手动触发重渲染（"状态变了就整块重画"）；
 * - 视图切换：单页内四个视图（chat / skills / knowledge / models），通过 setView()
 *   切换各区块的 hidden class，而非路由；
 * - 持久化：模型选择、知识库选择、侧边栏折叠状态存 localStorage，业务数据全部走后端 API；
 * - 流式对话：POST /api/conversations/:id/chat/stream 返回 SSE，前端用 fetch +
 *   ReadableStream 手动解析（而非 EventSource，因为需要 POST body），
 *   按事件类型增量渲染"思考过程 trace + 回答 Markdown"；
 * - 多模型回答：一次提问可同时请求 1 个主模型 + 至多 2 个副模型，每个模型一张卡片
 *   并行流式渲染，回答完成后用户可点选任一回答作为本轮的 canonical（进入后续上下文）。
 */
import { renderMarkdown } from "./markdown.js";

// ===================== 类型定义（与后端 JSON 协议一一对应） =====================

/** 消息角色：用户 / 助手 / 工具结果 */
type Role = "user" | "assistant" | "tool";

type Conversation = {
  id: string;
  title: string;
  createdAt: string;
  updatedAt: string;
};

type Message = {
  id?: string;
  conversationId?: string;
  role: Role;
  content: string;
  createdAt?: string;
  metadata?: MessageMetadata;
};

/**
 * 助手消息的元数据。多模型回答的关键信息都存在这里：
 * - multiModel：标记该消息是"一轮多模型回答"，渲染时走多卡片布局；
 * - turnId：本轮对话的 ID，用于 PUT canonical-response 接口定位轮次；
 * - canonicalResponseId：当前被选为"用于上下文"的回答 ID；
 * - modelResponses：本轮所有模型的回答摘要列表（历史消息回放时使用）。
 */
type MessageMetadata = Record<string, unknown> & {
  multiModel?: boolean;
  turnId?: string;
  primaryModelConfigId?: string;
  canonicalResponseId?: string;
  modelResponses?: ModelResponseSummary[];
};

/** 多模型回答中单个模型的回答快照（含状态、错误信息、是否主模型等） */
type ModelResponseSummary = {
  id: string;
  turnId?: string;
  modelConfigId: string;
  traceId?: string;
  content: string;
  status: string;
  error?: string;
  primaryResponse?: boolean;
  createdAt?: string;
  completedAt?: string;
  metadata?: Record<string, unknown>;
};

type ToolCall = {
  id: string;
  toolId: string;
  name?: string;
  input: Record<string, unknown>;
};

type ToolResult = {
  toolId: string;
  ok: boolean;
  output?: unknown;
  error?: string;
};

/** Skill（技能）定义，对应后端 /api/skills 返回结构；readme/instructions 等重字段按需懒加载 */
type Skill = {
  id: string;
  name: string;
  purpose?: string;
  description: string;
  whenToUse?: string;
  triggers: string[];
  source?: string;
  path?: string;
  disableModelInvocation?: boolean;
  userInvocable?: boolean;
  allowedTools?: string[];
  disallowedTools?: string[];
  model?: string;
  effort?: string;
  context?: string;
  agent?: string;
  shell?: string;
  enabled: boolean;
  readme?: string;
  instructions?: string;
  examples?: SkillExample[];
  resources?: SkillResource[];
};

type SkillExample = {
  name?: string;
  user: string;
  assistant: string;
};

type SkillResource = {
  name: string;
  type: string;
  content?: string;
  uri?: string;
};

/**
 * SSE 流式事件的统一载荷。后端把事件类型放在 data JSON 的 type 字段
 * （若缺失则回退到 SSE 的 event: 行）。前端消费的主要类型：
 * - user_message：用户消息回显（前端已本地渲染，直接忽略）；
 * - model_message：agent 过程消息（思考、工具调用/结果），进入"思考过程" trace 面板；
 * - answer_delta / answer_reset：最终回答的增量文本 / 重置（模型重新作答时清空重来）；
 * - final：单个回答完成（含完整内容）；done：整个流结束；error：出错；
 * - 多模型专属：multi_model_start（本轮开始）、model_response_start（某模型开始回答）、
 *   canonical_selected（后端选定用于上下文的回答）。
 * modelConfigId/responseId 用于把事件路由到对应模型卡片。
 */
type AgentStreamEvent = {
  type: string;
  round?: number;
  title?: string;
  content?: string;
  turnId?: string;
  responseId?: string;
  modelConfigId?: string;
  primary?: boolean;
  canonical?: boolean;
  message?: Message;
  toolCall?: ToolCall;
  toolResult?: ToolResult;
  toolCalls?: ToolCall[];
  toolResults?: ToolResult[];
  traceId?: string;
  createdAt?: string;
};

type ConversationsResponse = {
  conversations: Conversation[];
  defaultConversationId: string;
};

type MessagesResponse = {
  messages: Message[];
};

type SkillsResponse = {
  skills: Skill[];
};

type SkillDetailResponse = {
  skill: Skill;
  skills?: Skill[];
};

/** 知识库中的单个文档（含分块统计：child 为检索用小块，parent 为召回上下文的大块） */
type KnowledgeItem = {
  id: string;
  knowledgeBaseId?: string;
  name: string;
  size: number;
  contentType?: string;
  importedAt: string;
  status?: string;
  chunkCount?: number;
  childChunkCount?: number;
  parentChunkCount?: number;
};

type KnowledgeBase = {
  id: string;
  name: string;
  description?: string;
  status: string;
  createdAt: string;
  updatedAt: string;
  documentCount: number;
  chunkCount: number;
  childChunkCount: number;
  parentChunkCount: number;
};

type KnowledgeResponse = {
  items: KnowledgeItem[];
  knowledgeBases?: KnowledgeBase[];
};

type KnowledgeImportResponse = {
  item: KnowledgeItem;
  items: KnowledgeItem[];
  knowledgeBases?: KnowledgeBase[];
};

type KnowledgeBasesResponse = {
  knowledgeBases: KnowledgeBase[];
  items?: KnowledgeItem[];
};

type KnowledgeBaseCreateResponse = {
  knowledgeBase: KnowledgeBase;
  knowledgeBases: KnowledgeBase[];
  items: KnowledgeItem[];
};

/**
 * 模型配置。modelType 区分两类：
 * - reasoning（推理/对话模型）：可作为主/副模型参与聊天；
 * - embedding（向量模型）：仅用于知识库向量化，不可被选为对话模型。
 * apiKeySet/apiKeyPreview：后端不回传完整 key，只回传是否已设置与掩码预览。
 */
type ModelConfig = {
  id: string;
  modelType?: "reasoning" | "embedding" | string;
  provider: string;
  baseURL: string;
  model: string;
  temperature: number;
  timeoutSeconds: number;
  extra?: Record<string, unknown>;
  updatedAt?: string;
  apiKeySet: boolean;
  apiKeyPreview?: string;
};

type ModelConfigsResponse = {
  configs: ModelConfig[];
  defaultConfigId: string;
};

type ModelConfigResponse = {
  config: ModelConfig;
};

// ===================== 流式渲染状态（本轮 SSE 期间的临时 DOM 句柄） =====================

/**
 * 一次流式回答的渲染状态。mode 决定布局：
 * - "single"：单模型，直接持有 traceList（思考过程）/answerPanel（回答）等 DOM 引用；
 * - "multi"：多模型，细节下沉到 multi.models（每个模型一份 ModelStreamState）。
 * answerMarkdown 累积原始 Markdown 文本，每收到 delta 就整体重渲染一次
 * （简单可靠，代价是重复解析，对聊天长度的文本可接受）。
 */
type StreamRenderState = {
  wrapper: HTMLElement;
  mode: "single" | "multi";
  traceList?: HTMLElement;
  answerPanel?: HTMLElement;
  answerContent?: HTMLElement;
  answerMarkdown?: string;
  loadingEl?: HTMLElement;
  thinkingDetails?: HTMLDetailsElement;
  hasFinalAnswer?: boolean;
  hasTraceEvents?: boolean;
  multi?: MultiStreamState;
};

/** 多模型模式下单个模型卡片的流式状态（与 single 模式的字段一一对应，外加状态标签） */
type ModelStreamState = {
  responseId?: string;
  modelConfigId: string;
  card: HTMLElement;
  statusEl: HTMLElement;
  traceList: HTMLElement;
  answerPanel: HTMLElement;
  answerContent: HTMLElement;
  answerMarkdown: string;
  loadingEl: HTMLElement;
  thinkingDetails: HTMLDetailsElement;
  hasFinalAnswer: boolean;
  hasTraceEvents: boolean;
};

/** 多模型整体状态：轮次 ID、已选 canonical 回答 ID、提示条、卡片网格与各模型状态映射 */
type MultiStreamState = {
  turnId?: string;
  canonicalResponseId?: string;
  tipEl: HTMLElement;
  gridEl: HTMLElement;
  models: Map<string, ModelStreamState>;
};

type ModalAction = {
  label: string;
  variant?: "primary" | "secondary" | "danger";
  onClick: () => void | Promise<void>;
};

type ActionMenuItem = {
  label: string;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void | Promise<void>;
};

/** 输入框"对话设置"级联弹层当前所在面板：根 / 知识库选择 / 多模型选择 */
type ComposerSettingsPanel = "root" | "knowledge" | "multi";

// ===================== 静态 DOM 引用（页面骨架在 index.html 中，启动时一次性抓取） =====================
// mustQuery 在元素缺失时直接抛错，保证后续代码不需要判空。

const conversationListEl = mustQuery<HTMLDivElement>("#conversationList");
const conversationTitleEl = mustQuery<HTMLHeadingElement>("#conversationTitle");
const messagesEl = mustQuery<HTMLDivElement>("#messages");
const composerEl = mustQuery<HTMLFormElement>("#composer");
const inputEl = mustQuery<HTMLTextAreaElement>("#messageInput");
const sendButtonEl = mustQuery<HTMLButtonElement>("#sendButton");
const shellEl = mustQuery<HTMLElement>(".shell");
const sidebarToggleEl = mustQuery<HTMLButtonElement>("#sidebarToggle");
const sidebarToggleTipEl = mustQuery<HTMLSpanElement>("#sidebarToggleTip");
const newConversationEl = mustQuery<HTMLButtonElement>("#newConversation");
const skillsNavEl = mustQuery<HTMLButtonElement>("#skillsNav");
const knowledgeNavEl = mustQuery<HTMLButtonElement>("#knowledgeNav");
const modelNavEl = mustQuery<HTMLButtonElement>("#modelNav");
const skillPageEl = mustQuery<HTMLElement>("#skillPage");
const skillSearchEl = mustQuery<HTMLInputElement>("#skillSearch");
const skillListEl = mustQuery<HTMLDivElement>("#skillList");
const knowledgePageEl = mustQuery<HTMLElement>("#knowledgePage");
const knowledgeBaseListEl = mustQuery<HTMLDivElement>("#knowledgeBaseList");
const knowledgeListEl = mustQuery<HTMLDivElement>("#knowledgeList");
const createKnowledgeBaseEl = mustQuery<HTMLButtonElement>("#createKnowledgeBase");
const addKnowledgeEl = mustQuery<HTMLButtonElement>("#addKnowledge");
const knowledgeFileInputEl = mustQuery<HTMLInputElement>("#knowledgeFileInput");
const modelPageEl = mustQuery<HTMLElement>("#modelPage");
const modelConfigListEl = mustQuery<HTMLDivElement>("#modelConfigList");
const addModelConfigEl = mustQuery<HTMLButtonElement>("#addModelConfig");
const primaryModelSelectorEl = mustQuery<HTMLSelectElement>("#primaryModelSelector");
const primaryModelToggleEl = mustQuery<HTMLButtonElement>("#primaryModelToggle");
const primaryModelPopoverEl = mustQuery<HTMLDivElement>("#primaryModelPopover");
const ragKnowledgeSelectorEl = mustQuery<HTMLSelectElement>("#ragKnowledgeSelector");
const composerSettingsButtonEl = mustQuery<HTMLButtonElement>("#composerSettingsButton");
const composerSettingsPopoverEl = mustQuery<HTMLDivElement>("#composerSettingsPopover");
const composerSelectionSummaryEl = mustQuery<HTMLDivElement>("#composerSelectionSummary");
const skillMenuEl = mustQuery<HTMLDivElement>("#skillMenu");
const messageLocatorEl = mustQuery<HTMLElement>("#messageLocator");
const chatPaneEl = mustQuery<HTMLElement>(".chatPane");
const modalRootEl = mustQuery<HTMLDivElement>("#modalRoot");

/** 知识库单文件导入上限：256MB（前端先行校验，避免无谓上传） */
const maxKnowledgeFileBytes = 256 * 1024 * 1024;

// ===================== 全局可变状态（模块级变量即"store"） =====================
// 约定：修改这些状态后必须手动调用对应的 renderXxx() 重绘相关区块。
// 用户偏好（当前模型、副模型、RAG 知识库、侧边栏折叠）额外落 localStorage 以便刷新后恢复。

let conversations: Conversation[] = [];
let availableSkills: Skill[] = [];
let knowledgeBases: KnowledgeBase[] = [];
let knowledgeItems: KnowledgeItem[] = [];
let modelConfigs: ModelConfig[] = [];
let enabledSkillNames = new Set<string>();
let activeConversationId = "";
// 当前主模型（对话使用的推理模型），持久化到 localStorage
let activeModelConfigId = window.localStorage.getItem("activeModelConfigId") || "default";
// 副模型列表（多模型回答时并行提问，至多 2 个）
let secondaryModelConfigIds = loadSecondaryModelConfigIds();
// 知识库管理页当前选中的知识库（管理视图用）
let activeKnowledgeBaseId = window.localStorage.getItem("activeKnowledgeBaseId") || "";
// 聊天时启用 RAG 检索的知识库（与上者独立：管理什么和用什么是两回事）
let activeRagKnowledgeBaseId = window.localStorage.getItem("activeRagKnowledgeBaseId") || "";
// 全局流式标记：SSE 进行中时禁用几乎所有交互（发送、切换视图、增删改配置），防止并发状态混乱
let isStreaming = false;
let currentView: "chat" | "skills" | "knowledge" | "models" = "chat";
let slashMenuIndex = 0;
let slashMenuSkills: Skill[] = [];
let skillLoadError = "";
let knowledgeLoadError = "";
let modelLoadError = "";
let isKnowledgeImporting = false;
let expandedSkillName: string | null = null;
let editingModelConfigId: string | null = null;
let expandedModelConfigId: string | null = null;
let primaryModelPopoverOpen = false;
let composerSettingsPopoverOpen = false;
let composerSettingsPanel: ComposerSettingsPanel = "root";
let modalCancelHandler: (() => void) | null = null;
let sidebarCollapsed = window.localStorage.getItem("sidebarCollapsed") === "true";

setSidebarCollapsed(sidebarCollapsed);

// ===================== 全局事件绑定（模块加载时执行一次） =====================

// 发送消息：表单提交（点击按钮或 Enter），流式进行中忽略
composerEl.addEventListener("submit", async (event) => {
  event.preventDefault();
  const message = inputEl.value.trim();
  if (!message || isStreaming) return;
  inputEl.value = "";
  hideSlashMenu();
  await sendMessage(message);
});

// 输入框按键：优先让斜杠技能菜单消费方向键/Enter/Esc；
// 其余情况下 Enter 直接发送（Shift+Enter 换行；isComposing 避免中文输入法候选期误发）
inputEl.addEventListener("keydown", (event) => {
  if (handleSlashMenuKeydown(event)) return;
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    composerEl.requestSubmit();
  }
});

inputEl.addEventListener("input", () => {
  renderSlashMenu();
});

// 失焦后延迟收起斜杠菜单：留出时间让菜单项的 mousedown 先触发选择
inputEl.addEventListener("blur", () => {
  window.setTimeout(hideSlashMenu, 120);
});

sidebarToggleEl.addEventListener("click", () => {
  setSidebarCollapsed(!sidebarCollapsed);
});

skillSearchEl.addEventListener("input", () => {
  renderSkills();
});

// 主模型切换（隐藏的原生 select，作为自定义弹层的可访问性兜底）：
// 主模型变更后要把它从副模型列表中剔除（同一模型不能既是主又是副）
primaryModelSelectorEl.addEventListener("change", () => {
  activeModelConfigId = primaryModelSelectorEl.value || defaultReasoningModelConfigId();
  secondaryModelConfigIds = secondaryModelConfigIds.filter((id) => id !== activeModelConfigId);
  saveModelSelection();
  renderModelSelector();
});

// 主模型选择弹层开关：与"对话设置"弹层互斥（同一时间只开一个）
primaryModelToggleEl.addEventListener("click", (event) => {
  event.stopPropagation();
  if (isStreaming || primaryModelToggleEl.disabled) return;
  primaryModelPopoverOpen = !primaryModelPopoverOpen;
  composerSettingsPopoverOpen = false;
  renderModelSelector();
});

// "对话设置"弹层开关（知识库 / 多模型级联面板），打开时默认进入知识库面板
composerSettingsButtonEl.addEventListener("click", (event) => {
  event.stopPropagation();
  if (isStreaming || composerSettingsButtonEl.disabled) return;
  composerSettingsPopoverOpen = !composerSettingsPopoverOpen;
  if (composerSettingsPopoverOpen) {
    composerSettingsPanel = "knowledge";
    primaryModelPopoverOpen = false;
  }
  renderComposerSettingsPopover();
  renderPrimaryModelPicker();
});

// 全局点击：点击弹层外部区域时关闭两个 popover（点在弹层/按钮内部则放行）
document.addEventListener("click", (event) => {
  const target = event.target;
  if (primaryModelPopoverOpen) {
    if (target instanceof Node && (primaryModelPopoverEl.contains(target) || primaryModelToggleEl.contains(target))) {
      return;
    }
    primaryModelPopoverOpen = false;
    renderPrimaryModelPicker();
  }
  if (composerSettingsPopoverOpen) {
    if (target instanceof Node && (composerSettingsPopoverEl.contains(target) || composerSettingsButtonEl.contains(target))) {
      return;
    }
    composerSettingsPopoverOpen = false;
    composerSettingsPanel = "root";
    renderComposerSettingsPopover();
  }
});

// 全局 Esc：优先关闭 popover，其次关闭模态弹窗
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && (primaryModelPopoverOpen || composerSettingsPopoverOpen)) {
    primaryModelPopoverOpen = false;
    composerSettingsPopoverOpen = false;
    composerSettingsPanel = "root";
    renderModelSelector();
    renderComposerSettingsPopover();
    event.preventDefault();
    return;
  }
  if (event.key === "Escape" && !modalRootEl.classList.contains("hidden")) {
    event.preventDefault();
    closeAppModal(true);
  }
});

ragKnowledgeSelectorEl.addEventListener("change", () => {
  activeRagKnowledgeBaseId = ragKnowledgeSelectorEl.value || "";
  saveRagKnowledgeSelection();
  renderComposerSettingsPopover();
  renderComposerSelectionSummary();
});

// 左侧导航：切换到技能库视图（首次进入才拉取数据，之后走内存缓存）
skillsNavEl.addEventListener("click", async () => {
  if (isStreaming) return;
  setView("skills");
  if (availableSkills.length === 0 && !skillLoadError) {
    await loadSkills().catch(showSkillError);
  } else {
    renderSkills();
  }
});

// 左侧导航：切换到知识库视图（每次进入都刷新，因为导入/分块状态可能变化）
knowledgeNavEl.addEventListener("click", async () => {
  if (isStreaming) return;
  setView("knowledge");
  await loadKnowledge().catch(showKnowledgeError);
});

// 左侧导航：切换到模型配置视图
modelNavEl.addEventListener("click", async () => {
  if (isStreaming) return;
  setView("models");
  if (modelConfigs.length === 0 && !modelLoadError) {
    await loadModelConfigs().catch(showModelError);
  } else {
    renderModelConfigs();
  }
});

// "添加"知识文件：借助隐藏的 <input type=file> 打开系统文件选择器
addKnowledgeEl.addEventListener("click", () => {
  if (isStreaming || isKnowledgeImporting || !activeKnowledgeBaseId) return;
  knowledgeFileInputEl.click();
});

// 新建知识库：弹出文本输入对话框获取名称
createKnowledgeBaseEl.addEventListener("click", async () => {
  if (isStreaming || isKnowledgeImporting) return;
  const name = await promptTextDialog({
    title: "新建知识库",
    label: "知识库名称",
    placeholder: "例如：产品文档、投研材料、项目知识",
    confirmText: "创建"
  });
  if (!name || !name.trim()) return;
  try {
    await createKnowledgeBase(name.trim());
  } catch (error) {
    showKnowledgeError(error);
  }
});

addModelConfigEl.addEventListener("click", () => {
  if (isStreaming) return;
  openModelConfigDialog();
});

// 文件选择完成：先做体积前置校验，再走 multipart 导入；随后清空 input 以便重复选同一文件
knowledgeFileInputEl.addEventListener("change", async () => {
  const file = knowledgeFileInputEl.files?.[0];
  knowledgeFileInputEl.value = "";
  if (!file) return;
  if (file.size > maxKnowledgeFileBytes) {
    showKnowledgeError(new Error(`文件大小超过限制：最大支持 ${formatFileSize(maxKnowledgeFileBytes)}，当前文件 ${formatFileSize(file.size)}`));
    return;
  }
  try {
    await importKnowledge(file);
  } catch (error) {
    showKnowledgeError(error);
  }
});

// 新建会话：POST 创建后切为当前会话并刷新列表与消息区
newConversationEl.addEventListener("click", async () => {
  if (isStreaming) return;
  try {
    setView("chat");
    const response = await request<{ conversation: Conversation }>("/api/conversations", {
      method: "POST",
      body: JSON.stringify({ title: "新对话" })
    });
    activeConversationId = response.conversation.id;
    await loadConversations();
    await loadMessages();
  } catch (error) {
    showPageError(error);
  }
});

// 应用启动入口
void boot().catch(showPageError);

// ===================== 数据加载 / API 调用层 =====================

/**
 * 启动流程：按依赖顺序加载会话列表 → 模型配置 → 知识库（供 RAG 下拉）→ 技能 → 当前会话消息。
 * 非核心数据（模型/知识库/技能）加载失败只在各自区块显示错误，不阻断聊天主流程。
 */
async function boot(): Promise<void> {
  await loadConversations(false);
  await loadModelConfigs().catch(showModelError);
  await loadKnowledgeBasesForSelector().catch(showKnowledgeError);
  await loadSkills().catch((error) => {
    showSkillError(error);
  });
  await loadMessages();
}

/**
 * 拉取会话列表并重绘侧边栏。
 * @param autoSelect 为 true 且当前无选中会话时，自动选中后端默认会话或第一个会话
 */
async function loadConversations(autoSelect = true): Promise<void> {
  const body = await request<ConversationsResponse>("/api/conversations");
  conversations = body.conversations ?? [];
  if (autoSelect && !activeConversationId) {
    activeConversationId = body.defaultConversationId || conversations[0]?.id || "";
  }
  const active = conversations.find((item) => item.id === activeConversationId);
  if (active && currentView === "chat") {
    conversationTitleEl.textContent = active.title || "新对话";
  }
  renderConversations();
}

/** 拉取技能列表（GET /api/skills），同步"已启用技能"集合并重绘技能页 */
async function loadSkills(): Promise<void> {
  const body = await request<SkillsResponse>("/api/skills");
  availableSkills = body.skills ?? [];
  enabledSkillNames = new Set(availableSkills.filter((skill) => skill.enabled).map((skill) => skill.name));
  skillLoadError = "";
  renderSkills();
}

/** 仅为输入框的 RAG 知识库下拉加载知识库列表（启动时调用，不加载文档明细） */
async function loadKnowledgeBasesForSelector(): Promise<void> {
  const body = await request<KnowledgeBasesResponse>("/api/knowledge-bases");
  knowledgeBases = body.knowledgeBases ?? [];
  normalizeRagKnowledgeSelection();
  knowledgeLoadError = "";
  renderRagKnowledgeSelector();
}

/**
 * 加载知识库管理页数据：先取知识库列表，校正当前选中项（被删则回退到第一个），
 * 再取选中知识库下的文档列表。
 */
async function loadKnowledge(): Promise<void> {
  const basesBody = await request<KnowledgeBasesResponse>("/api/knowledge-bases");
  knowledgeBases = basesBody.knowledgeBases ?? [];
  normalizeRagKnowledgeSelection();
  if (!knowledgeBases.some((base) => base.id === activeKnowledgeBaseId)) {
    activeKnowledgeBaseId = knowledgeBases[0]?.id || "";
    if (activeKnowledgeBaseId) {
      window.localStorage.setItem("activeKnowledgeBaseId", activeKnowledgeBaseId);
    } else {
      window.localStorage.removeItem("activeKnowledgeBaseId");
    }
  }
  if (activeKnowledgeBaseId) {
    const body = await request<KnowledgeResponse>(`/api/knowledge?knowledgeBaseId=${encodeURIComponent(activeKnowledgeBaseId)}`);
    knowledgeItems = body.items ?? [];
    if (body.knowledgeBases) {
      knowledgeBases = body.knowledgeBases;
    }
  } else {
    knowledgeItems = [];
  }
  knowledgeLoadError = "";
  renderKnowledge();
}

/** 创建知识库并自动选中它（后端一次性返回最新列表，避免二次请求） */
async function createKnowledgeBase(name: string): Promise<void> {
  const body = await request<KnowledgeBaseCreateResponse>("/api/knowledge-bases", {
    method: "POST",
    body: JSON.stringify({ name })
  });
  knowledgeBases = body.knowledgeBases ?? [];
  activeKnowledgeBaseId = body.knowledgeBase.id;
  window.localStorage.setItem("activeKnowledgeBaseId", activeKnowledgeBaseId);
  knowledgeItems = body.items ?? [];
  knowledgeLoadError = "";
  renderKnowledge();
}

async function selectKnowledgeBase(id: string): Promise<void> {
  activeKnowledgeBaseId = id;
  window.localStorage.setItem("activeKnowledgeBaseId", activeKnowledgeBaseId);
  const body = await request<KnowledgeResponse>(`/api/knowledge?knowledgeBaseId=${encodeURIComponent(activeKnowledgeBaseId)}`);
  knowledgeItems = body.items ?? [];
  if (body.knowledgeBases) {
    knowledgeBases = body.knowledgeBases;
  }
  knowledgeLoadError = "";
  renderKnowledge();
}

async function deleteKnowledgeBase(id: string): Promise<void> {
  const body = await request<KnowledgeBasesResponse>(`/api/knowledge-bases/${encodeURIComponent(id)}`, {
    method: "DELETE"
  });
  knowledgeBases = body.knowledgeBases ?? [];
  if (activeKnowledgeBaseId === id || !knowledgeBases.some((base) => base.id === activeKnowledgeBaseId)) {
    activeKnowledgeBaseId = knowledgeBases[0]?.id || "";
    if (activeKnowledgeBaseId) {
      window.localStorage.setItem("activeKnowledgeBaseId", activeKnowledgeBaseId);
    } else {
      window.localStorage.removeItem("activeKnowledgeBaseId");
    }
  }
  if (activeKnowledgeBaseId) {
    const docs = await request<KnowledgeResponse>(`/api/knowledge?knowledgeBaseId=${encodeURIComponent(activeKnowledgeBaseId)}`);
    knowledgeItems = docs.items ?? [];
  } else {
    knowledgeItems = [];
  }
  knowledgeLoadError = "";
  renderKnowledge();
}

async function loadModelConfigs(): Promise<void> {
  const body = await request<ModelConfigsResponse>("/api/model/configs");
  modelConfigs = body.configs ?? [];
  const fallback = body.defaultConfigId || defaultReasoningModelConfigId();
  if (!modelConfigs.some((config) => config.id === activeModelConfigId && modelConfigType(config) === "reasoning")) {
    activeModelConfigId = fallback;
  }
  normalizeModelSelection();
  modelLoadError = "";
  renderModelSelector();
  renderModelConfigs();
}

/**
 * 新增或更新一个模型配置（PUT /api/model/configs/:id）。写回本地列表并排序（default 置顶、推理模型在前）。
 * 若保存的是推理模型则自动设为当前主模型并从副模型剔除；若当前主模型失效则回退默认。
 */
async function saveModelConfig(id: string, payload: Record<string, unknown>): Promise<void> {
  const body = await request<ModelConfigResponse>(`/api/model/configs/${encodeURIComponent(id)}`, {
    method: "PUT",
    body: JSON.stringify(payload)
  });
  const next = body.config;
  const index = modelConfigs.findIndex((config) => config.id === next.id);
  if (index >= 0) {
    modelConfigs[index] = next;
  } else {
    modelConfigs.push(next);
  }
  modelConfigs.sort((left, right) => {
    if (left.id === "default") return -1;
    if (right.id === "default") return 1;
    if (modelConfigType(left) !== modelConfigType(right)) return modelConfigType(left) === "reasoning" ? -1 : 1;
    return left.id.localeCompare(right.id);
  });
  if (modelConfigType(next) === "reasoning") {
    activeModelConfigId = next.id;
    secondaryModelConfigIds = secondaryModelConfigIds.filter((id) => id !== next.id).slice(0, 2);
  } else if (!modelConfigs.some((config) => config.id === activeModelConfigId && modelConfigType(config) === "reasoning")) {
    activeModelConfigId = defaultReasoningModelConfigId();
  }
  normalizeModelSelection();
  modelLoadError = "";
  editingModelConfigId = null;
  renderModelSelector();
  renderModelConfigs();
}

async function deleteModelConfig(id: string): Promise<void> {
  const body = await request<ModelConfigsResponse>(`/api/model/configs/${encodeURIComponent(id)}`, {
    method: "DELETE"
  });
  modelConfigs = body.configs ?? [];
  if (activeModelConfigId === id || !modelConfigs.some((config) => config.id === activeModelConfigId && modelConfigType(config) === "reasoning")) {
    activeModelConfigId = body.defaultConfigId || defaultReasoningModelConfigId();
  }
  secondaryModelConfigIds = secondaryModelConfigIds.filter((configID) => configID !== id);
  normalizeModelSelection();
  if (editingModelConfigId === id) editingModelConfigId = null;
  if (expandedModelConfigId === id) expandedModelConfigId = null;
  renderModelSelector();
  renderModelConfigs();
}

/**
 * 上传单个文件到当前知识库（multipart POST /api/knowledge/import，后端负责解析/分块/向量化）。
 * 用独立 fetch（非 request 封装）以支持 FormData。导入期间置 isKnowledgeImporting 并把"添加"按钮改为"导入中"，
 * finally 中无论成败都复位按钮状态并重绘。
 */
async function importKnowledge(file: File): Promise<void> {
  if (!activeKnowledgeBaseId) {
    throw new Error("请先创建或选择知识库");
  }
  if (file.size > maxKnowledgeFileBytes) {
    throw new Error(`文件大小超过限制：最大支持 ${formatFileSize(maxKnowledgeFileBytes)}，当前文件 ${formatFileSize(file.size)}`);
  }
  isKnowledgeImporting = true;
  addKnowledgeEl.disabled = true;
  addKnowledgeEl.textContent = "导入中";
  renderKnowledge();
  try {
    const form = new FormData();
    form.append("file", file);
    form.append("knowledgeBaseId", activeKnowledgeBaseId);
    const response = await fetch("/api/knowledge/import", {
      method: "POST",
      body: form
    });
    const text = await response.text();
    let body: (KnowledgeImportResponse & { error?: string }) | null = null;
    if (text) {
      try {
        body = JSON.parse(text) as KnowledgeImportResponse & { error?: string };
      } catch {
        body = null;
      }
    }
    if (!response.ok) {
      throw new Error(body?.error || text || `Request failed: ${response.status}`);
    }
    if (!body) {
      throw new Error("Invalid JSON response from /api/knowledge/import");
    }
    knowledgeItems = body.items ?? [];
    if (body.knowledgeBases) {
      knowledgeBases = body.knowledgeBases;
    }
    knowledgeLoadError = "";
  } finally {
    isKnowledgeImporting = false;
    addKnowledgeEl.disabled = false;
    addKnowledgeEl.textContent = "添加";
    renderKnowledge();
  }
}

async function deleteKnowledgeItem(id: string): Promise<void> {
  const suffix = activeKnowledgeBaseId ? `?knowledgeBaseId=${encodeURIComponent(activeKnowledgeBaseId)}` : "";
  const body = await request<KnowledgeResponse>(`/api/knowledge/${encodeURIComponent(id)}${suffix}`, {
    method: "DELETE"
  });
  knowledgeItems = body.items ?? [];
  if (body.knowledgeBases) {
    knowledgeBases = body.knowledgeBases;
  }
  knowledgeLoadError = "";
  renderKnowledge();
}

/** 惰性加载技能完整详情：若本地已含 readme（重字段已拉取）则直接返回，否则 GET /api/skills/:name 并合并进缓存。 */
async function ensureSkillDetail(name: string): Promise<Skill> {
  const index = availableSkills.findIndex((skill) => skill.name === name);
  if (index >= 0 && availableSkills[index].readme !== undefined) {
    return availableSkills[index];
  }
  const detail = await request<SkillDetailResponse>(`/api/skills/${encodeURIComponent(name)}`);
  const merged = { ...(availableSkills[index] ?? {}), ...detail.skill };
  if (index >= 0) {
    availableSkills[index] = merged;
  } else {
    availableSkills.push(merged);
  }
  return merged;
}

async function deleteConversation(id: string): Promise<void> {
  const body = await request<ConversationsResponse>(`/api/conversations/${encodeURIComponent(id)}`, {
    method: "DELETE"
  });
  conversations = body.conversations ?? [];
  if (activeConversationId === id || !conversations.some((item) => item.id === activeConversationId)) {
    activeConversationId = body.defaultConversationId || conversations[0]?.id || "";
  }
  renderConversations();
  if (currentView === "chat") {
    await loadMessages();
  }
}

/** 加载并渲染当前会话的历史消息（GET /messages）。无会话时显示空态；同时同步标题栏文案。 */
async function loadMessages(): Promise<void> {
  if (!activeConversationId) {
    conversationTitleEl.textContent = "新会话";
    renderMessages([]);
    return;
  }
  const conversation = conversations.find((item) => item.id === activeConversationId);
  conversationTitleEl.textContent = conversation?.title || "新对话";
  const body = await request<MessagesResponse>(`/api/conversations/${activeConversationId}/messages`);
  renderMessages(body.messages ?? []);
}

/**
 * 发送一条用户消息并驱动整轮流式回答。
 * 流程：
 * 1. 锁定上一轮多模型消息的 canonical 选择（历史轮次不再可切换）；
 * 2. 本地立即回显用户气泡（乐观渲染，不等后端）；
 * 3. 依据当前选中的推理模型数量决定单模型 / 多模型渲染骨架；
 * 4. 置 isStreaming=true 并禁用全部交互（防并发），确保当前会话存在后发起 SSE；
 * 5. 无论成功失败，finally 中恢复交互状态并把焦点还给输入框。
 * 出错时用 finishStreamWithAnswer 把错误写进回答区，不抛给上层。
 */
async function sendMessage(content: string): Promise<void> {
  lockExistingCanonicalSelection();
  addMessage({ role: "user", content });
  const modelConfigIds = selectedReasoningModelConfigIds();
  const streamState = modelConfigIds.length > 1 ? addMultiAssistantStream(modelConfigIds) : addAssistantStream();
  isStreaming = true;
  setInteractionDisabled(true);
  renderSkills();
  renderKnowledge();
  renderModelSelector();
  renderModelConfigs();
  hideSlashMenu();

  try {
    await ensureActiveConversation();
    await streamChat(content, streamState, modelConfigIds);
    await loadConversations();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    finishStreamWithAnswer(streamState, "请求失败：" + message);
  } finally {
    isStreaming = false;
    setInteractionDisabled(false);
    renderSkills();
    renderKnowledge();
    renderModelSelector();
    renderModelConfigs();
    inputEl.focus();
  }
}

/** 统一开关所有会触发状态变更的交互元素（发送、新建会话、切视图、增删配置），流式期间置灰以防并发。 */
function setInteractionDisabled(disabled: boolean): void {
  sendButtonEl.disabled = disabled;
  inputEl.disabled = disabled;
  newConversationEl.disabled = disabled;
  skillsNavEl.disabled = disabled;
  knowledgeNavEl.disabled = disabled;
  modelNavEl.disabled = disabled;
  createKnowledgeBaseEl.disabled = disabled;
  addKnowledgeEl.disabled = disabled || isKnowledgeImporting;
  addModelConfigEl.disabled = disabled;
}

/** 确保存在可用会话：无选中会话时先尝试从后端拉取默认会话，仍无则新建一个，避免 SSE 请求缺少会话 ID。 */
async function ensureActiveConversation(): Promise<void> {
  if (activeConversationId) return;
  await loadConversations(false);
  if (activeConversationId) return;
  const response = await request<{ conversation: Conversation }>("/api/conversations", {
    method: "POST",
    body: JSON.stringify({ title: "新对话" })
  });
  activeConversationId = response.conversation.id;
  await loadConversations();
}

/**
 * 发起流式对话请求并逐块解析 SSE。
 * 用 fetch + ReadableStream 而非 EventSource，因为需要 POST body（携带模型/知识库选择）。
 * 请求体同时带 modelConfigId（兼容单模型旧协议）、modelConfigIds（多模型列表）与
 * primaryModelConfigId（主模型），knowledgeBaseId 仅在选中且非空知识库时下发。
 * 读取循环：TextDecoder 流式解码字节 → 累积到 buffer → consumeSSEBuffer 按事件边界切分消费，
 * done 后再 flush 一次解码器尾部残留。
 */
async function streamChat(content: string, streamState: StreamRenderState, modelConfigIds: string[]): Promise<void> {
  const response = await fetch(`/api/conversations/${activeConversationId}/chat/stream`, {
    method: "POST",
    headers: {
      "accept": "text/event-stream",
      "content-type": "application/json"
    },
    body: JSON.stringify({
      message: content,
      modelConfigId: activeModelConfigId,
      modelConfigIds,
      primaryModelConfigId: activeModelConfigId,
      knowledgeBaseId: selectedRagKnowledgeBaseId()
    })
  });

  if (!response.ok) {
    throw new Error(await responseError(response));
  }
  if (!response.body) {
    throw new Error("Streaming response body is empty");
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  while (true) {
    const { value, done } = await reader.read();
    if (value) {
      buffer += decoder.decode(value, { stream: !done });
      buffer = consumeSSEBuffer(buffer, streamState);
    }
    if (done) break;
  }

  buffer += decoder.decode();
  consumeSSEBuffer(buffer, streamState);
}

/**
 * 从累积缓冲区中按 SSE 事件分隔（连续两个换行 "\n\n"）切出完整事件逐个处理，
 * 返回尚未构成完整事件的剩余片段留给下一次读取。这样即使一个事件被 TCP 分包，也能正确重组。
 */
function consumeSSEBuffer(buffer: string, streamState: StreamRenderState): string {
  let remaining = buffer.replace(/\r\n/g, "\n");
  while (true) {
    const boundary = remaining.indexOf("\n\n");
    if (boundary < 0) return remaining;
    const rawEvent = remaining.slice(0, boundary);
    remaining = remaining.slice(boundary + 2);
    const event = parseSSEEvent(rawEvent);
    if (event) handleStreamEvent(event, streamState);
  }
}

/**
 * 解析单个 SSE 事件文本块：
 * - 忽略空行与注释行（以 ":" 开头）；
 * - 读取 "event:" 行作为事件类型的兜底；
 * - 拼接所有 "data:" 行（去掉前导空格）后 JSON.parse 为业务事件对象；
 * - 若 data JSON 里带 type 字段则以其为准，否则回退到 event: 行的类型；
 * - JSON 解析失败时返回一个 error 事件而非抛异常，保证渲染不中断。
 */
function parseSSEEvent(raw: string): AgentStreamEvent | null {
  let eventType = "message";
  const dataLines: string[] = [];
  for (const line of raw.split(/\r?\n/)) {
    if (!line || line.startsWith(":")) continue;
    if (line.startsWith("event:")) {
      eventType = line.slice("event:".length).trim();
      continue;
    }
    if (line.startsWith("data:")) {
      dataLines.push(line.slice("data:".length).replace(/^ /, ""));
    }
  }
  if (dataLines.length === 0) return null;
  let parsed: AgentStreamEvent;
  try {
    parsed = JSON.parse(dataLines.join("\n")) as AgentStreamEvent;
  } catch (error) {
    return {
      type: "error",
      content: error instanceof Error ? error.message : "Invalid SSE event payload"
    };
  }
  if (!parsed.type) parsed.type = eventType;
  return parsed;
}

/**
 * 单模型模式的 SSE 事件分发（多模型模式转交 handleMultiStreamEvent）。事件语义：
 * - user_message：用户消息回显，已本地渲染，直接忽略；
 * - answer_delta：最终回答的增量文本，追加进回答区并重渲染 Markdown；
 * - answer_reset：模型决定重新作答，清空已积累的回答（例如加载 skill 后重来）；
 * - final：本条回答完成，用完整内容落地；
 * - done：整个流结束，收尾（若尚无最终回答则用 done 携带的内容补齐）；
 * - error：出错，把错误信息渲染进回答区；
 * - model_message：agent 的过程消息（思考/工具调用与结果），进入"思考过程" trace 面板。
 */
function handleStreamEvent(event: AgentStreamEvent, streamState: StreamRenderState): void {
  if (event.type === "user_message") return;
  if (streamState.mode === "multi") {
    handleMultiStreamEvent(event, streamState);
    return;
  }
  if (event.type === "answer_delta") {
    appendAnswerDelta(streamState, event.content || "");
    return;
  }
  if (event.type === "answer_reset") {
    resetLiveAnswer(streamState);
    return;
  }
  if (event.type === "final") {
    finishStreamWithAnswer(streamState, event.message?.content || event.content || "");
    return;
  }
  if (event.type === "done") {
    if (!streamState.hasFinalAnswer && event.message?.content) {
      finishStreamWithAnswer(streamState, event.message.content);
    }
    streamState.loadingEl?.remove();
    streamState.wrapper.classList.remove("streaming");
    return;
  }
  if (event.type === "error") {
    finishStreamWithAnswer(streamState, "请求失败：" + (event.content || "unknown error"));
    return;
  }
  if (event.type === "model_message") {
    appendTraceEvent(streamState, event);
  }
}

/**
 * 多模型模式的 SSE 事件分发。核心是靠 event.modelConfigId 把事件路由到对应模型卡片。
 * 特有事件：
 * - multi_model_start：本轮多模型开始（占位，无需渲染）；
 * - model_response_start：某个模型开始回答，更新其卡片状态标签（区分主/副模型）；
 * - canonical_selected：后端已选定用于后续上下文的回答，切换高亮并回填最新消息元数据；
 * - done：整轮结束，移除各卡片的 loading，并用最终 message 更新这条历史消息（供刷新后回放）；
 * - 无 modelConfigId 的 error：视为全局错误，广播到所有模型卡片；
 * 带 modelConfigId 的 answer_delta/answer_reset/final/error/model_message 则各自作用于单张卡片。
 */
function handleMultiStreamEvent(event: AgentStreamEvent, streamState: StreamRenderState): void {
  const multi = streamState.multi;
  if (!multi) return;
  if (event.turnId) multi.turnId = event.turnId;
  if (event.type === "multi_model_start") {
    return;
  }
  if (event.type === "model_response_start") {
    const state = ensureModelStreamState(streamState, event.modelConfigId || "", event.responseId);
    state.statusEl.textContent = event.primary ? "主模型 · 回答中" : "回答中";
    return;
  }
  if (event.type === "canonical_selected") {
    multi.canonicalResponseId = event.responseId;
    updateMultiCanonicalStyles(streamState, true);
    if (event.message) {
      streamState.wrapper.dataset.messageId = event.message.id || "";
      streamState.wrapper.dataset.turnId = event.turnId || "";
      updateLatestAssistantMessage(event.message);
    }
    return;
  }
  if (event.type === "done") {
    for (const state of multi.models.values()) {
      state.loadingEl.remove();
      state.card.classList.remove("streaming");
    }
    streamState.wrapper.classList.remove("streaming");
    if (event.message) {
      streamState.wrapper.dataset.messageId = event.message.id || "";
      streamState.wrapper.dataset.turnId = event.turnId || "";
      updateLatestAssistantMessage(event.message);
    }
    return;
  }
  if (event.type === "error" && !event.modelConfigId) {
    for (const state of multi.models.values()) {
      finishModelStreamWithAnswer(state, "请求失败：" + (event.content || "unknown error"));
      state.statusEl.textContent = "失败";
      state.card.classList.add("failed");
    }
    streamState.wrapper.classList.remove("streaming");
    return;
  }
  if (!event.modelConfigId) return;
  const modelState = ensureModelStreamState(streamState, event.modelConfigId, event.responseId);
  if (event.responseId) modelState.responseId = event.responseId;
  if (event.type === "answer_delta") {
    appendModelAnswerDelta(modelState, event.content || "");
    return;
  }
  if (event.type === "answer_reset") {
    resetModelLiveAnswer(modelState);
    return;
  }
  if (event.type === "final") {
    finishModelStreamWithAnswer(modelState, event.message?.content || event.content || "");
    return;
  }
  if (event.type === "error") {
    finishModelStreamWithAnswer(modelState, "请求失败：" + (event.content || "unknown error"));
    modelState.statusEl.textContent = "失败";
    modelState.card.classList.add("failed");
    return;
  }
  if (event.type === "model_message") {
    appendModelTraceEvent(modelState, event);
  }
}

/**
 * 按 modelConfigId 惰性获取/创建某模型的卡片流式状态。
 * 首个事件到达时才建卡并挂到网格；后续事件复用同一状态，并在拿到 responseId 后补写到 dataset。
 */
function ensureModelStreamState(streamState: StreamRenderState, modelConfigId: string, responseId?: string): ModelStreamState {
  const multi = streamState.multi;
  if (!multi) throw new Error("missing multi stream state");
  let state = multi.models.get(modelConfigId);
  if (state) {
    if (responseId) {
      state.responseId = responseId;
      state.card.dataset.responseId = responseId;
    }
    return state;
  }
  state = createModelStreamCard(modelConfigId, responseId);
  multi.models.set(modelConfigId, state);
  multi.gridEl.appendChild(state.card);
  return state;
}

/** 重绘左侧会话列表：整块清空后逐条重建（含标题、更新时间、切换点击与删除按钮）。 */
function renderConversations(): void {
  conversationListEl.innerHTML = "";
  for (const conversation of conversations) {
    const row = document.createElement("div");
    row.className = `conversationItem${conversation.id === activeConversationId && currentView === "chat" ? " active" : ""}`;
    const button = document.createElement("button");
    button.type = "button";
    button.className = "conversationButton";
    const title = document.createElement("span");
    title.className = "conversationTitle";
    title.textContent = conversation.title || "Untitled";
    const meta = document.createElement("span");
    meta.className = "conversationMeta";
    meta.textContent = new Date(conversation.updatedAt).toLocaleString();
    button.append(title, meta);
    button.addEventListener("click", async () => {
      setView("chat");
      activeConversationId = conversation.id;
      renderConversations();
      await loadMessages();
    });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "conversationDeleteButton";
    remove.textContent = "×";
    remove.setAttribute("aria-label", "删除会话");
    remove.addEventListener("click", async (event) => {
      event.stopPropagation();
      if (!(await confirmDelete("删除会话", `确认删除会话「${conversation.title || "Untitled"}」？此操作不可撤销。`))) return;
      try {
        await deleteConversation(conversation.id);
      } catch (error) {
        showPageError(error);
      }
    });
    row.append(button, remove);
    conversationListEl.appendChild(row);
  }
}

/**
 * 视图切换（非路由）：通过 toggle 各区块的 hidden class 在同一页内切换
 * 聊天 / 技能库 / 知识库 / 模型配置四个视图，并同步导航高亮、标题栏文案与对应区块的重绘。
 * 聊天区（messages/composer）与工作区页面互斥显示。
 */
function setView(view: "chat" | "skills" | "knowledge" | "models"): void {
  currentView = view;
  const isSkills = view === "skills";
  const isKnowledge = view === "knowledge";
  const isModels = view === "models";
  const isWorkspace = isSkills || isKnowledge || isModels;
  skillsNavEl.classList.toggle("active", isSkills);
  knowledgeNavEl.classList.toggle("active", isKnowledge);
  modelNavEl.classList.toggle("active", isModels);
  messagesEl.classList.toggle("hidden", isWorkspace);
  composerEl.classList.toggle("hidden", isWorkspace);
  skillPageEl.classList.toggle("hidden", !isSkills);
  knowledgePageEl.classList.toggle("hidden", !isKnowledge);
  modelPageEl.classList.toggle("hidden", !isModels);
  chatPaneEl.classList.toggle("skillView", isWorkspace);
  if (isSkills) {
    setEmptyChat(false);
    conversationTitleEl.textContent = "技能库";
    renderMessageLocator();
    renderSkills();
    return;
  }
  if (isKnowledge) {
    setEmptyChat(false);
    conversationTitleEl.textContent = "知识库";
    renderMessageLocator();
    renderKnowledge();
    return;
  }
  if (isModels) {
    setEmptyChat(false);
    conversationTitleEl.textContent = "模型配置";
    renderMessageLocator();
    renderModelConfigs();
    return;
  }
  const conversation = conversations.find((item) => item.id === activeConversationId);
  conversationTitleEl.textContent = conversation?.title || "新会话";
  renderConversations();
  renderMessageLocator();
}

/**
 * 渲染技能库列表页。先处理加载错误 / 空态 / 搜索无结果三种边界，
 * 再按搜索关键字过滤，逐个技能渲染成卡片：头像、名称、启用状态徽标、说明、触发词 pill，
 * 以及行内操作（启用/禁用开关、删除）。点击卡片主体打开详情弹窗；开关与删除按钮 stopPropagation
 * 避免冒泡到卡片点击。流式进行中所有操作置灰。
 */
function renderSkills(): void {
  skillListEl.innerHTML = "";
  skillSearchEl.disabled = isStreaming;
  if (skillLoadError) {
    const wrapper = document.createElement("div");
    wrapper.className = "skillError";
    const text = document.createElement("div");
    text.textContent = "Skill 加载失败：" + skillLoadError;
    const retry = document.createElement("button");
    retry.type = "button";
    retry.textContent = "Retry";
    retry.disabled = isStreaming;
    retry.addEventListener("click", async () => {
      try {
        await loadSkills();
      } catch (error) {
        showSkillError(error);
      }
    });
    wrapper.append(text, retry);
    skillListEl.appendChild(wrapper);
    return;
  }
  if (availableSkills.length === 0) {
    skillListEl.innerHTML = `<div class="emptyState compact">暂无技能。</div>`;
    return;
  }
  const query = skillSearchEl.value.trim().toLowerCase();
  const visibleSkills = availableSkills.filter((rawSkill) => skillMatchesQuery(normalizeSkill(rawSkill), query));
  if (visibleSkills.length === 0) {
    skillListEl.innerHTML = `<div class="emptyState compact">没有匹配的 Skill。</div>`;
    return;
  }
  for (const rawSkill of visibleSkills) {
    const skill = normalizeSkill(rawSkill);
    const enabled = enabledSkillNames.has(skill.name);
    const card = document.createElement("section");
    card.className = "skillListItem";
    card.addEventListener("click", async () => {
      if (isStreaming) return;
      try {
        await openSkillDetailDialog(skill.name);
      } catch (error) {
        showSkillError(error);
      }
    });

    const summary = document.createElement("div");
    summary.className = "skillRowSummary";

    const avatar = document.createElement("div");
    avatar.className = "skillAvatar";
    avatar.textContent = skill.name.slice(0, 1).toUpperCase();

    const info = document.createElement("div");
    info.className = "skillInfo";
    const titleRow = document.createElement("div");
    titleRow.className = "skillTitleRow";
    const name = document.createElement("span");
    name.className = "skillName";
    name.textContent = "/" + skill.name;
    const meta = document.createElement("span");
    meta.className = `skillStatus ${enabled ? "enabled" : "disabled"}`;
    meta.textContent = enabled ? "Enabled" : "Disabled";
    titleRow.append(name, meta);
    const description = document.createElement("span");
    description.className = "skillDescription";
    description.textContent = skill.purpose || skill.description || "暂无说明";
    info.append(titleRow, description, renderSkillTriggerPills(skill.triggers));

    const actions = document.createElement("div");
    actions.className = "skillRowActions";
    actions.addEventListener("click", (event) => {
      event.stopPropagation();
    });

    const switchLabel = document.createElement("label");
    switchLabel.className = "skillSwitch";
    switchLabel.setAttribute("aria-label", enabled ? `禁用 /${skill.name}` : `启用 /${skill.name}`);
    switchLabel.addEventListener("click", (event) => {
      event.stopPropagation();
    });
    const switchInput = document.createElement("input");
    switchInput.type = "checkbox";
    switchInput.checked = enabled;
    switchInput.disabled = isStreaming;
    switchInput.addEventListener("change", async (event) => {
      event.stopPropagation();
      switchInput.disabled = true;
      await setSkillEnabled(skill.name, !enabled);
    });
    const switchTrack = document.createElement("span");
    switchTrack.className = "skillSwitchTrack";
    switchLabel.append(switchInput, switchTrack);

    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "skillDeleteButton";
    remove.textContent = "×";
    remove.setAttribute("aria-label", `删除 /${skill.name}`);
    remove.disabled = isStreaming;
    remove.addEventListener("click", async (event) => {
      event.stopPropagation();
      if (!(await confirmDelete("删除 Skill", `确认删除 /${skill.name}？此操作不可撤销。`))) return;
      await deleteSkill(skill.name);
    });

    actions.append(switchLabel, remove);
    summary.append(avatar, info, actions);
    card.append(summary);
    skillListEl.appendChild(card);
  }
}

async function openSkillDetailDialog(skillName: string): Promise<void> {
  const skill = normalizeSkill(await ensureSkillDetail(skillName));
  openAppModal({
    title: `/${skill.name}`,
    description: skill.purpose || skill.description || "Skill detail",
    body: renderSkillDetail(skill),
    size: "wide",
    actions: [
      { label: "关闭", variant: "secondary", onClick: () => closeAppModal(false) }
    ]
  });
}

/**
 * 渲染知识库管理页：左侧知识库列表 + 右侧当前知识库的文档明细。
 * 处理加载错误、无知识库、导入中提示、未选中知识库、空文档等多种状态。
 * 每个知识库/文档卡片展示文档数与 chunk 统计（child 检索块 / parent 上下文块），并挂删除操作菜单。
 * default 知识库不允许删除。同时刷新输入框的 RAG 知识库下拉。
 */
function renderKnowledge(): void {
  renderRagKnowledgeSelector();
  const activeBase = knowledgeBases.find((base) => base.id === activeKnowledgeBaseId) || null;
  createKnowledgeBaseEl.disabled = isStreaming || isKnowledgeImporting;
  addKnowledgeEl.disabled = isStreaming || isKnowledgeImporting || !activeBase;
  addKnowledgeEl.textContent = isKnowledgeImporting ? "导入中" : "添加";
  knowledgeBaseListEl.innerHTML = "";
  knowledgeListEl.innerHTML = "";
  if (knowledgeLoadError) {
    const wrapper = document.createElement("div");
    wrapper.className = "knowledgeError";
    const text = document.createElement("div");
    text.textContent = "知识库操作失败：" + knowledgeLoadError;
    const retry = document.createElement("button");
    retry.type = "button";
    retry.textContent = "重试";
    retry.disabled = isStreaming || isKnowledgeImporting;
    retry.addEventListener("click", async () => {
      try {
        await loadKnowledge();
      } catch (error) {
        showKnowledgeError(error);
      }
    });
    wrapper.append(text, retry);
    knowledgeBaseListEl.appendChild(wrapper);
    return;
  }
  if (knowledgeBases.length === 0) {
    const empty = document.createElement("div");
    empty.className = "emptyState compact knowledgeEmpty";
    empty.textContent = "还没有知识库。";
    knowledgeBaseListEl.appendChild(empty);
    const detailEmpty = document.createElement("div");
    detailEmpty.className = "emptyState compact knowledgeEmpty";
    detailEmpty.textContent = "请先创建知识库。";
    knowledgeListEl.appendChild(detailEmpty);
    return;
  }
  for (const base of knowledgeBases) {
    const row = document.createElement("article");
    row.className = `knowledgeBaseItem${base.id === activeKnowledgeBaseId ? " active" : ""}`;
    row.addEventListener("click", async () => {
      if (isStreaming || isKnowledgeImporting || base.id === activeKnowledgeBaseId) return;
      try {
        await selectKnowledgeBase(base.id);
      } catch (error) {
        showKnowledgeError(error);
      }
    });
    const info = document.createElement("div");
    info.className = "knowledgeBaseInfo";
    const name = document.createElement("div");
    name.className = "knowledgeBaseName";
    name.textContent = base.name || "Untitled";
    const meta = document.createElement("div");
    meta.className = "knowledgeBaseMeta";
    meta.textContent = [`${base.documentCount || 0} docs`, chunkSummary(base)].join(" · ");
    info.append(name, meta);
    row.appendChild(info);
    if (base.id !== "default") {
      row.appendChild(createActionMenu("知识库操作", [
        {
          label: "删除",
          danger: true,
          disabled: isStreaming || isKnowledgeImporting,
          onClick: async () => {
            if (!(await confirmDelete("删除知识库", `确认删除知识库「${base.name || "Untitled"}」？其中的文件和索引也会被删除。`))) return;
            try {
              await deleteKnowledgeBase(base.id);
            } catch (error) {
              showKnowledgeError(error);
            }
          }
        }
      ]));
    }
    knowledgeBaseListEl.appendChild(row);
  }
  if (isKnowledgeImporting) {
    const status = document.createElement("div");
    status.className = "knowledgeImportStatus";
    status.textContent = `正在导入到 ${activeBase?.name || "知识库"}...`;
    knowledgeListEl.appendChild(status);
  }
  if (!activeBase) {
    const empty = document.createElement("div");
    empty.className = "emptyState compact knowledgeEmpty";
    empty.textContent = "请选择知识库。";
    knowledgeListEl.appendChild(empty);
    return;
  }
  const header = document.createElement("div");
  header.className = "knowledgeDocumentsHeader";
  const title = document.createElement("strong");
  title.textContent = activeBase.name;
  const meta = document.createElement("span");
  meta.textContent = `${activeBase.documentCount || 0} docs · ${chunkSummary(activeBase)}`;
  header.append(title, meta);
  knowledgeListEl.appendChild(header);
  if (knowledgeItems.length === 0) {
    const empty = document.createElement("div");
    empty.className = "emptyState compact knowledgeEmpty";
    empty.textContent = "这个知识库还没有导入文件。";
    knowledgeListEl.appendChild(empty);
    return;
  }
  for (const item of knowledgeItems) {
    const card = document.createElement("article");
    card.className = "knowledgeItem";

    const icon = document.createElement("div");
    icon.className = "knowledgeIcon";
    icon.textContent = fileExtension(item.name);

    const info = document.createElement("div");
    info.className = "knowledgeInfo";
    const name = document.createElement("div");
    name.className = "knowledgeName";
    name.textContent = item.name || "Untitled";
    const meta = document.createElement("div");
    meta.className = "knowledgeMeta";
    meta.textContent = [
      formatFileSize(item.size),
      item.contentType || "unknown",
      chunkSummary(item),
      item.status || "",
      formatDateTime(item.importedAt)
    ].filter(Boolean).join(" · ");
    info.append(name, meta);

    const menu = createActionMenu("知识文件操作", [
      {
        label: "删除",
        danger: true,
        disabled: isStreaming || isKnowledgeImporting,
        onClick: async () => {
          if (!(await confirmDelete("删除知识文件", `确认删除知识文件「${item.name || "Untitled"}」？对应 chunk 和向量索引也会被删除。`))) return;
          try {
            await deleteKnowledgeItem(item.id);
          } catch (error) {
            showKnowledgeError(error);
          }
        }
      }
    ]);

    card.append(icon, info, menu);
    knowledgeListEl.appendChild(card);
  }
}

/** 重建输入框旁隐藏的原生 RAG 知识库下拉（作为自定义弹层的可访问性兜底），并联动刷新设置弹层与选择摘要。 */
function renderRagKnowledgeSelector(): void {
  normalizeRagKnowledgeSelection();
  ragKnowledgeSelectorEl.innerHTML = "";
  ragKnowledgeSelectorEl.disabled = isStreaming;

  const empty = document.createElement("option");
  empty.value = "";
  empty.textContent = "不使用知识库";
  ragKnowledgeSelectorEl.appendChild(empty);

  const group = document.createElement("optgroup");
  group.label = "知识库";
  for (const base of knowledgeBases) {
    const option = document.createElement("option");
    option.value = base.id;
    const hasContent = (base.childChunkCount ?? 0) > 0;
    const meta = hasContent ? `${base.documentCount || 0} docs` : "空";
    option.textContent = `${base.name || "Untitled"} · ${meta}`;
    group.appendChild(option);
  }
  if (group.children.length > 0) {
    ragKnowledgeSelectorEl.appendChild(group);
  }

  ragKnowledgeSelectorEl.value = activeRagKnowledgeBaseId;
  renderComposerSettingsPopover();
  renderComposerSelectionSummary();
}

function saveRagKnowledgeSelection(): void {
  if (activeRagKnowledgeBaseId) {
    window.localStorage.setItem("activeRagKnowledgeBaseId", activeRagKnowledgeBaseId);
  } else {
    window.localStorage.removeItem("activeRagKnowledgeBaseId");
  }
}

/** 在输入框上方渲染一行选择摘要（如"知识库：X · 多模型：3 个模型"），无选择时隐藏。 */
function renderComposerSelectionSummary(): void {
  const items: string[] = [];
  const knowledge = knowledgeBases.find((base) => base.id === activeRagKnowledgeBaseId);
  if (knowledge) {
    items.push(`知识库：${knowledge.name || "Untitled"}`);
  }
  if (secondaryModelConfigIds.length > 0) {
    items.push(`多模型：${secondaryModelConfigIds.length + 1} 个模型`);
  }
  composerSelectionSummaryEl.textContent = items.join(" · ");
  composerSelectionSummaryEl.classList.toggle("hidden", items.length === 0);
}

/**
 * 渲染主模型选择器：输入框旁的自定义下拉按钮 + 弹层。只列出 reasoning 类型模型，
 * 当前主模型打勾。选中新主模型时把它从副模型列表剔除并持久化。无推理模型时禁用并给出引导文案。
 */
function renderPrimaryModelPicker(reasoningConfigs = modelConfigs.filter((config) => modelConfigType(config) === "reasoning")): void {
  primaryModelToggleEl.disabled = isStreaming || reasoningConfigs.length === 0;
  primaryModelToggleEl.setAttribute("aria-expanded", String(primaryModelPopoverOpen));
  const activeLabel = activeModelConfigId && reasoningConfigs.length > 0 ? modelDisplayName(activeModelConfigId) : "无推理模型";
  primaryModelToggleEl.innerHTML = `<span class="pickerLabel">模型</span><span class="pickerValue">${escapeHTML(activeLabel)}</span><span class="pickerChevron">⌄</span>`;
  primaryModelPopoverEl.innerHTML = "";
  primaryModelPopoverEl.classList.toggle("hidden", !primaryModelPopoverOpen);
  if (!primaryModelPopoverOpen) return;

  const title = document.createElement("div");
  title.className = "pickerMenuTitle";
  title.textContent = "模型选择";
  primaryModelPopoverEl.appendChild(title);

  if (reasoningConfigs.length === 0) {
    const empty = document.createElement("div");
    empty.className = "pickerMenuEmpty";
    empty.textContent = "请先在模型配置中新增推理模型。";
    primaryModelPopoverEl.appendChild(empty);
    return;
  }

  for (const config of reasoningConfigs) {
    const selected = config.id === activeModelConfigId;
    const button = document.createElement("button");
    button.type = "button";
    button.className = `pickerMenuItem${selected ? " checked" : ""}`;
    button.innerHTML = `<span>${escapeHTML(modelConfigLabel(config))}</span><span class="pickerCheck">${selected ? "✓" : ""}</span>`;
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      activeModelConfigId = config.id;
      secondaryModelConfigIds = secondaryModelConfigIds.filter((id) => id !== config.id).slice(0, 2);
      saveModelSelection();
      primaryModelPopoverOpen = false;
      renderModelSelector();
    });
    primaryModelPopoverEl.appendChild(button);
  }
}

/**
 * 渲染"对话设置"级联弹层：左栏两个入口（使用知识库 / 使用多模型回答），右栏展示当前入口对应的
 * 详细选项列表。composerSettingsPanel 记录当前停留的子面板，root 会被规整为 knowledge。
 */
function renderComposerSettingsPopover(): void {
  composerSettingsButtonEl.disabled = isStreaming;
  composerSettingsButtonEl.classList.toggle("active", composerSettingsPopoverOpen);
  composerSettingsButtonEl.setAttribute("aria-expanded", String(composerSettingsPopoverOpen));
  composerSettingsPopoverEl.innerHTML = "";
  composerSettingsPopoverEl.classList.toggle("hidden", !composerSettingsPopoverOpen);
  if (!composerSettingsPopoverOpen) return;
  if (composerSettingsPanel === "root") {
    composerSettingsPanel = "knowledge";
  }

  const title = document.createElement("div");
  title.className = "settingsMenuTitle";
  title.textContent = "对话设置";
  composerSettingsPopoverEl.appendChild(title);

  const cascade = document.createElement("div");
  cascade.className = "settingsCascade";
  const root = document.createElement("div");
  root.className = "settingsCascadeRoot";
  root.append(
    settingsRootItem("knowledge", "使用知识库", knowledgeSettingSummary(), Boolean(activeRagKnowledgeBaseId)),
    settingsRootItem("multi", "使用多模型回答", multiModelSettingSummary(), secondaryModelConfigIds.length > 0)
  );
  const detail = document.createElement("div");
  detail.className = "settingsCascadeDetail";
  const detailTitle = document.createElement("div");
  detailTitle.className = "settingsSubHeader";
  detailTitle.textContent = composerSettingsPanel === "knowledge" ? "使用知识库" : "使用多模型回答";
  detail.appendChild(detailTitle);

  if (composerSettingsPanel === "knowledge") {
    renderKnowledgeSettingsList(detail);
  } else {
    renderMultiModelSettingsList(detail);
  }
  cascade.append(root, detail);
  composerSettingsPopoverEl.appendChild(cascade);
}

function settingsRootItem(panel: ComposerSettingsPanel, label: string, summary: string, checked: boolean): HTMLElement {
  const button = document.createElement("button");
  button.type = "button";
  const active = panel === composerSettingsPanel;
  button.className = `settingsRootItem${checked ? " checked" : ""}${active ? " active" : ""}`;
  button.innerHTML = `<span class="settingsRootMain"><span>${escapeHTML(label)}</span><small>${escapeHTML(summary)}</small></span><span class="settingsRootMark">${checked ? "✓" : "›"}</span>`;
  button.addEventListener("click", (event) => {
    event.stopPropagation();
    composerSettingsPanel = panel;
    renderComposerSettingsPopover();
  });
  return button;
}

function renderKnowledgeSettingsList(container: HTMLElement): void {
  const none = document.createElement("button");
  none.type = "button";
  none.className = `settingsOption${activeRagKnowledgeBaseId ? "" : " checked"}`;
  none.innerHTML = `<span>不使用知识库</span><span class="settingsCheck">${activeRagKnowledgeBaseId ? "" : "✓"}</span>`;
  none.addEventListener("click", (event) => {
    event.stopPropagation();
    activeRagKnowledgeBaseId = "";
    ragKnowledgeSelectorEl.value = "";
    saveRagKnowledgeSelection();
    renderComposerSettingsPopover();
    renderComposerSelectionSummary();
  });
  container.appendChild(none);

  if (knowledgeBases.length === 0) {
    const empty = document.createElement("div");
    empty.className = "settingsEmpty";
    empty.textContent = "暂无知识库。";
    container.appendChild(empty);
    return;
  }

  for (const base of knowledgeBases) {
    const selected = base.id === activeRagKnowledgeBaseId;
    const hasContent = (base.childChunkCount ?? 0) > 0;
    const button = document.createElement("button");
    button.type = "button";
    button.className = `settingsOption${selected ? " checked" : ""}`;
    button.innerHTML = `<span class="settingsOptionMain"><span>${escapeHTML(base.name || "Untitled")}</span><small>${hasContent ? `${base.documentCount || 0} docs` : "空"}</small></span><span class="settingsCheck">${selected ? "✓" : ""}</span>`;
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      activeRagKnowledgeBaseId = base.id;
      ragKnowledgeSelectorEl.value = base.id;
      saveRagKnowledgeSelection();
      renderComposerSettingsPopover();
      renderComposerSelectionSummary();
    });
    container.appendChild(button);
  }
}

/**
 * 渲染多模型副模型选择列表：主模型置顶且禁用（固定选中），其余推理模型作为副模型候选。
 * 副模型最多 2 个，选满后其余候选置灰。切换后立即持久化并保持在 multi 面板。
 */
function renderMultiModelSettingsList(container: HTMLElement): void {
  const reasoningConfigs = modelConfigs.filter((config) => modelConfigType(config) === "reasoning");
  const active = reasoningConfigs.find((config) => config.id === activeModelConfigId);
  if (active) {
    const primary = document.createElement("div");
    primary.className = "settingsOption disabled checked";
    primary.innerHTML = `<span class="settingsOptionMain"><span>${escapeHTML(modelConfigLabel(active))}</span><small>主模型</small></span><span class="settingsCheck">✓</span>`;
    container.appendChild(primary);
  }

  const candidates = reasoningConfigs.filter((config) => config.id !== activeModelConfigId);
  if (candidates.length === 0) {
    const empty = document.createElement("div");
    empty.className = "settingsEmpty";
    empty.textContent = "暂无可选副模型。";
    container.appendChild(empty);
    return;
  }

  for (const config of candidates) {
    const checked = secondaryModelConfigIds.includes(config.id);
    const disabled = !checked && secondaryModelConfigIds.length >= 2;
    const button = document.createElement("button");
    button.type = "button";
    button.className = `settingsOption${checked ? " checked" : ""}`;
    button.disabled = disabled || isStreaming;
    button.innerHTML = `<span class="settingsOptionMain"><span>${escapeHTML(modelConfigLabel(config))}</span><small>${disabled ? "最多选择 2 个副模型" : "副模型"}</small></span><span class="settingsCheck">${checked ? "✓" : ""}</span>`;
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      if (button.disabled) return;
      if (checked) {
        secondaryModelConfigIds = secondaryModelConfigIds.filter((id) => id !== config.id);
      } else {
        secondaryModelConfigIds = [...secondaryModelConfigIds, config.id].filter((id, index, ids) => ids.indexOf(id) === index).slice(0, 2);
      }
      saveModelSelection();
      renderModelSelector();
      composerSettingsPanel = "multi";
      composerSettingsPopoverOpen = true;
      renderComposerSettingsPopover();
    });
    container.appendChild(button);
  }
}

function knowledgeSettingSummary(): string {
  const base = knowledgeBases.find((item) => item.id === activeRagKnowledgeBaseId);
  return base ? base.name || "Untitled" : "未启用";
}

function multiModelSettingSummary(): string {
  return secondaryModelConfigIds.length > 0 ? `${secondaryModelConfigIds.length + 1} 个模型` : "未启用";
}

/** 通用"⋯"操作菜单：触发按钮 + 悬浮菜单，菜单项支持 danger 样式与禁用；全部项禁用时触发按钮也禁用。 */
function createActionMenu(label: string, items: ActionMenuItem[]): HTMLElement {
  const wrapper = document.createElement("div");
  wrapper.className = "actionMenu";
  wrapper.addEventListener("click", (event) => {
    event.stopPropagation();
  });

  const trigger = document.createElement("button");
  trigger.type = "button";
  trigger.className = "actionMenuButton";
  trigger.textContent = "⋯";
  trigger.setAttribute("aria-label", label);
  trigger.disabled = items.every((item) => item.disabled);

  const menu = document.createElement("div");
  menu.className = "actionMenuPopover";
  for (const item of items) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `actionMenuItem${item.danger ? " danger" : ""}`;
    button.textContent = item.label;
    button.disabled = Boolean(item.disabled);
    button.addEventListener("click", async (event) => {
      event.stopPropagation();
      if (button.disabled) return;
      await item.onClick();
      trigger.blur();
    });
    menu.appendChild(button);
  }
  wrapper.append(trigger, menu);
  return wrapper;
}

/**
 * 重建隐藏的原生主模型 select（仅列 reasoning 模型），并联动刷新自定义选择器、对话设置弹层与选择摘要。
 * 无推理模型时退化为占位项、把主模型重置为 "default"、清空副模型并禁用。
 */
function renderModelSelector(): void {
  primaryModelSelectorEl.innerHTML = "";
  primaryModelSelectorEl.disabled = isStreaming;
  const reasoningConfigs = modelConfigs.filter((config) => modelConfigType(config) === "reasoning");
  if (reasoningConfigs.length === 0) {
    const option = document.createElement("option");
    option.value = "default";
    option.textContent = "无推理模型";
    primaryModelSelectorEl.appendChild(option);
    activeModelConfigId = "default";
    secondaryModelConfigIds = [];
    primaryModelSelectorEl.disabled = true;
    renderPrimaryModelPicker([]);
    renderComposerSettingsPopover();
    renderComposerSelectionSummary();
    return;
  }
  normalizeModelSelection();
  const group = document.createElement("optgroup");
  group.label = "推理模型";
  for (const config of reasoningConfigs) {
    const primaryOption = document.createElement("option");
    primaryOption.value = config.id;
    primaryOption.textContent = modelConfigLabel(config);
    group.appendChild(primaryOption);
  }
  primaryModelSelectorEl.appendChild(group);
  primaryModelSelectorEl.value = activeModelConfigId;
  renderPrimaryModelPicker(reasoningConfigs);
  renderComposerSettingsPopover();
  renderComposerSelectionSummary();
}

function normalizeRagKnowledgeSelection(): void {
  if (!activeRagKnowledgeBaseId) return;
  if (knowledgeBases.some((base) => base.id === activeRagKnowledgeBaseId)) return;
  activeRagKnowledgeBaseId = "";
  window.localStorage.removeItem("activeRagKnowledgeBaseId");
}

/** 计算实际下发给后端的 RAG 知识库 ID：选中的知识库必须存在且有可检索的 child chunk，否则返回空串（不启用 RAG）。 */
function selectedRagKnowledgeBaseId(): string {
  const base = knowledgeBases.find((item) => item.id === activeRagKnowledgeBaseId);
  if (!base || (base.childChunkCount ?? 0) <= 0) return "";
  return base.id;
}

/** 从 localStorage 读取副模型列表，兼容旧 key（selectedModelConfigIds），去空去重并截断到 2 个。 */
function loadSecondaryModelConfigIds(): string[] {
  const raw = window.localStorage.getItem("secondaryModelConfigIds") || window.localStorage.getItem("selectedModelConfigIds");
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (Array.isArray(parsed)) {
      return parsed.map((value) => String(value).trim()).filter(Boolean).slice(0, 2);
    }
  } catch {
    return [];
  }
  return [];
}

/**
 * 校正主/副模型选择的一致性：主模型必须是存在的 reasoning 模型，否则回退到默认；
 * 副模型必须是 reasoning、不等于主模型、且不超过 2 个。校正后立即持久化。
 */
function normalizeModelSelection(): void {
  const reasoningIDs = new Set(modelConfigs.filter((config) => modelConfigType(config) === "reasoning").map((config) => config.id));
  if (!reasoningIDs.has(activeModelConfigId)) {
    activeModelConfigId = defaultReasoningModelConfigId();
  }
  secondaryModelConfigIds = secondaryModelConfigIds.filter((id) => reasoningIDs.has(id) && id !== activeModelConfigId).slice(0, 2);
  saveModelSelection();
}

function saveModelSelection(): void {
  window.localStorage.setItem("activeModelConfigId", activeModelConfigId);
  window.localStorage.setItem("secondaryModelConfigIds", JSON.stringify(secondaryModelConfigIds.slice(0, 2)));
  window.localStorage.removeItem("selectedModelConfigIds");
}

/** 本轮实际使用的推理模型 ID 列表：主模型在前 + 副模型，最多 3 个（决定单/多模型渲染路径与请求体）。 */
function selectedReasoningModelConfigIds(): string[] {
  normalizeModelSelection();
  return [activeModelConfigId, ...secondaryModelConfigIds].slice(0, 3);
}

/**
 * 渲染模型配置管理页：逐条列出模型（含类型徽标 推理/向量、Key 是否已设置、provider/baseURL 摘要），
 * 每张卡片带"设为当前 / 编辑 / 删除"操作；default 配置不可删。
 * editingModelConfigId === "__new__" 时在顶部内联渲染新增编辑器。点击卡片主体打开只读详情弹窗。
 */
function renderModelConfigs(): void {
  addModelConfigEl.disabled = isStreaming;
  modelConfigListEl.innerHTML = "";
  if (modelLoadError) {
    const wrapper = document.createElement("div");
    wrapper.className = "modelError";
    const text = document.createElement("div");
    text.textContent = "模型配置加载失败：" + modelLoadError;
    const retry = document.createElement("button");
    retry.type = "button";
    retry.textContent = "重试";
    retry.disabled = isStreaming;
    retry.addEventListener("click", async () => {
      try {
        await loadModelConfigs();
      } catch (error) {
        showModelError(error);
      }
    });
    wrapper.append(text, retry);
    modelConfigListEl.appendChild(wrapper);
    return;
  }

  if (editingModelConfigId === "__new__") {
    modelConfigListEl.appendChild(renderModelConfigEditor(null));
  }

  if (modelConfigs.length === 0 && editingModelConfigId !== "__new__") {
    const empty = document.createElement("div");
    empty.className = "emptyState compact modelEmpty";
    empty.textContent = "还没有模型配置。";
    modelConfigListEl.appendChild(empty);
    return;
  }

  for (const config of modelConfigs) {
    const type = modelConfigType(config);
    const card = document.createElement("article");
    card.className = `modelConfigItem${type === "reasoning" && config.id === activeModelConfigId ? " active" : ""}`;
    card.addEventListener("click", () => {
      if (isStreaming) return;
      openModelDetailDialog(config);
    });

    const info = document.createElement("div");
    info.className = "modelConfigInfo";
    const title = document.createElement("div");
    title.className = "modelConfigTitle";
    const name = document.createElement("span");
    name.textContent = modelConfigLabel(config);
    const status = document.createElement("span");
    status.className = `modelConfigStatus ${config.apiKeySet ? "ready" : "missing"}`;
    status.textContent = type === "embedding" ? "向量模型" : "推理模型";
    const keyStatus = document.createElement("span");
    keyStatus.className = `modelConfigStatus ${config.apiKeySet ? "ready" : "missing"}`;
    keyStatus.textContent = config.apiKeySet ? "Key set" : "No key";
    title.append(name, status, keyStatus);
    const meta = document.createElement("div");
    meta.className = "modelConfigMeta";
    meta.textContent = [type === "embedding" ? "Embedding" : "Chat", config.provider || "openai-compatible", config.baseURL || "No base URL", config.apiKeyPreview ? `Key ${config.apiKeyPreview}` : ""].filter(Boolean).join(" · ");
    info.append(title, meta);

    const actions = document.createElement("div");
    actions.className = "modelConfigActions";
    actions.addEventListener("click", (event) => {
      event.stopPropagation();
    });
    const select = document.createElement("button");
    select.type = "button";
    select.textContent = type === "embedding" ? "用于向量" : config.id === activeModelConfigId ? "当前模型" : "设为当前";
    select.disabled = isStreaming || type === "embedding" || config.id === activeModelConfigId;
    select.addEventListener("click", () => {
      if (type !== "reasoning") return;
      activeModelConfigId = config.id;
      secondaryModelConfigIds = secondaryModelConfigIds.filter((id) => id !== config.id).slice(0, 2);
      saveModelSelection();
      renderModelSelector();
      renderModelConfigs();
    });
    const edit = document.createElement("button");
    edit.type = "button";
    edit.textContent = "编辑";
    edit.disabled = isStreaming;
    edit.addEventListener("click", () => {
      openModelConfigDialog(config);
    });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.textContent = "删除";
    remove.disabled = isStreaming || config.id === "default";
    remove.addEventListener("click", async () => {
      if (!(await confirmDelete("删除模型配置", `确认删除模型配置「${config.id}」？此操作不可撤销。`))) return;
      try {
        await deleteModelConfig(config.id);
      } catch (error) {
        showModelError(error);
      }
    });
    actions.append(select, edit, remove);
    card.append(info, actions);
    modelConfigListEl.appendChild(card);
  }
}

function renderModelConfigDetail(config: ModelConfig): HTMLElement {
  const detail = document.createElement("div");
  detail.className = "modelConfigDetail";
  detail.append(
    modelConfigDetailItem("ID", config.id),
    modelConfigDetailItem("Type", modelConfigType(config) === "embedding" ? "向量模型" : "推理模型"),
    modelConfigDetailItem("Provider", config.provider || "openai-compatible"),
    modelConfigDetailItem("Base URL", config.baseURL || "No base URL"),
    modelConfigDetailItem("Model", config.model || "No model"),
    modelConfigDetailItem("API Key", config.apiKeySet ? config.apiKeyPreview || "Set" : "Not set"),
    modelConfigDetailItem("Temperature", String(config.temperature)),
    modelConfigDetailItem("Timeout", `${config.timeoutSeconds}s`),
    modelConfigDetailItem("Updated", config.updatedAt ? formatDateTime(config.updatedAt) : "")
  );
  return detail;
}

function modelConfigDetailItem(labelText: string, value: string): HTMLElement {
  const item = document.createElement("div");
  item.className = "modelConfigDetailItem";
  const label = document.createElement("span");
  label.textContent = labelText;
  const content = document.createElement("strong");
  content.textContent = value || "-";
  item.append(label, content);
  return item;
}

function openModelDetailDialog(config: ModelConfig): void {
  openAppModal({
    title: "模型详情",
    description: modelConfigLabel(config),
    body: renderModelConfigDetail(config),
    size: "wide",
    actions: [
      { label: "关闭", variant: "secondary", onClick: () => closeAppModal(false) }
    ]
  });
}

function openModelConfigDialog(config: ModelConfig | null = null): void {
  const editor = renderModelConfigEditor(config, {
    onCancel: () => closeAppModal(false),
    onSaved: () => closeAppModal(false)
  });
  openAppModal({
    title: config ? "编辑模型" : "添加模型",
    description: config ? `更新 ${config.id} 的连接配置。` : "配置推理模型或向量模型，保存后会写入本地 SQLite。",
    body: editor,
    size: "wide"
  });
}

/**
 * 构建模型配置编辑表单（新增或编辑）。字段：配置 ID（编辑时锁定）、模型类型（default 强制 reasoning 且锁定）、
 * provider、API Key（留空表示保留原 key）、Base URL、Model、Temperature、Timeout。
 * 保存时组装 payload（apiKey 仅在填写时下发，剔除历史 extra.embeddingModel）并调用 saveModelConfig。
 */
function renderModelConfigEditor(config: ModelConfig | null, options: { onCancel?: () => void; onSaved?: () => void } = {}): HTMLElement {
  const isNew = config === null;
  const panel = document.createElement("section");
  panel.className = "modelConfigEditor";
  const id = createLabeledInput("配置 ID", config?.id || nextModelConfigID(), "text");
  id.input.disabled = isStreaming || !isNew;
  const type = createLabeledSelect("模型类型", modelConfigType(config || undefined), [
    { value: "reasoning", label: "推理模型" },
    { value: "embedding", label: "向量模型" }
  ]);
  if (config?.id === "default") {
    type.select.value = "reasoning";
    type.select.disabled = true;
    type.sync();
  }
  const provider = createLabeledInput("Provider", config?.provider || "openai-compatible", "text");
  const apiKey = createLabeledInput("API Key", "", "password");
  apiKey.input.placeholder = config?.apiKeySet ? "留空则保留已保存的 key" : "请输入 API key";
  const baseURL = createLabeledInput("Base URL", config?.baseURL || "https://api.openai.com/v1", "text");
  const modelName = createLabeledInput("Model", config?.model || "", "text");
  const temperature = createLabeledInput("Temperature", String(config?.temperature ?? 0.2), "number");
  temperature.input.step = "0.1";
  temperature.input.min = "0";
  temperature.input.max = "2";
  const timeout = createLabeledInput("Timeout seconds", String(config?.timeoutSeconds ?? 60), "number");
  timeout.input.min = "1";
  timeout.input.step = "1";

  const actions = document.createElement("div");
  actions.className = "modelEditorActions";
  const save = document.createElement("button");
  save.type = "button";
  save.textContent = "保存";
  save.disabled = isStreaming;
  save.addEventListener("click", async () => {
    const configID = id.input.value.trim();
    if (!configID) {
      window.alert("配置 ID 不能为空");
      return;
    }
    const extra = { ...(config?.extra || {}) };
    delete extra["embeddingModel"];
    const payload: Record<string, unknown> = {
      modelType: type.select.value,
      provider: provider.input.value.trim(),
      baseURL: baseURL.input.value.trim(),
      model: modelName.input.value.trim(),
      extra,
      temperature: Number(temperature.input.value || 0.2),
      timeoutSeconds: Number(timeout.input.value || 60)
    };
    if (apiKey.input.value.trim()) {
      payload.apiKey = apiKey.input.value.trim();
    }
    try {
      await saveModelConfig(configID, payload);
      options.onSaved?.();
    } catch (error) {
      showModelError(error);
    }
  });
  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.textContent = "取消";
  cancel.disabled = isStreaming;
  cancel.addEventListener("click", () => {
    if (options.onCancel) {
      options.onCancel();
      return;
    }
    editingModelConfigId = null;
    renderModelConfigs();
  });
  actions.append(save, cancel);
  panel.append(id.wrapper, type.wrapper, provider.wrapper, apiKey.wrapper, baseURL.wrapper, modelName.wrapper, temperature.wrapper, timeout.wrapper, actions);
  return panel;
}

function renderSkillTriggerPills(triggers: string[]): HTMLElement {
  const wrapper = document.createElement("div");
  wrapper.className = "skillTriggers";
  const visible = triggers.slice(0, 5);
  for (const trigger of visible) {
    const pill = document.createElement("span");
    pill.className = "skillTrigger";
    pill.textContent = trigger;
    wrapper.appendChild(pill);
  }
  if (triggers.length > visible.length) {
    const more = document.createElement("span");
    more.className = "skillTrigger muted";
    more.textContent = `+${triggers.length - visible.length}`;
    wrapper.appendChild(more);
  }
  return wrapper;
}

/** 把 Skill 的所有可选字段补齐为安全默认值（空串/空数组/布尔），让渲染与搜索无需到处判空。 */
function normalizeSkill(skill: Skill): Skill {
  return {
    ...skill,
    purpose: skill.purpose || "",
    description: skill.description || "",
    whenToUse: skill.whenToUse || "",
    triggers: skill.triggers || [],
    source: skill.source || "",
    path: skill.path || "",
    disableModelInvocation: Boolean(skill.disableModelInvocation),
    userInvocable: skill.userInvocable !== false,
    allowedTools: skill.allowedTools || [],
    disallowedTools: skill.disallowedTools || [],
    model: skill.model || "",
    effort: skill.effort || "",
    context: skill.context || "",
    agent: skill.agent || "",
    shell: skill.shell || "",
    readme: skill.readme || "",
    instructions: skill.instructions || "",
    examples: skill.examples || [],
    resources: skill.resources || []
  };
}

function skillMatchesQuery(skill: Skill, query: string): boolean {
  if (!query) return true;
  return [
    skill.name,
    skill.purpose,
    skill.description,
    skill.whenToUse,
    skill.source,
    skill.path,
    ...(skill.triggers || [])
  ]
    .filter(Boolean)
    .some((value) => String(value).toLowerCase().includes(query));
}

/** 构建技能详情弹窗主体：元数据网格（ID/来源/触发词/可调用性/工具白黑名单等）+ README/描述/指令/示例/资源分段。 */
function renderSkillDetail(skill: Skill): HTMLElement {
  const panel = document.createElement("div");
  panel.className = "skillDetailPanel";

  const metadata = document.createElement("div");
  metadata.className = "skillMetadataGrid";
  metadata.append(
    skillMetadataItem("ID", skill.id),
    skillMetadataItem("Name", "/" + skill.name),
    skillMetadataItem("Source", skill.source || "unknown"),
    skillMetadataItem("Path", skill.path || "bundled"),
    skillMetadataItem("Purpose", skill.purpose || "No purpose"),
    skillMetadataItem("When To Use", skill.whenToUse || skill.description || "No usage description"),
    skillMetadataItem("Triggers", skill.triggers.length > 0 ? skill.triggers.join(", ") : "None"),
    skillMetadataItem("User Invocable", skill.userInvocable === false ? "false" : "true"),
    skillMetadataItem("Model Invocation", skill.disableModelInvocation ? "disabled" : "enabled"),
    skillMetadataItem("Allowed Tools", (skill.allowedTools || []).join(", ") || "Not restricted"),
    skillMetadataItem("Disallowed Tools", (skill.disallowedTools || []).join(", ") || "None")
  );

  appendSkillSection(panel, "Skill Metadata", metadata);
  appendSkillSection(panel, "README.md", preformattedBlock(skill.readme || "No README.md content."));
  appendSkillSection(panel, "Description", preformattedBlock(skill.description || "No description."));
  appendSkillSection(panel, "Instructions", preformattedBlock(skill.instructions || "No instructions."));
  appendSkillSection(panel, "Examples", preformattedBlock(formatExamples(skill.examples || [])));
  appendSkillSection(panel, "Resources", preformattedBlock(formatResources(skill.resources || [])));
  return panel;
}

function skillMetadataItem(labelText: string, value: string): HTMLElement {
  const item = document.createElement("div");
  item.className = "skillMetadataItem";
  const label = document.createElement("span");
  label.textContent = labelText;
  const content = document.createElement("strong");
  content.textContent = value;
  item.append(label, content);
  return item;
}

function appendSkillSection(panel: HTMLElement, title: string, content: HTMLElement): void {
  const section = document.createElement("section");
  section.className = "skillSection";
  const label = document.createElement("div");
  label.className = "skillDetailLabel";
  label.textContent = title;
  section.append(label, content);
  panel.appendChild(section);
}

function preformattedBlock(value: string): HTMLElement {
  const pre = document.createElement("pre");
  pre.className = "skillInstructions";
  pre.textContent = value;
  return pre;
}

function createLabeledInput(labelText: string, value: string, type: string): { wrapper: HTMLElement; input: HTMLInputElement } {
  const wrapper = document.createElement("label");
  wrapper.className = "modelField";
  const label = document.createElement("span");
  label.textContent = labelText;
  const input = document.createElement("input");
  input.type = type;
  input.value = value;
  input.disabled = isStreaming;
  wrapper.append(label, input);
  return { wrapper, input };
}

/**
 * 构建带标签的自定义下拉：底层保留一个隐藏原生 <select>（承载表单值与 change 事件、a11y 兜底），
 * 上层用按钮 + 自定义菜单实现可样式化的下拉外观。sync() 把原生 select 的当前值同步到自定义 UI（勾选态与显示文案）。
 * 点击菜单项即改写原生 select 并派发 change 事件；点击外部关闭菜单。
 */
function createLabeledSelect(labelText: string, value: string, options: Array<{ value: string; label: string }>): { wrapper: HTMLElement; select: HTMLSelectElement; sync: () => void } {
  const wrapper = document.createElement("div");
  wrapper.className = "modelField customModelSelectField";
  const label = document.createElement("span");
  label.textContent = labelText;
  const select = document.createElement("select");
  select.className = "modelNativeSelect";
  select.tabIndex = -1;
  select.setAttribute("aria-hidden", "true");
  select.disabled = isStreaming;
  const optionByValue = new Map(options.map((option) => [option.value, option.label]));
  for (const option of options) {
    const item = document.createElement("option");
    item.value = option.value;
    item.textContent = option.label;
    select.appendChild(item);
  }
  select.value = value;

  const customSelect = document.createElement("div");
  customSelect.className = "customModelSelect";
  const button = document.createElement("button");
  button.type = "button";
  button.className = "customModelSelectButton";
  button.setAttribute("aria-haspopup", "listbox");
  button.setAttribute("aria-expanded", "false");
  button.disabled = select.disabled;
  const menu = document.createElement("div");
  menu.className = "customModelSelectMenu hidden";
  menu.setAttribute("role", "listbox");

  const closeMenu = (): void => {
    menu.classList.add("hidden");
    button.setAttribute("aria-expanded", "false");
  };
  const selectedLabel = (): string => optionByValue.get(select.value) || options[0]?.label || "请选择";
  const itemButtons: HTMLButtonElement[] = [];
  const sync = (): void => {
    button.disabled = select.disabled;
    button.innerHTML = `<span class="customModelSelectValue">${escapeHTML(selectedLabel())}</span><span class="pickerChevron">⌄</span>`;
    for (const item of itemButtons) {
      const checked = item.dataset.value === select.value;
      item.classList.toggle("checked", checked);
      item.setAttribute("aria-selected", String(checked));
      const check = item.querySelector<HTMLElement>(".customModelSelectCheck");
      if (check) check.textContent = checked ? "✓" : "";
    }
  };

  for (const option of options) {
    const item = document.createElement("button");
    item.type = "button";
    item.className = "customModelSelectItem";
    item.dataset.value = option.value;
    item.setAttribute("role", "option");
    item.innerHTML = `<span>${escapeHTML(option.label)}</span><span class="customModelSelectCheck"></span>`;
    item.addEventListener("click", (event) => {
      event.stopPropagation();
      if (select.disabled) return;
      select.value = option.value;
      select.dispatchEvent(new Event("change", { bubbles: true }));
      sync();
      closeMenu();
    });
    itemButtons.push(item);
    menu.appendChild(item);
  }

  button.addEventListener("click", (event) => {
    event.stopPropagation();
    if (select.disabled) return;
    const shouldOpen = menu.classList.contains("hidden");
    menu.classList.toggle("hidden", !shouldOpen);
    button.setAttribute("aria-expanded", String(shouldOpen));
  });
  document.addEventListener("click", (event) => {
    if (event.target instanceof Node && wrapper.contains(event.target)) return;
    closeMenu();
  });
  select.addEventListener("change", sync);

  customSelect.append(button, menu);
  wrapper.append(label, customSelect, select);
  sync();
  return { wrapper, select, sync };
}

function modelConfigType(config?: ModelConfig): "reasoning" | "embedding" {
  return config?.modelType === "embedding" ? "embedding" : "reasoning";
}

function defaultReasoningModelConfigId(): string {
  return modelConfigs.find((config) => modelConfigType(config) === "reasoning")?.id || "default";
}

function modelConfigLabel(config: ModelConfig): string {
  if (modelConfigType(config) === "embedding") {
    return `${config.id} · 向量 · ${config.model || "model"}`;
  }
  return `${config.id} · ${config.model || "model"}`;
}

function escapeHTML(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function nextModelConfigID(): string {
  let index = modelConfigs.length + 1;
  while (modelConfigs.some((config) => config.id === `model-${index}`)) {
    index += 1;
  }
  return `model-${index}`;
}

function formatExamples(examples: SkillExample[]): string {
  if (examples.length === 0) return "No examples.";
  return examples
    .map((example, index) => {
      const title = example.name || `Example ${index + 1}`;
      return [`### ${title}`, `User: ${example.user || ""}`, `Assistant: ${example.assistant || ""}`].join("\n");
    })
    .join("\n\n");
}

function formatResources(resources: SkillResource[]): string {
  if (resources.length === 0) return "No resources.";
  return resources
    .map((resource, index) => {
      const lines = [`### ${resource.name || `Resource ${index + 1}`}`, `Type: ${resource.type || "document"}`];
      if (resource.uri) lines.push(`URI: ${resource.uri}`);
      if (resource.content) lines.push(`Content: ${resource.content}`);
      return lines.join("\n");
    })
    .join("\n\n");
}

function fileExtension(name: string): string {
  const clean = name.trim();
  const dot = clean.lastIndexOf(".");
  if (dot < 0 || dot === clean.length - 1) return "FILE";
  return clean.slice(dot + 1, dot + 5).toUpperCase();
}

function formatFileSize(size: number): string {
  if (!Number.isFinite(size) || size <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = size;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  const digits = value >= 10 || unitIndex === 0 ? 0 : 1;
  return `${value.toFixed(digits)} ${units[unitIndex]}`;
}

function chunkSummary(value: { chunkCount?: number; childChunkCount?: number; parentChunkCount?: number }): string {
  const total = value.chunkCount ?? 0;
  const child = value.childChunkCount ?? 0;
  const parent = value.parentChunkCount ?? 0;
  if (child > 0 || parent > 0) {
    return `${total} chunks (${child} child, ${parent} parent)`;
  }
  return `${total} chunks`;
}

function formatDateTime(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

/**
 * 启用/禁用某技能：把目标状态合并进"已启用集合"后整体 PUT /api/skills/config，
 * 用后端回传的最新技能列表覆盖本地（并保留本地已懒加载的重字段），再刷新列表与斜杠菜单。
 */
async function setSkillEnabled(name: string, enabled: boolean): Promise<void> {
  if (isStreaming) return;
  try {
    const nextEnabled = new Set(enabledSkillNames);
    if (enabled) {
      nextEnabled.add(name);
    } else {
      nextEnabled.delete(name);
    }
    const previousByName = new Map(availableSkills.map((skill) => [skill.name, skill]));
    const body = await request<SkillsResponse>("/api/skills/config", {
      method: "PUT",
      body: JSON.stringify({ enabledSkills: Array.from(nextEnabled) })
    });
    availableSkills = (body.skills ?? []).map((skill) => ({ ...(previousByName.get(skill.name) ?? {}), ...skill }));
    enabledSkillNames = new Set(availableSkills.filter((skill) => skill.enabled).map((skill) => skill.name));
    skillLoadError = "";
    renderSkills();
    renderSlashMenu();
  } catch (error) {
    showSkillError(error);
  }
}

async function deleteSkill(name: string): Promise<void> {
  if (isStreaming) return;
  try {
    const body = await request<SkillsResponse>(`/api/skills/${encodeURIComponent(name)}`, {
      method: "DELETE"
    });
    availableSkills = body.skills ?? [];
    enabledSkillNames = new Set(availableSkills.filter((skill) => skill.enabled).map((skill) => skill.name));
    if (expandedSkillName === name) expandedSkillName = null;
    renderSkills();
    renderSlashMenu();
  } catch (error) {
    showSkillError(error);
  }
}

/**
 * 渲染输入框的斜杠技能菜单（/skill 快捷调用）。仅当光标所在行是 "/关键字" 形态时展示，
 * 候选来自"已启用且允许用户调用"的技能并按关键字过滤。流式期间或无候选时隐藏。
 */
function renderSlashMenu(): void {
  if (isStreaming) {
    hideSlashMenu();
    return;
  }
  const state = currentSlashState();
  if (!state) {
    hideSlashMenu();
    return;
  }
  const enabled = availableSkills.filter((skill) => enabledSkillNames.has(skill.name) && skill.userInvocable !== false);
  slashMenuSkills = enabled.filter((skill) => skill.name.toLowerCase().includes(state.query.toLowerCase()));
  if (slashMenuSkills.length === 0) {
    hideSlashMenu();
    return;
  }
  if (slashMenuIndex >= slashMenuSkills.length) {
    slashMenuIndex = 0;
  }
  skillMenuEl.innerHTML = "";
  slashMenuSkills.forEach((skill, index) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `slashItem${index === slashMenuIndex ? " active" : ""}`;
    button.setAttribute("role", "option");
    button.setAttribute("aria-selected", String(index === slashMenuIndex));
    const name = document.createElement("span");
    name.className = "slashName";
    name.textContent = "/" + skill.name;
    button.append(name);
    button.addEventListener("mousedown", (event) => {
      event.preventDefault();
      selectSlashSkill(index);
    });
    skillMenuEl.appendChild(button);
  });
  skillMenuEl.classList.remove("hidden");
}

function hideSlashMenu(): void {
  skillMenuEl.classList.add("hidden");
  skillMenuEl.innerHTML = "";
  slashMenuSkills = [];
  slashMenuIndex = 0;
}

/**
 * 探测光标当前是否处于斜杠命令输入态：取光标前所在行，若整行匹配 "/词" 则返回该 token 的
 * 起止位置与查询词，供菜单过滤与选中替换使用；否则返回 null。
 */
function currentSlashState(): { tokenStart: number; tokenEnd: number; query: string } | null {
  const cursor = inputEl.selectionStart ?? inputEl.value.length;
  const beforeCursor = inputEl.value.slice(0, cursor);
  const lineStart = Math.max(beforeCursor.lastIndexOf("\n") + 1, 0);
  const line = beforeCursor.slice(lineStart);
  const match = line.match(/^\/([A-Za-z0-9_:-]*)$/);
  if (!match) return null;
  return {
    tokenStart: lineStart,
    tokenEnd: cursor,
    query: match[1] || ""
  };
}

/** 斜杠菜单键盘导航：上下键循环移动高亮、Enter/Tab 选中、Esc 关闭。返回 true 表示已消费该按键（阻止发送/换行）。 */
function handleSlashMenuKeydown(event: KeyboardEvent): boolean {
  if (skillMenuEl.classList.contains("hidden")) return false;
  if (event.key === "ArrowDown") {
    event.preventDefault();
    slashMenuIndex = (slashMenuIndex + 1) % slashMenuSkills.length;
    renderSlashMenu();
    return true;
  }
  if (event.key === "ArrowUp") {
    event.preventDefault();
    slashMenuIndex = (slashMenuIndex - 1 + slashMenuSkills.length) % slashMenuSkills.length;
    renderSlashMenu();
    return true;
  }
  if (event.key === "Enter" || event.key === "Tab") {
    event.preventDefault();
    selectSlashSkill(slashMenuIndex);
    return true;
  }
  if (event.key === "Escape") {
    event.preventDefault();
    hideSlashMenu();
    return true;
  }
  return false;
}

/** 选中斜杠菜单某项：用 "/技能名 " 替换输入框中当前的 "/词" token，并把光标移到插入内容之后。 */
function selectSlashSkill(index: number): void {
  const skill = slashMenuSkills[index];
  const state = currentSlashState();
  if (!skill || !state) return;
  const prefix = inputEl.value.slice(0, state.tokenStart);
  const suffix = inputEl.value.slice(state.tokenEnd);
  const insertion = "/" + skill.name + " ";
  inputEl.value = prefix + insertion + suffix;
  const cursor = prefix.length + insertion.length;
  inputEl.setSelectionRange(cursor, cursor);
  inputEl.focus();
  hideSlashMenu();
}

/** 整块重绘消息列表（切会话/首次加载时调用）。空列表显示欢迎空态；否则逐条渲染并给最后一条打上"可选 canonical"标记。 */
function renderMessages(messages: Message[]): void {
  messagesEl.innerHTML = "";
  setEmptyChat(messages.length === 0);
  if (messages.length === 0) {
    messagesEl.innerHTML = `<div class="emptyState"><div class="emptyTitle">今天想做什么？</div><div class="emptyHint">开始对话，或输入 / 选择一个技能。</div></div>`;
    renderMessageLocator();
    return;
  }
  for (let index = 0; index < messages.length; index += 1) {
    addMessage(messageWithLatestFlag(messages[index], index, messages), false);
  }
  renderMessageLocator();
  scrollMessages();
}

/**
 * 追加渲染一条消息气泡。多模型助手消息走多卡片布局；其余（用户/单模型助手）渲染成普通气泡：
 * 助手回答外包一层 answerPanel 并挂"查看 Markdown 原文"按钮，metadata.loopRounds>1 时附带 agent 轮数标注。
 */
function addMessage(input: Message, shouldScroll = true): HTMLElement {
  setEmptyChat(false);
  if (input.role === "assistant" && input.metadata?.multiModel) {
    return addMultiModelMessage(input, shouldScroll);
  }
  const wrapper = document.createElement("article");
  wrapper.className = `message ${input.role}`;

  const content = document.createElement("div");
  content.className = "messageContent";
  renderMarkdown(content, input.content);
  if (input.role === "assistant") {
    const answerPanel = document.createElement("section");
    answerPanel.className = "answerPanel sourceCapable";
    attachAnswerSourceButton(answerPanel, () => input.content || "", "回答内容");
    answerPanel.append(content);
    wrapper.append(answerPanel);
  } else {
    wrapper.append(content);
  }

  if (input.role === "assistant" && input.metadata) {
    const loopRounds = input.metadata["loopRounds"];
    if (typeof loopRounds === "number" && loopRounds > 1) {
      const meta = document.createElement("div");
      meta.className = "messageMeta";
      meta.textContent = `Agent loops: ${loopRounds}`;
      wrapper.appendChild(meta);
    }
  }

  appendMessageElement(wrapper, shouldScroll);
  return wrapper;
}

/** 仅对"最后一条多模型助手消息"注入 latestSelectable 标记，使其 canonical 回答可被点选切换（历史轮次不可改）。 */
function messageWithLatestFlag(message: Message, index: number, messages: Message[]): Message {
  if (message.role !== "assistant" || !message.metadata?.multiModel) return message;
  return {
    ...message,
    metadata: {
      ...message.metadata,
      latestSelectable: index === messages.length - 1
    }
  };
}

function addMultiModelMessage(input: Message, shouldScroll = true): HTMLElement {
  const wrapper = buildMultiModelMessageElement(input);
  appendMessageElement(wrapper, shouldScroll);
  return wrapper;
}

/**
 * 由一条多模型助手消息（历史回放）构建整块 DOM：可选的顶部提示条 + N 列卡片网格（每模型一张回答卡）。
 * canonicalResponseId 决定哪张卡片高亮为"用于上下文"，latestSelectable 决定卡片是否可点选切换。
 */
function buildMultiModelMessageElement(input: Message): HTMLElement {
  const metadata = input.metadata || {};
  const responses = Array.isArray(metadata.modelResponses) ? metadata.modelResponses : [];
  const canonicalResponseId = String(metadata.canonicalResponseId || "");
  const latestSelectable = Boolean(metadata["latestSelectable"]);
  const wrapper = document.createElement("article");
  wrapper.className = "message assistant multiModelMessage";
  wrapper.dataset.messageId = input.id || "";
  wrapper.dataset.turnId = String(metadata.turnId || "");

  if (latestSelectable) {
    const tip = document.createElement("div");
    tip.className = "multiModelTip";
    tip.textContent = "默认使用主模型回答作为后续上下文。你可以选择本轮要采用的回答。";
    wrapper.appendChild(tip);
  }

  const grid = document.createElement("div");
  grid.className = `multiModelGrid count${Math.min(Math.max(responses.length, 1), 3)}`;
  for (const response of responses) {
    grid.appendChild(renderModelResponseCard(input, response, canonicalResponseId, latestSelectable));
  }
  wrapper.appendChild(grid);
  return wrapper;
}

/**
 * 渲染多模型历史消息中单个模型的回答卡片：标题（模型名）+ 状态标签（用于上下文/主模型候选/候选回答）+
 * Markdown 回答内容（失败态显示错误）。可选中且未选中且已完成时，点击卡片会把该回答设为 canonical。
 */
function renderModelResponseCard(message: Message, response: ModelResponseSummary, canonicalResponseId: string, selectable: boolean): HTMLElement {
  const selected = response.id === canonicalResponseId;
  const card = document.createElement("section");
  card.className = `multiModelCard${selected ? " canonical" : " muted"}${selectable ? " selectable" : ""}${response.status === "failed" ? " failed" : ""}`;
  card.dataset.responseId = response.id;
  card.dataset.modelConfigId = response.modelConfigId;

  const header = document.createElement("div");
  header.className = "multiModelCardHeader";
  const title = document.createElement("strong");
  title.textContent = modelDisplayName(response.modelConfigId);
  const status = document.createElement("span");
  status.className = "multiModelStatus";
  status.textContent = selected ? "用于上下文" : response.primaryResponse ? "主模型候选" : "候选回答";
  header.append(title, status);

  const answer = document.createElement("div");
  answer.className = "answerContent";
  renderMarkdown(answer, response.status === "failed" ? `请求失败：${response.error || "unknown error"}` : response.content || "(empty response)");
  const answerPanel = document.createElement("section");
  answerPanel.className = "answerPanel sourceCapable";
  attachAnswerSourceButton(answerPanel, () => response.status === "failed" ? `请求失败：${response.error || "unknown error"}` : response.content || "(empty response)", `${modelDisplayName(response.modelConfigId)} 回答内容`);
  answerPanel.append(answer);
  card.append(header, answerPanel);

  if (selectable && !selected && response.status === "completed") {
    card.addEventListener("click", async () => {
      if (!card.classList.contains("selectable")) return;
      await selectCanonicalResponse(message, response.id);
    });
  }
  return card;
}

/** 发送新一轮前调用：把上一轮多模型卡片的可选态摘除并更新提示文案，锁定历史轮次的 canonical 选择。 */
function lockExistingCanonicalSelection(): void {
  messagesEl.querySelectorAll<HTMLElement>(".multiModelCard.selectable").forEach((card) => {
    card.classList.remove("selectable");
  });
  messagesEl.querySelectorAll<HTMLElement>(".multiModelTip").forEach((tip) => {
    tip.textContent = "本轮已进入历史，只保留用于上下文的回答。";
  });
}

/**
 * 用户点选某模型回答作为本轮 canonical（进入后续上下文）：PUT 到 turns/:turnId/canonical-response，
 * 后端返回更新后的消息，前端据此重绘该条多模型消息以更新高亮。缺 turnId/会话/responseId 或流式中时忽略。
 */
async function selectCanonicalResponse(message: Message, responseId: string): Promise<void> {
  const turnId = String(message.metadata?.turnId || "");
  const conversationId = message.conversationId || activeConversationId;
  if (!turnId || !conversationId || !responseId || isStreaming) return;
  try {
    const body = await request<{ message: Message }>(`/api/conversations/${encodeURIComponent(conversationId)}/turns/${encodeURIComponent(turnId)}/canonical-response`, {
      method: "PUT",
      body: JSON.stringify({ responseId })
    });
    updateLatestAssistantMessage(body.message);
  } catch (error) {
    window.alert(error instanceof Error ? error.message : String(error));
  }
}

/** 用后端回传的最新消息就地替换页面上对应 messageId 的多模型消息 DOM（始终标记为可选，因为它是最新一轮）。 */
function updateLatestAssistantMessage(message: Message): void {
  const existing = Array.from(messagesEl.querySelectorAll<HTMLElement>(".message")).find((item) => item.dataset.messageId === (message.id || ""));
  if (!existing) return;
  const next = buildMultiModelMessageElement({ ...message, metadata: { ...(message.metadata || {}), latestSelectable: true } });
  existing.replaceWith(next);
  scrollMessages();
}

/**
 * 流式过程中同步多模型卡片的高亮：canonical（后端已选或默认主模型）卡片高亮、其余置灰并更新状态标签。
 * completed=true 时额外更新顶部提示条文案，告知用户可切换本轮回答。
 */
function updateMultiCanonicalStyles(streamState: StreamRenderState, completed: boolean): void {
  const multi = streamState.multi;
  if (!multi) return;
  for (const state of multi.models.values()) {
    const selected = state.responseId === multi.canonicalResponseId || (!multi.canonicalResponseId && state.modelConfigId === activeModelConfigId);
    state.card.classList.toggle("canonical", selected);
    state.card.classList.toggle("muted", !selected);
    state.statusEl.textContent = selected ? "用于上下文" : completed ? "候选回答" : state.statusEl.textContent;
  }
  if (completed) {
    multi.tipEl.textContent = "已选择用于后续上下文的回答。你仍可在发送下一轮前切换本轮回答。";
  }
}

function modelDisplayName(modelConfigId: string): string {
  const config = modelConfigs.find((item) => item.id === modelConfigId);
  return config ? modelConfigLabel(config) : modelConfigId;
}

/**
 * 构建单模型流式回答的空骨架并挂到消息区：可折叠的"思考过程"面板（含 loading 动画）+ 初始隐藏的回答面板。
 * 返回持有各 DOM 句柄的 StreamRenderState，供后续 delta/trace/final 事件增量填充。
 */
function addAssistantStream(): StreamRenderState {
  const wrapper = document.createElement("article");
  wrapper.className = "message assistant streaming";

  const thinkingDetails = document.createElement("details");
  thinkingDetails.className = "thinkingPanel";
  thinkingDetails.open = true;
  const thinkingHeader = document.createElement("summary");
  thinkingHeader.className = "panelHeader";
  const thinkingTitle = document.createElement("span");
  thinkingTitle.textContent = "思考过程";
  const loadingEl = document.createElement("span");
  loadingEl.className = "loadingDots";
  loadingEl.setAttribute("aria-label", "loading");
  loadingEl.textContent = "";
  thinkingHeader.append(thinkingTitle, loadingEl);

  const traceList = document.createElement("div");
  traceList.className = "traceList";
  thinkingDetails.append(thinkingHeader, traceList);

  const answerPanel = document.createElement("section");
  answerPanel.className = "answerPanel hidden sourceCapable";
  const answerContent = document.createElement("div");
  answerContent.className = "answerContent";
  answerPanel.append(answerContent);

  wrapper.append(thinkingDetails, answerPanel);
  appendMessageElement(wrapper, true);
  const streamState: StreamRenderState = { wrapper, mode: "single", traceList, answerPanel, answerContent, answerMarkdown: "", loadingEl, thinkingDetails, hasFinalAnswer: false, hasTraceEvents: false };
  attachAnswerSourceButton(answerPanel, () => streamState.answerMarkdown || "", "回答内容");
  return streamState;
}

/**
 * 构建多模型流式回答骨架：顶部提示条 + N 列卡片网格，为每个选中模型预建一张流式卡片并存入 multi.models 映射。
 * 初始按"主模型默认高亮"设置卡片样式，后续由 SSE 事件驱动各卡片内容与 canonical 高亮。
 */
function addMultiAssistantStream(modelConfigIds: string[]): StreamRenderState {
  const wrapper = document.createElement("article");
  wrapper.className = "message assistant streaming multiModelMessage";

  const tipEl = document.createElement("div");
  tipEl.className = "multiModelTip";
  tipEl.textContent = "默认使用主模型回答作为后续上下文。你可以在本轮结束后选择本轮采用的回答。";

  const gridEl = document.createElement("div");
  gridEl.className = `multiModelGrid count${Math.min(modelConfigIds.length, 3)}`;

  const multi: MultiStreamState = {
    tipEl,
    gridEl,
    models: new Map()
  };
  const streamState: StreamRenderState = {
    wrapper,
    mode: "multi",
    multi
  };
  for (const modelConfigId of modelConfigIds) {
    const state = createModelStreamCard(modelConfigId);
    multi.models.set(modelConfigId, state);
    gridEl.appendChild(state.card);
  }
  wrapper.append(tipEl, gridEl);
  appendMessageElement(wrapper, true);
  updateMultiCanonicalStyles(streamState, false);
  return streamState;
}

/** 构建单张多模型流式卡片（标题+状态标签+紧凑思考过程面板+隐藏回答面板），返回其 ModelStreamState 供事件填充。 */
function createModelStreamCard(modelConfigId: string, responseId?: string): ModelStreamState {
  const card = document.createElement("section");
  card.className = `multiModelCard streaming${modelConfigId === activeModelConfigId ? " canonical" : " muted"}`;
  card.dataset.modelConfigId = modelConfigId;
  if (responseId) card.dataset.responseId = responseId;

  const header = document.createElement("div");
  header.className = "multiModelCardHeader";
  const title = document.createElement("strong");
  title.textContent = modelDisplayName(modelConfigId);
  const statusEl = document.createElement("span");
  statusEl.className = "multiModelStatus";
  statusEl.textContent = modelConfigId === activeModelConfigId ? "主模型 · 回答中" : "候选回答中";
  header.append(title, statusEl);

  const thinkingDetails = document.createElement("details");
  thinkingDetails.className = "thinkingPanel compact";
  thinkingDetails.open = true;
  const thinkingHeader = document.createElement("summary");
  thinkingHeader.className = "panelHeader";
  const thinkingTitle = document.createElement("span");
  thinkingTitle.textContent = "思考过程";
  const loadingEl = document.createElement("span");
  loadingEl.className = "loadingDots";
  thinkingHeader.append(thinkingTitle, loadingEl);
  const traceList = document.createElement("div");
  traceList.className = "traceList";
  thinkingDetails.append(thinkingHeader, traceList);

  const answerPanel = document.createElement("section");
  answerPanel.className = "answerPanel hidden sourceCapable";
  const answerContent = document.createElement("div");
  answerContent.className = "answerContent";
  answerPanel.append(answerContent);

  card.append(header, thinkingDetails, answerPanel);
  const state: ModelStreamState = { responseId, modelConfigId, card, statusEl, traceList, answerPanel, answerContent, answerMarkdown: "", loadingEl, thinkingDetails, hasFinalAnswer: false, hasTraceEvents: false };
  attachAnswerSourceButton(answerPanel, () => state.answerMarkdown || "", `${modelDisplayName(modelConfigId)} 回答内容`);
  return state;
}

/** 向某模型卡片的思考过程列表追加一条 trace（过程消息或工具调用/结果的 JSON），并滚动到底。 */
function appendModelTraceEvent(modelState: ModelStreamState, event: AgentStreamEvent): void {
  modelState.hasTraceEvents = true;
  const item = document.createElement("section");
  item.className = `traceEvent ${event.type}`;
  const body = document.createElement("pre");
  body.className = "traceBody";
  body.textContent = event.content || eventPayload(event);
  item.append(body);
  modelState.traceList.appendChild(item);
  scrollMessages();
}

/** 追加某模型回答的增量文本：累积原始 Markdown 后整体重渲染并显示回答面板（已 final 或应抑制时跳过）。 */
function appendModelAnswerDelta(modelState: ModelStreamState, delta: string): void {
  if (!delta || modelState.hasFinalAnswer) return;
  modelState.answerMarkdown += delta;
  if (shouldSuppressLiveAnswer(modelState.answerMarkdown)) return;
  renderMarkdown(modelState.answerContent, modelState.answerMarkdown);
  modelState.answerPanel.classList.remove("hidden");
  scrollMessages();
}

function resetModelLiveAnswer(modelState: ModelStreamState): void {
  if (modelState.hasFinalAnswer) return;
  modelState.answerMarkdown = "";
  modelState.answerContent.innerHTML = "";
  modelState.answerPanel.classList.add("hidden");
}

/** 某模型回答完成：落地完整内容、移除 loading、有过程则折叠思考面板（无过程则移除），更新状态标签。 */
function finishModelStreamWithAnswer(modelState: ModelStreamState, content: string): void {
  modelState.hasFinalAnswer = true;
  modelState.answerMarkdown = content || "(empty response)";
  renderMarkdown(modelState.answerContent, modelState.answerMarkdown);
  modelState.answerPanel.classList.remove("hidden");
  modelState.loadingEl.remove();
  if (modelState.hasTraceEvents) {
    modelState.thinkingDetails.open = false;
  } else {
    modelState.thinkingDetails.remove();
  }
  modelState.card.classList.remove("streaming");
  modelState.statusEl.textContent = modelState.modelConfigId === activeModelConfigId ? "主模型" : "候选回答";
  scrollMessages();
}

/** 单模型模式：向思考过程列表追加一条 trace（过程消息或工具调用/结果 JSON）。 */
function appendTraceEvent(streamState: StreamRenderState, event: AgentStreamEvent): void {
  streamState.hasTraceEvents = true;
  const item = document.createElement("section");
  item.className = `traceEvent ${event.type}`;

  const body = document.createElement("pre");
  body.className = "traceBody";
  body.textContent = event.content || eventPayload(event);

  item.append(body);
  streamState.traceList?.appendChild(item);
  scrollMessages();
}

/** 单模型模式：追加回答增量文本，累积原始 Markdown 后整体重渲染并显示回答面板。 */
function appendAnswerDelta(streamState: StreamRenderState, delta: string): void {
  if (!delta || streamState.hasFinalAnswer) return;
  streamState.answerMarkdown = (streamState.answerMarkdown || "") + delta;
  if (shouldSuppressLiveAnswer(streamState.answerMarkdown || "")) {
    return;
  }
  if (streamState.answerContent) renderMarkdown(streamState.answerContent, streamState.answerMarkdown || "");
  streamState.answerPanel?.classList.remove("hidden");
  scrollMessages();
}

function resetLiveAnswer(streamState: StreamRenderState): void {
  if (streamState.hasFinalAnswer) return;
  streamState.answerMarkdown = "";
  if (streamState.answerContent) streamState.answerContent.innerHTML = "";
  streamState.answerPanel?.classList.add("hidden");
}

/**
 * 判断是否暂时抑制实时回答渲染：当回答开头正在逐字拼出 "<load_skill" 指令标记（尚不完整）时，
 * 先不渲染，避免把内部的 skill 加载指令闪现给用户。等待后续 answer_reset 清空重来。
 */
function shouldSuppressLiveAnswer(markdown: string): boolean {
  const trimmed = markdown.trimStart().toLowerCase();
  if (!trimmed) return true;
  return "<load_skill".startsWith(trimmed) || trimmed.startsWith("<load_skill");
}

/** 单模型回答收尾：落地完整内容、移除 loading、有过程则折叠思考面板（无过程则移除）、去掉 streaming 状态。 */
function finishStreamWithAnswer(streamState: StreamRenderState, content: string): void {
  streamState.hasFinalAnswer = true;
  streamState.answerMarkdown = content || "(empty response)";
  if (streamState.answerContent) renderMarkdown(streamState.answerContent, streamState.answerMarkdown);
  streamState.answerPanel?.classList.remove("hidden");
  streamState.loadingEl?.remove();
  if (streamState.hasTraceEvents) {
    if (streamState.thinkingDetails) streamState.thinkingDetails.open = false;
  } else {
    streamState.thinkingDetails?.remove();
  }
  streamState.wrapper.classList.remove("streaming");
  scrollMessages();
}

/** 从事件中提取用于 trace 展示的 payload：优先工具调用/结果（数组或单个），序列化为紧凑 JSON 文本。 */
function eventPayload(event: AgentStreamEvent): string {
  if (event.toolCalls?.length) return compactJSONString(event.toolCalls);
  if (event.toolResults?.length) return compactJSONString(event.toolResults);
  if (event.toolCall) return compactJSONString(event.toolCall);
  if (event.toolResult) return compactJSONString(event.toolResult);
  return "";
}

/** 序列化为带缩进的 JSON，过长（>2200 字符）时截断中间部分，保留头尾以免 trace 面板被巨型 payload 撑爆。 */
function compactJSONString(value: unknown): string {
  const text = JSON.stringify(value, null, 2);
  if (text.length <= 2200) return text;
  return text.slice(0, 1800) + "\n... truncated ...\n" + text.slice(-300);
}

function appendMessageElement(element: HTMLElement, shouldScroll: boolean): void {
  const empty = messagesEl.querySelector(".emptyState");
  if (empty) empty.remove();
  messagesEl.appendChild(element);
  renderMessageLocator();
  if (shouldScroll) scrollMessages();
}

function setSidebarCollapsed(collapsed: boolean): void {
  sidebarCollapsed = collapsed;
  shellEl.classList.toggle("sidebarCollapsed", collapsed);
  sidebarToggleEl.setAttribute("aria-expanded", String(!collapsed));
  const label = collapsed ? "展开侧边栏" : "收起侧边栏";
  sidebarToggleEl.setAttribute("aria-label", label);
  sidebarToggleEl.setAttribute("title", label);
  sidebarToggleTipEl.textContent = label;
  window.localStorage.setItem("sidebarCollapsed", String(collapsed));
}

/**
 * 渲染右侧消息定位器（迷你目录）：仅聊天视图且消息多于一条时显示。为每条消息补 id 锚点，
 * 生成带 Q/A 前缀与内容摘要的按钮，点击平滑滚动到对应消息。
 */
function renderMessageLocator(): void {
  messageLocatorEl.innerHTML = "";
  const messages = Array.from(messagesEl.querySelectorAll<HTMLElement>(".message"));
  const shouldShow = currentView === "chat" && messages.length > 1;
  messageLocatorEl.classList.toggle("hidden", !shouldShow);
  if (!shouldShow) return;

  messages.forEach((message, index) => {
    if (!message.id) {
      message.id = `message-anchor-${index + 1}`;
    }
    const button = document.createElement("button");
    button.type = "button";
    button.className = `messageLocatorItem ${message.classList.contains("user") ? "user" : "assistant"}`;
    button.textContent = locatorLabel(message, index);
    button.setAttribute("aria-label", `定位到第 ${index + 1} 条对话`);
    button.addEventListener("click", () => {
      message.scrollIntoView({ behavior: "smooth", block: "center" });
    });
    messageLocatorEl.appendChild(button);
  });
}

function locatorLabel(message: HTMLElement, index: number): string {
  const prefix = message.classList.contains("user") ? "Q" : "A";
  const text = (message.querySelector(".messageContent, .answerContent")?.textContent || "").replace(/\s+/g, " ").trim();
  if (!text) return `${prefix}${index + 1}`;
  return `${prefix}${index + 1} · ${text.slice(0, 18)}`;
}

/** 给回答面板加一个"MD"角标按钮，点击弹窗展示该回答的 Markdown 原文预览。getContent 惰性取值以适配流式内容。 */
function attachAnswerSourceButton(container: HTMLElement, getContent: () => string, title: string): void {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "answerSourceButton";
  button.textContent = "MD";
  button.setAttribute("aria-label", "查看 Markdown 原文");
  button.setAttribute("title", "查看 Markdown 原文");
  button.addEventListener("click", (event) => {
    event.stopPropagation();
    openSourceDialog(title, getContent());
  });
  container.prepend(button);
}

function openSourceDialog(title: string, content: string): void {
  const preview = document.createElement("div");
  preview.className = "sourcePreviewMarkdown answerContent";
  renderMarkdown(preview, content || "(empty response)");
  openAppModal({
    title,
    description: "Markdown 格式预览",
    body: preview,
    size: "source",
    actions: [
      { label: "关闭", variant: "secondary", onClick: () => closeAppModal(false) }
    ]
  });
}

/**
 * 通用模态弹窗构建器。先关掉已有弹窗，再按参数搭建 backdrop + dialog（标题/描述/正文/底部按钮）。
 * 点击遮罩或关闭按钮视为取消。仅"关闭"一个动作时不渲染底部按钮区。动作按钮点击期间禁用全部按钮防重复提交。
 * size 控制弹窗宽度变体（default/wide/source）。
 */
function openAppModal(options: {
  title: string;
  description?: string;
  body?: HTMLElement;
  actions?: ModalAction[];
  onCancel?: () => void;
  size?: "default" | "wide" | "source";
}): void {
  closeAppModal(false);
  modalCancelHandler = options.onCancel || null;
  modalRootEl.innerHTML = "";
  modalRootEl.className = `modalRoot${options.size === "wide" ? " wide" : ""}${options.size === "source" ? " sourceWide" : ""}`;
  document.body.classList.add("modalOpen");

  const backdrop = document.createElement("div");
  backdrop.className = "modalBackdrop";
  backdrop.addEventListener("click", () => closeAppModal(true));

  const dialog = document.createElement("section");
  dialog.className = "modalDialog";
  dialog.setAttribute("role", "dialog");
  dialog.setAttribute("aria-modal", "true");
  dialog.setAttribute("aria-label", options.title);

  const closeButton = document.createElement("button");
  closeButton.type = "button";
  closeButton.className = "modalCloseButton";
  closeButton.textContent = "×";
  closeButton.setAttribute("aria-label", "关闭弹窗");
  closeButton.addEventListener("click", () => closeAppModal(true));
  dialog.appendChild(closeButton);

  const header = document.createElement("div");
  header.className = "modalHeader";
  const title = document.createElement("h3");
  title.textContent = options.title;
  header.appendChild(title);
  if (options.description) {
    const description = document.createElement("p");
    description.textContent = options.description;
    header.appendChild(description);
  }

  const content = document.createElement("div");
  content.className = "modalContent";
  if (options.body) {
    content.appendChild(options.body);
  }

  dialog.append(header, content);

  const actions = options.actions?.length === 1 && options.actions[0]?.label === "关闭" ? [] : options.actions || [];
  if (actions.length) {
    const footer = document.createElement("div");
    footer.className = "modalFooter";
    for (const action of actions) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = `modalAction ${action.variant || "secondary"}`;
      button.textContent = action.label;
      button.addEventListener("click", async () => {
        const buttons = Array.from(footer.querySelectorAll<HTMLButtonElement>("button"));
        buttons.forEach((item) => {
          item.disabled = true;
        });
        try {
          await action.onClick();
        } finally {
          if (!modalRootEl.classList.contains("hidden")) {
            buttons.forEach((item) => {
              item.disabled = false;
            });
          }
        }
      });
      footer.appendChild(button);
    }
    dialog.appendChild(footer);
  }

  modalRootEl.append(backdrop, dialog);
  window.setTimeout(() => {
    dialog.querySelector<HTMLElement>("input, select, textarea, .modalAction, .modalCloseButton")?.focus();
  }, 0);
}

/** 关闭模态弹窗并清理 DOM/body 状态。notifyCancel=true 时触发注册的 onCancel（用于 Promise 化弹窗 resolve 取消值）。 */
function closeAppModal(notifyCancel = true): void {
  const cancel = modalCancelHandler;
  modalCancelHandler = null;
  modalRootEl.classList.add("hidden");
  modalRootEl.className = "modalRoot hidden";
  modalRootEl.innerHTML = "";
  document.body.classList.remove("modalOpen");
  if (notifyCancel) cancel?.();
}

/** 把删除确认弹窗 Promise 化：确认返回 true、取消/关闭返回 false，settled 保证只 resolve 一次。 */
function confirmDelete(title: string, message: string): Promise<boolean> {
  return new Promise((resolve) => {
    let settled = false;
    const settle = (value: boolean) => {
      if (settled) return;
      settled = true;
      resolve(value);
      closeAppModal(false);
    };
    openAppModal({
      title,
      description: message,
      onCancel: () => settle(false),
      actions: [
        { label: "取消", variant: "secondary", onClick: () => settle(false) },
        { label: "确认删除", variant: "danger", onClick: () => settle(true) }
      ]
    });
  });
}

/** 把单行文本输入弹窗 Promise 化：确认返回去空白后的非空字符串、取消返回 null；空值时就地报错不关闭。 */
function promptTextDialog(options: { title: string; label: string; placeholder?: string; confirmText?: string }): Promise<string | null> {
  return new Promise((resolve) => {
    let settled = false;
    const settle = (value: string | null) => {
      if (settled) return;
      settled = true;
      resolve(value);
      closeAppModal(false);
    };

    const field = document.createElement("label");
    field.className = "modalField";
    const label = document.createElement("span");
    label.textContent = options.label;
    const input = document.createElement("input");
    input.type = "text";
    input.placeholder = options.placeholder || "";
    const error = document.createElement("div");
    error.className = "modalFieldError";
    field.append(label, input, error);

    const submit = () => {
      const value = input.value.trim();
      if (!value) {
        error.textContent = `${options.label}不能为空`;
        input.focus();
        return;
      }
      settle(value);
    };
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        submit();
      }
    });

    openAppModal({
      title: options.title,
      body: field,
      onCancel: () => settle(null),
      actions: [
        { label: "取消", variant: "secondary", onClick: () => settle(null) },
        { label: options.confirmText || "确认", variant: "primary", onClick: submit }
      ]
    });
  });
}

/**
 * 统一的 JSON API 请求封装：默认带 content-type: application/json，读取响应文本并尝试 JSON 解析。
 * 非 2xx 时抛出后端返回的 error 字段（或原始文本/状态码）；成功但非法 JSON 也抛错。所有 /api 调用都走这里。
 */
async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      "content-type": "application/json",
      ...(init.headers ?? {})
    }
  });
  const text = await response.text();
  let body: (T & { error?: string }) | null = null;
  if (text) {
    try {
      body = JSON.parse(text) as T & { error?: string };
    } catch {
      body = null;
    }
  }
  if (!response.ok) {
    throw new Error(body?.error || text || `Request failed: ${response.status}`);
  }
  if (!body) throw new Error(`Invalid JSON response from ${path}`);
  return body;
}

async function responseError(response: Response): Promise<string> {
  const text = await response.text();
  if (!text) return `Request failed: ${response.status}`;
  try {
    const body = JSON.parse(text) as { error?: string };
    return body.error || text;
  } catch {
    return text;
  }
}

/** 页面级致命错误（如启动失败）：清空消息区并在其中显示错误提示。 */
function showPageError(error: unknown): void {
  const message = error instanceof Error ? error.message : String(error);
  setEmptyChat(true);
  messagesEl.innerHTML = "";
  const errorEl = document.createElement("div");
  errorEl.className = "emptyState errorState";
  errorEl.textContent = "页面初始化失败：" + message;
  messagesEl.appendChild(errorEl);
}

// 各视图专属错误处理：把错误信息写入对应的 xxxLoadError 状态并重绘该视图（在区块内展示错误+重试）。
function showSkillError(error: unknown): void {
  skillLoadError = error instanceof Error ? error.message : String(error);
  availableSkills = [];
  enabledSkillNames = new Set();
  hideSlashMenu();
  renderSkills();
}

function showKnowledgeError(error: unknown): void {
  knowledgeLoadError = error instanceof Error ? error.message : String(error);
  renderKnowledge();
}

function showModelError(error: unknown): void {
  modelLoadError = error instanceof Error ? error.message : String(error);
  renderModelConfigs();
}

function scrollMessages(): void {
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

function setEmptyChat(isEmpty: boolean): void {
  chatPaneEl.classList.toggle("emptyChat", isEmpty);
}

/** 抓取必须存在的 DOM 元素：缺失即抛错（fail-fast），让后续引用无需判空。 */
function mustQuery<T extends Element>(selector: string): T {
  const element = document.querySelector<T>(selector);
  if (!element) throw new Error(`Missing element: ${selector}`);
  return element;
}
