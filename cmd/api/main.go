package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	gormlogger "gorm.io/gorm/logger"

	"q-wash-api/internal/app"
	"q-wash-api/internal/config"
	"q-wash-api/internal/platform/db"
	"q-wash-api/internal/platform/push"
	"q-wash-api/internal/platform/sms"
	"q-wash-api/internal/platform/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	_ = godotenv.Load() // .env is optional; real env vars always take precedence

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	gormLogLevel := gormlogger.Warn
	if cfg.Env == "development" {
		gormLogLevel = gormlogger.Info
	}
	database, err := db.Connect(cfg.DB, gormlogger.Default.LogMode(gormLogLevel))
	if err != nil {
		return err
	}
	sqlDB, err := database.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	fileStorage, err := storage.NewLocalDisk(cfg.Storage.Dir, cfg.Storage.BaseURL)
	if err != nil {
		return err
	}

	pushSender, err := push.New(context.Background(), cfg.Push.ProjectID, cfg.Push.ServiceAccountJSON)
	if err != nil {
		return err
	}

	handler := app.New(database, cfg, sms.NewStubSender(), pushSender, fileStorage)

	server := &http.Server{
		Addr:         ":" + cfg.HTTP.Port,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go app.NewNotificationScheduler(database, sms.NewStubSender(), pushSender, time.Minute).Run(ctx)

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("http server starting", "addr", server.Addr, "env", cfg.Env)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-serveErr:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	slog.Info("http server stopped cleanly")
	return nil
}
