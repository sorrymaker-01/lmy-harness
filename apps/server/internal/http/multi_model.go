package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/agent"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/knowledge"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/model"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/state"
)

const maxChatModels = 3

type chatRequestBody struct {
	Message              string   `json:"message"`
	ModelConfigID        string   `json:"modelConfigId"`
	ModelConfigIDs       []string `json:"modelConfigIds"`
	PrimaryModelConfigID string   `json:"primaryModelConfigId"`
	KnowledgeBaseID      string   `json:"knowledgeBaseId"`
}

type multiModelRunResult struct {
	response state.ModelResponseRecord
	output   agent.RunOutput
	err      error
}

func (s *HTTPServer) normalizeChatModelSelection(body chatRequestBody) ([]string, string, error) {
	ids := []string{}
	seen := map[string]struct{}{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, id := range body.ModelConfigIDs {
		add(id)
	}
	add(body.ModelConfigID)
	if len(ids) == 0 {
		add("default")
	}
	if len(ids) > maxChatModels {
		return nil, "", fmt.Errorf("at most %d models can answer one request", maxChatModels)
	}
	primary := strings.TrimSpace(body.PrimaryModelConfigID)
	if primary == "" {
		primary = strings.TrimSpace(body.ModelConfigID)
	}
	if primary == "" {
		primary = ids[0]
	}
	foundPrimary := false
	for _, id := range ids {
		if id == primary {
			foundPrimary = true
			break
		}
	}
	if !foundPrimary {
		return nil, "", fmt.Errorf("primary model config %q must be one of selected models", primary)
	}
	return ids, primary, nil
}

func (s *HTTPServer) runMultiModelChatStream(ctx context.Context, writer *sse.Writer, conversationID string, body chatRequestBody, modelIDs []string, primaryModelID string) error {
	if s.stateStore == nil {
		return errors.New("state store is required for multi-model chat")
	}
	clients := map[string]model.Client{}
	for _, id := range modelIDs {
		client, err := s.modelClientForRequest(id)
		if err != nil {
			return err
		}
		clients[id] = client
	}

	recent := s.store.RecentMessages(conversationID, 12)
	userMessage := contracts.Message{
		ID:             shared.NewID("msg"),
		ConversationID: conversationID,
		Role:           contracts.RoleUser,
		Content:        body.Message,
		CreatedAt:      shared.Now(),
	}
	s.store.AddMessage(userMessage)
	if err := writeSSEEvent(writer, contracts.AgentStreamEvent{
		Type:    "user_message",
		Title:   "用户输入",
		Content: body.Message,
		Message: &userMessage,
	}); err != nil {
		return err
	}

	turnID := shared.NewID("chatturn")
	assistantMessageID := shared.NewID("msg")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	turn := state.ChatTurnRecord{
		ID:                   turnID,
		ConversationID:       conversationID,
		UserMessageID:        userMessage.ID,
		AssistantMessageID:   assistantMessageID,
		Mode:                 "multi",
		PrimaryModelConfigID: primaryModelID,
		Status:               "running",
		Metadata: map[string]any{
			"modelConfigIds": modelIDs,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.stateStore.SaveChatTurn(turn); err != nil {
		return err
	}
	if err := writeSSEEvent(writer, contracts.AgentStreamEvent{
		Type:          "multi_model_start",
		Title:         "多模型回答",
		Content:       mustHTTPJSON(map[string]any{"modelConfigIds": modelIDs, "primaryModelConfigId": primaryModelID}),
		TurnID:        turnID,
		ModelConfigID: primaryModelID,
		Primary:       true,
	}); err != nil {
		return err
	}

	retrievalResult, err := s.retrievalForChat(ctx, conversationID, body.Message, body.KnowledgeBaseID)
	if err != nil {
		_ = writeSSEEvent(writer, contracts.AgentStreamEvent{
			Type:    "knowledge_retrieval_error",
			Title:   "知识库召回失败",
			Content: err.Error(),
			TurnID:  turnID,
		})
	}

	eventCh := make(chan contracts.AgentStreamEvent, 128)
	resultCh := make(chan multiModelRunResult, len(modelIDs))
	var wg sync.WaitGroup
	for _, modelID := range modelIDs {
		modelID := modelID
		response := state.ModelResponseRecord{
			ID:              shared.NewID("resp"),
			TurnID:          turnID,
			ConversationID:  conversationID,
			ModelConfigID:   modelID,
			Status:          "running",
			PrimaryResponse: modelID == primaryModelID,
			Metadata:        map[string]any{},
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := s.stateStore.SaveModelResponse(response); err != nil {
			return err
		}
		if err := writeSSEEvent(writer, contracts.AgentStreamEvent{
			Type:          "model_response_start",
			Title:         modelID,
			TurnID:        turnID,
			ResponseID:    response.ID,
			ModelConfigID: modelID,
			Primary:       modelID == primaryModelID,
			Canonical:     modelID == primaryModelID,
		}); err != nil {
			return err
		}
		wg.Add(1)
		go func(response state.ModelResponseRecord) {
			defer wg.Done()
			output, err := s.agent.RunStream(ctx, agent.RunInput{
				ConversationID:            conversationID,
				UserMessage:               body.Message,
				UserMessageID:             userMessage.ID,
				Model:                     clients[modelID],
				ModelConfigID:             modelID,
				RetrievalResult:           retrievalResult,
				RecentMessages:            recent,
				UseRecentMessages:         true,
				AssistantMetadata:         map[string]any{"modelConfigId": modelID, "turnId": turnID, "responseId": response.ID},
				SkipUserMessageStore:      true,
				SuppressUserMessageEvent:  true,
				SkipAssistantMessageStore: true,
				SkipShortMemoryUpdate:     true,
				SuppressKnowledgeEvent:    true,
			}, func(event contracts.AgentStreamEvent) error {
				event.TurnID = turnID
				event.ResponseID = response.ID
				event.ModelConfigID = modelID
				event.Primary = modelID == primaryModelID
				event.Canonical = modelID == primaryModelID
				eventCh <- event
				return nil
			})
			resultCh <- multiModelRunResult{response: response, output: output, err: err}
		}(response)
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	results := []multiModelRunResult{}
	for resultCh != nil || len(eventCh) > 0 {
		select {
		case event := <-eventCh:
			if event.Type != "" {
				if err := writeSSEEvent(writer, event); err != nil {
					return err
				}
			}
		case result, ok := <-resultCh:
			if !ok {
				resultCh = nil
				continue
			}
			result.response.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if result.err != nil {
				result.response.Status = "failed"
				result.response.Error = result.err.Error()
			} else {
				result.response.Status = "completed"
				result.response.Content = result.output.AssistantMessage.Content
				result.response.TraceID = result.output.Trace.ID
				result.response.Metadata = result.output.AssistantMessage.Metadata
				result.response.CompletedAt = result.response.UpdatedAt
			}
			_ = s.stateStore.SaveModelResponse(result.response)
			results = append(results, result)
		}
	}

	responses, err := s.stateStore.ModelResponsesByTurn(turnID)
	if err != nil {
		return err
	}
	canonical := chooseCanonicalResponse(responses, primaryModelID)
	if canonical.ID == "" {
		canonical = state.ModelResponseRecord{
			ID:             shared.NewID("resp"),
			TurnID:         turnID,
			ConversationID: conversationID,
			ModelConfigID:  primaryModelID,
			Content:        "所有模型回答失败。",
			Status:         "failed",
			Error:          "all selected models failed",
			CreatedAt:      now,
			UpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		}
		responses = append(responses, canonical)
	}
	turn.CanonicalResponseID = canonical.ID
	turn.Status = "completed"
	if canonical.Status != "completed" {
		turn.Status = "failed"
	}
	turn.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.stateStore.SaveChatTurn(turn); err != nil {
		return err
	}
	assistantMessage := contracts.Message{
		ID:             assistantMessageID,
		ConversationID: conversationID,
		Role:           contracts.RoleAssistant,
		Content:        canonical.Content,
		CreatedAt:      shared.Now(),
		Metadata:       multiModelAssistantMetadata(turn, responses),
	}
	s.store.AddMessage(assistantMessage)
	for _, result := range results {
		if result.response.ID == canonical.ID {
			s.store.UpdateShortMemory(conversationID, body.Message, canonical.Content, result.output.Trace.ToolResults)
			break
		}
	}
	if err := writeSSEEvent(writer, contracts.AgentStreamEvent{
		Type:          "canonical_selected",
		Title:         "用于上下文",
		Content:       canonical.Content,
		TurnID:        turnID,
		ResponseID:    canonical.ID,
		ModelConfigID: canonical.ModelConfigID,
		Primary:       canonical.ModelConfigID == primaryModelID,
		Canonical:     true,
		Message:       &assistantMessage,
	}); err != nil {
		return err
	}
	return writeSSEEvent(writer, contracts.AgentStreamEvent{
		Type:      "done",
		Title:     "完成",
		Content:   "stream completed",
		Message:   &assistantMessage,
		TraceID:   canonical.TraceID,
		TurnID:    turnID,
		CreatedAt: shared.Now(),
	})
}

func (s *HTTPServer) handleSelectCanonicalResponse(ctx context.Context, c *app.RequestContext) {
	if s.stateStore == nil {
		writeHertzError(c, consts.StatusInternalServerError, "state store is unavailable")
		return
	}
	conversationID := strings.TrimSpace(c.Param("conversationId"))
	turnID := strings.TrimSpace(c.Param("turnId"))
	var body struct {
		ResponseID string `json:"responseId"`
	}
	if err := c.BindJSON(&body); err != nil {
		writeHertzError(c, consts.StatusBadRequest, err.Error())
		return
	}
	body.ResponseID = strings.TrimSpace(body.ResponseID)
	if conversationID == "" || turnID == "" || body.ResponseID == "" {
		writeHertzError(c, consts.StatusBadRequest, "conversationId, turnId and responseId are required")
		return
	}
	turn, response, responses, err := s.stateStore.SelectCanonicalResponse(conversationID, turnID, body.ResponseID)
	if err != nil {
		writeHertzError(c, canonicalSelectionStatus(err), err.Error())
		return
	}
	message, ok := s.canonicalMessageForTurn(conversationID, turn, response, responses)
	if !ok {
		writeHertzError(c, consts.StatusNotFound, "canonical assistant message not found")
		return
	}
	if !s.store.UpdateMessage(message) {
		writeHertzError(c, consts.StatusNotFound, "canonical assistant message not found")
		return
	}
	c.JSON(consts.StatusOK, utils.H{
		"message":   message,
		"turn":      turn,
		"responses": responses,
	})
}

func (s *HTTPServer) retrievalForChat(ctx context.Context, conversationID string, query string, knowledgeBaseID string) (*knowledge.RetrievalResult, error) {
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	if s.knowledgeStore == nil || knowledgeBaseID == "" {
		return nil, nil
	}
	result, err := s.knowledgeStore.Retrieve(ctx, query, knowledge.RetrievalOptions{
		ConversationID: conversationID,
		TopK:           8,
		Filter: knowledge.RetrievalFilter{
			KnowledgeBaseIDs: []string{knowledgeBaseID},
		},
	})
	if err != nil || len(result.RerankedResults) == 0 {
		return nil, err
	}
	return &result, nil
}

func chooseCanonicalResponse(responses []state.ModelResponseRecord, primaryModelID string) state.ModelResponseRecord {
	for _, response := range responses {
		if response.ModelConfigID == primaryModelID && response.Status == "completed" {
			return response
		}
	}
	for _, response := range responses {
		if response.Status == "completed" {
			return response
		}
	}
	return state.ModelResponseRecord{}
}

func multiModelAssistantMetadata(turn state.ChatTurnRecord, responses []state.ModelResponseRecord) map[string]any {
	return map[string]any{
		"multiModel":           true,
		"turnId":               turn.ID,
		"mode":                 turn.Mode,
		"primaryModelConfigId": turn.PrimaryModelConfigID,
		"canonicalResponseId":  turn.CanonicalResponseID,
		"modelResponses":       modelResponsesMetadata(responses),
	}
}

func modelResponsesMetadata(responses []state.ModelResponseRecord) []map[string]any {
	out := make([]map[string]any, 0, len(responses))
	for _, response := range responses {
		out = append(out, map[string]any{
			"id":              response.ID,
			"turnId":          response.TurnID,
			"modelConfigId":   response.ModelConfigID,
			"traceId":         response.TraceID,
			"content":         response.Content,
			"status":          response.Status,
			"error":           response.Error,
			"primaryResponse": response.PrimaryResponse,
			"createdAt":       response.CreatedAt,
			"completedAt":     response.CompletedAt,
			"metadata":        response.Metadata,
		})
	}
	return out
}

func mustHTTPJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func (s *HTTPServer) canonicalMessageForTurn(conversationID string, turn state.ChatTurnRecord, response state.ModelResponseRecord, responses []state.ModelResponseRecord) (contracts.Message, bool) {
	for _, message := range s.store.Messages(conversationID) {
		if message.ID != turn.AssistantMessageID {
			continue
		}
		message.Content = response.Content
		message.Metadata = multiModelAssistantMetadata(turn, responses)
		return message, true
	}
	return contracts.Message{}, false
}

func canonicalSelectionStatus(err error) int {
	if state.IsNotLatestTurn(err) {
		return 409
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 404
	}
	return 500
}
