// Command calibruh serves Calibre libraries over the web and OPDS.
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

	"calibruh/internal/calibredb"
	"calibruh/internal/config"
	"calibruh/internal/libraries"
	"calibruh/internal/server"
)

func main() {
	defaultConfig := "calibruh.json"
	if env := os.Getenv("CALIBRUH_CONFIG"); env != "" {
		defaultConfig = env
	}
	configPath := flag.String("config", defaultConfig, "path to the JSON config file (env CALIBRUH_CONFIG)")
	flag.Parse()

	if err := run(*configPath); err != nil {
		slog.Error("calibruh stopped", "err", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	libs, err := libraries.New(cfg.Libraries, cfg.LibrariesDir)
	if err != nil {
		return err
	}
	defer libs.Close()
	if cfg.LibrariesDir != "" {
		slog.Info("watching for libraries", "dir", cfg.LibrariesDir, "every", time.Duration(cfg.RescanSeconds)*time.Second)
		go libs.Watch(ctx, time.Duration(cfg.RescanSeconds)*time.Second)
	}

	runner := &calibredb.Runner{Bin: cfg.Calibredb, Duplicates: cfg.AllowDuplicates, Timeout: 5 * time.Minute}
	srv, err := server.New(cfg, libs, runner)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{Addr: cfg.Addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()

	slog.Info("listening", "addr", cfg.Addr, "auth", cfg.Username != "")
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
