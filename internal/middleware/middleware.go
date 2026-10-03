// Package middleware holds Gin middleware: request id, logging, recovery, CORS, auth, rate limits.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"operan-be/internal/apperr"
)

const (
	CtxUserID    = "user_id"
	CtxRequestID = "request_id"
)

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		c.Set(CtxRequestID, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// Logger logs method, route, status and latency only. Bodies (health data, transcripts) are never logged.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "(unmatched)"
		}
		lvl := slog.LevelInfo
		if c.Writer.Status() >= 500 {
			lvl = slog.LevelError
		}
		slog.Log(c, lvl, "http", "method", c.Request.Method, "route", route, "status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(), "rid", c.GetString(CtxRequestID))
	}
}

func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, err any) {
		slog.Error("panic", "err", err, "rid", c.GetString(CtxRequestID))
		Abort(c, apperr.Internal())
	})
}

func Abort(c *gin.Context, e *apperr.Error) {
	c.AbortWithStatusJSON(e.Status, gin.H{"error": e})
}

func CORS(origins []string) gin.HandlerFunc {
	allowAll := false
	allowed := map[string]bool{}
	for _, o := range origins {
		if o == "*" {
			allowAll = true
		}
		allowed[strings.TrimRight(o, "/")] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowAll || allowed[origin] || isLocalDev(origin)) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, Last-Event-ID")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Expose-Headers", "X-Request-ID")
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// isLocalDev lets `flutter run -d chrome` work on whatever port it picks.
func isLocalDev(origin string) bool {
	return strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "http://127.0.0.1:")
}

// Auth validates the bearer token. SSE clients may pass ?access_token= instead.
func Auth(parse func(string) (string, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if raw == "" || raw == c.GetHeader("Authorization") {
			raw = c.Query("access_token")
		}
		if raw == "" {
			Abort(c, apperr.Unauthorized("Silakan masuk dulu."))
			return
		}
		uid, err := parse(raw)
		if err != nil {
			if e, ok := err.(*apperr.Error); ok {
				Abort(c, e)
				return
			}
			Abort(c, apperr.Unauthorized("Sesi berakhir. Silakan masuk lagi."))
			return
		}
		c.Set(CtxUserID, uid)
		c.Next()
	}
}

// RateLimit allows `limit` requests per `window` per user for one bucket name.
type RateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func NewRateLimiter() *RateLimiter { return &RateLimiter{hits: map[string][]time.Time{}} }

func (r *RateLimiter) Limit(bucket string, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := bucket + ":" + c.GetString(CtxUserID)
		now := time.Now()
		r.mu.Lock()
		kept := r.hits[key][:0]
		for _, t := range r.hits[key] {
			if now.Sub(t) < window {
				kept = append(kept, t)
			}
		}
		if len(kept) >= limit {
			r.hits[key] = kept
			r.mu.Unlock()
			Abort(c, apperr.RateLimited())
			return
		}
		r.hits[key] = append(kept, now)
		r.mu.Unlock()
		c.Next()
	}
}
