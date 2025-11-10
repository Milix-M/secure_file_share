package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"ephemeralpod/internal/config"
	"ephemeralpod/internal/server"
	"ephemeralpod/internal/storage"
)

func main() {
	cfg := config.Load()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mgr, err := storage.NewManager(cfg.StoragePath, cfg.MaxUploadSize, cfg.UploadTTL, cfg.StorageCapacity)
	if err != nil {
		logger.Error("storage init failed")
		os.Exit(1)
	}
	defer mgr.Close()

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mgr.StartCleanup(rootCtx, cfg.CleanupInterval)

	srv := server.New(cfg, mgr, logger)

	err = srv.Run(rootCtx)
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}
