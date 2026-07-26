package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Config struct {
	ListenAddress    string
	ForgejoAPIURL    string
	ForgejoPublicURL string
	Owner            string
	PagesDomain      string
	GitPagesURL      string
	WebhookSecret    []byte
	MaxWebhookBytes  int64
	DeployTimeout    time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:    valueOrDefault("LISTEN_ADDRESS", ":8081"),
		ForgejoAPIURL:    os.Getenv("FORGEJO_API_URL"),
		ForgejoPublicURL: os.Getenv("FORGEJO_PUBLIC_URL"),
		Owner:            os.Getenv("FORGEJO_PAGES_OWNER"),
		PagesDomain:      os.Getenv("PAGES_DOMAIN"),
		GitPagesURL:      os.Getenv("GIT_PAGES_URL"),
		WebhookSecret:    []byte(os.Getenv("WEBHOOK_SECRET")),
	}

	var err error
	cfg.MaxWebhookBytes, err = positiveInt64("MAX_WEBHOOK_BYTES", 1<<20)
	if err != nil {
		return Config{}, err
	}
	cfg.DeployTimeout, err = positiveDuration("DEPLOY_TIMEOUT", 90*time.Second)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) validate() error {
	if cfg.ListenAddress == "" {
		return errors.New("LISTEN_ADDRESS is required")
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "FORGEJO_API_URL", value: cfg.ForgejoAPIURL},
		{name: "FORGEJO_PUBLIC_URL", value: cfg.ForgejoPublicURL},
		{name: "GIT_PAGES_URL", value: cfg.GitPagesURL},
	} {
		if err := validateHTTPURL(item.name, item.value); err != nil {
			return err
		}
	}
	if !dnsLabelPattern.MatchString(cfg.Owner) {
		return errors.New("FORGEJO_PAGES_OWNER must be a lowercase DNS label")
	}
	if cfg.PagesDomain == "" || strings.ContainsAny(cfg.PagesDomain, "/:") {
		return errors.New("PAGES_DOMAIN must be a hostname")
	}
	if len(cfg.WebhookSecret) < 32 {
		return errors.New("WEBHOOK_SECRET must contain at least 32 bytes")
	}
	return nil
}

func validateHTTPURL(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute HTTP URL", name)
	}
	return nil
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func positiveInt64(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func positiveDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}
