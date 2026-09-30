package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"homenode/services/home-api/internal/aniliberty"
	"homenode/services/home-api/internal/config"
	"homenode/services/home-api/internal/maintenance"
	"homenode/services/home-api/internal/rutracker"
	"homenode/services/home-api/internal/server"
	"homenode/services/home-api/internal/store"
	"homenode/services/home-api/internal/telegram"
	"homenode/services/home-api/internal/transmission"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}
	database, err := store.Open(cfg.DatabasePath)
	if err != nil {
		logger.Error("database error", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	telegramBot := telegram.New(telegram.Config{
		Token: cfg.TelegramBotToken, APIBaseURL: cfg.TelegramAPIBaseURL,
		AppURL: cfg.TelegramAppURL, InternalURL: cfg.HomeNodeInternalURL,
		APIUser: cfg.APIUsername, APIPassword: cfg.APIPassword,
		BindAddress: cfg.VPNBindAddress,
	}, database, logger)
	app, err := server.New(
		cfg,
		aniliberty.New(cfg.AniLibertyBaseURL, cfg.RequestTimeout, cfg.VPNBindAddress),
		rutracker.New(cfg.RuTrackerBaseURL, cfg.RuTrackerProxyURL, cfg.RuTrackerUsername, cfg.RuTrackerPassword, cfg.RequestTimeout, cfg.VPNBindAddress),
		transmission.New(cfg.TransmissionRPCURL, cfg.TransmissionUsername, cfg.TransmissionPassword, cfg.RequestTimeout),
		database,
		telegramBot,
		logger,
	)
	if err != nil {
		logger.Error("startup error", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go maintenance.Run(ctx, database, cfg.BackupDir, logger)
	go telegramBot.Run(ctx)
	logger.Info("HomeNode API started", "listen", cfg.ListenAddress)
	if err := app.ListenAndServe(ctx); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
