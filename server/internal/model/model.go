package model

import "time"

// DefaultConversationTitle 新会话缺省标题;首轮对话后由编排层异步生成真实标题替换。
const DefaultConversationTitle = "新会话"

// User 注册用户。
type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"uniqueIndex;size:64" json:"username"`
	Password  string    `gorm:"size:128" json:"-"` // bcrypt hash
	CreatedAt time.Time `json:"created_at"`
}

// Conversation 会话。
type Conversation struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	UserID uint   `gorm:"index" json:"user_id"`
	Title  string `gorm:"size:128" json:"title"`
	// Summary 窗口外历史的滚动摘要:发给模型时注入 system,替代被硬截断的旧对话
	Summary string `gorm:"type:text" json:"summary,omitempty"`
	// SummarizedCount 已被摘要覆盖的消息条数;新增窗口外消息数超过它才触发重压缩
	SummarizedCount int       `gorm:"default 0" json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Message 消息。role: user / assistant / tool。
// ToolCallsJSON / ToolCallID 用于表达 assistant 的工具调用与 tool 角色的回填,
// 形状与 OpenAI messages 协议对齐,回放历史时可直接拼进 provider 请求。
type Message struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ConversationID uint      `gorm:"index:idx_conv_created,priority:1" json:"conversation_id"`
	Role           string    `gorm:"size:16;index:idx_conv_created,priority:2" json:"role"`
	Content        string    `gorm:"type:text" json:"content"`
	ToolCallsJSON  string    `gorm:"type:text" json:"tool_calls_json,omitempty"` // assistant 发起的工具调用(JSON 数组)
	ToolCallID     string    `gorm:"size:64" json:"tool_call_id,omitempty"`      // role=tool 时对应哪个调用
	Name           string    `gorm:"size:64" json:"name,omitempty"`              // role=tool 时的工具名
	Tokens         int       `json:"tokens"`
	CreatedAt      time.Time `json:"created_at"`
}
