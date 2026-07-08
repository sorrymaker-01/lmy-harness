package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/claudecode"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/memory"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/model"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/skills"
)

func TestParseModelSkillRequestXML(t *testing.T) {
	request, ok := parseModelSkillRequest(`<load_skill name="earnings-analysis">Need the full workflow.</load_skill>`)
	if !ok {
		t.Fatal("expected request to parse")
	}
	if request.Name != "earnings-analysis" {
		t.Fatalf("unexpected skill name: %q", request.Name)
	}
	if request.Reason != "Need the full workflow." {
		t.Fatalf("unexpected reason: %q", request.Reason)
	}
}

func TestParseModelSkillRequestLine(t *testing.T) {
	request, ok := parseModelSkillRequest("LOAD_SKILL: /custom-review need repository rules")
	if !ok {
		t.Fatal("expected request to parse")
	}
	if request.Name != "custom-review" {
		t.Fatalf("unexpected skill name: %q", request.Name)
	}
	if request.Reason != "need repository rules" {
		t.Fatalf("unexpected reason: %q", request.Reason)
	}
}

type skillRecoveryModel struct {
	inputs []model.Input
}

func (m *skillRecoveryModel) Chat(ctx context.Context, input model.Input) (contracts.ModelResponse, error) {
	m.inputs = append(m.inputs, input)
	content := "最终直接回答"
	if len(m.inputs) == 1 {
		content = `<load_skill name="missing-skill">需要更多上下文</load_skill>`
	}
	return contracts.ModelResponse{
		Content: content,
		Message: contracts.LLMMessage{
			Role:    contracts.RoleAssistant,
			Content: content,
		},
	}, nil
}

func TestAgentRecoversWhenModelRequestsUnavailableSkill(t *testing.T) {
	capture := &skillRecoveryModel{}
	agent := NewAgent(memory.NewInMemoryStore(), runtime.NewRuntime(), capture, skills.NewRegistry(), skills.NewConfigStore(skills.NewRegistry()), claudecode.StartupContext{})
	output, err := agent.Run(context.Background(), RunInput{
		ConversationID: "conv-skill-recovery",
		UserMessage:    "你好",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output.AssistantMessage.Content != "最终直接回答" {
		t.Fatalf("assistant content = %q, want recovered answer", output.AssistantMessage.Content)
	}
	if len(capture.inputs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(capture.inputs))
	}
	if len(capture.inputs[1].Tools) != 0 {
		t.Fatalf("recovery call tools = %d, want 0", len(capture.inputs[1].Tools))
	}
	if !strings.Contains(capture.inputs[1].System, "最终回答恢复阶段") {
		t.Fatalf("recovery system prompt missing recovery instruction: %q", capture.inputs[1].System)
	}
}
