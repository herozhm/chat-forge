package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"chat-forge-server/internal/auth"
	"chat-forge-server/internal/model"
)

// ─── 认证 ───

type authHandler struct {
	db     *gorm.DB
	secret string
}

type registerReq struct {
	Username string `json:"username" binding:"required,min=3,max=32"`
	Password string `json:"password" binding:"required,min=6,max=64"`
}

// register 用户注册。
func (h *authHandler) register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "用户名 3-32 字符,密码至少 6 位"})
		return
	}
	var exists model.User
	if err := h.db.Where("username = ?", req.Username).First(&exists).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"message": "用户名已存在"})
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "hash failed"})
		return
	}
	user := model.User{Username: req.Username, Password: hash}
	if err := h.db.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "create user failed"})
		return
	}
	token, err := auth.IssueToken(h.secret, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "issue token failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "username": user.Username})
}

// login 用户登录。
func (h *authHandler) login(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数不完整"})
		return
	}
	var user model.User
	if err := h.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "用户名或密码错误"})
		return
	}
	if !auth.CheckPassword(user.Password, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "用户名或密码错误"})
		return
	}
	token, err := auth.IssueToken(h.secret, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "issue token failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "username": user.Username})
}

// ─── 会话与消息 ───

type convHandler struct{ db *gorm.DB }

// listConversations 当前用户的会话列表。
func (h *convHandler) list(c *gin.Context) {
	userID := auth.UserID(c)
	var convs []model.Conversation
	if err := h.db.Where("user_id = ?", userID).Order("updated_at DESC").Find(&convs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "query failed"})
		return
	}
	c.JSON(http.StatusOK, convs)
}

type createConvReq struct {
	Title string `json:"title"`
}

// createConversation 新建会话,标题缺省取「新会话」。
func (h *convHandler) create(c *gin.Context) {
	var req createConvReq
	_ = c.ShouldBindJSON(&req)
	conv := model.Conversation{
		UserID: auth.UserID(c),
		Title:  firstNonEmpty(strings.TrimSpace(req.Title), "新会话"),
	}
	if err := h.db.Create(&conv).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "create failed"})
		return
	}
	c.JSON(http.StatusOK, conv)
}

// deleteConversation 删除会话(连带消息),归属校验防止越权。
func (h *convHandler) delete(c *gin.Context) {
	userID := auth.UserID(c)
	id := c.Param("id")
	var conv model.Conversation
	if err := h.db.First(&conv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "conversation not found"})
		return
	}
	if conv.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"message": "not your conversation"})
		return
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("conversation_id = ?", conv.ID).Delete(&model.Message{}).Error; err != nil {
			return err
		}
		return tx.Delete(&conv).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "delete failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// listMessages 会话消息列表,归属校验 + 按 created_at 正序。
func (h *convHandler) messages(c *gin.Context) {
	userID := auth.UserID(c)
	id := c.Param("id")
	var conv model.Conversation
	if err := h.db.First(&conv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "conversation not found"})
		return
	}
	if conv.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"message": "not your conversation"})
		return
	}
	var msgs []model.Message
	if err := h.db.Where("conversation_id = ?", conv.ID).
		Order("created_at ASC, id ASC").Find(&msgs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "query failed"})
		return
	}
	c.JSON(http.StatusOK, msgs)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
