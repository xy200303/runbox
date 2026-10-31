// Command runboxd serves the Runbox execution sandbox.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xy200303/runbox/internal/config"
	"github.com/xy200303/runbox/internal/runner"
	"github.com/xy200303/runbox/internal/server"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	check := flag.Bool("check", false, "validate configuration and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "runboxd:", err)
		os.Exit(2)
	}
	if version != "dev" {
		cfg.Version = version
	}
	if *check {
		fmt.Printf("configuration ok (version=%s, addr=%s, root=%s)\n", cfg.Version, cfg.Addr, cfg.Root)
		return
	}

	logger := newLogger(cfg.LogFormat)
	slog.SetDefault(logger)
	if cfg.AllowAnonymous {
		logger.Warn("authentication is disabled (RUNBOX_ALLOW_ANONYMOUS=1); do not expose this runner")
	}

	executor, err := runner.New(runner.Options{
		Root:                     cfg.Root,
		MaxConcurrency:           cfg.MaxConcurrency,
		UIDMin:                   cfg.UIDMin,
		UIDMax:                   cfg.UIDMax,
		MaxUploadBytes:           cfg.MaxUploadBytes,
		MaxCommandBytes:          cfg.MaxCommandBytes,
		MaxInputs:                cfg.MaxInputs,
		MaxStdoutBytes:           cfg.MaxStdoutBytes,
		MaxOutputFiles:           cfg.MaxOutputFiles,
		MaxOutputBytes:           cfg.MaxOutputBytes,
		QuotaBytes:               cfg.QuotaBytes,
		KeepBytes:                cfg.KeepBytes,
		Retention:                cfg.Retention,
		DefaultTimeout:           cfg.DefaultTimeout,
		MaxTimeout:               cfg.MaxTimeout,
		Logger:                   logger,
		AllowMissingCapabilities: cfg.AllowMissingCapabilities,
	})
	if err != nil {
		logger.Error("runner initialization failed", "error", err)
		os.Exit(1)
	}
	defer func() { _ = executor.Close() }()

	app := server.New(cfg, executor, logger)
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: 10 * time.Second,
		// Large uploads and downloads can legitimately run longer than a
		// command. Put slow-client protection in the reverse proxy instead of
		// imposing a fixed body timeout here.
		ReadTimeout:    0,
		WriteTimeout:   0,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go sweepLoop(ctx, executor, cfg.SweepInterval, logger)
	go shutdownLoop(ctx, httpServer, cfg.ShutdownTimeout, cancelBase, logger)

	logger.Info("runboxd starting",
		"version", cfg.Version,
		"addr", cfg.Addr,
		"root", cfg.Root,
		"max_concurrency", cfg.MaxConcurrency,
		"tls", cfg.TLSCert != "",
	)
	var serveErr error
	if cfg.TLSCert != "" {
		serveErr = httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		serveErr = httpServer.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		logger.Error("server stopped", "error", serveErr)
		os.Exit(1)
	}
	logger.Info("runboxd stopped")
}

func newLogger(format string) *slog.Logger {
	options := &slog.HandlerOptions{Level: slog.LevelInfo}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, options))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, options))
}

func sweepLoop(ctx context.Context, executor *runner.Runner, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swept, err := executor.Sweep(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("workspace sweep failed", "error", err)
				continue
			}
			if swept > 0 {
				logger.Info("workspace sweep finished", "removed", swept)
			}
		}
	}
}

func shutdownLoop(ctx context.Context, httpServer *http.Server, timeout time.Duration, cancelBase context.CancelFunc, logger *slog.Logger) {
	<-ctx.Done()
	logger.Info("shutdown requested")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		// Shutdown stops accepting new requests but does not cancel handlers.
		// Cancel the base context so in-flight commands are killed, then close.
		logger.Warn("graceful shutdown timed out; cancelling in-flight requests", "error", err)
		cancelBase()
		_ = httpServer.Close()
		return
	}
	cancelBase()
}
