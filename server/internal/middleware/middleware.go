package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// CORS 跨域中间件——开发期 Vite dev server (5173) 与生产 nginx 同源策略都需要。
func CORS(allowOrigins []string) gin.HandlerFunc {
	allowed := map[string]bool{}
	for _, o := range allowOrigins {
		allowed[o] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowed[origin] || len(allowOrigins) == 0) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// RateLimit 每用户(或按 IP)滑动窗口限流。
// 生产应换 Redis,内存版足够项目自用与演示——诚实标注,不过度设计。
type RateLimit struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	buckets map[string][]time.Time
	lastGC  time.Time
}

func NewRateLimit(limit int, window time.Duration) *RateLimit {
	return &RateLimit{
		window:  window,
		limit:   limit,
		buckets: map[string][]time.Time{},
		lastGC:  time.Now(),
	}
}

func (r *RateLimit) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	// 惰性清理:防止 buckets 无界增长
	if now.Sub(r.lastGC) > 10*r.window {
		for k, ts := range r.buckets {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > r.window {
				delete(r.buckets, k)
			}
		}
		r.lastGC = now
	}

	times := r.buckets[key]
	valid := times[:0]
	for _, t := range times {
		if now.Sub(t) < r.window {
			valid = append(valid, t)
		}
	}
	if len(valid) >= r.limit {
		r.buckets[key] = valid
		return false
	}
	r.buckets[key] = append(valid, now)
	return true
}

// Middleware gin 中间件形态。限流键按 IP + 路由;生产多实例应换 Redis 共享窗口。
func (r *RateLimit) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := fmt.Sprintf("%s:%s", c.ClientIP(), c.FullPath())
		if !r.Allow(key) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"message": "请求过于频繁,稍后再试"})
			return
		}
		c.Next()
	}
}
