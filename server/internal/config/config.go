package config

import "os"

// Config 服务配置,全部来自环境变量,提供开发友好缺省值。
type Config struct {
	Port      string // HTTP 监听端口
	JWTSecret string // JWT 签名密钥
	DBPath    string // SQLite 文件路径

	// 模型接入
	AIProvider    string // mock | openai(OpenAI 兼容协议,DeepSeek/Kimi/Qwen 均可)
	OpenAIBaseURL string // OpenAI 兼容 base url(不带尾部 /chat/completions)
	OpenAIAPIKey  string
	Model         string // 模型名
	MaxToolRounds int    // tool-loop 最大轮次,防死循环
}

func Load() *Config {
	return &Config{
		Port:      env("PORT", "8080"),
		JWTSecret: env("JWT_SECRET", "dev-secret-change-me"),
		DBPath:    env("DB_PATH", "chat-forge.db"),

		AIProvider:    env("AI_PROVIDER", "mock"),
		OpenAIBaseURL: env("OPENAI_BASE_URL", "https://api.deepseek.com/v1"),
		OpenAIAPIKey:  env("OPENAI_API_KEY", ""),
		Model:         env("OPENAI_MODEL", "deepseek-chat"),
		MaxToolRounds: 5,
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
