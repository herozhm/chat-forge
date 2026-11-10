package provider

import "context"

// Role 常量,与 OpenAI messages 协议对齐。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleSystem    = "system"
)

// ToolDefinition 工具定义(function calling)。
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

// ToolCall 模型发起的一次工具调用。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON 字符串,由执行方校验
}

// ChatMessage 对话消息,Provider 请求的最小形状。
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ChatRequest 一次对话请求。
type ChatRequest struct {
	Messages []ChatMessage
	Tools    []ToolDefinition
}

// StreamEvent 流式事件,由 Provider 产出、编排层消费。
type StreamEvent struct {
	Type      string     // delta | tool_calls | done | error
	Content   string     // delta: 增量文本
	ToolCalls []ToolCall // tool_calls: 完整工具调用列表(聚合后)
	Reason    string     // done: stop | tool_calls
	Err       error      // error
}

// Provider 模型接入抽象。
// 换模型 = 换实现,编排层与存储层零改动;Mock 实现让测试与 CI 不依赖真模型。
type Provider interface {
	// StreamChat 流式对话。返回事件通道,ctx 取消时必须停止上游请求并关闭通道。
	StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
