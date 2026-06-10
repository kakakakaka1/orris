package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/orris-inc/orris/internal/shared/utils"
)

// RateLimiter provides Redis-backed IP rate limiting using a fixed-window counter.
// Each IP gets a counter key with TTL equal to the window duration.
// This works correctly in multi-instance deployments since all instances share Redis.
type RateLimiter struct {
	redisClient *redis.Client
	limit       int
	window      time.Duration
}

// NewRateLimiter creates a new Redis-backed rate limiter.
// limit is the maximum number of requests allowed per window.
// window is the duration of the fixed time window.
func NewRateLimiter(redisClient *redis.Client, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		redisClient: redisClient,
		limit:       limit,
		window:      window,
	}
}

// Limit returns a Gin middleware that enforces the default rate limit per client IP.
func (rl *RateLimiter) Limit() gin.HandlerFunc {
	return rl.limitWithKey("ratelimit:ip", rl.limit)
}

// LimitN returns a Gin middleware that enforces a custom (typically stricter) per-IP
// limit over the same window, using a separate counter namespace so it does not share
// the default bucket. Use it to cap sensitive endpoints (login, password reset) more
// tightly than general traffic.
func (rl *RateLimiter) LimitN(limit int) gin.HandlerFunc {
	return rl.limitWithKey(fmt.Sprintf("ratelimit:strict:%d", limit), limit)
}

// limitWithKey is the shared fixed-window counter implementation. keyPrefix isolates
// independent buckets so different limits applied to the same IP do not interfere.
func (rl *RateLimiter) limitWithKey(keyPrefix string, limit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := c.ClientIP()
		windowBucket := time.Now().Unix() / int64(rl.window.Seconds())
		key := fmt.Sprintf("%s:%s:%d", keyPrefix, clientIP, windowBucket)

		ctx := context.Background()

		// Use INCR to atomically increment the counter and check if this is the first request
		count, err := rl.redisClient.Incr(ctx, key).Result()
		if err != nil {
			// If Redis is unavailable, allow the request to avoid blocking all traffic
			c.Next()
			return
		}

		// Set TTL on the key for the first request in this window
		if count == 1 {
			rl.redisClient.Expire(ctx, key, rl.window+time.Second)
		}

		if count > int64(limit) {
			utils.ErrorResponse(c, http.StatusTooManyRequests, "rate limit exceeded, please try again later")
			c.Abort()
			return
		}

		c.Next()
	}
}
