import { ConversationStore } from "@agent/agent-runtime";
import { AgentTrace, Conversation, createId, Message, nowIso } from "@agent/shared";

export class InMemoryStore implements ConversationStore {
  private readonly conversations = new Map<string, Conversation>();
  private readonly messages = new Map<string, Message[]>();
  private readonly traces = new Map<string, AgentTrace[]>();

  async createConversation(title = "New conversation"): Promise<Conversation> {
    const conversation: Conversation = {
      id: createId("conv"),
      title,
      createdAt: nowIso(),
      updatedAt: nowIso()
    };
    this.conversations.set(conversation.id, conversation);
    this.messages.set(conversation.id, []);
    this.traces.set(conversation.id, []);
    return conversation;
  }

  async listConversations(): Promise<Conversation[]> {
    return [...this.conversations.values()].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
  }

  async getMessages(conversationId: string): Promise<Message[]> {
    return this.messages.get(conversationId) ?? [];
  }

  async getTraces(conversationId: string): Promise<AgentTrace[]> {
    return this.traces.get(conversationId) ?? [];
  }

  async addMessage(message: Message): Promise<void> {
    if (!this.conversations.has(message.conversationId)) {
      await this.createConversation("Imported conversation");
    }
    const messages = this.messages.get(message.conversationId) ?? [];
    messages.push(message);
    this.messages.set(message.conversationId, messages);
    const conversation = this.conversations.get(message.conversationId);
    if (conversation) {
      conversation.updatedAt = nowIso();
      if (conversation.title === "New conversation" && message.role === "user") {
        conversation.title = message.content.slice(0, 48) || conversation.title;
      }
    }
  }

  async getRecentMessages(conversationId: string, limit: number): Promise<Message[]> {
    return (this.messages.get(conversationId) ?? []).slice(-limit);
  }

  async addTrace(trace: AgentTrace): Promise<void> {
    const traces = this.traces.get(trace.conversationId) ?? [];
    traces.push(trace);
    this.traces.set(trace.conversationId, traces);
  }

  async updateTrace(trace: AgentTrace): Promise<void> {
    const traces = this.traces.get(trace.conversationId) ?? [];
    const index = traces.findIndex((candidate) => candidate.id === trace.id);
    if (index >= 0) traces[index] = trace;
    else traces.push(trace);
    this.traces.set(trace.conversationId, traces);
  }
}

