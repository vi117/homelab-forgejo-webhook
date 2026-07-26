package config

import (
	"strings"
	"testing"
)

func TestLoadRequiresDeploymentSpecificValues(t *testing.T) {
	for _, name := range []string{
		"FORGEJO_API_URL",
		"FORGEJO_PUBLIC_URL",
		"FORGEJO_PAGES_OWNER",
		"PAGES_DOMAIN",
		"GIT_PAGES_URL",
		"WEBHOOK_SECRET",
	} {
		t.Setenv(name, "")
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "FORGEJO_API_URL") {
		t.Fatalf("Load() error = %v, want missing FORGEJO_API_URL", err)
	}
}

func TestLoadUsesOnlyOperationalDefaults(t *testing.T) {
	t.Setenv("FORGEJO_API_URL", "https://forge.example.com")
	t.Setenv("FORGEJO_PUBLIC_URL", "https://forge.example.com")
	t.Setenv("FORGEJO_PAGES_OWNER", "pages")
	t.Setenv("PAGES_DOMAIN", "pages.example.com")
	t.Setenv("GIT_PAGES_URL", "http://127.0.0.1:3000")
	t.Setenv("WEBHOOK_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("LISTEN_ADDRESS", "")
	t.Setenv("MAX_WEBHOOK_BYTES", "")
	t.Setenv("DEPLOY_TIMEOUT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddress != ":8081" || cfg.MaxWebhookBytes != 1<<20 || cfg.DeployTimeout.String() != "1m30s" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
}
