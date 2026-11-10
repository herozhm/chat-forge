package api

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"chat-forge-server/internal/auth"
	"chat-forge-server/internal/chat"
	"chat-forge-server/internal/config"
	"chat-forge-server/internal/middleware"
	"chat-forge-server/internal/provider"
	"chat-forge-server/internal/tool"
)

// NewRouter 组装全部路由与中间件。
func NewRouter(cfg *config.Config, db *gorm.DB) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(middleware.CORS([]string{
		"http://localhost:5173", "http://127.0.0.1:5173", // Vite dev
	}))

	// Provider 选择:配置驱动,编排层零改动
	var p provider.Provider
	switch cfg.AIProvider {
	case "openai":
		p = provider.NewOpenAI(cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.Model)
	default:
		p = provider.NewMock()
	}

	orch := &chat.Orchestrator{
		DB:        db,
		Provider:  p,
		Tools:     tool.DefaultRegistry(),
		MaxRounds: cfg.MaxToolRounds,
	}

	authAPI := &authHandler{db: db, secret: cfg.JWTSecret}
	convAPI := &convHandler{db: db}
	chatAPI := &chatHandler{db: db, orch: orch}
	limiter := middleware.NewRateLimit(20, time.Minute)

	apiGroup := r.Group("/api")
	apiGroup.POST("/auth/register", authAPI.register)
	apiGroup.POST("/auth/login", authAPI.login)

	protected := apiGroup.Group("")
	protected.Use(auth.Middleware(cfg.JWTSecret))
	protected.Use(limiter.Middleware())
	{
		protected.POST("/chat", chatAPI.stream)
		protected.GET("/conversations", convAPI.list)
		protected.POST("/conversations", convAPI.create)
		protected.DELETE("/conversations/:id", convAPI.delete)
		protected.GET("/conversations/:id/messages", convAPI.messages)
	}

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	return r
}
