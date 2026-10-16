package gitpages

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL     *url.URL
	pagesDomain string
	http        *http.Client
}

type Deployment struct {
	Repository string
	Commit     string
	Branch     string
	CloneURL   string
}

type manifest struct {
	RepoURL string `json:"repoUrl"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
}

func NewClient(baseURL, pagesDomain string, client *http.Client) (*Client, error) {
	parsed, err := parseHTTPURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("git-pages URL: %w", err)
	}
	if pagesDomain == "" {
		return nil, fmt.Errorf("pages domain is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{baseURL: parsed, pagesDomain: pagesDomain, http: client}, nil
}

func (client *Client) Deploy(ctx context.Context, deployment Deployment) error {
	matches, err := client.manifestMatches(ctx, deployment)
	if err == nil && matches {
		return nil
	}

	target := *client.baseURL
	target.Path = "/"
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		target.String(),
		strings.NewReader(deployment.CloneURL),
	)
	if err != nil {
		return err
	}
	request.Host = client.siteHost(deployment.Repository)
	request.Header.Set("Branch", deployment.Branch)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("git-pages update: %w", err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
	closeErr := response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("git-pages response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("git-pages response close: %w", closeErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("git-pages returned %s: %s",
			response.Status, strings.TrimSpace(string(responseBody)))
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		matches, err := client.manifestMatches(ctx, deployment)
		if err == nil && matches {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("deployment confirmation: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (client *Client) Ready(ctx context.Context) error {
	port := client.baseURL.Port()
	if port == "" {
		port = "80"
		if client.baseURL.Scheme == "https" {
			port = "443"
		}
	}
	// Check the listener without requesting a nonexistent site and emitting 404 logs.
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(client.baseURL.Hostname(), port))
	if err != nil {
		return fmt.Errorf("git-pages TCP readiness: %w", err)
	}
	_ = conn.Close()
	return nil
}

func (client *Client) manifestMatches(ctx context.Context, deployment Deployment) (bool, error) {
	target := *client.baseURL
	target.Path = "/.git-pages/manifest.json"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false, err
	}
	request.Host = client.siteHost(deployment.Repository)
	request.Header.Set("Accept", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return false, fmt.Errorf("manifest returned %s", response.Status)
	}
	var current manifest
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&current); err != nil {
		return false, err
	}
	return sameURL(current.RepoURL, deployment.CloneURL) &&
		current.Branch == deployment.Branch &&
		hmac.Equal([]byte(current.Commit), []byte(deployment.Commit)), nil
}

func (client *Client) siteHost(repository string) string {
	return repository + "." + client.pagesDomain
}

func sameURL(left, right string) bool {
	leftURL, leftErr := url.Parse(left)
	rightURL, rightErr := url.Parse(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(leftURL.Scheme, rightURL.Scheme) &&
		strings.EqualFold(leftURL.Host, rightURL.Host) &&
		strings.TrimSuffix(leftURL.Path, "/") == strings.TrimSuffix(rightURL.Path, "/") &&
		leftURL.User == nil && rightURL.User == nil &&
		leftURL.Fragment == "" && rightURL.Fragment == "" &&
		leftURL.RawQuery == "" && rightURL.RawQuery == ""
}

func parseHTTPURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, fmt.Errorf("must be an absolute HTTP URL")
	}
	return parsed, nil
}
