package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"operan-be/internal/config"
	"operan-be/internal/db"
	"operan-be/internal/handler"
	"operan-be/internal/langflow"
	"operan-be/internal/middleware"
	"operan-be/internal/service"
	"operan-be/internal/sse"
	"operan-be/internal/stt"
)

func main() {
	migrateOnly := flag.Bool("migrate", false, "jalankan migrasi lalu keluar")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	store, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	if *migrateOnly {
		slog.Info("migrasi selesai")
		return
	}

	svc := service.New(store, sse.NewHub(), newRunner(cfg), newSTT(cfg), cfg.Location, cfg.JWTSecret, cfg.PublicBaseURL)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.Logger(), middleware.Recovery(), middleware.CORS(cfg.CORSOrigins))
	r.MaxMultipartMemory = 4 << 20
	(&handler.Handler{Svc: svc, InternalToken: cfg.InternalToolToken, Limiter: middleware.NewRateLimiter()}).Register(r)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go svc.RunWorker(ctx, 5*time.Minute)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		slog.Info("Operan API berjalan", "addr", srv.Addr, "langflow_mock", cfg.LangflowMock, "stt_mock", cfg.STTMock)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	slog.Info("server berhenti")
}

func newRunner(cfg *config.Config) langflow.Runner {
	if cfg.LangflowMock {
		return langflow.NewMockRunner(cfg.Location)
	}
	return langflow.NewHTTPRunner(cfg.LangflowURL, cfg.LangflowAPIKey, cfg.FlowIDs)
}

func newSTT(cfg *config.Config) stt.Transcriber {
	if cfg.STTMock {
		return stt.Mock{}
	}
	if cfg.STTProvider == "openai" {
		return stt.NewOpenAI(cfg.STTBaseURL, cfg.STTAPIKey, cfg.STTModel)
	}
	return stt.NewGemini(cfg.STTBaseURL, cfg.STTAPIKey, cfg.STTModel)
}
