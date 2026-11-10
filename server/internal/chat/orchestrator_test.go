package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"chat-forge-server/internal/model"
	"chat-forge-server/internal/provider"
	"chat-forge-server/internal/store"
	"chat-forge-server/internal/tool"
)

// TestRunToolLoop 端到端走通 tool-loop:用户问天气 → mock 发起 tool_call → 执行 → 结果回喂 → done。
// 全程零 API key——Mock provider 一等公民,这条测试在 CI 里就是工具循环的回归护栏。
func TestRunToolLoop(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	conv := model.Conversation{UserID: 1, Title: "测试会话"}
	if err := db.Create(&conv).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	orch := &Orchestrator{
		DB:        db,
		Provider:  provider.NewMock(),
		Tools:     tool.DefaultRegistry(),
		MaxRounds: 3,
	}

	events := make(chan SSEEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		orch.Run(context.Background(), &conv, "今天杭州天气怎么样", events)
	}()

	seen := map[string]bool{}
	var toolCall, toolResult *SSEEvent
	for ev := range events {
		seen[ev.Type] = true
		switch ev.Type {
		case "tool_call":
			cp := ev.ToolCall
			toolCall = &SSEEvent{ToolCall: cp}
			if cp.Name != "get_weather" {
				t.Fatalf("expect tool get_weather, got %s", cp.Name)
			}
		case "tool_result":
			toolResult = &SSEEvent{ToolResult: ev.ToolResult}
			if !strings.Contains(ev.ToolResult.Result, "杭州") {
				t.Fatalf("tool result should contain city, got %s", ev.ToolResult.Result)
			}
		case "error":
			t.Fatalf("unexpected error event: %s", ev.Message)
		}
	}
	<-done

	if !seen["delta"] {
		t.Fatal("expect delta events before done")
	}
	if toolCall == nil || toolResult == nil {
		t.Fatal("expect tool_call and tool_result events in weather flow")
	}

	// 落库校验:用户消息 + assistant(tool_calls) + tool + assistant(最终回答)
	var msgs []model.Message
	if err := db.Where("conversation_id = ?", conv.ID).Order("id ASC").Find(&msgs).Error; err != nil {
		t.Fatalf("query messages: %v", err)
	}
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	expect := []string{"user", "assistant", "tool", "assistant"}
	if len(roles) != len(expect) {
		t.Fatalf("expect %v, got %v", expect, roles)
	}
	for i := range expect {
		if roles[i] != expect[i] {
			t.Fatalf("message %d: expect %s, got %s", i, expect[i], roles[i])
		}
	}
	if !json.Valid([]byte(msgs[2].Content)) {
		t.Fatalf("tool message content should be valid JSON, got %s", msgs[2].Content)
	}
}

// TestRunPlainChat 纯对话路径(不触发工具)。
func TestRunPlainChat(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	conv := model.Conversation{UserID: 1, Title: "普通对话"}
	db.Create(&conv)

	orch := &Orchestrator{DB: db, Provider: provider.NewMock(), Tools: tool.DefaultRegistry(), MaxRounds: 3}
	events := make(chan SSEEvent, 64)
	go orch.Run(context.Background(), &conv, "你好,介绍一下你自己", events)

	var content strings.Builder
	for ev := range events {
		if ev.Type == "delta" {
			content.WriteString(ev.Content)
		}
		if ev.Type == "error" {
			t.Fatalf("unexpected error: %s", ev.Message)
		}
	}
	if !strings.Contains(content.String(), "chat-forge") {
		t.Fatalf("mock answer should mention chat-forge, got %s", content.String())
	}
}
