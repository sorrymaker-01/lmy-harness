let conversationId;

const messagesEl = document.querySelector("#messages");
const toolsEl = document.querySelector("#tools");
const traceEl = document.querySelector("#trace");
const form = document.querySelector("#chatForm");
const input = document.querySelector("#messageInput");
const newConversation = document.querySelector("#newConversation");

await boot();

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const message = input.value.trim();
  if (!message) return;
  input.value = "";
  await sendMessage(message);
});

newConversation.addEventListener("click", async () => {
  const res = await fetch("/api/conversations", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ title: "New conversation" })
  });
  const body = await res.json();
  conversationId = body.conversation.id;
  await renderMessages();
  traceEl.textContent = "";
});

async function boot() {
  const res = await fetch("/api/conversations");
  const body = await res.json();
  conversationId = body.defaultConversationId || body.conversations[0]?.id;
  await Promise.all([renderMessages(), renderTools(), renderTrace()]);
}

async function sendMessage(message) {
  addMessage("user", message);
  const res = await fetch(`/api/conversations/${conversationId}/chat`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ message })
  });
  const body = await res.json();
  if (!res.ok) {
    addMessage("assistant", body.error || "Request failed.");
    return;
  }
  addMessage("assistant", body.assistantMessage.content);
  await renderTrace();
}

async function renderMessages() {
  const res = await fetch(`/api/conversations/${conversationId}/messages`);
  const body = await res.json();
  messagesEl.innerHTML = "";
  for (const message of body.messages) {
    addMessage(message.role, message.content);
  }
}

async function renderTools() {
  const res = await fetch("/api/tools");
  const body = await res.json();
  toolsEl.innerHTML = "";
  for (const tool of body.tools) {
    const div = document.createElement("div");
    div.className = "tool";
    div.innerHTML = `<strong>${tool.name}</strong><small>${tool.source} · ${tool.risk}</small><small>${tool.description}</small>`;
    toolsEl.appendChild(div);
  }
}

async function renderTrace() {
  const res = await fetch(`/api/conversations/${conversationId}/traces`);
  const body = await res.json();
  traceEl.textContent = JSON.stringify(body.traces?.at(-1) ?? {}, null, 2);
}

function addMessage(role, content) {
  const div = document.createElement("div");
  div.className = `message ${role}`;
  div.textContent = `${role}: ${content}`;
  messagesEl.appendChild(div);
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

