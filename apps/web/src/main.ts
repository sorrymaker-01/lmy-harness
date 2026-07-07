import { renderMarkdown } from "./markdown.js";

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

type MessageMetadata = Record<string, unknown> & {
  multiModel?: boolean;
  turnId?: string;
  primaryModelConfigId?: string;
  canonicalResponseId?: string;
  modelResponses?: ModelResponseSummary[];
};

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
const multiModelToggleEl = mustQuery<HTMLButtonElement>("#multiModelToggle");
const multiModelPopoverEl = mustQuery<HTMLDivElement>("#multiModelPopover");
const modelModeHintEl = mustQuery<HTMLDivElement>("#modelModeHint");
const ragKnowledgeSelectorEl = mustQuery<HTMLSelectElement>("#ragKnowledgeSelector");
const skillMenuEl = mustQuery<HTMLDivElement>("#skillMenu");
const messageLocatorEl = mustQuery<HTMLElement>("#messageLocator");
const chatPaneEl = mustQuery<HTMLElement>(".chatPane");
const modalRootEl = mustQuery<HTMLDivElement>("#modalRoot");

const maxKnowledgeFileBytes = 256 * 1024 * 1024;

let conversations: Conversation[] = [];
let availableSkills: Skill[] = [];
let knowledgeBases: KnowledgeBase[] = [];
let knowledgeItems: KnowledgeItem[] = [];
let modelConfigs: ModelConfig[] = [];
let enabledSkillNames = new Set<string>();
let activeConversationId = "";
let activeModelConfigId = window.localStorage.getItem("activeModelConfigId") || "default";
let secondaryModelConfigIds = loadSecondaryModelConfigIds();
let activeKnowledgeBaseId = window.localStorage.getItem("activeKnowledgeBaseId") || "";
let activeRagKnowledgeBaseId = window.localStorage.getItem("activeRagKnowledgeBaseId") || "";
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
let multiModelPopoverOpen = false;
let modalCancelHandler: (() => void) | null = null;
let sidebarCollapsed = window.localStorage.getItem("sidebarCollapsed") === "true";

setSidebarCollapsed(sidebarCollapsed);

composerEl.addEventListener("submit", async (event) => {
  event.preventDefault();
  const message = inputEl.value.trim();
  if (!message || isStreaming) return;
  inputEl.value = "";
  hideSlashMenu();
  await sendMessage(message);
});

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

inputEl.addEventListener("blur", () => {
  window.setTimeout(hideSlashMenu, 120);
});

sidebarToggleEl.addEventListener("click", () => {
  setSidebarCollapsed(!sidebarCollapsed);
});

skillSearchEl.addEventListener("input", () => {
  renderSkills();
});

primaryModelSelectorEl.addEventListener("change", () => {
  activeModelConfigId = primaryModelSelectorEl.value || defaultReasoningModelConfigId();
  secondaryModelConfigIds = secondaryModelConfigIds.filter((id) => id !== activeModelConfigId);
  saveModelSelection();
  renderModelSelector();
});

multiModelToggleEl.addEventListener("click", (event) => {
  event.stopPropagation();
  if (isStreaming) return;
  multiModelPopoverOpen = !multiModelPopoverOpen;
  renderModelSelector();
});

document.addEventListener("click", (event) => {
  if (!multiModelPopoverOpen) return;
  const target = event.target;
  if (target instanceof Node && (multiModelPopoverEl.contains(target) || multiModelToggleEl.contains(target))) {
    return;
  }
  multiModelPopoverOpen = false;
  renderModelSelector();
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !modalRootEl.classList.contains("hidden")) {
    event.preventDefault();
    closeAppModal(true);
  }
});

ragKnowledgeSelectorEl.addEventListener("change", () => {
  activeRagKnowledgeBaseId = ragKnowledgeSelectorEl.value || "";
  if (activeRagKnowledgeBaseId) {
    window.localStorage.setItem("activeRagKnowledgeBaseId", activeRagKnowledgeBaseId);
  } else {
    window.localStorage.removeItem("activeRagKnowledgeBaseId");
  }
});

skillsNavEl.addEventListener("click", async () => {
  if (isStreaming) return;
  setView("skills");
  if (availableSkills.length === 0 && !skillLoadError) {
    await loadSkills().catch(showSkillError);
  } else {
    renderSkills();
  }
});

knowledgeNavEl.addEventListener("click", async () => {
  if (isStreaming) return;
  setView("knowledge");
  await loadKnowledge().catch(showKnowledgeError);
});

modelNavEl.addEventListener("click", async () => {
  if (isStreaming) return;
  setView("models");
  if (modelConfigs.length === 0 && !modelLoadError) {
    await loadModelConfigs().catch(showModelError);
  } else {
    renderModelConfigs();
  }
});

addKnowledgeEl.addEventListener("click", () => {
  if (isStreaming || isKnowledgeImporting || !activeKnowledgeBaseId) return;
  knowledgeFileInputEl.click();
});

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

newConversationEl.addEventListener("click", async () => {
  if (isStreaming) return;
  try {
    setView("chat");
    const response = await request<{ conversation: Conversation }>("/api/conversations", {
      method: "POST",
      body: JSON.stringify({ title: "New conversation" })
    });
    activeConversationId = response.conversation.id;
    await loadConversations();
    await loadMessages();
  } catch (error) {
    showPageError(error);
  }
});

void boot().catch(showPageError);

async function boot(): Promise<void> {
  await loadConversations();
  await loadModelConfigs().catch(showModelError);
  await loadKnowledgeBasesForSelector().catch(showKnowledgeError);
  await loadSkills().catch((error) => {
    showSkillError(error);
  });
  if (!activeConversationId) {
    activeConversationId = conversations[0]?.id ?? "";
  }
  await loadMessages();
}

async function loadConversations(): Promise<void> {
  const body = await request<ConversationsResponse>("/api/conversations");
  conversations = body.conversations ?? [];
  if (!activeConversationId) {
    activeConversationId = body.defaultConversationId || conversations[0]?.id || "";
  }
  const active = conversations.find((item) => item.id === activeConversationId);
  if (active && currentView === "chat") {
    conversationTitleEl.textContent = active.title || "Conversation";
  }
  renderConversations();
}

async function loadSkills(): Promise<void> {
  const body = await request<SkillsResponse>("/api/skills");
  availableSkills = body.skills ?? [];
  enabledSkillNames = new Set(availableSkills.filter((skill) => skill.enabled).map((skill) => skill.name));
  skillLoadError = "";
  renderSkills();
}

async function loadKnowledgeBasesForSelector(): Promise<void> {
  const body = await request<KnowledgeBasesResponse>("/api/knowledge-bases");
  knowledgeBases = body.knowledgeBases ?? [];
  normalizeRagKnowledgeSelection();
  knowledgeLoadError = "";
  renderRagKnowledgeSelector();
}

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

async function loadMessages(): Promise<void> {
  if (!activeConversationId) {
    messagesEl.innerHTML = `<div class="emptyState">No conversation selected.</div>`;
    conversationTitleEl.textContent = "Conversation";
    setEmptyChat(true);
    return;
  }
  const conversation = conversations.find((item) => item.id === activeConversationId);
  conversationTitleEl.textContent = conversation?.title || "Conversation";
  const body = await request<MessagesResponse>(`/api/conversations/${activeConversationId}/messages`);
  renderMessages(body.messages ?? []);
}

async function sendMessage(content: string): Promise<void> {
  lockExistingCanonicalSelection();
  addMessage({ role: "user", content });
  const streamState = selectedReasoningModelConfigIds().length > 1 ? addMultiAssistantStream(selectedReasoningModelConfigIds()) : addAssistantStream();
  isStreaming = true;
  sendButtonEl.disabled = true;
  inputEl.disabled = true;
  newConversationEl.disabled = true;
  skillsNavEl.disabled = true;
  knowledgeNavEl.disabled = true;
  modelNavEl.disabled = true;
  createKnowledgeBaseEl.disabled = true;
  addKnowledgeEl.disabled = true;
  addModelConfigEl.disabled = true;
  renderSkills();
  renderKnowledge();
  renderModelSelector();
  renderModelConfigs();
  hideSlashMenu();

  try {
    await ensureActiveConversation();
    await streamChat(content, streamState);
    await loadConversations();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    finishStreamWithAnswer(streamState, "请求失败：" + message);
  } finally {
    isStreaming = false;
    sendButtonEl.disabled = false;
    inputEl.disabled = false;
    newConversationEl.disabled = false;
    skillsNavEl.disabled = false;
    knowledgeNavEl.disabled = false;
    modelNavEl.disabled = false;
    createKnowledgeBaseEl.disabled = false;
    addKnowledgeEl.disabled = isKnowledgeImporting;
    addModelConfigEl.disabled = false;
    renderSkills();
    renderKnowledge();
    renderModelSelector();
    renderModelConfigs();
    inputEl.focus();
  }
}

async function ensureActiveConversation(): Promise<void> {
  if (activeConversationId) return;
  await loadConversations();
  if (activeConversationId) return;
  const response = await request<{ conversation: Conversation }>("/api/conversations", {
    method: "POST",
    body: JSON.stringify({ title: "New conversation" })
  });
  activeConversationId = response.conversation.id;
  await loadConversations();
}

async function streamChat(content: string, streamState: StreamRenderState): Promise<void> {
  const modelConfigIds = selectedReasoningModelConfigIds();
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
  conversationTitleEl.textContent = conversation?.title || "Conversation";
  renderConversations();
  renderMessageLocator();
}

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
    skillListEl.innerHTML = `<div class="emptyState compact">No skills.</div>`;
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
    description.textContent = skill.purpose || skill.description || "No purpose";
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
    retry.textContent = "Retry";
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

function renderRagKnowledgeSelector(): void {
  normalizeRagKnowledgeSelection();
  ragKnowledgeSelectorEl.innerHTML = "";
  ragKnowledgeSelectorEl.disabled = isStreaming;

  const empty = document.createElement("option");
  empty.value = "";
  empty.textContent = "不选择知识库";
  ragKnowledgeSelectorEl.appendChild(empty);

  for (const base of knowledgeBases) {
    const option = document.createElement("option");
    option.value = base.id;
    const hasContent = (base.childChunkCount ?? 0) > 0;
    const meta = hasContent ? `${base.documentCount || 0} docs` : "空";
    option.textContent = `${base.name || "Untitled"} · ${meta}`;
    ragKnowledgeSelectorEl.appendChild(option);
  }

  ragKnowledgeSelectorEl.value = activeRagKnowledgeBaseId;
}

function createActionMenu(label: string, items: ActionMenuItem[]): HTMLElement {
  const wrapper = document.createElement("div");
  wrapper.className = "actionMenu";
  wrapper.addEventListener("click", (event) => {
    event.stopPropagation();
  });

  const trigger = document.createElement("button");
  trigger.type = "button";
  trigger.className = "actionMenuButton";
  trigger.textContent = "...";
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

function renderModelSelector(): void {
  primaryModelSelectorEl.innerHTML = "";
  multiModelPopoverEl.innerHTML = "";
  primaryModelSelectorEl.disabled = isStreaming;
  multiModelToggleEl.disabled = isStreaming;
  const reasoningConfigs = modelConfigs.filter((config) => modelConfigType(config) === "reasoning");
  if (reasoningConfigs.length === 0) {
    const option = document.createElement("option");
    option.value = "default";
    option.textContent = "无推理模型";
    primaryModelSelectorEl.appendChild(option);
    activeModelConfigId = "default";
    secondaryModelConfigIds = [];
    primaryModelSelectorEl.disabled = true;
    multiModelToggleEl.disabled = true;
    multiModelToggleEl.textContent = "开启多模型回答";
    multiModelToggleEl.setAttribute("aria-expanded", "false");
    multiModelPopoverEl.classList.add("hidden");
    modelModeHintEl.textContent = "请先配置推理模型。";
    return;
  }
  normalizeModelSelection();
  for (const config of reasoningConfigs) {
    const primaryOption = document.createElement("option");
    primaryOption.value = config.id;
    primaryOption.textContent = modelConfigLabel(config);
    primaryModelSelectorEl.appendChild(primaryOption);
  }
  primaryModelSelectorEl.value = activeModelConfigId;
  renderMultiModelPopover(reasoningConfigs);
  const selectedModels = selectedReasoningModelConfigIds();
  multiModelToggleEl.textContent = selectedModels.length > 1 ? `多模型回答 · ${selectedModels.length} 个模型` : "开启多模型回答";
  multiModelToggleEl.classList.toggle("active", selectedModels.length > 1);
  multiModelToggleEl.setAttribute("aria-expanded", String(multiModelPopoverOpen));
  multiModelPopoverEl.classList.toggle("hidden", !multiModelPopoverOpen);
  modelModeHintEl.textContent = selectedModels.length > 1
    ? `多模型回答已开启：${modelDisplayName(activeModelConfigId)} + ${secondaryModelConfigIds.map(modelDisplayName).join(" + ")}。默认使用“模型选择”中的模型作为后续上下文。`
    : "默认只使用模型选择中的模型回答；点击“开启多模型回答”可选择最多 2 个副模型并列回答。";
}

function renderMultiModelPopover(reasoningConfigs: ModelConfig[]): void {
  const candidates = reasoningConfigs.filter((config) => config.id !== activeModelConfigId);
  if (candidates.length === 0) {
    const empty = document.createElement("div");
    empty.className = "multiModelEmpty";
    empty.textContent = "暂无可选副模型。请先在模型配置中新增推理模型。";
    multiModelPopoverEl.appendChild(empty);
    return;
  }

  const title = document.createElement("div");
  title.className = "multiModelPopoverTitle";
  title.textContent = "选择副模型（最多 2 个）";
  multiModelPopoverEl.appendChild(title);

  for (const config of candidates) {
    const checked = secondaryModelConfigIds.includes(config.id);
    const label = document.createElement("label");
    label.className = "multiModelOption";
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.value = config.id;
    checkbox.checked = checked;
    checkbox.disabled = isStreaming || (!checked && secondaryModelConfigIds.length >= 2);
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) {
        secondaryModelConfigIds = [...secondaryModelConfigIds, config.id].filter((id, index, ids) => ids.indexOf(id) === index).slice(0, 2);
      } else {
        secondaryModelConfigIds = secondaryModelConfigIds.filter((id) => id !== config.id);
      }
      saveModelSelection();
      renderModelSelector();
    });
    const text = document.createElement("span");
    text.textContent = modelConfigLabel(config);
    label.append(checkbox, text);
    multiModelPopoverEl.appendChild(label);
  }
}

function normalizeRagKnowledgeSelection(): void {
  if (!activeRagKnowledgeBaseId) return;
  if (knowledgeBases.some((base) => base.id === activeRagKnowledgeBaseId)) return;
  activeRagKnowledgeBaseId = "";
  window.localStorage.removeItem("activeRagKnowledgeBaseId");
}

function selectedRagKnowledgeBaseId(): string {
  const base = knowledgeBases.find((item) => item.id === activeRagKnowledgeBaseId);
  if (!base || (base.childChunkCount ?? 0) <= 0) return "";
  return base.id;
}

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

function selectedReasoningModelConfigIds(): string[] {
  normalizeModelSelection();
  return [activeModelConfigId, ...secondaryModelConfigIds].slice(0, 3);
}

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
    retry.textContent = "Retry";
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
  }
  const provider = createLabeledInput("Provider", config?.provider || "openai-compatible", "text");
  const apiKey = createLabeledInput("API Key", "", "password");
  apiKey.input.placeholder = config?.apiKeySet ? "留空则保留已保存的 key" : "请输入 API key";
  const baseURL = createLabeledInput("Base URL", config?.baseURL || "https://ark-cn-beijing.bytedance.net/api/v3", "text");
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

function createLabeledSelect(labelText: string, value: string, options: Array<{ value: string; label: string }>): { wrapper: HTMLElement; select: HTMLSelectElement } {
  const wrapper = document.createElement("label");
  wrapper.className = "modelField";
  const label = document.createElement("span");
  label.textContent = labelText;
  const select = document.createElement("select");
  select.disabled = isStreaming;
  for (const option of options) {
    const item = document.createElement("option");
    item.value = option.value;
    item.textContent = option.label;
    select.appendChild(item);
  }
  select.value = value;
  wrapper.append(label, select);
  return { wrapper, select };
}

function modelConfigType(config?: ModelConfig): "reasoning" | "embedding" {
  return config?.modelType === "embedding" ? "embedding" : "reasoning";
}

function defaultReasoningModelConfigId(): string {
  return modelConfigs.find((config) => modelConfigType(config) === "reasoning")?.id || "default";
}

function modelConfigLabel(config: ModelConfig): string {
  const typeLabel = modelConfigType(config) === "embedding" ? "向量" : "推理";
  return `${config.id} · ${typeLabel} · ${config.model || "model"}`;
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

function renderMessages(messages: Message[]): void {
  messagesEl.innerHTML = "";
  setEmptyChat(messages.length === 0);
  if (messages.length === 0) {
    messagesEl.innerHTML = `<div class="emptyState"><div class="emptyTitle">What can I help with?</div><div class="emptyHint">Start a conversation or use / to choose a skill.</div></div>`;
    renderMessageLocator();
    return;
  }
  for (let index = 0; index < messages.length; index += 1) {
    addMessage(messageWithLatestFlag(messages[index], index, messages), false);
  }
  renderMessageLocator();
  scrollMessages();
}

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

function lockExistingCanonicalSelection(): void {
  messagesEl.querySelectorAll<HTMLElement>(".multiModelCard.selectable").forEach((card) => {
    card.classList.remove("selectable");
  });
  messagesEl.querySelectorAll<HTMLElement>(".multiModelTip").forEach((tip) => {
    tip.textContent = "本轮已进入历史，只保留用于上下文的回答。";
  });
}

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

function updateLatestAssistantMessage(message: Message): void {
  const existing = Array.from(messagesEl.querySelectorAll<HTMLElement>(".message")).find((item) => item.dataset.messageId === (message.id || ""));
  if (!existing) return;
  const next = buildMultiModelMessageElement({ ...message, metadata: { ...(message.metadata || {}), latestSelectable: true } });
  existing.replaceWith(next);
  scrollMessages();
}

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

function shouldSuppressLiveAnswer(markdown: string): boolean {
  const trimmed = markdown.trimStart().toLowerCase();
  if (!trimmed) return true;
  return "<load_skill".startsWith(trimmed) || trimmed.startsWith("<load_skill");
}

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

function eventPayload(event: AgentStreamEvent): string {
  if (event.toolCalls?.length) return compactJSONString(event.toolCalls);
  if (event.toolResults?.length) return compactJSONString(event.toolResults);
  if (event.toolCall) return compactJSONString(event.toolCall);
  if (event.toolResult) return compactJSONString(event.toolResult);
  return "";
}

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

function attachAnswerSourceButton(container: HTMLElement, getContent: () => string, title: string): void {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "answerSourceButton";
  button.textContent = "<>";
  button.setAttribute("aria-label", "查看原始回答内容");
  button.setAttribute("title", "查看回答内容");
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
    description: "Markdown 内容",
    body: preview,
    size: "wide",
    actions: [
      { label: "关闭", variant: "secondary", onClick: () => closeAppModal(false) }
    ]
  });
}

function openAppModal(options: {
  title: string;
  description?: string;
  body?: HTMLElement;
  actions?: ModalAction[];
  onCancel?: () => void;
  size?: "default" | "wide";
}): void {
  closeAppModal(false);
  modalCancelHandler = options.onCancel || null;
  modalRootEl.innerHTML = "";
  modalRootEl.className = `modalRoot${options.size === "wide" ? " wide" : ""}`;
  document.body.classList.add("modalOpen");

  const backdrop = document.createElement("div");
  backdrop.className = "modalBackdrop";
  backdrop.addEventListener("click", () => closeAppModal(true));

  const dialog = document.createElement("section");
  dialog.className = "modalDialog";
  dialog.setAttribute("role", "dialog");
  dialog.setAttribute("aria-modal", "true");
  dialog.setAttribute("aria-label", options.title);

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

  if (options.actions?.length) {
    const footer = document.createElement("div");
    footer.className = "modalFooter";
    for (const action of options.actions) {
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
    dialog.querySelector<HTMLElement>("input, select, textarea, button")?.focus();
  }, 0);
}

function closeAppModal(notifyCancel = true): void {
  const cancel = modalCancelHandler;
  modalCancelHandler = null;
  modalRootEl.classList.add("hidden");
  modalRootEl.className = "modalRoot hidden";
  modalRootEl.innerHTML = "";
  document.body.classList.remove("modalOpen");
  if (notifyCancel) cancel?.();
}

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

function showPageError(error: unknown): void {
  const message = error instanceof Error ? error.message : String(error);
  setEmptyChat(true);
  messagesEl.innerHTML = "";
  const errorEl = document.createElement("div");
  errorEl.className = "emptyState errorState";
  errorEl.textContent = "页面初始化失败：" + message;
  messagesEl.appendChild(errorEl);
}

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

function mustQuery<T extends Element>(selector: string): T {
  const element = document.querySelector<T>(selector);
  if (!element) throw new Error(`Missing element: ${selector}`);
  return element;
}
