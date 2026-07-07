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
  traceList: HTMLElement;
  answerPanel: HTMLElement;
  answerContent: HTMLElement;
  answerMarkdown: string;
  loadingEl: HTMLElement;
  thinkingDetails: HTMLDetailsElement;
  hasFinalAnswer: boolean;
  hasTraceEvents: boolean;
};

const conversationListEl = mustQuery<HTMLDivElement>("#conversationList");
const conversationTitleEl = mustQuery<HTMLHeadingElement>("#conversationTitle");
const messagesEl = mustQuery<HTMLDivElement>("#messages");
const composerEl = mustQuery<HTMLFormElement>("#composer");
const inputEl = mustQuery<HTMLTextAreaElement>("#messageInput");
const sendButtonEl = mustQuery<HTMLButtonElement>("#sendButton");
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
const modelSelectorEl = mustQuery<HTMLSelectElement>("#modelSelector");
const ragKnowledgeSelectorEl = mustQuery<HTMLSelectElement>("#ragKnowledgeSelector");
const skillMenuEl = mustQuery<HTMLDivElement>("#skillMenu");
const chatPaneEl = mustQuery<HTMLElement>(".chatPane");

const maxKnowledgeFileBytes = 256 * 1024 * 1024;

let conversations: Conversation[] = [];
let availableSkills: Skill[] = [];
let knowledgeBases: KnowledgeBase[] = [];
let knowledgeItems: KnowledgeItem[] = [];
let modelConfigs: ModelConfig[] = [];
let enabledSkillNames = new Set<string>();
let activeConversationId = "";
let activeModelConfigId = window.localStorage.getItem("activeModelConfigId") || "default";
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

skillSearchEl.addEventListener("input", () => {
  renderSkills();
});

modelSelectorEl.addEventListener("change", () => {
  activeModelConfigId = modelSelectorEl.value || "default";
  window.localStorage.setItem("activeModelConfigId", activeModelConfigId);
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
  const name = window.prompt("知识库名称");
  if (!name || !name.trim()) return;
  try {
    await createKnowledgeBase(name.trim());
  } catch (error) {
    showKnowledgeError(error);
  }
});

addModelConfigEl.addEventListener("click", () => {
  if (isStreaming) return;
  editingModelConfigId = "__new__";
  renderModelConfigs();
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
    window.localStorage.setItem("activeModelConfigId", activeModelConfigId);
  }
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
    window.localStorage.setItem("activeModelConfigId", activeModelConfigId);
  } else if (!modelConfigs.some((config) => config.id === activeModelConfigId && modelConfigType(config) === "reasoning")) {
    activeModelConfigId = defaultReasoningModelConfigId();
    window.localStorage.setItem("activeModelConfigId", activeModelConfigId);
  }
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
    window.localStorage.setItem("activeModelConfigId", activeModelConfigId);
  }
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
  addMessage({ role: "user", content });
  const streamState = addAssistantStream();
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
  const response = await fetch(`/api/conversations/${activeConversationId}/chat/stream`, {
    method: "POST",
    headers: {
      "accept": "text/event-stream",
      "content-type": "application/json"
    },
    body: JSON.stringify({ message: content, modelConfigId: activeModelConfigId, knowledgeBaseId: selectedRagKnowledgeBaseId() })
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
    streamState.loadingEl.remove();
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
      if (!window.confirm("删除这个会话？")) return;
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
    renderSkills();
    return;
  }
  if (isKnowledge) {
    setEmptyChat(false);
    conversationTitleEl.textContent = "知识库";
    renderKnowledge();
    return;
  }
  if (isModels) {
    setEmptyChat(false);
    conversationTitleEl.textContent = "模型配置";
    renderModelConfigs();
    return;
  }
  const conversation = conversations.find((item) => item.id === activeConversationId);
  conversationTitleEl.textContent = conversation?.title || "Conversation";
  renderConversations();
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

    const summary = document.createElement("div");
    summary.className = "skillRowSummary";
    summary.addEventListener("click", async () => {
      if (expandedSkillName === skill.name) {
        expandedSkillName = null;
        renderSkills();
        return;
      }
      try {
        await ensureSkillDetail(skill.name);
        expandedSkillName = skill.name;
        renderSkills();
      } catch (error) {
        showSkillError(error);
      }
    });

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
      if (!window.confirm(`删除 skill /${skill.name}？`)) return;
      await deleteSkill(skill.name);
    });

    actions.append(switchLabel, remove);
    summary.append(avatar, info, actions);
    card.append(summary);
    if (expandedSkillName === skill.name) {
      card.append(renderSkillDetail(skill));
    }
    skillListEl.appendChild(card);
  }
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
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "knowledgeBaseDeleteButton";
      remove.textContent = "×";
      remove.setAttribute("aria-label", "删除知识库");
      remove.disabled = isStreaming || isKnowledgeImporting;
      remove.addEventListener("click", async (event) => {
        event.stopPropagation();
        if (!window.confirm(`删除知识库 ${base.name}？`)) return;
        try {
          await deleteKnowledgeBase(base.id);
        } catch (error) {
          showKnowledgeError(error);
        }
      });
      row.appendChild(remove);
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

    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "knowledgeDeleteButton";
    remove.textContent = "删除";
    remove.disabled = isStreaming || isKnowledgeImporting;
    remove.addEventListener("click", async () => {
      if (!window.confirm(`删除知识文件 ${item.name}？`)) return;
      try {
        await deleteKnowledgeItem(item.id);
      } catch (error) {
        showKnowledgeError(error);
      }
    });

    card.append(icon, info, remove);
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

function renderModelSelector(): void {
  modelSelectorEl.innerHTML = "";
  modelSelectorEl.disabled = isStreaming;
  const reasoningConfigs = modelConfigs.filter((config) => modelConfigType(config) === "reasoning");
  if (reasoningConfigs.length === 0) {
    const option = document.createElement("option");
    option.value = "default";
    option.textContent = "无推理模型";
    modelSelectorEl.appendChild(option);
    activeModelConfigId = "default";
    modelSelectorEl.disabled = true;
    return;
  }
  for (const config of reasoningConfigs) {
    const option = document.createElement("option");
    option.value = config.id;
    option.textContent = modelConfigLabel(config);
    modelSelectorEl.appendChild(option);
  }
  if (!reasoningConfigs.some((config) => config.id === activeModelConfigId)) {
    activeModelConfigId = defaultReasoningModelConfigId();
    window.localStorage.setItem("activeModelConfigId", activeModelConfigId);
  }
  modelSelectorEl.value = activeModelConfigId;
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
    if (editingModelConfigId === config.id) {
      modelConfigListEl.appendChild(renderModelConfigEditor(config));
      continue;
    }
    const type = modelConfigType(config);
    const card = document.createElement("article");
    card.className = `modelConfigItem${type === "reasoning" && config.id === activeModelConfigId ? " active" : ""}`;
    card.addEventListener("click", () => {
      expandedModelConfigId = expandedModelConfigId === config.id ? null : config.id;
      renderModelConfigs();
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
      window.localStorage.setItem("activeModelConfigId", activeModelConfigId);
      renderModelSelector();
      renderModelConfigs();
    });
    const edit = document.createElement("button");
    edit.type = "button";
    edit.textContent = "编辑";
    edit.disabled = isStreaming;
    edit.addEventListener("click", () => {
      editingModelConfigId = config.id;
      renderModelConfigs();
    });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.textContent = "删除";
    remove.disabled = isStreaming || config.id === "default";
    remove.addEventListener("click", async () => {
      if (!window.confirm(`删除模型配置 ${config.id}？`)) return;
      try {
        await deleteModelConfig(config.id);
      } catch (error) {
        showModelError(error);
      }
    });
    actions.append(select, edit, remove);
    card.append(info, actions);
    if (expandedModelConfigId === config.id) {
      card.appendChild(renderModelConfigDetail(config));
    }
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

function renderModelConfigEditor(config: ModelConfig | null): HTMLElement {
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
    } catch (error) {
      showModelError(error);
    }
  });
  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.textContent = "取消";
  cancel.disabled = isStreaming;
  cancel.addEventListener("click", () => {
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
    return;
  }
  for (const message of messages) {
    addMessage(message, false);
  }
  scrollMessages();
}

function addMessage(input: Message, shouldScroll = true): HTMLElement {
  setEmptyChat(false);
  const wrapper = document.createElement("article");
  wrapper.className = `message ${input.role}`;

  const content = document.createElement("div");
  content.className = "messageContent";
  renderMarkdown(content, input.content);
  wrapper.append(content);

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
  answerPanel.className = "answerPanel hidden";
  const answerContent = document.createElement("div");
  answerContent.className = "answerContent";
  answerPanel.append(answerContent);

  wrapper.append(thinkingDetails, answerPanel);
  appendMessageElement(wrapper, true);
  return { wrapper, traceList, answerPanel, answerContent, answerMarkdown: "", loadingEl, thinkingDetails, hasFinalAnswer: false, hasTraceEvents: false };
}

function appendTraceEvent(streamState: StreamRenderState, event: AgentStreamEvent): void {
  streamState.hasTraceEvents = true;
  const item = document.createElement("section");
  item.className = `traceEvent ${event.type}`;

  const body = document.createElement("pre");
  body.className = "traceBody";
  body.textContent = event.content || eventPayload(event);

  item.append(body);
  streamState.traceList.appendChild(item);
  scrollMessages();
}

function appendAnswerDelta(streamState: StreamRenderState, delta: string): void {
  if (!delta || streamState.hasFinalAnswer) return;
  streamState.answerMarkdown += delta;
  if (shouldSuppressLiveAnswer(streamState.answerMarkdown)) {
    return;
  }
  renderMarkdown(streamState.answerContent, streamState.answerMarkdown);
  streamState.answerPanel.classList.remove("hidden");
  scrollMessages();
}

function resetLiveAnswer(streamState: StreamRenderState): void {
  if (streamState.hasFinalAnswer) return;
  streamState.answerMarkdown = "";
  streamState.answerContent.innerHTML = "";
  streamState.answerPanel.classList.add("hidden");
}

function shouldSuppressLiveAnswer(markdown: string): boolean {
  const trimmed = markdown.trimStart().toLowerCase();
  if (!trimmed) return true;
  return "<load_skill".startsWith(trimmed) || trimmed.startsWith("<load_skill");
}

function finishStreamWithAnswer(streamState: StreamRenderState, content: string): void {
  streamState.hasFinalAnswer = true;
  streamState.answerMarkdown = content || "(empty response)";
  renderMarkdown(streamState.answerContent, streamState.answerMarkdown);
  streamState.answerPanel.classList.remove("hidden");
  streamState.loadingEl.remove();
  if (streamState.hasTraceEvents) {
    streamState.thinkingDetails.open = false;
  } else {
    streamState.thinkingDetails.remove();
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
  if (shouldScroll) scrollMessages();
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
