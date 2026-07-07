package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"code.byted.org/ai/lmy/apps/server/internal/claudecode"
	"code.byted.org/ai/lmy/apps/server/internal/contracts"
	statedb "code.byted.org/ai/lmy/apps/server/internal/infra/db"
	"code.byted.org/ai/lmy/apps/server/internal/knowledge"
	"code.byted.org/ai/lmy/apps/server/internal/memory"
	"code.byted.org/ai/lmy/apps/server/internal/model"
	"code.byted.org/ai/lmy/apps/server/internal/runtime"
	"code.byted.org/ai/lmy/apps/server/internal/skills"
)

type captureModel struct {
	inputs []model.Input
}

func (m *captureModel) Chat(ctx context.Context, input model.Input) (contracts.ModelResponse, error) {
	m.inputs = append(m.inputs, input)
	content := "ok"
	return contracts.ModelResponse{
		Content: content,
		Message: contracts.LLMMessage{
			Role:    contracts.RoleAssistant,
			Content: content,
		},
	}, nil
}

func TestAgentInjectsKnowledgeOnlyWhenKnowledgeBaseSelected(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database, err := statedb.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer database.Close()
	knowledgeStore, err := knowledge.NewStoreWithDB(filepath.Join(root, "knowledge"), database)
	if err != nil {
		t.Fatalf("NewStoreWithDB() error = %v", err)
	}
	base, err := knowledgeStore.CreateKnowledgeBase(ctx, "Reference", "")
	if err != nil {
		t.Fatalf("CreateKnowledgeBase() error = %v", err)
	}
	if _, err := knowledgeStore.ImportToKnowledgeBase(ctx, base.ID, "reference.md", "text/markdown", strings.NewReader("# Reference\n\nneedle recall context should be injected only when selected.")); err != nil {
		t.Fatalf("ImportToKnowledgeBase() error = %v", err)
	}

	registry := skills.NewRegistry()
	capture := &captureModel{}
	agent := NewAgent(memory.NewInMemoryStore(), runtime.NewRuntime(), capture, registry, skills.NewConfigStore(registry), claudecode.StartupContext{})
	agent.SetKnowledgeStore(knowledgeStore)

	if _, err := agent.Run(ctx, RunInput{
		ConversationID: "conv-without-rag",
		UserMessage:    "needle recall context",
	}); err != nil {
		t.Fatalf("Run(without knowledge base) error = %v", err)
	}
	if len(capture.inputs) != 1 {
		t.Fatalf("model calls without knowledge base = %d, want 1", len(capture.inputs))
	}
	if modelInputContains(capture.inputs[0], "[Retrieved knowledge context]") {
		t.Fatal("model input included knowledge context without selected knowledge base")
	}

	capture.inputs = nil
	if _, err := agent.Run(ctx, RunInput{
		ConversationID:  "conv-with-rag",
		UserMessage:     "needle recall context",
		KnowledgeBaseID: base.ID,
	}); err != nil {
		t.Fatalf("Run(with knowledge base) error = %v", err)
	}
	if len(capture.inputs) != 1 {
		t.Fatalf("model calls with knowledge base = %d, want 1", len(capture.inputs))
	}
	if !modelInputContains(capture.inputs[0], "[Retrieved knowledge context]") {
		t.Fatal("model input did not include selected knowledge base context")
	}
}

func modelInputContains(input model.Input, fragment string) bool {
	for _, message := range input.Messages {
		if strings.Contains(message.Content, fragment) {
			return true
		}
	}
	return false
}
