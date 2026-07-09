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

// maxChatModels 限定一次聊天请求最多可选择的模型数量（主模型 + 副模型合计）。
const maxChatModels = 3

// chatRequestBody 是 /chat 与 /chat/stream 两个聊天接口共用的请求体。
//
// 字段说明：
//   - Message：用户输入（必填）；
//   - ModelConfigID：单模型场景的模型配置 ID（旧字段，兼容保留）；
//   - ModelConfigIDs：多模型场景的模型配置 ID 列表；
//   - PrimaryModelConfigID：多模型时指定的主模型（其回答默认作为 canonical 落入上下文）；
//   - KnowledgeBaseID：可选的目标知识库，用于本轮 RAG 召回。
//
// 模型选择的最终归一化（去重、上限、确定主模型）见 normalizeChatModelSelection。
type chatRequestBody struct {
	Message              string   `json:"message"`
	ModelConfigID        string   `json:"modelConfigId"`
	ModelConfigIDs       []string `json:"modelConfigIds"`
	PrimaryModelConfigID string   `json:"primaryModelConfigId"`
	KnowledgeBaseID      string   `json:"knowledgeBaseId"`
}

// multiModelRunResult 承载一路模型（一个副本/主模型）执行完成后的结果，
// 通过 channel 从各并行 goroutine 汇总回主流程：response 为该路的响应记录，
// output 为 Agent 执行输出（消息 + Trace），err 为该路的执行错误（成功时为 nil）。
type multiModelRunResult struct {
	response state.ModelResponseRecord
	output   agent.RunOutput
	err      error
}

// normalizeChatModelSelection 归一化一次请求的模型选择，返回 (去重后的模型 ID 列表, 主模型 ID, 错误)。
//
// 归一化规则：
//  1. 先按顺序合并 ModelConfigIDs 与 ModelConfigID 并去重（保持首次出现顺序）；
//  2. 若一个都没有，兜底为 "default"；
//  3. 数量超过 maxChatModels 时报错；
//  4. 主模型优先取 PrimaryModelConfigID，其次 ModelConfigID，再兜底为列表首个；
//  5. 主模型必须包含在所选模型集合内，否则报错。
//
// 返回的列表长度决定后续走单模型还是多模型分支（len>1 即多模型）。
func (s *HTTPServer) normalizeChatModelSelection(body chatRequestBody) ([]string, string, error) {
	ids := []string{}
	seen := map[string]struct{}{}
	// add 负责去空白、去重并按首次出现顺序追加。
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

// runMultiModelChatStream 编排"多模型并行回答"的完整流程，并全程通过 SSE 向前端推送事件。
//
// 依赖 stateStore 持久化轮次（ChatTurn）与每路回答（ModelResponse），因此无状态库时直接报错。
//
// 整体流程：
//  1. 为每个选中的模型建立客户端；一旦某个建失败则整轮失败。
//  2. 取最近 12 条历史消息（各路共享同一份上下文），落库并推送 user_message 事件。
//  3. 创建一条 running 状态的 ChatTurn（mode=multi）落库，推送 multi_model_start 事件。
//  4. 做一次共享的知识库召回（所有模型复用同一召回结果，保证可比性）；召回失败仅推送错误事件、不中断。
//  5. 为每个模型起一个 goroutine 并行执行 agent.RunStream：
//     每路先落一条 running 的 ModelResponse 并推 model_response_start；
//     RunStream 的回调事件打上 turn/response/model 归属标记后送入 eventCh，由主循环统一写 SSE
//     （集中写出避免多 goroutine 并发写同一 SSE writer）。各路的最终结果送入 resultCh。
//  6. 主循环用 select 从 eventCh（增量事件）与 resultCh（完成结果）二选一消费：
//     完成的每路更新其 ModelResponse 状态（completed/failed）并落库；
//     直到 resultCh 关闭且 eventCh 排空。
//  7. 从库里重新读回全部回答，用 chooseCanonicalResponse 选出 canonical（主模型优先、否则首个成功）；
//     全部失败时构造一条 failed 占位回答。
//  8. 把 canonical 写回 ChatTurn，并据 canonical 内容生成最终助手消息落库（携带多模型元数据），
//     用 canonical 那一路的 Trace 更新短期记忆。
//  9. 推送 canonical_selected 与 done 两条收尾事件。
//
// 说明：各路 RunStream 通过一系列 Skip*/Suppress* 选项避免重复落库/重复推送用户与知识库事件，
// 因为这些副作用已由本函数在外层统一处理。
func (s *HTTPServer) runMultiModelChatStream(ctx context.Context, writer *sse.Writer, conversationID string, body chatRequestBody, modelIDs []string, primaryModelID string) error {
	if s.stateStore == nil {
		return errors.New("state store is required for multi-model chat")
	}
	// 先为每个模型建立客户端（任一失败即整轮失败，避免只跑通一部分模型造成结果不完整）。
	clients := map[string]model.Client{}
	for _, id := range modelIDs {
		client, err := s.modelClientForRequest(id)
		if err != nil {
			return err
		}
		clients[id] = client
	}

	// 取最近 12 条历史消息作为各路共享的对话上下文。
	recent := s.store.RecentMessages(conversationID, 12)
	userMessage := contracts.Message{
		ID:             shared.NewID("msg"),
		ConversationID: conversationID,
		Role:           contracts.RoleUser,
		Content:        body.Message,
		CreatedAt:      shared.Now(),
	}
	// 用户消息由本函数统一落库并推送（各路 RunStream 因此设置 Skip/Suppress 用户消息，避免重复）。
	s.store.AddMessage(userMessage)
	if err := writeSSEEvent(writer, contracts.AgentStreamEvent{
		Type:    "user_message",
		Title:   "用户输入",
		Content: body.Message,
		Message: &userMessage,
	}); err != nil {
		return err
	}

	// 预分配轮次 ID 与最终助手消息 ID：canonical 选定后助手消息内容会填成 canonical 的回答。
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
	// 先落一条 running 轮次记录；后续 canonical 选定后再更新状态与 CanonicalResponseID。
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

	// 共享的知识库召回：所有模型复用同一份召回结果，保证各路回答基于相同证据、可横向比较。
	// 召回失败不影响主流程，仅推送一条错误事件提示前端。
	retrievalResult, err := s.retrievalForChat(ctx, conversationID, body.Message, body.KnowledgeBaseID)
	if err != nil {
		_ = writeSSEEvent(writer, contracts.AgentStreamEvent{
			Type:    "knowledge_retrieval_error",
			Title:   "知识库召回失败",
			Content: err.Error(),
			TurnID:  turnID,
		})
	}

	// eventCh：各路 goroutine 的增量事件汇入此处，由主循环单线程写 SSE（避免并发写同一 writer）。
	// resultCh：各路完成后的最终结果汇入此处，容量等于模型数，确保 goroutine 不会因发送阻塞。
	eventCh := make(chan contracts.AgentStreamEvent, 128)
	resultCh := make(chan multiModelRunResult, len(modelIDs))
	var wg sync.WaitGroup
	for _, modelID := range modelIDs {
		modelID := modelID // 捕获循环变量副本，避免 goroutine 闭包共享同一变量
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
		// 每路先落一条 running 状态的回答记录，并推送 model_response_start 让前端建好对应 UI 区块。
		// 初始将主模型标为 canonical（临时），真正的 canonical 在全部完成后再定夺。
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
		// 每个模型一个 goroutine，彼此并行执行，从而多模型回答的总耗时接近最慢的一路而非各路之和。
		go func(response state.ModelResponseRecord) {
			defer wg.Done()
			// 各 Skip*/Suppress* 选项：用户消息、助手消息落库、短期记忆、知识库/用户事件推送
			// 均已由外层统一处理，避免多路重复副作用；各路只负责产出自己的增量事件与最终结果。
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
				// 给每条增量事件打上归属标记（轮次/回答/模型），前端据此归位到对应模型区块；
				// 然后交给 eventCh 由主循环串行写出，本回调不直接写 SSE。
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
	// 单独一个 goroutine 等所有路完成后关闭 resultCh，作为主循环的退出信号。
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// 主循环：单线程消费两条 channel，既实时写出增量事件，又收集各路最终结果。
	// 退出条件：resultCh 已关闭（置 nil）且 eventCh 已排空。
	results := []multiModelRunResult{}
	for resultCh != nil || len(eventCh) > 0 {
		select {
		case event := <-eventCh:
			// 增量事件：直接写 SSE（空类型事件跳过）。
			if event.Type != "" {
				if err := writeSSEEvent(writer, event); err != nil {
					return err
				}
			}
		case result, ok := <-resultCh:
			if !ok {
				// resultCh 关闭：所有路已结束，置 nil 使 select 不再命中此分支。
				resultCh = nil
				continue
			}
			// 一路完成：把该路的 ModelResponse 更新为 completed/failed 并落库。
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

	// 从库里重新读回全部回答（含状态/内容），作为选 canonical 与落最终消息的权威数据。
	responses, err := s.stateStore.ModelResponsesByTurn(turnID)
	if err != nil {
		return err
	}
	// 选出 canonical：主模型成功优先，否则任意首个成功；全部失败时下方构造 failed 占位。
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
	// 将 canonical 结果写回轮次：记录 CanonicalResponseID，轮次状态随 canonical 成败置为 completed/failed。
	turn.CanonicalResponseID = canonical.ID
	turn.Status = "completed"
	if canonical.Status != "completed" {
		turn.Status = "failed"
	}
	turn.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.stateStore.SaveChatTurn(turn); err != nil {
		return err
	}
	// 最终助手消息：内容取 canonical 的回答，并附上完整多模型元数据（含每路回答摘要），
	// 这样即使刷新页面也能从消息本身还原出多模型对比视图。
	assistantMessage := contracts.Message{
		ID:             assistantMessageID,
		ConversationID: conversationID,
		Role:           contracts.RoleAssistant,
		Content:        canonical.Content,
		CreatedAt:      shared.Now(),
		Metadata:       multiModelAssistantMetadata(turn, responses),
	}
	s.store.AddMessage(assistantMessage)
	// 只用 canonical 那一路的工具结果更新短期记忆，保证进入后续上下文的是被采纳的那份回答。
	for _, result := range results {
		if result.response.ID == canonical.ID {
			s.store.UpdateShortMemory(conversationID, body.Message, canonical.Content, result.output.Trace.ToolResults)
			break
		}
	}
	// 推送 canonical_selected：告知前端哪路被选为上下文答案，并附带最终助手消息。
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
	// done：流收尾事件，携带最终助手消息与 canonical 的 TraceID，供前端落定状态并可跳转追踪详情。
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

// handleSelectCanonicalResponse 处理 PUT /api/conversations/:conversationId/turns/:turnId/canonical-response。
//
// 允许用户在多模型回答完成后，手动改选另一路回答作为 canonical（进入上下文的答案）。
// 请求体 {responseId}；委托 state.Store.SelectCanonicalResponse 做并发安全的切换
// （仅允许切换"最新一轮"，否则返回 409；轮次/回答不存在返回 404），
// 成功后同步更新会话中的助手消息内容与元数据，并回传最新的 message/turn/responses。
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

// retrievalForChat 为一次聊天做知识库召回，返回可注入 Agent 的召回结果。
//
// 无知识库存储或未指定 knowledgeBaseID 时返回 (nil, nil)（表示本轮不做 RAG）；
// 召回 TopK=8，限定在指定知识库内；出错或重排后无结果时返回 nil 结果，让调用方安静跳过 RAG。
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

// chooseCanonicalResponse 从多路回答中挑选 canonical（作为对话上下文的那一份答案）。
//
// 优先级：主模型且成功完成 > 任意首个成功完成的回答 > 都没有成功则返回零值
// （零值由调用方识别为"全部失败"，进而构造 failed 占位）。
func chooseCanonicalResponse(responses []state.ModelResponseRecord, primaryModelID string) state.ModelResponseRecord {
	// 第一优先：主模型且已完成。
	for _, response := range responses {
		if response.ModelConfigID == primaryModelID && response.Status == "completed" {
			return response
		}
	}
	// 次优先：任意一路已完成。
	for _, response := range responses {
		if response.Status == "completed" {
			return response
		}
	}
	// 全部失败：返回零值（ID 为空）。
	return state.ModelResponseRecord{}
}

// multiModelAssistantMetadata 组装多模型助手消息的元数据，
// 包含轮次信息、主模型/canonical 标识以及每路回答的摘要（见 modelResponsesMetadata），
// 便于前端在消息层面完整还原多模型对比与切换 UI。
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

// modelResponsesMetadata 把每路回答记录展开为前端可读的元数据数组
// （每项含模型 ID、内容、状态、错误、Trace ID、时间等），用于消息内嵌的多模型明细。
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

// mustHTTPJSON 将任意值序列化为 JSON 字符串，失败时返回 "{}"。
// 用于把结构化内容塞进 SSE 事件的 Content 字符串字段（如 multi_model_start 的负载）。
func mustHTTPJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// canonicalMessageForTurn 在会话消息中定位该轮的助手消息，并把其内容/元数据
// 更新为给定 canonical 回答对应的值，返回更新后的消息副本。
// 找不到对应助手消息时返回 (零值, false)。用于用户手动改选 canonical 时同步消息内容。
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

// canonicalSelectionStatus 把手动改选 canonical 时的错误映射为合适的 HTTP 状态码：
// 非最新轮次（不允许改选历史轮）→ 409 Conflict；记录不存在 → 404；其余 → 500。
func canonicalSelectionStatus(err error) int {
	if state.IsNotLatestTurn(err) {
		return 409
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 404
	}
	return 500
}
