// Package handler maps HTTP requests to the service layer. Handlers stay thin.
package handler

import (
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"operan-be/internal/apperr"
	"operan-be/internal/middleware"
	"operan-be/internal/service"
)

type Handler struct {
	Svc           *service.Service
	InternalToken string
	Limiter       *middleware.RateLimiter
}

func (h *Handler) Register(r *gin.Engine) {
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	api := r.Group("/api/v1")
	api.POST("/auth/register", h.register)
	api.POST("/auth/login", h.login)

	a := api.Group("", middleware.Auth(h.Svc.ParseToken))
	ai := func(bucket string, limit int) gin.HandlerFunc { return h.Limiter.Limit(bucket, limit, time.Hour) }

	a.GET("/auth/me", h.me)
	a.POST("/devices", h.registerDevice)

	a.POST("/families", h.createFamily)
	a.POST("/families/join", h.joinFamily)
	a.GET("/families/:id", h.getFamily)
	a.GET("/families/:id/members", h.members)
	a.POST("/families/:id/profiles", h.createProfile)
	a.GET("/families/:id/profiles", h.listProfiles)
	a.GET("/families/:id/stream", h.stream)

	a.GET("/profiles/:id", h.getProfile)
	a.PATCH("/profiles/:id", h.updateProfile)
	a.GET("/profiles/:id/today", h.today)
	a.GET("/profiles/:id/timeline", h.timeline)

	a.POST("/profiles/:id/medications", h.createMedication)
	a.GET("/profiles/:id/medications", h.listMedications)
	a.PATCH("/medications/:id", h.updateMedication)
	a.POST("/medications/:id/give-prn", h.givePRN)
	a.GET("/profiles/:id/doses", h.listDoses)
	a.POST("/doses/:id/give", h.giveDose)
	a.POST("/doses/:id/skip", h.skipDose)
	a.POST("/doses/:id/undo", h.undoDose)

	a.POST("/profiles/:id/logs", h.createLog)
	a.GET("/profiles/:id/logs", h.listLogs)
	a.POST("/profiles/:id/voice", ai("voice", 40), h.voicePreview)
	a.POST("/profiles/:id/voice/confirm", h.voiceConfirm)

	a.POST("/profiles/:id/shifts/start", h.startShift)
	a.GET("/shifts/:id/handover-draft", ai("handover", 40), h.handoverDraft)
	a.POST("/shifts/:id/end", h.endShift)
	a.GET("/profiles/:id/handovers/latest", h.latestHandover)
	a.PATCH("/handovers/:id/read", h.readHandover)

	a.GET("/profiles/:id/alerts", h.listAlerts)
	a.PATCH("/alerts/:id/read", h.readAlert)
	a.POST("/profiles/:id/analyze", ai("analyze", 20), h.analyze)

	a.POST("/profiles/:id/reports", ai("reports", 20), h.createReport)
	a.GET("/profiles/:id/reports/latest", h.latestReport)
	a.GET("/reports/:id", h.getReport)

	a.POST("/profiles/:id/ask", ai("ask", 20), h.ask)

	tools := r.Group("/internal/tools", h.internalAuth)
	tools.GET("/profiles/:id/logs", h.toolLogs)
	tools.GET("/profiles/:id/medications", h.toolMedications)
	tools.GET("/profiles/:id/doses", h.toolDoses)
}

func uid(c *gin.Context) string { return c.GetString(middleware.CtxUserID) }

// fail writes the uniform error body; unknown errors become 500 without leaking details.
func fail(c *gin.Context, err error) {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		middleware.Abort(c, ae)
		return
	}
	slog.Error("unhandled", "err", err, "route", c.FullPath(), "rid", c.GetString(middleware.CtxRequestID))
	middleware.Abort(c, apperr.Internal())
}

// bind decodes JSON; an empty body is allowed (all-optional payloads).
func bind(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil && !errors.Is(err, io.EOF) {
		fail(c, apperr.BadRequest("Format data tidak valid."))
		return false
	}
	return true
}

func respond(c *gin.Context, status int, v any, err error) {
	if err != nil {
		fail(c, err)
		return
	}
	if v == nil {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(status, v)
}

// parseDay reads ?date=YYYY-MM-DD in the app timezone (default: today).
func (h *Handler) parseDay(c *gin.Context) (time.Time, bool) {
	raw := c.Query("date")
	if raw == "" {
		return h.Svc.Now(), true
	}
	t, err := time.ParseInLocation("2006-01-02", raw, h.Svc.Loc)
	if err != nil {
		fail(c, apperr.Validation("Parameter date harus berformat YYYY-MM-DD."))
		return time.Time{}, false
	}
	return t.Add(12 * time.Hour), true
}

// parseRange reads ?from=&to= (RFC3339 or YYYY-MM-DD). Defaults to the last 7 days.
func (h *Handler) parseRange(c *gin.Context) (time.Time, time.Time, bool) {
	now := h.Svc.Now()
	to := now.Add(time.Minute)
	from := now.AddDate(0, 0, -7)
	parse := func(s string, end bool) (time.Time, error) {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, nil
		}
		t, err := time.ParseInLocation("2006-01-02", s, h.Svc.Loc)
		if err == nil && end {
			t = t.AddDate(0, 0, 1)
		}
		return t, err
	}
	if s := c.Query("from"); s != "" {
		t, err := parse(s, false)
		if err != nil {
			fail(c, apperr.Validation("Parameter from tidak valid."))
			return from, to, false
		}
		from = t
	}
	if s := c.Query("to"); s != "" {
		t, err := parse(s, true)
		if err != nil {
			fail(c, apperr.Validation("Parameter to tidak valid."))
			return from, to, false
		}
		to = t
	}
	return from, to, true
}

// internalAuth protects agent tools: static token + a session scoped to one profile.
func (h *Handler) internalAuth(c *gin.Context) {
	tok := c.GetHeader("X-Internal-Token")
	if tok == "" {
		tok = strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	}
	if h.InternalToken == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(h.InternalToken)) != 1 {
		middleware.Abort(c, apperr.Unauthorized("Token internal tidak valid."))
		return
	}
	c.Next()
}

func (h *Handler) requireToolSession(c *gin.Context) (string, bool) {
	pid := c.Param("id")
	if !h.Svc.ToolSessionAllows(c.Query("session"), pid) {
		middleware.Abort(c, apperr.New(http.StatusForbidden, "TOOL_SCOPE", "Sesi tool tidak berlaku untuk profil ini."))
		return "", false
	}
	return pid, true
}
