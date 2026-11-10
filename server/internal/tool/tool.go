package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Definition 工具定义(function calling 契约)。
type Definition struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// Executor 工具执行函数。返回 JSON 字符串作为 tool 消息回喂模型。
type Executor func(ctx context.Context, args json.RawMessage) (string, error)

// Registry 工具注册表。
// 设计原则:模型可调用什么、参数长什么样、怎么执行,全部收口在这里——
// 新增工具 = Register 一次,编排层零改动(与 xTool Agent Action 的注册思想同源)。
type Registry struct {
	defs map[string]Definition
	exec map[string]Executor
}

func NewRegistry() *Registry {
	return &Registry{defs: map[string]Definition{}, exec: map[string]Executor{}}
}

func (r *Registry) Register(def Definition, exec Executor) {
	r.defs[def.Name] = def
	r.exec[def.Name] = exec
}

// Definitions 供 provider 请求使用的工具定义列表(顺序稳定)。
func (r *Registry) Definitions() []Definition {
	names := sortedKeys(r.defs)
	out := make([]Definition, 0, len(names))
	for _, n := range names {
		out = append(out, r.defs[n])
	}
	return out
}

// Execute 按名执行工具。失败返回 error,由编排层转结构化失败回喂模型。
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	exec, ok := r.exec[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return exec(ctx, args)
}

func sortedKeys(m map[string]Definition) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// DefaultRegistry 内置演示工具。
// get_weather 返回模拟数据,用于端到端演示 Function Calling 全流程。
func DefaultRegistry() *Registry {
	r := NewRegistry()

	r.Register(Definition{
		Name:        "get_current_time",
		Description: "获取当前时间(服务器时区)",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(ctx context.Context, _ json.RawMessage) (string, error) {
		return fmt.Sprintf(`{"now":%q}`, time.Now().Format(time.RFC3339)), nil
	})

	r.Register(Definition{
		Name:        "get_weather",
		Description: "查询城市天气(演示用模拟数据)",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string", "description": "城市名,如:杭州"},
			},
			"required": []string{"city"},
		},
	}, func(ctx context.Context, args json.RawMessage) (string, error) {
		var in struct {
			City string `json:"city"`
		}
		if err := json.Unmarshal(args, &in); err != nil || in.City == "" {
			return "", fmt.Errorf("city is required")
		}
		return fmt.Sprintf(`{"city":%q,"weather":"晴","temp_c":24,"note":"mock 数据"}`, in.City), nil
	})

	return r
}
