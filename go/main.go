package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"vol20toglm/types"
)

const version = "0.1.0"

func main() {
	fmt.Printf("vol20toglm v%s\n", version)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Create context that cancels on SIGINT/SIGTERM
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Central action channel — all input sources send here, consumer reads
	actions := make(chan types.Action, 100)
	_ = actions // used by subsystems in later phases

	log.Info("starting", "version", version)
	log.Info("waiting for shutdown signal")
	<-ctx.Done()
	log.Info("shutting down")
}
