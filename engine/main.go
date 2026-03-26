package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"flowsentinel/engine/handler"
	"flowsentinel/engine/limiter"
	"flowsentinel/engine/metrics"
	"flowsentinel/engine/store"
)

type config struct {
	ListenAddr          string
	PostgresDSN         string
	RedisAddr           string
	RedisPassword       string
	RedisDB             int
	RulesRefreshSeconds int
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, err
	}
	return i, nil
}
// Config chnages handling 
func loadConfig() (config, error) {
	var cfg config
	cfg.ListenAddr = envString("LISTEN_ADDR", ":8080")
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	cfg.RedisAddr = envString("REDIS_ADDR", "redis:6379")
	cfg.RedisPassword = os.Getenv("REDIS_PASSWORD")
	db, err := envInt("REDIS_DB", 0)
	if err != nil {
		return config{}, errors.New("invalid REDIS_DB")
	}
	cfg.RedisDB = db
	refresh, err := envInt("RULES_REFRESH_SECONDS", 30)
	if err != nil {
		return config{}, errors.New("invalid RULES_REFRESH_SECONDS")
	}
	cfg.RulesRefreshSeconds = refresh
	if cfg.PostgresDSN == "" {
		return config{}, errors.New("POSTGRES_DSN is required")
	}
	return cfg, nil
}

func newLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func main() {
	logger := newLogger()
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("config_error", "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := metrics.NewRegistry()

	pgPool, err := store.NewPostgresPool(ctx, cfg.PostgresDSN)
	if err != nil {
		logger.Error("postgres_connect_error", "error", err.Error())
		os.Exit(1)
	}
	defer pgPool.Close()

	redisClient, err := store.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		logger.Error("redis_client_error", "error", err.Error())
		os.Exit(1)
	}
	defer redisClient.Close()

	ruleCache := store.NewRuleCache(logger, pgPool, time.Duration(cfg.RulesRefreshSeconds)*time.Second)
	if err := ruleCache.Start(ctx); err != nil {
		logger.Error("rule_cache_start_error", "error", err.Error())
		os.Exit(1)
	}

	lim := limiter.NewRedisSlidingWindow(redisClient, reg.RedisErrorsTotal)
	auditWriter := store.NewAuditWriter(logger, pgPool)
	auditWriter.Start(ctx)

	h := handler.New(logger, ruleCache, lim, auditWriter, reg)

	r := chi.NewRouter()
	r.Use(handler.RecoverMiddleware(logger))
	r.Use(handler.CorrelationIDMiddleware())
	r.Use(handler.AccessLogMiddleware(logger))

	r.Post("/check", h.Check)
	r.Get("/health", h.Health)
	r.Handle("/metrics", promhttp.HandlerFor(reg.Registry, promhttp.HandlerOpts{}))

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("server_start", "listen_addr", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server_error", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("server_stop")
}

