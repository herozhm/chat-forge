package auth

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	jwtv5 "github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const ClaimsKey = "userID"

// IssueToken 签发 HS256 JWT,有效期 7 天。
func IssueToken(secret string, userID uint) (string, error) {
	claims := jwtv5.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(7 * 24 * time.Hour).Unix(),
		"iat": time.Now().Unix(),
		"iss": "chat-forge",
	}
	return jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// Middleware 校验 Authorization: Bearer <token>,通过后把 userID 注入 gin.Context。
func Middleware(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			c.AbortWithStatusJSON(401, gin.H{"message": "missing bearer token"})
			return
		}
		parsed, err := jwtv5.Parse(token, func(t *jwtv5.Token) (any, error) {
			if _, ok := t.Method.(*jwtv5.SigningMethodHMAC); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(secret), nil
		})
		if err != nil || !parsed.Valid {
			c.AbortWithStatusJSON(401, gin.H{"message": "invalid token"})
			return
		}
		claims, ok := parsed.Claims.(jwtv5.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(401, gin.H{"message": "invalid claims"})
			return
		}
		sub, ok := claims["sub"].(float64)
		if !ok {
			c.AbortWithStatusJSON(401, gin.H{"message": "invalid subject"})
			return
		}
		c.Set(ClaimsKey, uint(sub))
		c.Next()
	}
}

// UserID 从上下文取当前登录用户 ID(仅 Middleware 之后可用)。
func UserID(c *gin.Context) uint {
	if v, ok := c.Get(ClaimsKey); ok {
		if id, ok := v.(uint); ok {
			return id
		}
	}
	return 0
}

// HashPassword bcrypt 加密。
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword bcrypt 校验。
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
