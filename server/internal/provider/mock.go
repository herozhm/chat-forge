package provider

import (
	"context"
	"strings"
	"time"
)

// MockProvider 离线实现:不依赖任何真模型,输出确定性的流式内容。
// 用途:本地零配置起服务、单元测试、CI 回归——AI 项目最容易被「没有 key 跑不了」卡死,
// Mock 一等公民是这个项目的设计原则之一(与 agent-harness 同理念)。
type MockProvider struct {
	// DelayMS 每个分片的间隔,模拟打字机节奏
	DelayMS int
}

func NewMock() *MockProvider {
	return &MockProvider{DelayMS: 30}
}

func (m *MockProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	events := make(chan StreamEvent, 64)
	go m.run(ctx, req, events)
	return events, nil
}

func (m *MockProvider) run(ctx context.Context, req ChatRequest, events chan<- StreamEvent) {
	defer close(events)

	last := lastUserMessage(req.Messages)
	lastIsUser := len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == RoleUser
	delay := time.Duration(m.DelayMS) * time.Millisecond

	// 演示工具循环:仅当「最后一条消息是 user」且提到天气时发起 tool_call;
	// 若最后一条已是 tool 结果,说明工具已执行,直接给最终回答——
	// 否则会陷入「每轮都发起工具调用」的死循环(这正是 tool-loop 需要 MaxRounds 兜底的原因)。
	if len(req.Tools) > 0 && lastIsUser && strings.Contains(last, "天气") {
		m.sleep(ctx, delay)
		events <- StreamEvent{Type: "delta", Content: "我来查一下天气。"}
		events <- StreamEvent{Type: "tool_calls", ToolCalls: []ToolCall{{
			ID:        "mock-call-" + time.Now().Format("150405"),
			Name:      "get_weather",
			Arguments: `{"city":"杭州"}`,
		}}}
		return
	}

	text := mockAnswer(last)
	for _, r := range splitChunks(text) {
		select {
		case <-ctx.Done():
			events <- StreamEvent{Type: "error", Err: ctx.Err()}
			return
		default:
		}
		m.sleep(ctx, delay)
		events <- StreamEvent{Type: "delta", Content: r}
	}
	events <- StreamEvent{Type: "done", Reason: "stop"}
}

func (m *MockProvider) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func lastUserMessage(msgs []ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// splitChunks 按 rune 分片,保持中文不被截断。
func splitChunks(text string) []string {
	runes := []rune(text)
	chunks := make([]string, 0, len(runes)/3+1)
	for i := 0; i < len(runes); i += 3 {
		end := i + 3
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}

func mockAnswer(question string) string {
	return "收到你的消息:**" + truncate(question, 60) + "**\n\n" +
		"我是 chat-forge 的离线 Mock 模型,当前没有连接真实 LLM。这个项目的设计原则:\n\n" +
		"- **Provider 抽象**:换模型只改环境变量(见 README),编排层零改动\n" +
		"- **Mock 一等公民**:测试与 CI 不依赖 API key\n" +
		"- **SSE 流式**:你现在看到的逐字输出就是 mock provider 按 rune 分片模拟的\n" +
		"- **工具循环**:发送包含「天气」的消息,可以演示 Function Calling 全流程\n\n" +
		"想接真实模型?设置环境变量 AI_PROVIDER=openai 与 OPENAI_API_KEY 即可。"
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
