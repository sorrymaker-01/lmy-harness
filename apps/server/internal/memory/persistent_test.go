package memory

import (
	"testing"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

func TestPersistentStoreRestoresConversationMessagesAndMemory(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPersistentStore(dir)
	if err != nil {
		t.Fatalf("NewPersistentStore returned error: %v", err)
	}
	conversation := store.CreateConversation("New conversation")
	user := contracts.Message{
		ID:             shared.NewID("msg"),
		ConversationID: conversation.ID,
		Role:           contracts.RoleUser,
		Content:        "First question",
		CreatedAt:      shared.Now(),
	}
	store.AddMessage(user)
	store.UpdateShortMemory(conversation.ID, "First question", "First answer", nil)

	restored, err := NewPersistentStore(dir)
	if err != nil {
		t.Fatalf("restore returned error: %v", err)
	}
	conversations := restored.ListConversations()
	if len(conversations) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(conversations))
	}
	if conversations[0].Title != "First question" {
		t.Fatalf("conversation title should come from first user query, got %q", conversations[0].Title)
	}
	messages := restored.Messages(conversation.ID)
	if len(messages) != 1 || messages[0].Content != "First question" {
		t.Fatalf("unexpected restored messages: %#v", messages)
	}
	memory := restored.GetShortMemory(conversation.ID)
	if memory.Summary == "" || memory.Summary == contracts.DefaultShortMemorySummary || memory.Summary == contracts.LegacyDefaultShortMemorySummary {
		t.Fatalf("short memory was not restored: %#v", memory)
	}
}

func TestPersistentStoreDeleteConversation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPersistentStore(dir)
	if err != nil {
		t.Fatalf("NewPersistentStore returned error: %v", err)
	}
	conversation := store.CreateConversation("Delete me")
	message := contracts.Message{
		ID:             shared.NewID("msg"),
		ConversationID: conversation.ID,
		Role:           contracts.RoleUser,
		Content:        "remove this conversation",
		CreatedAt:      shared.Now(),
	}
	store.AddMessage(message)
	store.UpdateShortMemory(conversation.ID, message.Content, "answer", nil)

	if !store.DeleteConversation(conversation.ID) {
		t.Fatalf("DeleteConversation returned false")
	}
	if store.DeleteConversation(conversation.ID) {
		t.Fatalf("second DeleteConversation returned true")
	}
	if messages := store.Messages(conversation.ID); len(messages) != 0 {
		t.Fatalf("messages should be deleted from memory, got %#v", messages)
	}

	restored, err := NewPersistentStore(dir)
	if err != nil {
		t.Fatalf("restore returned error: %v", err)
	}
	if conversations := restored.ListConversations(); len(conversations) != 0 {
		t.Fatalf("deleted conversation was restored: %#v", conversations)
	}
	if messages := restored.Messages(conversation.ID); len(messages) != 0 {
		t.Fatalf("deleted messages were restored: %#v", messages)
	}
	if memory := restored.GetShortMemory(conversation.ID); memory.Summary != contracts.DefaultShortMemorySummary {
		t.Fatalf("deleted memory was restored: %#v", memory)
	}
}

func TestPersistentStoreUpdatesMessage(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPersistentStore(dir)
	if err != nil {
		t.Fatalf("NewPersistentStore returned error: %v", err)
	}
	conversation := store.CreateConversation("Update message")
	message := contracts.Message{
		ID:             shared.NewID("msg"),
		ConversationID: conversation.ID,
		Role:           contracts.RoleAssistant,
		Content:        "first answer",
		CreatedAt:      shared.Now(),
		Metadata:       map[string]any{"canonicalResponseId": "resp-a"},
	}
	store.AddMessage(message)
	message.Content = "second answer"
	message.Metadata = map[string]any{"canonicalResponseId": "resp-b"}
	if !store.UpdateMessage(message) {
		t.Fatal("UpdateMessage returned false")
	}

	restored, err := NewPersistentStore(dir)
	if err != nil {
		t.Fatalf("restore returned error: %v", err)
	}
	messages := restored.Messages(conversation.ID)
	if len(messages) != 1 || messages[0].Content != "second answer" {
		t.Fatalf("unexpected restored messages: %#v", messages)
	}
	if messages[0].Metadata["canonicalResponseId"] != "resp-b" {
		t.Fatalf("metadata was not updated: %#v", messages[0].Metadata)
	}
}
