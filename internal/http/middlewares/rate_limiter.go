package middlewares

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type RateLimiter struct {
	mu       sync.Mutex
	rate     int
	burst    int
	tokens   map[string]int
	lastTime map[string]time.Time
}

func NewRateLimiter(rate, burst int) *RateLimiter {
	return &RateLimiter{
		rate:     rate,
		burst:    burst,
		tokens:   make(map[string]int),
		lastTime: make(map[string]time.Time),
	}
}

func (rl *RateLimiter) RateLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()

		rl.mu.Lock()
		now := time.Now()

		if _, exists := rl.tokens[ip]; !exists {
			rl.tokens[ip] = rl.burst
			rl.lastTime[ip] = now
		}

		elapsed := now.Sub(rl.lastTime[ip])
		rl.lastTime[ip] = now

		tokensToAdd := int(elapsed.Seconds()) * rl.rate
		rl.tokens[ip] += tokensToAdd
		if rl.tokens[ip] > rl.burst {
			rl.tokens[ip] = rl.burst
		}

		if rl.tokens[ip] <= 0 {
			rl.mu.Unlock()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}

		rl.tokens[ip]--
		rl.mu.Unlock()

		c.Next()
	}
}
