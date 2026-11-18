// Command artifactd serves the Artifact HTTP API and the public artifact URLs.
//
// It is configured entirely through environment variables and validates all of
// them at startup, so a misconfiguration is a refusal to boot rather than a
// surprise on the first request.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/10/artifact/internal/api"
	"github.com/10/artifact/internal/store"
)

// Version is overwritten at build time with -ldflags.
var Version = "dev"

const (
	minTokenLength    = 32
	defaultMaxUpload  = 100 << 20 // 100 MiB
	readHeaderTimeout = 15 * time.Second
	idleTimeout       = 120 * time.Second
	shutdownTimeout   = 20 * time.Second
)

type config struct {
	token     string
	baseURL   string
	addr      string
	dataDir   string
	dbPath    string
	maxUpload int64
	logLevel  slog.Level
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "artifactd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	slog.SetDefault(logger)

	st, err := store.Open(cfg.dataDir, cfg.dbPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("close database", "error", err)
		}
	}()

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// Fail here rather than on the first upload.
	if err := st.Ping(ctx); err != nil {
		return fmt.Errorf("data directory %q unusable: %w", cfg.dataDir, err)
	}

	srv := &http.Server{
		Addr: cfg.addr,
		Handler: api.New(st, api.Config{
			Token:         cfg.token,
			BaseURL:       cfg.baseURL,
			MaxUploadSize: cfg.maxUpload,
			Logger:        logger,
		}),
		// No read or write deadline: a 100 MiB upload and a long video
		// download are both legitimate. The header timeout is what guards
		// against a client that connects and says nothing.
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	failed := make(chan error, 1)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	logger.Info("artifactd started",
		"version", Version,
		"addr", cfg.addr,
		"base_url", cfg.baseURL,
		"data_dir", cfg.dataDir,
		"max_upload_size", cfg.maxUpload,
	)

	select {
	case err := <-failed:
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	case <-ctx.Done():
	}

	logger.Info("shutting down, draining in-flight requests")

	drain, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(drain); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	return nil
}

func loadConfig() (*config, error) {
	cfg := &config{
		addr:      envOr("ARTIFACT_ADDR", ":8080"),
		dataDir:   envOr("ARTIFACT_DATA_DIR", "/data"),
		maxUpload: defaultMaxUpload,
		logLevel:  slog.LevelInfo,
	}

	// Railway and similar platforms assign the port through PORT. An explicit
	// ARTIFACT_ADDR still wins.
	if raw := os.Getenv("PORT"); raw != "" && os.Getenv("ARTIFACT_ADDR") == "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("PORT must be a TCP port number, got %q", raw)
		}

		cfg.addr = ":" + strconv.Itoa(port)
	}

	cfg.token = os.Getenv("ARTIFACT_TOKEN")
	if len(cfg.token) < minTokenLength {
		return nil, fmt.Errorf("ARTIFACT_TOKEN must be set and at least %d characters", minTokenLength)
	}

	base := os.Getenv("ARTIFACT_BASE_URL")
	if base == "" {
		return nil, errors.New("ARTIFACT_BASE_URL must be set, for example https://artifact.example.com")
	}

	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("ARTIFACT_BASE_URL must be an absolute http(s) URL, got %q", base)
	}

	cfg.baseURL = strings.TrimRight(base, "/")
	cfg.dbPath = envOr("ARTIFACT_DB", filepath.Join(cfg.dataDir, "artifact.db"))

	if raw := os.Getenv("ARTIFACT_MAX_UPLOAD_SIZE"); raw != "" {
		size, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || size <= 0 {
			return nil, fmt.Errorf("ARTIFACT_MAX_UPLOAD_SIZE must be a positive number of bytes, got %q", raw)
		}

		cfg.maxUpload = size
	}

	if raw := os.Getenv("ARTIFACT_LOG_LEVEL"); raw != "" {
		if err := cfg.logLevel.UnmarshalText([]byte(raw)); err != nil {
			return nil, fmt.Errorf("ARTIFACT_LOG_LEVEL must be debug, info, warn, or error, got %q", raw)
		}
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
