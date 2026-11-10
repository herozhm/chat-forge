package provider

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestOpenAIRealSmoke 真模型冒烟测试。
//
// 动机:Mock Provider 测不出 wire 契约——2026-11-10 实测 DeepSeek 422
// 「messages[2]: missing field type」:assistant tool_calls 回传用了内部扁平形状,
// 而 OpenAI 协议要求 {id, type:"function", function:{name, arguments}};
// 且流式增量的 name/arguments 嵌套在 function 字段下,Mock 全绿但真模型一打就挂。
// 本测试把这两条契约固化成回归:
//
//	阶段 1:带工具发起对话 → 必须收到 tool_calls,且 name/arguments 解析完整;
//	阶段 2:回传 assistant(tool_calls)+tool 结果继续对话 → 必须正常收敛到 done。
//
// 运行条件:SMOKE=1 且 OPENAI_API_KEY 非空(CI 由 Secrets 注入;本地手动触发)。
// 普通单元测试与无 key 环境(包括开源用户的 CI)自动跳过——Mock 一等公民原则不破坏。
func TestOpenAIRealSmoke(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if os.Getenv("SMOKE") != "1" || key == "" {
		t.Skip("SMOKE!=1 或无 OPENAI_API_KEY,跳过真模型冒烟")
	}
	baseURL := envOr("OPENAI_BASE_URL", "https://api.deepseek.com/v1")
	model := envOr("OPENAI_MODEL", "deepseek-chat")
	p := NewOpenAI(baseURL, key, model)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	weatherTool := ToolDefinition{
		Name:        "get_weather",
		Description: "查询城市天气",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string", "description": "城市名"},
			},
			"required": []string{"city"},
		},
	}

	// ── 阶段 1:带工具对话,断言 tool_calls 契约 ──
	msgs := []ChatMessage{
		{Role: RoleSystem, Content: "你是助手,需要实时信息时必须调用提供的工具。"},
		{Role: RoleUser, Content: "查一下杭州的天气"},
	}
	ev1 := collectEvents(t, ctx, p, ChatRequest{Messages: msgs, Tools: []ToolDefinition{weatherTool}})

	if !ev1.sawToolCalls {
		t.Fatalf("期望模型发起 tool_calls,实际没有(回答片段: %q)", ev1.text)
	}
	if len(ev1.calls) == 0 || ev1.calls[0].Name != "get_weather" {
		t.Fatalf("tool_calls 契约破坏:期望 get_weather,得到 %+v", ev1.calls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(ev1.calls[0].Arguments), &args); err != nil {
		t.Fatalf("tool_calls arguments 应为合法 JSON(流式聚合契约),得到 %q: %v", ev1.calls[0].Arguments, err)
	}

	// ── 阶段 2:回传工具结果继续对话,断言收敛(今日 422 的复现路径) ──
	msgs = append(msgs,
		ChatMessage{Role: RoleAssistant, ToolCalls: ev1.calls},
		ChatMessage{Role: RoleTool, ToolCallID: ev1.calls[0].ID, Name: ev1.calls[0].Name,
			Content: `{"city":"杭州","weather":"晴","temp_c":24}`},
	)
	ev2 := collectEvents(t, ctx, p, ChatRequest{Messages: msgs, Tools: []ToolDefinition{weatherTool}})
	if ev1.errMsg != "" || ev2.errMsg != "" {
		t.Fatalf("冒烟过程出现 error 事件: %q / %q", ev1.errMsg, ev2.errMsg)
	}
	if !ev2.sawDone || ev2.text == "" {
		t.Fatalf("阶段 2 未正常收敛: sawDone=%v text=%q", ev2.sawDone, ev2.text)
	}
	t.Logf("冒烟通过: arguments=%s, 二轮回答 %d 字", ev1.calls[0].Arguments, len([]rune(ev2.text)))
}

type smokeResult struct {
	sawToolCalls bool
	calls        []ToolCall
	sawDone      bool
	text         string
	errMsg       string
}

func collectEvents(t *testing.T, ctx context.Context, p Provider, req ChatRequest) smokeResult {
	t.Helper()
	stream, err := p.StreamChat(ctx, req)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	res := smokeResult{}
	for ev := range stream {
		switch ev.Type {
		case "delta":
			res.text += ev.Content
		case "tool_calls":
			res.sawToolCalls = true
			res.calls = ev.ToolCalls
		case "done":
			res.sawDone = true
		case "error":
			res.errMsg = ev.Err.Error()
		}
	}
	return res
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
