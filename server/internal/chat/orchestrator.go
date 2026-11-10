package chat

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"chat-forge-server/internal/model"
	"chat-forge-server/internal/provider"
	"chat-forge-server/internal/tool"
)

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

	// 2. 组装历史(system + 近 N 条),控制上下文体积
	history, err := o.buildMessages(conv.ID)
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
			events <- SSEEvent{Type: "done", Message: itoa(int(conv.ID))}
			return
		}
		events <- errEvent("provider stream ended unexpectedly")
		return
	}

	// 走到这说明超过 MaxRounds 仍未收敛
	events <- errEvent("tool loop exceeded max rounds")
}

// buildMessages 组装请求消息:system prompt + 最近 20 条历史。
func (o *Orchestrator) buildMessages(convID uint) ([]provider.ChatMessage, error) {
	var msgs []model.Message
	if err := o.DB.Where("conversation_id = ?", convID).
		Order("created_at DESC, id DESC").Limit(20).Find(&msgs).Error; err != nil {
		return nil, err
	}
	out := []provider.ChatMessage{{
		Role: provider.RoleSystem,
		Content: "你是 chat-forge,一个乐于助人的中文 AI 助手。" +
			"回答使用 Markdown;需要实时信息时调用提供的工具。",
	}}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
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
