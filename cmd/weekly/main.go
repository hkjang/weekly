package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hkjang/weekly/internal/app"
)

// Web assets are copied here by the container build and scripts/build.sh.
//
//go:embed web/*
var embeddedWeb embed.FS

//go:embed templates/*
var embeddedTemplates embed.FS

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	web, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		logger.Error("load embedded web assets", "error", err)
		os.Exit(1)
	}
	referencePPTX, _ := embeddedTemplates.ReadFile("templates/reference.pptx")

	server, err := app.New(ctx, app.Options{
		Logger: logger,
		Web:    web,
		Build: app.BuildInfo{
			Version: version,
			Commit:  commit,
			BuiltAt: buildTime,
		},
		DefaultPPTX:     referencePPTX,
		DefaultPPTXName: "1월5주간업무보고_AI엔지니어링.pptx",
	})
	if err != nil {
		logger.Error("initialize Weekly", "error", err)
		os.Exit(1)
	}
	defer server.Close()

	httpServer := &http.Server{
		Addr:              ":8080",
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      330 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("Weekly started", "address", httpServer.Addr, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown", "error", err)
	}
	fmt.Println("Weekly stopped")
}
