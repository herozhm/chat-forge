package chat

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"chat-forge-server/internal/model"
	"chat-forge-server/internal/provider"
	"chat-forge-server/internal/tool"
)

// summaryMu 摘要压缩串行化:同一会话的并发 done 不重复触发压缩。
// 进程级锁即可——摘要是非关键路径,多实例部署时应换 DB 行锁或分布式锁。
var summaryMu sync.Mutex

// Orchestrator 编排层:prompt 组装 → 调模型 → 工具循环 → 落库。
// 失败语义:工具失败也结构化回喂模型(role=tool + error 内容),让模型自纠,
// 而不是中断对话——失败是信息,不是异常。
type Orchestrator struct {
	DB        *gorm.DB
	Provider  provider.Provider
	Tools     *tool.Registry
	MaxRounds int // tool-loop 最大轮次,防死循环
}

// SSEEvent 推给前端的流式事件(与 SSE data payload 同形状)。
type SSEEvent struct {
	Type       string             `json:"type"` // delta | tool_call | tool_result | done | error
	Content    string             `json:"content,omitempty"`
	ToolCall   *provider.ToolCall `json:"tool_call,omitempty"`
	ToolResult *ToolResult        `json:"tool_result,omitempty"`
	Message    string             `json:"message,omitempty"` // error 时的人类可读信息
}

// ToolResult 工具执行结果(推给前端展示 + 落库)。
type ToolResult struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Result string `json:"result"` // JSON 字符串;失败时为 {"error": "..."}
}

// Run 执行一轮对话:流式产出事件、持久化用户/助手/工具消息。
// ctx 贯通到 provider——客户端断开即取消上游请求。
// Run 是 events 的唯一发送方,退出时统一关闭,消费方可安全 range。
func (o *Orchestrator) Run(ctx context.Context, conv *model.Conversation, userText string, events chan<- SSEEvent) {
	defer close(events)

	// 1. 持久化用户消息
	userMsg := model.Message{ConversationID: conv.ID, Role: provider.RoleUser, Content: userText}
	if err := o.DB.Create(&userMsg).Error; err != nil {
		events <- errEvent("persist user message failed")
		return
	}

	// 2. 组装历史(system + 会话摘要 + 原文窗口),控制上下文体积
	history, err := o.buildMessages(conv)
	if err != nil {
		events <- errEvent("load history failed")
		return
	}

	// 3. tool-loop
	req := provider.ChatRequest{Messages: history, Tools: o.toolDefs()}
	answer := ""
	for round := 0; round <= o.MaxRounds; round++ {
		stream, err := o.Provider.StreamChat(ctx, req)
		if err != nil {
			events <- errEvent("provider request failed: " + err.Error())
			return
		}

		roundHadTools := false
		for ev := range stream {
			switch ev.Type {
			case "delta":
				answer += ev.Content
				events <- SSEEvent{Type: "delta", Content: ev.Content}
			case "tool_calls":
				roundHadTools = true
				// 持久化助手的工具调用
				if err := o.persistAssistantToolCalls(conv.ID, ev.ToolCalls); err != nil {
					events <- errEvent("persist tool calls failed")
					return
				}
				// OpenAI 协议:tool 消息之前必须有带 tool_calls 的 assistant 消息
				req.Messages = append(req.Messages, provider.ChatMessage{
					Role: provider.RoleAssistant, ToolCalls: ev.ToolCalls,
				})
				for _, call := range ev.ToolCalls {
					events <- SSEEvent{Type: "tool_call", ToolCall: &call}
					result := o.executeTool(ctx, call)
					events <- SSEEvent{Type: "tool_result", ToolResult: &result}

					// 工具结果作为 role=tool 消息落库 + 拼进下一轮请求
					toolMsg := model.Message{
						ConversationID: conv.ID,
						Role:           provider.RoleTool,
						Content:        result.Result,
						ToolCallID:     result.CallID,
						Name:           result.Name,
					}
					if err := o.DB.Create(&toolMsg).Error; err != nil {
						events <- errEvent("persist tool result failed")
						return
					}
					req.Messages = append(req.Messages, provider.ChatMessage{
						Role: provider.RoleTool, Content: result.Result, ToolCallID: result.CallID, Name: result.Name,
					})
				}
			case "done":
				if err := o.persistAssistant(conv.ID, answer); err != nil {
					events <- errEvent("persist answer failed")
					return
				}
				// 首轮对话后异步生成会话标题:用 Background ctx(请求 ctx 随响应结束被取消),
				// 不阻塞 done 事件的返回——标题晚几秒出现在侧栏属可接受
				go o.generateTitleIfDefault(conv.ID, userText)
				// 窗口外有新增历史时,后台滚动压缩摘要,供下一轮注入(滞后一轮,不阻塞响应)
				go o.updateSummaryIfStale(conv.ID)
				events <- SSEEvent{Type: "done", Message: itoa(int(conv.ID))}
				return
			case "error":
				// 已收到的部分内容照常落库(可读的成功消息),失败原因推给前端
				if answer != "" {
					_ = o.persistAssistant(conv.ID, answer)
				}
				events <- errEvent(ev.Err.Error())
				return
			}
		}

		// 流关闭:本轮有工具调用 → 继续下一轮(携带工具结果);否则按结束处理
		if roundHadTools {
			continue
		}
		if answer != "" {
			_ = o.persistAssistant(conv.ID, answer)
			go o.generateTitleIfDefault(conv.ID, userText)
			go o.updateSummaryIfStale(conv.ID)
			events <- SSEEvent{Type: "done", Message: itoa(int(conv.ID))}
			return
		}
		events <- errEvent("provider stream ended unexpectedly")
		return
	}

	// 走到这说明超过 MaxRounds 仍未收敛
	events <- errEvent("tool loop exceeded max rounds")
}

// 长对话上下文管理:原文窗口(条数与字符预算双重约束)+ 窗口外历史滚动摘要。
// 窗口外内容不丢弃——由 updateSummaryIfStale 压成摘要,buildMessages 注入 system,
// 把「硬失忆」变成「模糊记忆」。
const (
	recentKeepMax     = 16    // 原文窗口最大条数
	contextCharBudget = 24000 // 原文窗口字符预算(中文 ≈ 1.2~1.6 万 token 的保守近似)
	compressMinOffset = 4     // 窗口外至少攒够 N 条才压缩,避免频繁调用
)

const systemPrompt = "你是 chat-forge,一个乐于助人的中文 AI 助手。" +
	"回答使用 Markdown;需要实时信息时调用提供的工具。"

// buildMessages 组装请求消息:system prompt + 会话摘要 + 原文窗口。
// 窗口取「最近 recentKeepMax 条」且「字符预算内」的双重约束;超预算即停,
// 至少保留最新一条(防止单条超长消息导致窗口为空)。
func (o *Orchestrator) buildMessages(conv *model.Conversation) ([]provider.ChatMessage, error) {
	var msgs []model.Message
	if err := o.DB.Where("conversation_id = ?", conv.ID).
		Order("created_at DESC, id DESC").Limit(recentKeepMax).Find(&msgs).Error; err != nil {
		return nil, err
	}

	out := []provider.ChatMessage{{Role: provider.RoleSystem, Content: systemPrompt}}
	if conv.Summary != "" {
		// 摘要作为独立 system 消息紧跟主 prompt:主 prompt 前缀稳定,利于 Prompt Cache 命中
		out = append(out, provider.ChatMessage{
			Role:    provider.RoleSystem,
			Content: "【此前对话摘要】(更早的对话已压缩,以下为其要点)\n" + conv.Summary,
		})
	}

	// 字符预算内从新到旧装箱;预算优先于条数
	used := 0
	var kept []model.Message
	for _, m := range msgs {
		cost := len([]rune(m.Content))
		if used > 0 && used+cost > contextCharBudget {
			break
		}
		used += cost
		kept = append(kept, m)
	}
	for i := len(kept) - 1; i >= 0; i-- {
		m := kept[i]
		switch m.Role {
		case provider.RoleUser:
			out = append(out, provider.ChatMessage{Role: m.Role, Content: m.Content})
		case provider.RoleAssistant:
			cm := provider.ChatMessage{Role: m.Role, Content: m.Content}
			// 还原历史中的工具调用,保证 tool 消息能对上 tool_call_id
			if m.ToolCallsJSON != "" {
				var calls []provider.ToolCall
				if err := json.Unmarshal([]byte(m.ToolCallsJSON), &calls); err == nil {
					cm.ToolCalls = calls
				}
			}
			out = append(out, cm)
		case provider.RoleTool:
			out = append(out, provider.ChatMessage{Role: provider.RoleTool, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name})
		}
	}
	return out, nil
}

func (o *Orchestrator) toolDefs() []provider.ToolDefinition {
	defs := o.Tools.Definitions()
	out := make([]provider.ToolDefinition, 0, len(defs))
	for _, d := range defs {
		out = append(out, provider.ToolDefinition{Name: d.Name, Description: d.Description, Parameters: d.Parameters})
	}
	return out
}

func (o *Orchestrator) executeTool(ctx context.Context, call provider.ToolCall) ToolResult {
	var args json.RawMessage
	if call.Arguments != "" {
		args = json.RawMessage(call.Arguments)
	}
	result, err := o.Tools.Execute(ctx, call.Name, args)
	if err != nil {
		// 失败也是结构化信息,回喂模型让它自纠
		result = `{"error":` + mustJSON(err.Error()) + `}`
	}
	return ToolResult{CallID: call.ID, Name: call.Name, Result: result}
}

func (o *Orchestrator) persistAssistant(convID uint, content string) error {
	msg := model.Message{ConversationID: convID, Role: provider.RoleAssistant, Content: content}
	return o.DB.Create(&msg).Error
}

func (o *Orchestrator) persistAssistantToolCalls(convID uint, calls []provider.ToolCall) error {
	if len(calls) == 0 {
		return nil
	}
	b, _ := json.Marshal(calls)
	msg := model.Message{ConversationID: convID, Role: provider.RoleAssistant, ToolCallsJSON: string(b)}
	return o.DB.Create(&msg).Error
}

func errEvent(msg string) SSEEvent {
	return SSEEvent{Type: "error", Message: msg}
}

// generateTitleIfDefault 首轮对话后为「仍叫缺省标题」的会话生成真实标题。
// - 真模型:一次廉价 LLM 调用生成(不带工具,输出约束在 system prompt);
// - Mock:降级为截断首条用户消息——既不烧一次假调用,标题也不会变成 mock 的固定文案。
// 用 Background ctx(此时请求 ctx 已随响应结束取消)+ 超时兜底,失败静默(标题非关键路径)。
func (o *Orchestrator) generateTitleIfDefault(convID uint, firstUserMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var conv model.Conversation
	if err := o.DB.First(&conv, convID).Error; err != nil {
		return
	}
	if conv.Title != model.DefaultConversationTitle {
		return
	}

	title := ""
	if _, isMock := o.Provider.(*provider.MockProvider); !isMock {
		title = o.llmTitle(ctx, firstUserMsg)
	}
	if title == "" {
		title = truncateTitle(firstUserMsg, 16)
	}
	if title == "" {
		return
	}
	_ = o.DB.Model(&model.Conversation{}).Where("id = ?", convID).Update("title", title).Error
}

// llmTitle 一次非流式语义的标题生成(复用 StreamChat 接口,收集全文)。
func (o *Orchestrator) llmTitle(ctx context.Context, userMsg string) string {
	stream, err := o.Provider.StreamChat(ctx, provider.ChatRequest{
		Messages: []provider.ChatMessage{
			{Role: provider.RoleSystem, Content: "你是会话标题生成器。根据用户消息生成不超过16字的简短标题,直接输出标题本身:不要引号、句号、任何前缀或解释。"},
			{Role: provider.RoleUser, Content: userMsg},
		},
	})
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for ev := range stream {
		switch ev.Type {
		case "delta":
			sb.WriteString(ev.Content)
		case "error":
			return ""
		}
	}
	return sanitizeTitle(sb.String())
}

// sanitizeTitle 清理 LLM 输出:压掉换行、剥引号与常见前缀,截断到 20 rune。
func sanitizeTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'“”‘’《》「」*.。 ")
	s = strings.TrimPrefix(s, "标题：")
	s = strings.TrimPrefix(s, "标题:")
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) > 20 {
		runes = runes[:20]
	}
	return string(runes)
}

func truncateTitle(s string, n int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return string(runes)
}

// updateSummaryIfStale 滚动压缩窗口外历史:把「窗口之前的消息 + 旧摘要」压成新摘要存库,
// 供下一轮 buildMessages 注入。滞后一轮(后台执行),不阻塞当前响应。
//
// 触发与去重:仅当「窗口外新增消息数」超过 SummarizedCount(上次已覆盖的条数)才压缩,
// 并用进程锁串行化——每个 done 都会触发本方法,靠这两个条件把调用压到真正必要时。
// Mock provider 直接跳过:固定文案作为摘要会污染后续上下文。
func (o *Orchestrator) updateSummaryIfStale(convID uint) {
	if _, isMock := o.Provider.(*provider.MockProvider); isMock {
		return
	}

	summaryMu.Lock()
	defer summaryMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var conv model.Conversation
	if err := o.DB.First(&conv, convID).Error; err != nil {
		return
	}

	var total int64
	if err := o.DB.Model(&model.Message{}).Where("conversation_id = ?", convID).Count(&total).Error; err != nil {
		return
	}
	offset := int(total) - recentKeepMax // 窗口外消息条数
	if offset < compressMinOffset || offset <= conv.SummarizedCount {
		return // 攒不够一批,或上次摘要已覆盖到最新
	}

	var old []model.Message
	if err := o.DB.Where("conversation_id = ?", convID).
		Order("created_at ASC, id ASC").Limit(offset).Find(&old).Error; err != nil {
		return
	}

	// 组装压缩输入:旧摘要 + 窗口外对话记录(跳过工具原始 JSON——噪声大、价值低)
	var transcript strings.Builder
	for _, m := range old {
		content := strings.TrimSpace(m.Content)
		if content == "" || m.Role == provider.RoleTool {
			continue
		}
		transcript.WriteString(roleLabel(m.Role) + ": ")
		transcript.WriteString(truncateRunes(content, 400))
		transcript.WriteString("\n")
	}
	userContent := ""
	if conv.Summary != "" {
		userContent += "现有摘要(请在其基础上融合新对话):\n" + conv.Summary + "\n\n"
	}
	userContent += "新增对话记录:\n" + transcript.String()

	newSummary := o.llmSummarize(ctx, userContent)
	if newSummary == "" {
		return
	}
	_ = o.DB.Model(&model.Conversation{}).Where("id = ?", convID).
		Updates(map[string]any{"summary": newSummary, "summarized_count": offset}).Error
}

// llmSummarize 一次廉价调用压缩对话历史。
func (o *Orchestrator) llmSummarize(ctx context.Context, userContent string) string {
	stream, err := o.Provider.StreamChat(ctx, provider.ChatRequest{
		Messages: []provider.ChatMessage{
			{Role: provider.RoleSystem, Content: "你是对话摘要器。把提供的对话历史压缩成一段简洁摘要,必须保留:用户的目标、已做的决定、关键信息(名称/数字/参数)、未完成事项。不超过300字,直接输出摘要本身,不要任何前缀或解释。"},
			{Role: provider.RoleUser, Content: userContent},
		},
	})
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for ev := range stream {
		switch ev.Type {
		case "delta":
			sb.WriteString(ev.Content)
		case "error":
			return ""
		}
	}
	s := strings.TrimSpace(sb.String())
	runes := []rune(s)
	if len(runes) > 800 {
		runes = runes[:800]
	}
	return string(runes)
}

func roleLabel(role string) string {
	switch role {
	case provider.RoleUser:
		return "用户"
	case provider.RoleAssistant:
		return "助手"
	case provider.RoleTool:
		return "工具"
	}
	return role
}

// truncateRunes 按 rune 截断(中文安全),无省略号后缀——用于摘要转写等拼接场景。
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
