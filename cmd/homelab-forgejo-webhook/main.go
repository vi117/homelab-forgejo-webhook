package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vi117/homelab-forgejo-webhook/internal/config"
	"github.com/vi117/homelab-forgejo-webhook/internal/forgejo"
	"github.com/vi117/homelab-forgejo-webhook/internal/gitpages"
	"github.com/vi117/homelab-forgejo-webhook/internal/webhook"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}

	httpClient := &http.Client{Timeout: cfg.DeployTimeout}
	forgejoClient, err := forgejo.NewClient(cfg.ForgejoAPIURL, httpClient)
	if err != nil {
		logger.Error("Forgejo client configuration error", "error", err)
		os.Exit(1)
	}
	gitPagesClient, err := gitpages.NewClient(cfg.GitPagesURL, cfg.PagesDomain, httpClient)
	if err != nil {
		logger.Error("git-pages client configuration error", "error", err)
		os.Exit(1)
	}
	handler, err := webhook.New(webhook.Config{
		Owner:            cfg.Owner,
		ForgejoPublicURL: cfg.ForgejoPublicURL,
		WebhookSecret:    cfg.WebhookSecret,
		MaxWebhookBytes:  cfg.MaxWebhookBytes,
		DeployTimeout:    cfg.DeployTimeout,
		DeployAsync:      true,
		Forgejo:          forgejoClient,
		GitPages:         gitPagesClient,
		Logger:           logger,
	})
	if err != nil {
		logger.Error("webhook configuration error", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("Forgejo webhook started", "listen", cfg.ListenAddress)
		serverErrors <- server.ListenAndServe()
	}()

	signalContext, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	select {
	case <-signalContext.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("server shutdown failed", "error", err)
	}
	if err := handler.Wait(shutdownContext); err != nil {
		logger.Error("deployment shutdown wait failed", "error", err)
	}
}
