package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"chat-forge-server/internal/auth"
	"chat-forge-server/internal/chat"
	"chat-forge-server/internal/model"
)

// chatHandler POST /api/chat 的 SSE 端点。
//
// 四个关键技术点(面试常问):
//  1. http.Flusher 逐块刷出,响应头关 buffering;
//  2. r.Context() 贯通到 provider——客户端断开即取消上游请求,不白烧 token;
//  3. 心跳注释行(: ping)防 nginx 等代理空闲超时掐连接;
//  4. 错误统一 event: error + 结构化 JSON,前端只认一种失败形状。
type chatHandler struct {
	db   *gorm.DB
	orch *chat.Orchestrator
}

type chatReq struct {
	ConversationID uint   `json:"conversation_id"`
	Content        string `json:"content" binding:"required"`
}

func (h *chatHandler) stream(c *gin.Context) {
	var req chatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "content is required"})
		return
	}

	userID := auth.UserID(c)
	var conv model.Conversation
	if err := h.db.First(&conv, req.ConversationID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "conversation not found"})
		return
	}
	if conv.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"message": "not your conversation"})
		return
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "streaming unsupported"})
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // nginx 不缓冲
	c.Writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 客户端断开 → ctx 取消 → provider 上游请求同步取消
	ctx := c.Request.Context()
	events := make(chan chat.SSEEvent, 64)
	go h.orch.Run(ctx, &conv, req.Content, events)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	writeSSE := func(event string, payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			return true // 序列化失败跳过该事件,不断流
		}
		if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false // 客户端断开
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case <-ctx.Done():
			// 客户端已断开,编排层会感知同一 ctx 并停止;这里只负责退出
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !writeSSE(ev.Type, ev) {
				return
			}
			if ev.Type == "done" || ev.Type == "error" {
				return
			}
		}
	}
}
