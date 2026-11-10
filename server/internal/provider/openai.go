package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIProvider OpenAI 兼容协议实现(DeepSeek / Kimi / Qwen / OpenAI 均可用)。
// 关键点: ctx 贯通到 http 请求——客户端断开时上游请求同步取消,不白烧 token。
type OpenAIProvider struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

func NewOpenAI(baseURL, apiKey, model string) *OpenAIProvider {
	return &OpenAIProvider{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 0}, // 流式响应不设整体超时,由 ctx 控制
	}
}

type openaiStreamChoice struct {
	Delta struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			Index    int    `json:"index"`
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

// wireMessage / wireToolCall:assistant 工具调用消息回传给模型时的 wire 形状。
// OpenAI 协议要求 tool_calls 元素为 {id, type:"function", function:{name, arguments}}——
// 内部的扁平 ToolCall(id/name/arguments)是流式聚合形状,直接序列化会被
// DeepSeek/OpenAI 以「missing field type」类 422 拒掉(Mock 不上 wire,测不出这条契约)。
type wireToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireToolCallFunc `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

// toWire 把内部 ChatMessage 转成 OpenAI wire 形状。
func toWire(msgs []ChatMessage) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		wm := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:       tc.ID,
				Type:     "function",
				Function: wireToolCallFunc{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		out = append(out, wm)
	}
	return out
}

func (p *OpenAIProvider) StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	body := map[string]any{
		"model":    p.Model,
		"messages": toWire(req.Messages),
		"stream":   true,
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("provider %d: %s", resp.StatusCode, string(b))
	}

	events := make(chan StreamEvent, 32)
	go p.pump(resp.Body, events)
	return events, nil
}

// pump 解析上游 SSE,聚合 tool_calls 增量,产出 StreamEvent。
func (p *OpenAIProvider) pump(body io.ReadCloser, events chan<- StreamEvent) {
	defer close(events)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	// tool_calls 增量按 index 聚合:arguments 是分片下发的字符串
	aggregated := map[int]*ToolCall{}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []openaiStreamChoice `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				events <- StreamEvent{Type: "delta", Content: choice.Delta.Content}
			}
			for _, tc := range choice.Delta.ToolCalls {
				cur, ok := aggregated[tc.Index]
				if !ok {
					cur = &ToolCall{}
					aggregated[tc.Index] = cur
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				// name/arguments 在流式增量里嵌套于 function 字段下
				if tc.Function.Name != "" {
					cur.Name = tc.Function.Name
				}
				cur.Arguments += tc.Function.Arguments
			}
			if choice.FinishReason == "tool_calls" {
				events <- StreamEvent{Type: "tool_calls", ToolCalls: collectCalls(aggregated)}
				return
			}
			if choice.FinishReason == "stop" {
				events <- StreamEvent{Type: "done", Reason: "stop"}
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) {
			events <- StreamEvent{Type: "error", Err: err}
		}
		return
	}
	// 流自然结束但未见 finish_reason:视为完成
	events <- StreamEvent{Type: "done", Reason: "stop"}
}

func collectCalls(m map[int]*ToolCall) []ToolCall {
	calls := make([]ToolCall, 0, len(m))
	for i := 0; i < len(m); i++ {
		if c, ok := m[i]; ok {
			calls = append(calls, *c)
		}
	}
	return calls
}
